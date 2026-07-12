package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// repositoryBase contains the persistence mechanics shared by the three
// bounded repositories. It is deliberately private: callers must select the
// repository matching the state component they are allowed to access.
type repositoryBase struct {
	store state.Store
	mu    sync.Mutex
}

func newRepositoryBase(store state.Store) repositoryBase {
	return repositoryBase{store: store}
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

func virtualKeysByUserIndexKey(userID string) string {
	return indexKey("virtual-keys-by-user/" + userID)
}

func keySubjectKey(userID string) string {
	return entityKey("key-subjects", userID)
}

func encode(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode state value: %w", err)
	}
	return encoded, nil
}

func getResource[T any](ctx context.Context, store state.Store, collection, id string) (T, error) {
	var result T
	entry, err := store.Get(ctx, entityKey(collection, id))
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

func readIndex(ctx context.Context, store state.Store, key string) (resourceIndex, string, error) {
	entry, err := store.Get(ctx, key)
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
	if index.IDs == nil {
		index.IDs = []string{}
	}
	return index, entry.ETag, nil
}

func listResources[T any](ctx context.Context, store state.Store, collection string) ([]T, error) {
	return listResourcesAtIndex[T](ctx, store, collection, indexKey(collection))
}

func listResourcesAtIndex[T any](ctx context.Context, store state.Store, collection, key string) ([]T, error) {
	index, _, err := readIndex(ctx, store, key)
	if err != nil {
		return nil, fmt.Errorf("get %s index: %w", collection, err)
	}
	result := make([]T, 0, len(index.IDs))
	for _, id := range index.IDs {
		value, err := getResource[T](ctx, store, collection, id)
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

func lookup(ctx context.Context, store state.Store, kind, value string) (string, error) {
	entry, err := store.Get(ctx, lookupKey(kind, value))
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

func transact(ctx context.Context, repository *repositoryBase, operations []state.Operation) error {
	if err := repository.store.Transact(ctx, operations); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func addIndexID(index resourceIndex, id string) resourceIndex {
	if !slices.Contains(index.IDs, id) {
		index.IDs = append(index.IDs, id)
	}
	return index
}

func removeIndexID(index resourceIndex, id string) resourceIndex {
	index.IDs = slices.DeleteFunc(index.IDs, func(candidate string) bool { return candidate == id })
	return index
}

func createResource(ctx context.Context, repository *repositoryBase, collection, id string, value any, lookups map[string]string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	entry, err := repository.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if entry.Exists {
		return ErrConflict
	}
	for key := range lookups {
		existing, err := repository.store.Get(ctx, key)
		if err != nil {
			return err
		}
		if existing.Exists {
			return ErrConflict
		}
	}
	index, indexETag, err := readIndex(ctx, repository.store, indexKey(collection))
	if err != nil {
		return err
	}
	index = addIndexID(index, id)
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
	return transact(ctx, repository, operations)
}

func replaceResource[T any](ctx context.Context, repository *repositoryBase, collection, id string, expected, value T, oldLookups, newLookups map[string]string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	entry, err := repository.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if !entry.Exists {
		return ErrNotFound
	}
	var current T
	if err := json.Unmarshal(entry.Value, &current); err != nil {
		return fmt.Errorf("decode %s %q: %w", collection, id, err)
	}
	if !reflect.DeepEqual(current, expected) {
		return ErrConflict
	}
	for key := range newLookups {
		existing, err := repository.store.Get(ctx, key)
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
	return transact(ctx, repository, operations)
}

func deleteResource[T any](ctx context.Context, repository *repositoryBase, collection, id string, expected T, lookups map[string]string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()

	entry, err := repository.store.Get(ctx, entityKey(collection, id))
	if err != nil {
		return err
	}
	if !entry.Exists {
		return ErrNotFound
	}
	var current T
	if err := json.Unmarshal(entry.Value, &current); err != nil {
		return fmt.Errorf("decode %s %q: %w", collection, id, err)
	}
	if !reflect.DeepEqual(current, expected) {
		return ErrConflict
	}
	index, indexETag, err := readIndex(ctx, repository.store, indexKey(collection))
	if err != nil {
		return err
	}
	index = removeIndexID(index, id)
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
	return transact(ctx, repository, operations)
}

// UserRepository owns private control-plane user state.
type UserRepository struct {
	repository repositoryBase
}

func NewUserRepository(store state.Store) *UserRepository {
	return &UserRepository{repository: newRepositoryBase(store)}
}

func userLookups(user User) map[string]string {
	return map[string]string{lookupKey("user-email", user.Email): user.ID}
}

func (r *UserRepository) CreateUser(ctx context.Context, user User) error {
	return createResource(ctx, &r.repository, "users", user.ID, user, userLookups(user))
}

func (r *UserRepository) GetUser(ctx context.Context, id string) (User, error) {
	return getResource[User](ctx, r.repository.store, "users", id)
}

func (r *UserRepository) GetUserByEmail(ctx context.Context, email string) (User, error) {
	id, err := lookup(ctx, r.repository.store, "user-email", email)
	if err != nil {
		return User{}, err
	}
	return r.GetUser(ctx, id)
}

func (r *UserRepository) ListUsers(ctx context.Context) ([]User, error) {
	return listResources[User](ctx, r.repository.store, "users")
}

func (r *UserRepository) ReplaceUser(ctx context.Context, oldUser, user User) error {
	return replaceResource(ctx, &r.repository, "users", user.ID, oldUser, user, userLookups(oldUser), userLookups(user))
}

func (r *UserRepository) DeleteUser(ctx context.Context, user User) error {
	return deleteResource(ctx, &r.repository, "users", user.ID, user, userLookups(user))
}

// ProviderRepository owns provider runtime records shared by the control plane,
// gateways, and provider adapters.
type ProviderRepository struct {
	repository repositoryBase
}

func NewProviderRepository(store state.Store) *ProviderRepository {
	return &ProviderRepository{repository: newRepositoryBase(store)}
}

func providerLookups(provider Provider) map[string]string {
	return map[string]string{
		lookupKey("provider-slug", provider.Slug):                   provider.ID,
		lookupKey("provider-adapter-app-id", provider.AdapterAppID): provider.ID,
	}
}

func (r *ProviderRepository) CreateProvider(ctx context.Context, provider Provider) error {
	return createResource(ctx, &r.repository, "providers", provider.ID, provider, providerLookups(provider))
}

func (r *ProviderRepository) GetProvider(ctx context.Context, id string) (Provider, error) {
	return getResource[Provider](ctx, r.repository.store, "providers", id)
}

func (r *ProviderRepository) GetProviderBySlug(ctx context.Context, slug string) (Provider, error) {
	id, err := lookup(ctx, r.repository.store, "provider-slug", slug)
	if err != nil {
		return Provider{}, err
	}
	return r.GetProvider(ctx, id)
}

func (r *ProviderRepository) ListProviders(ctx context.Context) ([]Provider, error) {
	return listResources[Provider](ctx, r.repository.store, "providers")
}

func (r *ProviderRepository) ReplaceProvider(ctx context.Context, oldProvider, provider Provider) error {
	return replaceResource(ctx, &r.repository, "providers", provider.ID, oldProvider, provider, providerLookups(oldProvider), providerLookups(provider))
}

func (r *ProviderRepository) DeleteProvider(ctx context.Context, provider Provider) error {
	return deleteResource(ctx, &r.repository, "providers", provider.ID, provider, providerLookups(provider))
}

// VirtualKeyRepository owns virtual keys, their indexes, and the authorization
// subject projection used by gateways.
type VirtualKeyRepository struct {
	repository repositoryBase
}

func NewVirtualKeyRepository(store state.Store) *VirtualKeyRepository {
	return &VirtualKeyRepository{repository: newRepositoryBase(store)}
}

func (r *VirtualKeyRepository) GetVirtualKey(ctx context.Context, id string) (VirtualKey, error) {
	return getResource[VirtualKey](ctx, r.repository.store, "virtual-keys", id)
}

func (r *VirtualKeyRepository) GetVirtualKeyByHash(ctx context.Context, hash string) (VirtualKey, error) {
	id, err := lookup(ctx, r.repository.store, "key-hash", hash)
	if err != nil {
		return VirtualKey{}, err
	}
	return r.GetVirtualKey(ctx, id)
}

func (r *VirtualKeyRepository) ListVirtualKeys(ctx context.Context) ([]VirtualKey, error) {
	return listResources[VirtualKey](ctx, r.repository.store, "virtual-keys")
}

func (r *VirtualKeyRepository) ListVirtualKeysByUser(ctx context.Context, userID string) ([]VirtualKey, error) {
	return listResourcesAtIndex[VirtualKey](ctx, r.repository.store, "virtual-keys", virtualKeysByUserIndexKey(userID))
}

type storedSubject struct {
	value  KeySubject
	etag   string
	exists bool
}

func (r *VirtualKeyRepository) readSubject(ctx context.Context, userID string) (storedSubject, error) {
	entry, err := r.repository.store.Get(ctx, keySubjectKey(userID))
	if err != nil {
		return storedSubject{}, fmt.Errorf("get key subject %q: %w", userID, err)
	}
	if !entry.Exists {
		return storedSubject{}, nil
	}
	var subject KeySubject
	if err := json.Unmarshal(entry.Value, &subject); err != nil {
		return storedSubject{}, fmt.Errorf("decode key subject %q: %w", userID, err)
	}
	if subject.UserID != userID {
		return storedSubject{}, fmt.Errorf("decode key subject %q: user_id is %q", userID, subject.UserID)
	}
	return storedSubject{value: subject, etag: entry.ETag, exists: true}, nil
}

func (r *VirtualKeyRepository) GetSubject(ctx context.Context, userID string) (KeySubject, error) {
	stored, err := r.readSubject(ctx, userID)
	if err != nil {
		return KeySubject{}, err
	}
	if !stored.exists {
		return KeySubject{}, ErrNotFound
	}
	return stored.value, nil
}

func subjectPayloadEqual(left, right KeySubject) bool {
	return left.UserID == right.UserID &&
		left.Status == right.Status &&
		left.Revision == right.Revision &&
		left.Deleted == right.Deleted
}

func validateSubjectPayload(subject KeySubject) error {
	if subject.UserID == "" {
		return fmt.Errorf("%w: key subject user_id is required", ErrConflict)
	}
	if subject.Revision == 0 {
		return fmt.Errorf("%w: key subject revision must be positive", ErrConflict)
	}
	if subject.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: key subject updated_at is required", ErrConflict)
	}
	if subject.Status != StatusActive && subject.Status != StatusDisabled {
		return fmt.Errorf("%w: key subject status is invalid", ErrConflict)
	}
	if subject.Deleted && subject.Status != StatusDisabled {
		return fmt.Errorf("%w: a deleted key subject must be disabled", ErrConflict)
	}
	return nil
}

func compareSubjectUpdate(current storedSubject, next KeySubject) (bool, error) {
	if !current.exists {
		return false, nil
	}
	if next.Revision < current.value.Revision {
		return false, fmt.Errorf("%w: key subject revision is stale", ErrConflict)
	}
	if next.Revision == current.value.Revision {
		if subjectPayloadEqual(current.value, next) {
			return true, nil
		}
		return false, fmt.Errorf("%w: key subject revision has a different payload", ErrConflict)
	}
	if current.value.Deleted && !next.Deleted {
		return false, fmt.Errorf("%w: a deleted key subject cannot be reactivated", ErrConflict)
	}
	return false, nil
}

func subjectTouchOperation(subject storedSubject) (state.Operation, error) {
	if !subject.exists {
		return state.Operation{}, fmt.Errorf("%w: key subject does not exist", ErrConflict)
	}
	value, err := encode(subject.value)
	if err != nil {
		return state.Operation{}, err
	}
	return state.Operation{Type: state.Upsert, Key: keySubjectKey(subject.value.UserID), Value: value, ETag: subject.etag}, nil
}

func ensureSubjectCanOwnKey(subject storedSubject, key VirtualKey) error {
	if !subject.exists {
		return fmt.Errorf("%w: key subject does not exist", ErrConflict)
	}
	if subject.value.Deleted {
		return fmt.Errorf("%w: key subject is deleted", ErrConflict)
	}
	if subject.value.Status != StatusActive && subject.value.Status != StatusDisabled {
		return fmt.Errorf("%w: key subject status is invalid", ErrConflict)
	}
	if key.Status == StatusActive && subject.value.Status != StatusActive {
		return fmt.Errorf("%w: an active virtual key requires an active subject", ErrConflict)
	}
	return nil
}

func (r *VirtualKeyRepository) SyncSubject(ctx context.Context, subject KeySubject) error {
	if err := validateSubjectPayload(subject); err != nil {
		return err
	}
	if subject.Deleted {
		return fmt.Errorf("%w: deleted key subjects must be fenced", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	current, err := r.readSubject(ctx, subject.UserID)
	if err != nil {
		return err
	}
	idempotent, err := compareSubjectUpdate(current, subject)
	if err != nil {
		return err
	}
	if idempotent {
		return nil
	}
	value, err := encode(subject)
	if err != nil {
		return err
	}
	return transact(ctx, &r.repository, []state.Operation{{
		Type: state.Upsert, Key: keySubjectKey(subject.UserID), Value: value, ETag: current.etag,
	}})
}

// FenceSubject persists a non-reversible tombstone only when the user owns no
// virtual keys. Every key mutation touches the same subject ETag, so a
// concurrent create or move either commits first and makes fencing conflict, or
// loses its CAS against the tombstone.
func (r *VirtualKeyRepository) FenceSubject(ctx context.Context, subject KeySubject) error {
	if err := validateSubjectPayload(subject); err != nil {
		return err
	}
	if !subject.Deleted {
		return fmt.Errorf("%w: a fenced key subject must be deleted", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	current, err := r.readSubject(ctx, subject.UserID)
	if err != nil {
		return err
	}
	index, _, err := readIndex(ctx, r.repository.store, virtualKeysByUserIndexKey(subject.UserID))
	if err != nil {
		return fmt.Errorf("get virtual keys by user index: %w", err)
	}
	if len(index.IDs) != 0 {
		return fmt.Errorf("%w: user still has virtual keys", ErrConflict)
	}
	idempotent, err := compareSubjectUpdate(current, subject)
	if err != nil {
		return err
	}
	if idempotent {
		return nil
	}
	value, err := encode(subject)
	if err != nil {
		return err
	}
	return transact(ctx, &r.repository, []state.Operation{{
		Type: state.Upsert, Key: keySubjectKey(subject.UserID), Value: value, ETag: current.etag,
	}})
}

func (r *VirtualKeyRepository) CreateVirtualKey(ctx context.Context, key VirtualKey) error {
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("virtual-keys", key.ID))
	if err != nil {
		return err
	}
	if entityEntry.Exists {
		return ErrConflict
	}
	hashLookupKey := lookupKey("key-hash", key.KeyHash)
	existingLookup, err := r.repository.store.Get(ctx, hashLookupKey)
	if err != nil {
		return err
	}
	if existingLookup.Exists {
		return ErrConflict
	}
	subject, err := r.readSubject(ctx, key.UserID)
	if err != nil {
		return err
	}
	if err := ensureSubjectCanOwnKey(subject, key); err != nil {
		return err
	}
	globalIndex, globalETag, err := readIndex(ctx, r.repository.store, indexKey("virtual-keys"))
	if err != nil {
		return err
	}
	userIndex, userETag, err := readIndex(ctx, r.repository.store, virtualKeysByUserIndexKey(key.UserID))
	if err != nil {
		return err
	}
	globalIndex = addIndexID(globalIndex, key.ID)
	userIndex = addIndexID(userIndex, key.ID)

	entityValue, err := encode(key)
	if err != nil {
		return err
	}
	globalIndexValue, err := encode(globalIndex)
	if err != nil {
		return err
	}
	userIndexValue, err := encode(userIndex)
	if err != nil {
		return err
	}
	lookupValue, err := encode(key.ID)
	if err != nil {
		return err
	}
	touch, err := subjectTouchOperation(subject)
	if err != nil {
		return err
	}
	return transact(ctx, &r.repository, []state.Operation{
		{Type: state.Upsert, Key: entityKey("virtual-keys", key.ID), Value: entityValue},
		{Type: state.Upsert, Key: indexKey("virtual-keys"), Value: globalIndexValue, ETag: globalETag},
		{Type: state.Upsert, Key: hashLookupKey, Value: lookupValue},
		{Type: state.Upsert, Key: virtualKeysByUserIndexKey(key.UserID), Value: userIndexValue, ETag: userETag},
		touch,
	})
}

func (r *VirtualKeyRepository) ReplaceVirtualKey(ctx context.Context, oldKey, key VirtualKey) error {
	if oldKey.ID != key.ID {
		return fmt.Errorf("%w: virtual key ID is immutable", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("virtual-keys", key.ID))
	if err != nil {
		return err
	}
	if !entityEntry.Exists {
		return ErrNotFound
	}
	var current VirtualKey
	if err := json.Unmarshal(entityEntry.Value, &current); err != nil {
		return fmt.Errorf("decode virtual-keys %q: %w", key.ID, err)
	}
	if !reflect.DeepEqual(current, oldKey) {
		return ErrConflict
	}

	newLookupKey := lookupKey("key-hash", key.KeyHash)
	existingLookup, err := r.repository.store.Get(ctx, newLookupKey)
	if err != nil {
		return err
	}
	if existingLookup.Exists {
		var target string
		if err := json.Unmarshal(existingLookup.Value, &target); err != nil || target != key.ID {
			return ErrConflict
		}
	}

	oldSubject, err := r.readSubject(ctx, current.UserID)
	if err != nil {
		return err
	}
	if !oldSubject.exists {
		return fmt.Errorf("%w: current key subject does not exist", ErrConflict)
	}
	newSubject := oldSubject
	if key.UserID != current.UserID {
		newSubject, err = r.readSubject(ctx, key.UserID)
		if err != nil {
			return err
		}
	}
	if err := ensureSubjectCanOwnKey(newSubject, key); err != nil {
		return err
	}

	oldUserIndex, oldUserETag, err := readIndex(ctx, r.repository.store, virtualKeysByUserIndexKey(current.UserID))
	if err != nil {
		return err
	}
	newUserIndex := oldUserIndex
	newUserETag := oldUserETag
	if key.UserID != current.UserID {
		newUserIndex, newUserETag, err = readIndex(ctx, r.repository.store, virtualKeysByUserIndexKey(key.UserID))
		if err != nil {
			return err
		}
		oldUserIndex = removeIndexID(oldUserIndex, key.ID)
	}
	newUserIndex = addIndexID(newUserIndex, key.ID)

	entityValue, err := encode(key)
	if err != nil {
		return err
	}
	lookupValue, err := encode(key.ID)
	if err != nil {
		return err
	}
	oldIndexValue, err := encode(oldUserIndex)
	if err != nil {
		return err
	}
	newIndexValue, err := encode(newUserIndex)
	if err != nil {
		return err
	}
	oldTouch, err := subjectTouchOperation(oldSubject)
	if err != nil {
		return err
	}
	operations := []state.Operation{{
		Type: state.Upsert, Key: entityKey("virtual-keys", key.ID), Value: entityValue, ETag: entityEntry.ETag,
	}}
	oldLookupKey := lookupKey("key-hash", current.KeyHash)
	if oldLookupKey != newLookupKey {
		operations = append(operations, state.Operation{Type: state.Delete, Key: oldLookupKey})
	}
	operations = append(operations, state.Operation{Type: state.Upsert, Key: newLookupKey, Value: lookupValue})
	if key.UserID == current.UserID {
		operations = append(operations,
			state.Operation{Type: state.Upsert, Key: virtualKeysByUserIndexKey(key.UserID), Value: newIndexValue, ETag: newUserETag},
			oldTouch,
		)
	} else {
		newTouch, err := subjectTouchOperation(newSubject)
		if err != nil {
			return err
		}
		operations = append(operations,
			state.Operation{Type: state.Upsert, Key: virtualKeysByUserIndexKey(current.UserID), Value: oldIndexValue, ETag: oldUserETag},
			state.Operation{Type: state.Upsert, Key: virtualKeysByUserIndexKey(key.UserID), Value: newIndexValue, ETag: newUserETag},
			oldTouch,
			newTouch,
		)
	}
	return transact(ctx, &r.repository, operations)
}

func (r *VirtualKeyRepository) DeleteVirtualKey(ctx context.Context, key VirtualKey) error {
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("virtual-keys", key.ID))
	if err != nil {
		return err
	}
	if !entityEntry.Exists {
		return ErrNotFound
	}
	var current VirtualKey
	if err := json.Unmarshal(entityEntry.Value, &current); err != nil {
		return fmt.Errorf("decode virtual-keys %q: %w", key.ID, err)
	}
	if !reflect.DeepEqual(current, key) {
		return ErrConflict
	}
	subject, err := r.readSubject(ctx, current.UserID)
	if err != nil {
		return err
	}
	if !subject.exists {
		return fmt.Errorf("%w: current key subject does not exist", ErrConflict)
	}
	globalIndex, globalETag, err := readIndex(ctx, r.repository.store, indexKey("virtual-keys"))
	if err != nil {
		return err
	}
	userIndex, userETag, err := readIndex(ctx, r.repository.store, virtualKeysByUserIndexKey(current.UserID))
	if err != nil {
		return err
	}
	globalIndex = removeIndexID(globalIndex, current.ID)
	userIndex = removeIndexID(userIndex, current.ID)
	globalIndexValue, err := encode(globalIndex)
	if err != nil {
		return err
	}
	userIndexValue, err := encode(userIndex)
	if err != nil {
		return err
	}
	touch, err := subjectTouchOperation(subject)
	if err != nil {
		return err
	}
	return transact(ctx, &r.repository, []state.Operation{
		{Type: state.Delete, Key: entityKey("virtual-keys", current.ID), ETag: entityEntry.ETag},
		{Type: state.Upsert, Key: indexKey("virtual-keys"), Value: globalIndexValue, ETag: globalETag},
		{Type: state.Delete, Key: lookupKey("key-hash", current.KeyHash)},
		{Type: state.Upsert, Key: virtualKeysByUserIndexKey(current.UserID), Value: userIndexValue, ETag: userETag},
		touch,
	})
}
