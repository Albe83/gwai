package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

func syncTestModelSubject(t *testing.T, repository *VirtualKeyRepository, id string, now time.Time) {
	t.Helper()
	if err := repository.SyncModelSubject(context.Background(), ModelSubject{
		ModelID: id, Alias: id, Status: StatusActive, Revision: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVirtualKeyRepositorySubjectRevisionsAndTombstone(t *testing.T) {
	repository := NewVirtualKeyRepository(state.NewMemoryStore())
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	active := KeySubject{UserID: "usr_revision", Status: StatusActive, Revision: 2, UpdatedAt: now}

	if err := repository.SyncSubject(ctx, active); err != nil {
		t.Fatal(err)
	}
	if err := repository.SyncSubject(ctx, active); err != nil {
		t.Fatalf("identical subject replay must be idempotent: %v", err)
	}
	replayed := active
	replayed.UpdatedAt = now.Add(time.Hour)
	if err := repository.SyncSubject(ctx, replayed); err != nil {
		t.Fatalf("same state and revision with a different observation time must be idempotent: %v", err)
	}
	persisted, err := repository.GetSubject(ctx, active.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.UpdatedAt.Equal(active.UpdatedAt) {
		t.Fatalf("idempotent replay rewrote the stored observation time: %v", persisted.UpdatedAt)
	}
	stale := active
	stale.Revision = 1
	if err := repository.SyncSubject(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale revision conflict, got %v", err)
	}
	different := active
	different.Status = StatusDisabled
	if err := repository.SyncSubject(ctx, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected same-revision payload conflict, got %v", err)
	}
	directTombstone := KeySubject{
		UserID: active.UserID, Status: StatusDisabled, Revision: 3, Deleted: true, UpdatedAt: now.Add(time.Minute),
	}
	if err := repository.SyncSubject(ctx, directTombstone); !errors.Is(err, ErrConflict) {
		t.Fatalf("SyncSubject must not bypass fencing, got %v", err)
	}
	if err := repository.FenceSubject(ctx, directTombstone); err != nil {
		t.Fatal(err)
	}
	if err := repository.FenceSubject(ctx, directTombstone); err != nil {
		t.Fatalf("identical fence replay must be idempotent: %v", err)
	}
	reactivated := KeySubject{
		UserID: active.UserID, Status: StatusActive, Revision: 4, UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := repository.SyncSubject(ctx, reactivated); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected tombstone reactivation conflict, got %v", err)
	}
	disabled := KeySubject{UserID: "usr_deleted_bit", Status: StatusDisabled, Revision: 5, UpdatedAt: now}
	if err := repository.SyncSubject(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	sameRevisionTombstone := disabled
	sameRevisionTombstone.Deleted = true
	if err := repository.FenceSubject(ctx, sameRevisionTombstone); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected same-revision deleted-bit conflict, got %v", err)
	}
}

func TestUserRepositoryRejectsStaleReplacement(t *testing.T) {
	store := state.NewMemoryStore()
	first := NewUserRepository(store)
	second := NewUserRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	user := User{
		ID: "usr_stale", Name: "Initial", Email: "initial@example.com", Status: StatusActive,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := first.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	firstUpdate := user
	firstUpdate.Name = "First"
	firstUpdate.Email = "first@example.com"
	firstUpdate.Revision++
	firstUpdate.UpdatedAt = now.Add(time.Minute)
	if err := first.ReplaceUser(ctx, user, firstUpdate); err != nil {
		t.Fatal(err)
	}
	staleUpdate := user
	staleUpdate.Name = "Stale"
	staleUpdate.Email = "stale@example.com"
	staleUpdate.Revision++
	staleUpdate.UpdatedAt = now.Add(2 * time.Minute)
	if err := second.ReplaceUser(ctx, user, staleUpdate); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected stale replacement conflict, got %v", err)
	}
	current, err := first.GetUser(ctx, user.ID)
	if err != nil || current.Email != firstUpdate.Email || current.Revision != firstUpdate.Revision {
		t.Fatalf("stale write changed current user: user=%+v err=%v", current, err)
	}
	if _, err := first.GetUserByEmail(ctx, staleUpdate.Email); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale email lookup was written: %v", err)
	}
}

func TestVirtualKeyRepositoryMaintainsUserIndexesAndFences(t *testing.T) {
	repository := NewVirtualKeyRepository(state.NewMemoryStore())
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for _, userID := range []string{"usr_first", "usr_second"} {
		if err := repository.SyncSubject(ctx, KeySubject{
			UserID: userID, Status: StatusActive, Revision: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	syncTestModelSubject(t, repository, "mdl_keys", now)
	key := VirtualKey{
		ID: "key_move", Name: "move", UserID: "usr_first", Prefix: "gwai_test", KeyHash: hashKey("gwai_test_secret"),
		ModelIDs: []string{"mdl_keys"}, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateVirtualKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	firstKeys, err := repository.ListVirtualKeysByUser(ctx, "usr_first")
	if err != nil || len(firstKeys) != 1 || firstKeys[0].ID != key.ID {
		t.Fatalf("unexpected first user index: keys=%+v err=%v", firstKeys, err)
	}
	firstFence := KeySubject{
		UserID: "usr_first", Status: StatusDisabled, Revision: 2, Deleted: true, UpdatedAt: now.Add(time.Minute),
	}
	if err := repository.FenceSubject(ctx, firstFence); !errors.Is(err, ErrConflict) {
		t.Fatalf("subject with a key must not be fenced, got %v", err)
	}

	moved := key
	moved.UserID = "usr_second"
	moved.UpdatedAt = now.Add(time.Minute)
	if err := repository.ReplaceVirtualKey(ctx, key, moved); err != nil {
		t.Fatal(err)
	}
	firstKeys, err = repository.ListVirtualKeysByUser(ctx, "usr_first")
	if err != nil || len(firstKeys) != 0 {
		t.Fatalf("old user index was not cleared: keys=%+v err=%v", firstKeys, err)
	}
	secondKeys, err := repository.ListVirtualKeysByUser(ctx, "usr_second")
	if err != nil || len(secondKeys) != 1 || secondKeys[0].ID != key.ID {
		t.Fatalf("new user index was not populated: keys=%+v err=%v", secondKeys, err)
	}
	if err := repository.FenceSubject(ctx, firstFence); err != nil {
		t.Fatalf("empty old subject should now be fenceable: %v", err)
	}
	disabledKey := moved
	disabledKey.ID = "key_deleted_subject"
	disabledKey.UserID = "usr_first"
	disabledKey.Status = StatusDisabled
	disabledKey.KeyHash = hashKey("another-secret")
	if err := repository.CreateVirtualKey(ctx, disabledKey); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted subject must not own even a disabled key, got %v", err)
	}

	secondFence := KeySubject{
		UserID: "usr_second", Status: StatusDisabled, Revision: 2, Deleted: true, UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := repository.FenceSubject(ctx, secondFence); !errors.Is(err, ErrConflict) {
		t.Fatalf("new subject with a key must not be fenced, got %v", err)
	}
	if err := repository.DeleteVirtualKey(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetVirtualKeyByHash(ctx, moved.KeyHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted hash lookup remains reachable: %v", err)
	}
	if err := repository.FenceSubject(ctx, secondFence); err != nil {
		t.Fatalf("subject should be fenceable after its final key is deleted: %v", err)
	}
}

type blockedKeyTransactionStore struct {
	base    *state.MemoryStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockedKeyTransactionStore) Get(ctx context.Context, key string) (state.Entry, error) {
	return s.base.Get(ctx, key)
}

func (s *blockedKeyTransactionStore) Transact(ctx context.Context, operations []state.Operation) error {
	block := false
	for _, operation := range operations {
		if operation.Type == state.Upsert && strings.HasPrefix(operation.Key, "virtual-keys/") {
			block = true
			break
		}
	}
	if block {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.base.Transact(ctx, operations)
}

func TestCreateVirtualKeyAndFenceCompeteOnSubjectETag(t *testing.T) {
	store := &blockedKeyTransactionStore{
		base: state.NewMemoryStore(), started: make(chan struct{}), release: make(chan struct{}),
	}
	creator := NewVirtualKeyRepository(store)
	fencer := NewVirtualKeyRepository(store) // Simulates another service replica.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	if err := creator.SyncSubject(ctx, KeySubject{
		UserID: "usr_race", Status: StatusActive, Revision: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	syncTestModelSubject(t, creator, "mdl_race", now)
	key := VirtualKey{
		ID: "key_race", Name: "race", UserID: "usr_race", Prefix: "gwai_race", KeyHash: hashKey("gwai_race_secret"),
		ModelIDs: []string{"mdl_race"}, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	createResult := make(chan error, 1)
	go func() { createResult <- creator.CreateVirtualKey(ctx, key) }()
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatal("key transaction did not reach the barrier")
	}
	tombstone := KeySubject{
		UserID: "usr_race", Status: StatusDisabled, Revision: 2, Deleted: true, UpdatedAt: now.Add(time.Minute),
	}
	if err := fencer.FenceSubject(ctx, tombstone); err != nil {
		t.Fatalf("fence should win while the create transaction is paused: %v", err)
	}
	close(store.release)
	if err := <-createResult; !errors.Is(err, ErrConflict) {
		t.Fatalf("stale create must lose its subject CAS, got %v", err)
	}
	keys, err := creator.ListVirtualKeys(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("losing create transaction left state behind: keys=%+v err=%v", keys, err)
	}
}

func TestMoveVirtualKeyAndTargetFenceCompeteOnSubjectETag(t *testing.T) {
	base := state.NewMemoryStore()
	setup := NewVirtualKeyRepository(base)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	for _, userID := range []string{"usr_move_source", "usr_move_target"} {
		if err := setup.SyncSubject(ctx, KeySubject{
			UserID: userID, Status: StatusActive, Revision: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	syncTestModelSubject(t, setup, "mdl_move", now)
	key := VirtualKey{
		ID: "key_move_race", Name: "move", UserID: "usr_move_source", Prefix: "gwai_move",
		KeyHash: hashKey("gwai_move_secret"), ModelIDs: []string{"mdl_move"}, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := setup.CreateVirtualKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	store := &blockedKeyTransactionStore{
		base: base, started: make(chan struct{}), release: make(chan struct{}),
	}
	mover := NewVirtualKeyRepository(store)
	fencer := NewVirtualKeyRepository(store)
	moved := key
	moved.UserID = "usr_move_target"
	moved.UpdatedAt = now.Add(time.Minute)
	moveResult := make(chan error, 1)
	go func() { moveResult <- mover.ReplaceVirtualKey(ctx, key, moved) }()
	select {
	case <-store.started:
	case <-ctx.Done():
		t.Fatal("move transaction did not reach the barrier")
	}
	tombstone := KeySubject{
		UserID: "usr_move_target", Status: StatusDisabled, Revision: 2, Deleted: true,
		UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := fencer.FenceSubject(ctx, tombstone); err != nil {
		t.Fatalf("target fence should win while move is paused: %v", err)
	}
	close(store.release)
	if err := <-moveResult; !errors.Is(err, ErrConflict) {
		t.Fatalf("stale move must lose target-subject CAS, got %v", err)
	}
	current, err := setup.GetVirtualKey(ctx, key.ID)
	if err != nil || current.UserID != key.UserID {
		t.Fatalf("losing move changed key owner: key=%+v err=%v", current, err)
	}
	sourceKeys, _ := setup.ListVirtualKeysByUser(ctx, key.UserID)
	targetKeys, _ := setup.ListVirtualKeysByUser(ctx, moved.UserID)
	if len(sourceKeys) != 1 || len(targetKeys) != 0 {
		t.Fatalf("losing move corrupted owner indexes: source=%+v target=%+v", sourceKeys, targetKeys)
	}
}

func TestGatewayRuntimeFailsClosedWithoutActiveSubject(t *testing.T) {
	keyStore := state.NewMemoryStore()
	keys := NewVirtualKeyRepository(keyStore)
	providerStore := state.NewMemoryStore()
	providers := NewProviderRepository(providerStore)
	models := NewModelRepository(providerStore)
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	provider := Provider{
		ID: "prv_runtime", Slug: "runtime", AdapterAppID: "runtime-adapter", Status: StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := providers.CreateProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	model := Model{
		ID: "mdl_runtime", Alias: "runtime-model", ProviderID: provider.ID, UpstreamModel: "model",
		Status: StatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := models.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	if err := keys.SyncModelSubject(ctx, modelSubjectFor(model, false)); err != nil {
		t.Fatal(err)
	}
	if err := keys.SyncSubject(ctx, KeySubject{
		UserID: "usr_runtime", Status: StatusActive, Revision: 1, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	token := "gwai_runtime_secret"
	key := VirtualKey{
		ID: "key_runtime", Name: "runtime", UserID: "usr_runtime", Prefix: "gwai_runtime", KeyHash: hashKey(token),
		ModelIDs: []string{model.ID}, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := keys.CreateVirtualKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	runtime := NewGatewayRuntime(keys, models, providers)
	runtime.now = func() time.Time { return now }
	if _, err := runtime.Authorize(ctx, token, model.Alias); err != nil {
		t.Fatalf("active subject should authorize: %v", err)
	}
	if err := keyStore.Transact(ctx, []state.Operation{{Type: state.Delete, Key: keySubjectKey(key.UserID)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Authorize(ctx, token, model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing subject must fail closed, got %v", err)
	}
	resolved, err := NewProviderRuntime(providers).ResolveProviderBySlug(ctx, provider.Slug)
	if err != nil || resolved.ID != provider.ID {
		t.Fatalf("provider runtime did not resolve provider: provider=%+v err=%v", resolved, err)
	}
}
