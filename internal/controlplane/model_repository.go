package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/Albe83/gwai/internal/state"
)

// ModelRepository owns the model catalog in the provider state domain. Model
// writes also CAS the referenced provider record and its per-provider index so
// that model creation or movement cannot race a provider deletion into an
// orphaned route.
type ModelRepository struct {
	repository repositoryBase
}

func NewModelRepository(store state.Store) *ModelRepository {
	return &ModelRepository{repository: newRepositoryBase(store)}
}

func modelsByProviderIndexKey(providerID string) string {
	return indexKey("models-by-provider/" + providerID)
}

func modelLookups(model Model) map[string]string {
	return map[string]string{lookupKey("model-alias", model.Alias): model.ID}
}

func readProviderEntry(ctx context.Context, store state.Store, id string) (Provider, state.Entry, error) {
	entry, err := store.Get(ctx, entityKey("providers", id))
	if err != nil {
		return Provider{}, state.Entry{}, fmt.Errorf("get providers %q: %w", id, err)
	}
	if !entry.Exists {
		return Provider{}, state.Entry{}, ErrNotFound
	}
	var provider Provider
	if err := json.Unmarshal(entry.Value, &provider); err != nil {
		return Provider{}, state.Entry{}, fmt.Errorf("decode providers %q: %w", id, err)
	}
	return provider, entry, nil
}

func providerTouchOperation(provider Provider, entry state.Entry) (state.Operation, error) {
	value, err := encode(provider)
	if err != nil {
		return state.Operation{}, err
	}
	return state.Operation{
		Type: state.Upsert, Key: entityKey("providers", provider.ID), Value: value, ETag: entry.ETag,
	}, nil
}

func ensureProviderCanBackModel(provider Provider, model Model) error {
	if model.Status == StatusActive && provider.Status != StatusActive {
		return fmt.Errorf("%w: an active model requires an active provider", ErrConflict)
	}
	return nil
}

func (r *ModelRepository) CreateModel(ctx context.Context, model Model) error {
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("models", model.ID))
	if err != nil {
		return err
	}
	if entityEntry.Exists {
		return ErrConflict
	}
	aliasKey := lookupKey("model-alias", model.Alias)
	aliasEntry, err := r.repository.store.Get(ctx, aliasKey)
	if err != nil {
		return err
	}
	if aliasEntry.Exists {
		return ErrConflict
	}
	provider, providerEntry, err := readProviderEntry(ctx, r.repository.store, model.ProviderID)
	if err != nil {
		return err
	}
	if err := ensureProviderCanBackModel(provider, model); err != nil {
		return err
	}
	globalIndex, globalETag, err := readIndex(ctx, r.repository.store, indexKey("models"))
	if err != nil {
		return err
	}
	providerIndex, providerIndexETag, err := readIndex(ctx, r.repository.store, modelsByProviderIndexKey(model.ProviderID))
	if err != nil {
		return err
	}
	globalIndex = addIndexID(globalIndex, model.ID)
	providerIndex = addIndexID(providerIndex, model.ID)

	modelValue, err := encode(model)
	if err != nil {
		return err
	}
	globalIndexValue, err := encode(globalIndex)
	if err != nil {
		return err
	}
	providerIndexValue, err := encode(providerIndex)
	if err != nil {
		return err
	}
	aliasValue, err := encode(model.ID)
	if err != nil {
		return err
	}
	providerTouch, err := providerTouchOperation(provider, providerEntry)
	if err != nil {
		return err
	}
	return transact(ctx, &r.repository, []state.Operation{
		{Type: state.Upsert, Key: entityKey("models", model.ID), Value: modelValue},
		{Type: state.Upsert, Key: indexKey("models"), Value: globalIndexValue, ETag: globalETag},
		{Type: state.Upsert, Key: aliasKey, Value: aliasValue},
		{Type: state.Upsert, Key: modelsByProviderIndexKey(model.ProviderID), Value: providerIndexValue, ETag: providerIndexETag},
		providerTouch,
	})
}

func (r *ModelRepository) GetModel(ctx context.Context, id string) (Model, error) {
	return getResource[Model](ctx, r.repository.store, "models", id)
}

func (r *ModelRepository) GetModelByAlias(ctx context.Context, alias string) (Model, error) {
	id, err := lookup(ctx, r.repository.store, "model-alias", alias)
	if err != nil {
		return Model{}, err
	}
	return r.GetModel(ctx, id)
}

func (r *ModelRepository) ListModels(ctx context.Context) ([]Model, error) {
	return listResources[Model](ctx, r.repository.store, "models")
}

func (r *ModelRepository) ListModelsByProvider(ctx context.Context, providerID string) ([]Model, error) {
	return listResourcesAtIndex[Model](ctx, r.repository.store, "models", modelsByProviderIndexKey(providerID))
}

func (r *ModelRepository) ReplaceModel(ctx context.Context, oldModel, model Model) error {
	if oldModel.ID != model.ID {
		return fmt.Errorf("%w: model ID is immutable", ErrConflict)
	}
	if oldModel.Alias != model.Alias {
		return fmt.Errorf("%w: model alias is immutable", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("models", model.ID))
	if err != nil {
		return err
	}
	if !entityEntry.Exists {
		return ErrNotFound
	}
	var current Model
	if err := json.Unmarshal(entityEntry.Value, &current); err != nil {
		return fmt.Errorf("decode models %q: %w", model.ID, err)
	}
	if !reflect.DeepEqual(current, oldModel) {
		return ErrConflict
	}
	aliasKey := lookupKey("model-alias", model.Alias)
	aliasEntry, err := r.repository.store.Get(ctx, aliasKey)
	if err != nil {
		return err
	}
	if aliasEntry.Exists {
		var target string
		if err := json.Unmarshal(aliasEntry.Value, &target); err != nil || target != model.ID {
			return ErrConflict
		}
	}

	oldProvider, oldProviderEntry, err := readProviderEntry(ctx, r.repository.store, oldModel.ProviderID)
	if err != nil {
		return err
	}
	newProvider := oldProvider
	newProviderEntry := oldProviderEntry
	if model.ProviderID != oldModel.ProviderID {
		newProvider, newProviderEntry, err = readProviderEntry(ctx, r.repository.store, model.ProviderID)
		if err != nil {
			return err
		}
	}
	if err := ensureProviderCanBackModel(newProvider, model); err != nil {
		return err
	}

	oldIndex, oldIndexETag, err := readIndex(ctx, r.repository.store, modelsByProviderIndexKey(oldModel.ProviderID))
	if err != nil {
		return err
	}
	newIndex := oldIndex
	newIndexETag := oldIndexETag
	if model.ProviderID != oldModel.ProviderID {
		newIndex, newIndexETag, err = readIndex(ctx, r.repository.store, modelsByProviderIndexKey(model.ProviderID))
		if err != nil {
			return err
		}
		oldIndex = removeIndexID(oldIndex, model.ID)
	}
	newIndex = addIndexID(newIndex, model.ID)

	modelValue, err := encode(model)
	if err != nil {
		return err
	}
	oldIndexValue, err := encode(oldIndex)
	if err != nil {
		return err
	}
	newIndexValue, err := encode(newIndex)
	if err != nil {
		return err
	}
	aliasValue, err := encode(model.ID)
	if err != nil {
		return err
	}
	oldProviderTouch, err := providerTouchOperation(oldProvider, oldProviderEntry)
	if err != nil {
		return err
	}
	operations := []state.Operation{
		{Type: state.Upsert, Key: entityKey("models", model.ID), Value: modelValue, ETag: entityEntry.ETag},
		{Type: state.Upsert, Key: aliasKey, Value: aliasValue},
	}
	if model.ProviderID == oldModel.ProviderID {
		operations = append(operations,
			state.Operation{Type: state.Upsert, Key: modelsByProviderIndexKey(model.ProviderID), Value: newIndexValue, ETag: newIndexETag},
			oldProviderTouch,
		)
	} else {
		newProviderTouch, err := providerTouchOperation(newProvider, newProviderEntry)
		if err != nil {
			return err
		}
		operations = append(operations,
			state.Operation{Type: state.Upsert, Key: modelsByProviderIndexKey(oldModel.ProviderID), Value: oldIndexValue, ETag: oldIndexETag},
			state.Operation{Type: state.Upsert, Key: modelsByProviderIndexKey(model.ProviderID), Value: newIndexValue, ETag: newIndexETag},
			oldProviderTouch,
			newProviderTouch,
		)
	}
	return transact(ctx, &r.repository, operations)
}

func (r *ModelRepository) DeleteModel(ctx context.Context, model Model) error {
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	entityEntry, err := r.repository.store.Get(ctx, entityKey("models", model.ID))
	if err != nil {
		return err
	}
	if !entityEntry.Exists {
		return ErrNotFound
	}
	var current Model
	if err := json.Unmarshal(entityEntry.Value, &current); err != nil {
		return fmt.Errorf("decode models %q: %w", model.ID, err)
	}
	if !reflect.DeepEqual(current, model) {
		return ErrConflict
	}
	globalIndex, globalETag, err := readIndex(ctx, r.repository.store, indexKey("models"))
	if err != nil {
		return err
	}
	providerIndex, providerIndexETag, err := readIndex(ctx, r.repository.store, modelsByProviderIndexKey(model.ProviderID))
	if err != nil {
		return err
	}
	globalIndex = removeIndexID(globalIndex, model.ID)
	providerIndex = removeIndexID(providerIndex, model.ID)
	globalIndexValue, err := encode(globalIndex)
	if err != nil {
		return err
	}
	providerIndexValue, err := encode(providerIndex)
	if err != nil {
		return err
	}
	operations := []state.Operation{
		{Type: state.Delete, Key: entityKey("models", model.ID), ETag: entityEntry.ETag},
		{Type: state.Upsert, Key: indexKey("models"), Value: globalIndexValue, ETag: globalETag},
		{Type: state.Upsert, Key: modelsByProviderIndexKey(model.ProviderID), Value: providerIndexValue, ETag: providerIndexETag},
		{Type: state.Delete, Key: lookupKey("model-alias", model.Alias)},
	}
	provider, providerEntry, providerErr := readProviderEntry(ctx, r.repository.store, model.ProviderID)
	if providerErr == nil {
		providerTouch, err := providerTouchOperation(provider, providerEntry)
		if err != nil {
			return err
		}
		operations = append(operations, providerTouch)
	} else if providerErr != ErrNotFound {
		return providerErr
	}
	return transact(ctx, &r.repository, operations)
}

// DeleteProviderIfNoModels atomically checks the provider-scoped model index
// and removes the provider plus its indexes. Model mutations touch the provider
// ETag in their transaction, so exactly one side of a create/move/delete race
// can commit.
func (r *ModelRepository) DeleteProviderIfNoModels(ctx context.Context, provider Provider) error {
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	current, providerEntry, err := readProviderEntry(ctx, r.repository.store, provider.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, provider) {
		return ErrConflict
	}
	modelIndex, modelIndexETag, err := readIndex(ctx, r.repository.store, modelsByProviderIndexKey(provider.ID))
	if err != nil {
		return err
	}
	if len(modelIndex.IDs) != 0 {
		return fmt.Errorf("%w: provider still has models", ErrConflict)
	}
	providerIndex, providerIndexETag, err := readIndex(ctx, r.repository.store, indexKey("providers"))
	if err != nil {
		return err
	}
	providerIndex = removeIndexID(providerIndex, provider.ID)
	providerIndexValue, err := encode(providerIndex)
	if err != nil {
		return err
	}
	operations := []state.Operation{
		{Type: state.Delete, Key: entityKey("providers", provider.ID), ETag: providerEntry.ETag},
		{Type: state.Upsert, Key: indexKey("providers"), Value: providerIndexValue, ETag: providerIndexETag},
	}
	for key := range providerLookups(provider) {
		operations = append(operations, state.Operation{Type: state.Delete, Key: key})
	}
	if modelIndexETag != "" {
		operations = append(operations, state.Operation{
			Type: state.Delete, Key: modelsByProviderIndexKey(provider.ID), ETag: modelIndexETag,
		})
	}
	return transact(ctx, &r.repository, operations)
}
