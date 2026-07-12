package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/Albe83/gwai/internal/state"
)

var (
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("resource conflict")
)

type resourceIndex struct {
	IDs []string `json:"ids"`
}

type Repository struct {
	store state.Store
	mu    sync.Mutex
}

func NewRepository(store state.Store) *Repository {
	return &Repository{store: store}
}

func entityKey(collection, id string) string {
	return collection + "/" + id
}

func indexKey(collection string) string {
	return "indexes/" + collection
}

func lookupKey(kind, value string) string {
	digest := sha256.Sum256([]byte(value))
	return "lookups/" + kind + "/" + hex.EncodeToString(digest[:])
}

func encode(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode state value: %w", err)
	}
	return encoded, nil
}

func getResource[T any](ctx context.Context, repository *Repository, collection, id string) (T, error) {
	var result T
	entry, err := repository.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return result, fmt.Errorf("get %s %q: %w", collection, id, err)
	}
	if !entry.Exists {
		return result, ErrNotFound
	}
	if err := json.Unmarshal(entry.Value, &result); err != nil {
		return result, fmt.Errorf("decode %s %q: %w", collection, id, err)
	}
	return result, nil
}

func listResources[T any](ctx context.Context, repository *Repository, collection string) ([]T, error) {
	entry, err := repository.store.Get(ctx, indexKey(collection))
	if err != nil {
		return nil, fmt.Errorf("get %s index: %w", collection, err)
	}
	if !entry.Exists {
		return []T{}, nil
	}
	var index resourceIndex
	if err := json.Unmarshal(entry.Value, &index); err != nil {
		return nil, fmt.Errorf("decode %s index: %w", collection, err)
	}
	result := make([]T, 0, len(index.IDs))
	for _, id := range index.IDs {
		value, err := getResource[T](ctx, repository, collection, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (r *Repository) readIndex(ctx context.Context, collection string) (resourceIndex, string, error) {
	entry, err := r.store.Get(ctx, indexKey(collection))
	if err != nil {
		return resourceIndex{}, "", err
	}
	if !entry.Exists {
		return resourceIndex{IDs: []string{}}, "", nil
	}
	var index resourceIndex
	if err := json.Unmarshal(entry.Value, &index); err != nil {
		return resourceIndex{}, "", err
	}
	return index, entry.ETag, nil
}

func (r *Repository) lookup(ctx context.Context, kind, value string) (string, error) {
	entry, err := r.store.Get(ctx, lookupKey(kind, value))
	if err != nil {
		return "", err
	}
	if !entry.Exists {
		return "", ErrNotFound
	}
	var id string
	if err := json.Unmarshal(entry.Value, &id); err != nil {
		return "", fmt.Errorf("decode %s lookup: %w", kind, err)
	}
	return id, nil
}

func createResource(ctx context.Context, r *Repository, collection, id string, value any, lookups map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, err := r.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if entry.Exists {
		return ErrConflict
	}
	for key := range lookups {
		existing, err := r.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if existing.Exists {
			return ErrConflict
		}
	}
	index, indexETag, err := r.readIndex(ctx, collection)
	if err != nil {
		return err
	}
	index.IDs = append(index.IDs, id)
	entityValue, err := encode(value)
	if err != nil {
		return err
	}
	indexValue, err := encode(index)
	if err != nil {
		return err
	}
	operations := []state.Operation{
		{Type: state.Upsert, Key: entityKey(collection, id), Value: entityValue},
		{Type: state.Upsert, Key: indexKey(collection), Value: indexValue, ETag: indexETag},
	}
	for key, targetID := range lookups {
		lookupValue, err := encode(targetID)
		if err != nil {
			return err
		}
		operations = append(operations, state.Operation{Type: state.Upsert, Key: key, Value: lookupValue})
	}
	if err := r.store.Transact(ctx, operations); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func replaceResource(ctx context.Context, r *Repository, collection, id string, value any, oldLookups, newLookups map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, err := r.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if !entry.Exists {
		return ErrNotFound
	}
	for key := range newLookups {
		existing, err := r.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if existing.Exists {
			var target string
			if err := json.Unmarshal(existing.Value, &target); err != nil || target != id {
				return ErrConflict
			}
		}
	}
	entityValue, err := encode(value)
	if err != nil {
		return err
	}
	operations := []state.Operation{{Type: state.Upsert, Key: entityKey(collection, id), Value: entityValue, ETag: entry.ETag}}
	for key := range oldLookups {
		if _, retained := newLookups[key]; !retained {
			operations = append(operations, state.Operation{Type: state.Delete, Key: key})
		}
	}
	for key, targetID := range newLookups {
		lookupValue, err := encode(targetID)
		if err != nil {
			return err
		}
		operations = append(operations, state.Operation{Type: state.Upsert, Key: key, Value: lookupValue})
	}
	if err := r.store.Transact(ctx, operations); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func deleteResource(ctx context.Context, r *Repository, collection, id string, lookups map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, err := r.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if !entry.Exists {
		return ErrNotFound
	}
	index, indexETag, err := r.readIndex(ctx, collection)
	if err != nil {
		return err
	}
	index.IDs = slices.DeleteFunc(index.IDs, func(candidate string) bool { return candidate == id })
	indexValue, err := encode(index)
	if err != nil {
		return err
	}
	operations := []state.Operation{
		{Type: state.Delete, Key: entityKey(collection, id), ETag: entry.ETag},
		{Type: state.Upsert, Key: indexKey(collection), Value: indexValue, ETag: indexETag},
	}
	for key := range lookups {
		operations = append(operations, state.Operation{Type: state.Delete, Key: key})
	}
	if err := r.store.Transact(ctx, operations); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func userLookups(user User) map[string]string {
	return map[string]string{lookupKey("user-email", user.Email): user.ID}
}

func (r *Repository) CreateUser(ctx context.Context, user User) error {
	return createResource(ctx, r, "users", user.ID, user, userLookups(user))
}

func (r *Repository) GetUser(ctx context.Context, id string) (User, error) {
	return getResource[User](ctx, r, "users", id)
}

func (r *Repository) GetUserByEmail(ctx context.Context, email string) (User, error) {
	id, err := r.lookup(ctx, "user-email", email)
	if err != nil {
		return User{}, err
	}
	return r.GetUser(ctx, id)
}

func (r *Repository) ListUsers(ctx context.Context) ([]User, error) {
	return listResources[User](ctx, r, "users")
}

func (r *Repository) ReplaceUser(ctx context.Context, oldUser, user User) error {
	return replaceResource(ctx, r, "users", user.ID, user, userLookups(oldUser), userLookups(user))
}

func (r *Repository) DeleteUser(ctx context.Context, user User) error {
	return deleteResource(ctx, r, "users", user.ID, userLookups(user))
}

func providerLookups(provider Provider) map[string]string {
	return map[string]string{
		lookupKey("provider-slug", provider.Slug):                   provider.ID,
		lookupKey("provider-adapter-app-id", provider.AdapterAppID): provider.ID,
	}
}

func (r *Repository) CreateProvider(ctx context.Context, provider Provider) error {
	return createResource(ctx, r, "providers", provider.ID, provider, providerLookups(provider))
}

func (r *Repository) GetProvider(ctx context.Context, id string) (Provider, error) {
	return getResource[Provider](ctx, r, "providers", id)
}

func (r *Repository) GetProviderBySlug(ctx context.Context, slug string) (Provider, error) {
	id, err := r.lookup(ctx, "provider-slug", slug)
	if err != nil {
		return Provider{}, err
	}
	return r.GetProvider(ctx, id)
}

func (r *Repository) ListProviders(ctx context.Context) ([]Provider, error) {
	return listResources[Provider](ctx, r, "providers")
}

func (r *Repository) ReplaceProvider(ctx context.Context, oldProvider, provider Provider) error {
	return replaceResource(ctx, r, "providers", provider.ID, provider, providerLookups(oldProvider), providerLookups(provider))
}

func (r *Repository) DeleteProvider(ctx context.Context, provider Provider) error {
	return deleteResource(ctx, r, "providers", provider.ID, providerLookups(provider))
}

func virtualKeyLookups(key VirtualKey) map[string]string {
	return map[string]string{lookupKey("key-hash", key.KeyHash): key.ID}
}

func (r *Repository) CreateVirtualKey(ctx context.Context, key VirtualKey) error {
	return createResource(ctx, r, "virtual-keys", key.ID, key, virtualKeyLookups(key))
}

func (r *Repository) GetVirtualKey(ctx context.Context, id string) (VirtualKey, error) {
	return getResource[VirtualKey](ctx, r, "virtual-keys", id)
}

func (r *Repository) GetVirtualKeyByHash(ctx context.Context, hash string) (VirtualKey, error) {
	id, err := r.lookup(ctx, "key-hash", hash)
	if err != nil {
		return VirtualKey{}, err
	}
	return r.GetVirtualKey(ctx, id)
}

func (r *Repository) ListVirtualKeys(ctx context.Context) ([]VirtualKey, error) {
	return listResources[VirtualKey](ctx, r, "virtual-keys")
}

func (r *Repository) ReplaceVirtualKey(ctx context.Context, key VirtualKey) error {
	return replaceResource(ctx, r, "virtual-keys", key.ID, key, virtualKeyLookups(key), virtualKeyLookups(key))
}

func (r *Repository) DeleteVirtualKey(ctx context.Context, key VirtualKey) error {
	return deleteResource(ctx, r, "virtual-keys", key.ID, virtualKeyLookups(key))
}
