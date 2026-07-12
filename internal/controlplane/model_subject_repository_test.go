package controlplane

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

type transactionBarrierStore struct {
	state.Store
	enabled  atomic.Bool
	arrivals atomic.Int32
	release  chan struct{}
}

func (s *transactionBarrierStore) Transact(ctx context.Context, operations []state.Operation) error {
	if s.enabled.Load() {
		if s.arrivals.Add(1) == 2 {
			close(s.release)
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.Transact(ctx, operations)
}

func modelProjectionFixture(t *testing.T) (*state.MemoryStore, *VirtualKeyRepository, KeySubject, ModelSubject) {
	t.Helper()
	store := state.NewMemoryStore()
	repository := NewVirtualKeyRepository(store)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	user := KeySubject{UserID: "usr_test", Status: StatusActive, Revision: 1, UpdatedAt: now}
	model := ModelSubject{ModelID: "mdl_one", Alias: "chat", Status: StatusActive, Revision: 1, UpdatedAt: now}
	if err := repository.SyncSubject(context.Background(), user); err != nil {
		t.Fatalf("sync user subject: %v", err)
	}
	if err := repository.SyncModelSubject(context.Background(), model); err != nil {
		t.Fatalf("sync model subject: %v", err)
	}
	return store, repository, user, model
}

func projectedTestKey(id string, subject KeySubject, modelIDs ...string) VirtualKey {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	return VirtualKey{
		ID: id, Name: id, UserID: subject.UserID, Prefix: "gwai_test", KeyHash: id,
		ModelIDs: append([]string(nil), modelIDs...), Status: StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestModelFenceConflictsUntilLastVirtualKeyIsDeleted(t *testing.T) {
	store, repository, user, model := modelProjectionFixture(t)
	ctx := context.Background()
	key := projectedTestKey("key_one", user, model.ModelID)
	if err := repository.CreateVirtualKey(ctx, key); err != nil {
		t.Fatalf("create key: %v", err)
	}
	index, _, err := readIndex(ctx, store, virtualKeysByModelIndexKey(model.ModelID))
	if err != nil || len(index.IDs) != 1 || index.IDs[0] != key.ID {
		t.Fatalf("unexpected reverse index %#v, err=%v", index, err)
	}

	fence := model
	fence.Status = StatusDisabled
	fence.Deleted = true
	fence.Revision++
	fence.UpdatedAt = fence.UpdatedAt.Add(time.Minute)
	if err := repository.FenceModelSubject(ctx, fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected referenced model fence conflict, got %v", err)
	}
	if err := repository.DeleteVirtualKey(ctx, key); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if err := repository.FenceModelSubject(ctx, fence); err != nil {
		t.Fatalf("fence unreferenced model: %v", err)
	}
	if err := repository.FenceModelSubject(ctx, fence); err != nil {
		t.Fatalf("repeat model fence idempotently: %v", err)
	}
	if err := repository.CreateVirtualKey(ctx, projectedTestKey("key_two", user, model.ModelID)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected tombstone to reject a new key, got %v", err)
	}
}

func TestReplacingVirtualKeyMovesModelReverseIndexesAtomically(t *testing.T) {
	store, repository, user, first := modelProjectionFixture(t)
	ctx := context.Background()
	second := ModelSubject{
		ModelID: "mdl_two", Alias: "reasoning", Status: StatusActive, Revision: 1,
		UpdatedAt: first.UpdatedAt,
	}
	if err := repository.SyncModelSubject(ctx, second); err != nil {
		t.Fatalf("sync second model: %v", err)
	}
	oldKey := projectedTestKey("key_one", user, first.ModelID)
	if err := repository.CreateVirtualKey(ctx, oldKey); err != nil {
		t.Fatalf("create key: %v", err)
	}
	newKey := oldKey
	newKey.ModelIDs = []string{second.ModelID}
	newKey.UpdatedAt = newKey.UpdatedAt.Add(time.Minute)
	if err := repository.ReplaceVirtualKey(ctx, oldKey, newKey); err != nil {
		t.Fatalf("replace key: %v", err)
	}
	firstIndex, _, err := readIndex(ctx, store, virtualKeysByModelIndexKey(first.ModelID))
	if err != nil || len(firstIndex.IDs) != 0 {
		t.Fatalf("old model index was not emptied: %#v, err=%v", firstIndex, err)
	}
	secondIndex, _, err := readIndex(ctx, store, virtualKeysByModelIndexKey(second.ModelID))
	if err != nil || len(secondIndex.IDs) != 1 || secondIndex.IDs[0] != newKey.ID {
		t.Fatalf("new model index was not populated: %#v, err=%v", secondIndex, err)
	}
}

func TestActiveVirtualKeyRequiresActiveProjectedModel(t *testing.T) {
	_, repository, user, model := modelProjectionFixture(t)
	ctx := context.Background()
	disabled := model
	disabled.Status = StatusDisabled
	disabled.Revision++
	disabled.UpdatedAt = disabled.UpdatedAt.Add(time.Minute)
	if err := repository.SyncModelSubject(ctx, disabled); err != nil {
		t.Fatalf("disable model subject: %v", err)
	}
	activeKey := projectedTestKey("key_active", user, model.ModelID)
	if err := repository.CreateVirtualKey(ctx, activeKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected active key to be rejected, got %v", err)
	}
	disabledKey := activeKey
	disabledKey.ID = "key_disabled"
	disabledKey.KeyHash = "key_disabled"
	disabledKey.Status = StatusDisabled
	if err := repository.CreateVirtualKey(ctx, disabledKey); err != nil {
		t.Fatalf("disabled key may retain a disabled model: %v", err)
	}
}

func TestDeletedModelSubjectCannotBeReactivated(t *testing.T) {
	_, repository, _, model := modelProjectionFixture(t)
	ctx := context.Background()
	fence := model
	fence.Status = StatusDisabled
	fence.Deleted = true
	fence.Revision++
	fence.UpdatedAt = fence.UpdatedAt.Add(time.Minute)
	if err := repository.FenceModelSubject(ctx, fence); err != nil {
		t.Fatalf("fence model: %v", err)
	}
	reactivate := fence
	reactivate.Status = StatusActive
	reactivate.Deleted = false
	reactivate.Revision++
	reactivate.UpdatedAt = reactivate.UpdatedAt.Add(time.Minute)
	if err := repository.SyncModelSubject(ctx, reactivate); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected permanent tombstone, got %v", err)
	}
}

func TestModelFenceAndKeyCreationCannotBothCommit(t *testing.T) {
	store := &transactionBarrierStore{Store: state.NewMemoryStore(), release: make(chan struct{})}
	setup := NewVirtualKeyRepository(store)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	user := KeySubject{UserID: "usr_test", Status: StatusActive, Revision: 1, UpdatedAt: now}
	model := ModelSubject{ModelID: "mdl_test", Alias: "chat", Status: StatusActive, Revision: 1, UpdatedAt: now}
	if err := setup.SyncSubject(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if err := setup.SyncModelSubject(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	creator := NewVirtualKeyRepository(store)
	fencer := NewVirtualKeyRepository(store)
	fence := model
	fence.Status = StatusDisabled
	fence.Deleted = true
	fence.Revision++
	fence.UpdatedAt = fence.UpdatedAt.Add(time.Minute)
	key := projectedTestKey("key_race", user, model.ModelID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	store.enabled.Store(true)
	createResult := make(chan error, 1)
	fenceResult := make(chan error, 1)
	go func() { createResult <- creator.CreateVirtualKey(ctx, key) }()
	go func() { fenceResult <- fencer.FenceModelSubject(ctx, fence) }()
	createErr := <-createResult
	fenceErr := <-fenceResult
	if (createErr == nil) == (fenceErr == nil) {
		t.Fatalf("exactly one operation must commit; create=%v fence=%v", createErr, fenceErr)
	}
	if createErr != nil && !errors.Is(createErr, ErrConflict) {
		t.Fatalf("unexpected create result: %v", createErr)
	}
	if fenceErr != nil && !errors.Is(fenceErr, ErrConflict) {
		t.Fatalf("unexpected fence result: %v", fenceErr)
	}
}
