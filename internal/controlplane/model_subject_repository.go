package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Albe83/gwai/internal/state"
)

type storedModelSubject struct {
	value  ModelSubject
	etag   string
	exists bool
}

type modelIndexMutation struct {
	subject storedModelSubject
	index   resourceIndex
	etag    string
}

func modelIDSet(ids []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("%w: virtual key model ID is empty", ErrConflict)
		}
		if _, duplicate := result[id]; duplicate {
			return nil, fmt.Errorf("%w: virtual key contains duplicate model ID %q", ErrConflict, id)
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func (r *VirtualKeyRepository) prepareModelIndexMutations(ctx context.Context, oldIDs, newIDs []string, key VirtualKey) ([]string, map[string]modelIndexMutation, error) {
	oldSet, err := modelIDSet(oldIDs)
	if err != nil {
		return nil, nil, err
	}
	newSet, err := modelIDSet(newIDs)
	if err != nil {
		return nil, nil, err
	}
	all := make([]string, 0, len(oldSet)+len(newSet))
	seen := make(map[string]struct{}, len(oldSet)+len(newSet))
	for id := range oldSet {
		seen[id] = struct{}{}
		all = append(all, id)
	}
	for id := range newSet {
		if _, exists := seen[id]; !exists {
			all = append(all, id)
		}
	}
	slices.Sort(all)
	mutations := make(map[string]modelIndexMutation, len(all))
	for _, modelID := range all {
		subject, err := r.readModelSubject(ctx, modelID)
		if err != nil {
			return nil, nil, err
		}
		if _, retained := newSet[modelID]; retained {
			if err := ensureModelCanBackKey(subject, key); err != nil {
				return nil, nil, fmt.Errorf("model %q: %w", modelID, err)
			}
		} else if !subject.exists || subject.value.Deleted {
			return nil, nil, fmt.Errorf("%w: referenced model subject %q is unavailable", ErrConflict, modelID)
		}
		index, etag, err := readIndex(ctx, r.repository.store, virtualKeysByModelIndexKey(modelID))
		if err != nil {
			return nil, nil, fmt.Errorf("get virtual keys by model index: %w", err)
		}
		mutations[modelID] = modelIndexMutation{subject: subject, index: index, etag: etag}
	}
	return all, mutations, nil
}

func modelIndexOperations(modelIDs []string, mutations map[string]modelIndexMutation, keyID string, oldIDs, newIDs []string) ([]state.Operation, error) {
	operations := make([]state.Operation, 0, len(modelIDs)*2)
	for _, modelID := range modelIDs {
		mutation := mutations[modelID]
		if slices.Contains(oldIDs, modelID) && !slices.Contains(newIDs, modelID) {
			mutation.index = removeIndexID(mutation.index, keyID)
		}
		if slices.Contains(newIDs, modelID) {
			mutation.index = addIndexID(mutation.index, keyID)
		}
		value, err := encode(mutation.index)
		if err != nil {
			return nil, err
		}
		touch, err := modelSubjectTouchOperation(mutation.subject)
		if err != nil {
			return nil, err
		}
		operations = append(operations,
			state.Operation{Type: state.Upsert, Key: virtualKeysByModelIndexKey(modelID), Value: value, ETag: mutation.etag},
			touch,
		)
	}
	return operations, nil
}

func (r *VirtualKeyRepository) readModelSubject(ctx context.Context, modelID string) (storedModelSubject, error) {
	entry, err := r.repository.store.Get(ctx, modelSubjectKey(modelID))
	if err != nil {
		return storedModelSubject{}, fmt.Errorf("get model subject %q: %w", modelID, err)
	}
	if !entry.Exists {
		return storedModelSubject{}, nil
	}
	var subject ModelSubject
	if err := json.Unmarshal(entry.Value, &subject); err != nil {
		return storedModelSubject{}, fmt.Errorf("decode model subject %q: %w", modelID, err)
	}
	if subject.ModelID != modelID {
		return storedModelSubject{}, fmt.Errorf("decode model subject %q: model_id is %q", modelID, subject.ModelID)
	}
	return storedModelSubject{value: subject, etag: entry.ETag, exists: true}, nil
}

func (r *VirtualKeyRepository) GetModelSubject(ctx context.Context, modelID string) (ModelSubject, error) {
	stored, err := r.readModelSubject(ctx, modelID)
	if err != nil {
		return ModelSubject{}, err
	}
	if !stored.exists {
		return ModelSubject{}, ErrNotFound
	}
	return stored.value, nil
}

func validateModelSubjectPayload(subject ModelSubject) error {
	if subject.ModelID == "" {
		return fmt.Errorf("%w: model subject model_id is required", ErrConflict)
	}
	if subject.Alias == "" {
		return fmt.Errorf("%w: model subject alias is required", ErrConflict)
	}
	if subject.Revision == 0 {
		return fmt.Errorf("%w: model subject revision must be positive", ErrConflict)
	}
	if subject.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: model subject updated_at is required", ErrConflict)
	}
	if subject.Status != StatusActive && subject.Status != StatusDisabled {
		return fmt.Errorf("%w: model subject status is invalid", ErrConflict)
	}
	if subject.Deleted && subject.Status != StatusDisabled {
		return fmt.Errorf("%w: a deleted model subject must be disabled", ErrConflict)
	}
	return nil
}

func modelSubjectPayloadEqual(left, right ModelSubject) bool {
	return left.ModelID == right.ModelID &&
		left.Alias == right.Alias &&
		left.Status == right.Status &&
		left.Revision == right.Revision &&
		left.Deleted == right.Deleted
}

func compareModelSubjectUpdate(current storedModelSubject, next ModelSubject) (bool, error) {
	if !current.exists {
		return false, nil
	}
	if next.ModelID != current.value.ModelID || next.Alias != current.value.Alias {
		return false, fmt.Errorf("%w: model subject identity is immutable", ErrConflict)
	}
	if next.Revision < current.value.Revision {
		return false, fmt.Errorf("%w: model subject revision is stale", ErrConflict)
	}
	if next.Revision == current.value.Revision {
		if modelSubjectPayloadEqual(current.value, next) {
			return true, nil
		}
		return false, fmt.Errorf("%w: model subject revision has a different payload", ErrConflict)
	}
	if current.value.Deleted && !next.Deleted {
		return false, fmt.Errorf("%w: a deleted model subject cannot be reactivated", ErrConflict)
	}
	return false, nil
}

func modelSubjectTouchOperation(subject storedModelSubject) (state.Operation, error) {
	if !subject.exists {
		return state.Operation{}, fmt.Errorf("%w: model subject does not exist", ErrConflict)
	}
	value, err := encode(subject.value)
	if err != nil {
		return state.Operation{}, err
	}
	return state.Operation{Type: state.Upsert, Key: modelSubjectKey(subject.value.ModelID), Value: value, ETag: subject.etag}, nil
}

func ensureModelCanBackKey(subject storedModelSubject, key VirtualKey) error {
	if !subject.exists {
		return fmt.Errorf("%w: model subject does not exist", ErrConflict)
	}
	if subject.value.Deleted {
		return fmt.Errorf("%w: model subject is deleted", ErrConflict)
	}
	if subject.value.Status != StatusActive && subject.value.Status != StatusDisabled {
		return fmt.Errorf("%w: model subject status is invalid", ErrConflict)
	}
	if key.Status == StatusActive && subject.value.Status != StatusActive {
		return fmt.Errorf("%w: an active virtual key requires active models", ErrConflict)
	}
	return nil
}

func (r *VirtualKeyRepository) SyncModelSubject(ctx context.Context, subject ModelSubject) error {
	if err := validateModelSubjectPayload(subject); err != nil {
		return err
	}
	if subject.Deleted {
		return fmt.Errorf("%w: deleted model subjects must be fenced", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	current, err := r.readModelSubject(ctx, subject.ModelID)
	if err != nil {
		return err
	}
	idempotent, err := compareModelSubjectUpdate(current, subject)
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
		Type: state.Upsert, Key: modelSubjectKey(subject.ModelID), Value: value, ETag: current.etag,
	}})
}

// FenceModelSubject persists a non-reversible tombstone only when no virtual
// key references the model. Key mutations touch this subject's ETag in the
// same transaction as their reverse-index updates, closing the delete race.
func (r *VirtualKeyRepository) FenceModelSubject(ctx context.Context, subject ModelSubject) error {
	if err := validateModelSubjectPayload(subject); err != nil {
		return err
	}
	if !subject.Deleted {
		return fmt.Errorf("%w: a fenced model subject must be deleted", ErrConflict)
	}
	r.repository.mu.Lock()
	defer r.repository.mu.Unlock()

	current, err := r.readModelSubject(ctx, subject.ModelID)
	if err != nil {
		return err
	}
	index, _, err := readIndex(ctx, r.repository.store, virtualKeysByModelIndexKey(subject.ModelID))
	if err != nil {
		return fmt.Errorf("get virtual keys by model index: %w", err)
	}
	if len(index.IDs) != 0 {
		return fmt.Errorf("%w: model is still referenced by virtual keys", ErrConflict)
	}
	idempotent, err := compareModelSubjectUpdate(current, subject)
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
		Type: state.Upsert, Key: modelSubjectKey(subject.ModelID), Value: value, ETag: current.etag,
	}})
}
