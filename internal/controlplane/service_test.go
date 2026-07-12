package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

type testControlPlanes struct {
	resources *ResourceService
	keys      *VirtualKeyService
	gateway   *GatewayRuntime
	users     *UserRepository
	providers *ProviderRepository
	models    *ModelRepository
	keyRepo   *VirtualKeyRepository
}

type flakySubjectRegistry struct {
	target               SubjectRegistry
	failSync             bool
	failFenceAfterCommit bool
}

func (r *flakySubjectRegistry) SyncSubject(ctx context.Context, subject KeySubject) error {
	if r.failSync {
		r.failSync = false
		return errors.New("injected subject sync failure")
	}
	return r.target.SyncSubject(ctx, subject)
}

func (r *flakySubjectRegistry) FenceSubject(ctx context.Context, subject KeySubject) error {
	if err := r.target.FenceSubject(ctx, subject); err != nil {
		return err
	}
	if r.failFenceAfterCommit {
		r.failFenceAfterCommit = false
		return errors.New("injected ambiguous fence response")
	}
	return nil
}

func newTestControlPlanes() testControlPlanes {
	// Deliberately distinct stores: a test accidentally reading the old shared
	// registry can no longer pass by coincidence.
	users := NewUserRepository(state.NewMemoryStore())
	providerStore := state.NewMemoryStore()
	providers := NewProviderRepository(providerStore)
	models := NewModelRepository(providerStore)
	keyRepo := NewVirtualKeyRepository(state.NewMemoryStore())
	keys := NewVirtualKeyService(keyRepo)
	resources := NewResourceService(users, providers, models, keys, keys)
	now := func() time.Time { return time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC) }
	resources.now = now
	keys.now = now
	gateway := NewGatewayRuntime(keyRepo, models, providers)
	gateway.now = now
	return testControlPlanes{
		resources: resources, keys: keys, gateway: gateway,
		users: users, providers: providers, models: models, keyRepo: keyRepo,
	}
}

func provisionTestRoute(t *testing.T, planes testControlPlanes) (User, Provider, Model) {
	t.Helper()
	ctx := context.Background()
	user, err := planes.resources.CreateUser(ctx, UserInput{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := planes.resources.CreateProvider(ctx, ProviderInput{
		Slug: "anthropic-primary", Name: "Anthropic primary", Kind: ProviderKindAnthropic,
		AdapterAppID: "gwai-anthropic-primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := planes.resources.CreateModel(ctx, ModelInput{
		Alias: "assistant", ProviderID: provider.ID, UpstreamModel: "claude/sonnet-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, provider, model
}

func TestSplitControlPlaneLifecycleAndAuthorization(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, provider, model := provisionTestRoute(t, planes)

	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "integration", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.VirtualKey.Prefix == "" {
		t.Fatal("created key did not return its one-time secret and prefix")
	}
	persisted, err := planes.keyRepo.GetVirtualKey(ctx, created.VirtualKey.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(created.VirtualKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || persisted.KeyHash == "" || persisted.KeyHash == created.Key {
		t.Fatal("virtual key must be persisted as a one-way hash")
	}

	authorization, err := planes.gateway.Authorize(ctx, created.Key, model.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.UserID != user.ID || authorization.KeyID != created.VirtualKey.ID {
		t.Fatalf("unexpected authorization: %+v", authorization)
	}
	if _, err := planes.gateway.Authorize(ctx, "gwai_wrong", model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}

	route, err := planes.gateway.ResolveRoute(ctx, model.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if route.ModelID != model.ID || route.Alias != model.Alias || route.ProviderID != provider.ID || route.UpstreamModel != "claude/sonnet-test" || route.AdapterAppID != provider.AdapterAppID {
		t.Fatalf("unexpected route: %+v", route)
	}
	if err := planes.resources.DeleteUser(ctx, user.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected user dependency conflict, got %v", err)
	}
	if err := planes.keys.DeleteVirtualKey(ctx, created.VirtualKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := planes.resources.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deleted key/subject must be unauthorized, got %v", err)
	}
}

func TestModelWithoutUpstreamOverrideUsesAliasAtRuntime(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	provider, err := planes.resources.CreateProvider(ctx, ProviderInput{
		Slug: "openai", Name: "OpenAI", Kind: ProviderKindOpenAIResponses, AdapterAppID: "openai-adapter",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := planes.resources.CreateModel(ctx, ModelInput{
		Alias: "public-model", ProviderID: provider.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.UpstreamModel != "" {
		t.Fatalf("optional upstream override was persisted as %q", model.UpstreamModel)
	}
	route, err := planes.gateway.ResolveRoute(ctx, model.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if route.UpstreamModel != model.Alias || route.AdapterAppID != provider.AdapterAppID {
		t.Fatalf("route without override = %+v", route)
	}

	model, err = planes.resources.UpdateModel(ctx, model.ID, ModelInput{
		Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: "real-model", Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err = planes.gateway.ResolveRoute(ctx, model.Alias)
	if err != nil || route.UpstreamModel != "real-model" {
		t.Fatalf("route with override = %+v, %v", route, err)
	}

	model, err = planes.resources.UpdateModel(ctx, model.ID, ModelInput{
		Alias: model.Alias, ProviderID: provider.ID, Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if model.UpstreamModel != "" {
		t.Fatalf("cleared upstream override was persisted as %q", model.UpstreamModel)
	}
	route, err = planes.gateway.ResolveRoute(ctx, model.Alias)
	if err != nil || route.UpstreamModel != model.Alias {
		t.Fatalf("route after clearing override = %+v, %v", route, err)
	}
}

func TestModelLifecycleRevokesRoutingAndRestrictsDeletion(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, provider, model := provisionTestRoute(t, planes)
	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "client", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := planes.resources.DeleteModel(ctx, model.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced model deletion returned %v, want conflict", err)
	}
	disabled, err := planes.resources.UpdateModel(ctx, model.ID, ModelInput{
		Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: model.UpstreamModel, Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled model remained authorized: %v", err)
	}
	active, err := planes.resources.UpdateModel(ctx, model.ID, ModelInput{
		Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: model.UpstreamModel, Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if active.Revision != disabled.Revision+1 {
		t.Fatalf("model revision did not advance: disabled=%d active=%d", disabled.Revision, active.Revision)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); err != nil {
		t.Fatalf("reactivated model did not restore authorization: %v", err)
	}
	providerInput := ProviderInput{
		Slug: provider.Slug, Name: provider.Name, Kind: provider.Kind,
		AdapterAppID: provider.AdapterAppID, Status: StatusDisabled,
	}
	if _, err := planes.resources.UpdateProvider(ctx, provider.ID, providerInput); err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.ResolveRoute(ctx, model.Alias); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled provider remained routable: %v", err)
	}
	providerInput.Status = StatusActive
	if _, err := planes.resources.UpdateProvider(ctx, provider.ID, providerInput); err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.ResolveRoute(ctx, model.Alias); err != nil {
		t.Fatalf("reactivated provider did not restore routing: %v", err)
	}
	if err := planes.keys.DeleteVirtualKey(ctx, created.VirtualKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := planes.resources.DeleteModel(ctx, model.ID); err != nil {
		t.Fatalf("unreferenced model deletion failed: %v", err)
	}
	if err := planes.resources.DeleteProvider(ctx, provider.ID); err != nil {
		t.Fatalf("empty provider deletion failed: %v", err)
	}
}

func TestGatewayRejectsMismatchedModelProjectionRevision(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, _, model := provisionTestRoute(t, planes)
	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "client", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	subject, err := planes.keyRepo.GetModelSubject(ctx, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	subject.Revision++
	subject.UpdatedAt = subject.UpdatedAt.Add(time.Minute)
	if err := planes.keyRepo.SyncModelSubject(ctx, subject); err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revision mismatch did not fail closed: %v", err)
	}
	repaired, err := planes.resources.UpdateModel(ctx, model.ID, ModelInput{
		Alias: model.Alias, ProviderID: model.ProviderID, UpstreamModel: model.UpstreamModel, Status: model.Status,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Revision != subject.Revision {
		t.Fatalf("repair did not converge revisions: model=%d subject=%d", repaired.Revision, subject.Revision)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); err != nil {
		t.Fatalf("repaired projection did not restore authorization: %v", err)
	}
}

func TestUserStatusProjectionRevokesAndRestoresKeys(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, _, model := provisionTestRoute(t, planes)
	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "client", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err = planes.resources.UpdateUser(ctx, user.ID, UserInput{
		Name: user.Name, Email: user.Email, Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled subject must revoke key, got %v", err)
	}
	_, err = planes.resources.UpdateUser(ctx, user.ID, UserInput{
		Name: user.Name, Email: user.Email, Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); err != nil {
		t.Fatalf("reactivated subject must restore key: %v", err)
	}
}

func TestUpdateUserRequiresExplicitStatus(t *testing.T) {
	planes := newTestControlPlanes()
	user, _, _ := provisionTestRoute(t, planes)
	disabled, err := planes.resources.UpdateUser(context.Background(), user.ID, UserInput{
		Name: user.Name, Email: user.Email, Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planes.resources.UpdateUser(context.Background(), user.ID, UserInput{
		Name: "Renamed", Email: user.Email,
	}); err == nil {
		t.Fatal("PUT without status must not implicitly reactivate a disabled user")
	}
	current, err := planes.resources.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusDisabled || current.Revision != disabled.Revision {
		t.Fatalf("invalid PUT changed disabled user: %+v", current)
	}
}

func TestFailedUserProjectionStaysFailClosedAndPUTRepairsIt(t *testing.T) {
	users := NewUserRepository(state.NewMemoryStore())
	providerStore := state.NewMemoryStore()
	providers := NewProviderRepository(providerStore)
	models := NewModelRepository(providerStore)
	keyRepo := NewVirtualKeyRepository(state.NewMemoryStore())
	keys := NewVirtualKeyService(keyRepo)
	flaky := &flakySubjectRegistry{target: keys, failSync: true}
	resources := NewResourceService(users, providers, models, flaky, keys)

	if _, err := resources.CreateUser(context.Background(), UserInput{Name: "Ada", Email: "ada@example.com"}); err == nil {
		t.Fatal("injected subject synchronization failure must reach the caller")
	}
	persisted, err := users.ListUsers(context.Background())
	if err != nil || len(persisted) != 1 {
		t.Fatalf("canonical user must remain available for repair: users=%+v err=%v", persisted, err)
	}
	provider, err := resources.CreateProvider(context.Background(), ProviderInput{
		Slug: "provider", Name: "Provider", Kind: ProviderKindAnthropic, AdapterAppID: "provider-adapter",
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := resources.CreateModel(context.Background(), ModelInput{
		Alias: "assistant", ProviderID: provider.ID, UpstreamModel: "claude-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.CreateVirtualKey(context.Background(), VirtualKeyInput{
		Name: "must fail", UserID: persisted[0].ID, ModelIDs: []string{model.ID},
	}); err == nil {
		t.Fatal("missing subject projection must fail key creation closed")
	}
	repaired, err := resources.UpdateUser(context.Background(), persisted[0].ID, UserInput{
		Name: persisted[0].Name, Email: persisted[0].Email, Status: persisted[0].Status,
	})
	if err != nil {
		t.Fatalf("PUT did not repair the projection: %v", err)
	}
	if repaired.Revision != persisted[0].Revision+1 {
		t.Fatalf("repair did not advance revision: before=%d after=%d", persisted[0].Revision, repaired.Revision)
	}
	if _, err := keys.CreateVirtualKey(context.Background(), VirtualKeyInput{
		Name: "repaired", UserID: repaired.ID, ModelIDs: []string{model.ID},
	}); err != nil {
		t.Fatalf("repaired subject must accept key creation: %v", err)
	}
}

func TestFailedActivationLeavesAuthorizationDisabledUntilRetry(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, _, model := provisionTestRoute(t, planes)
	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "client", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := planes.resources.UpdateUser(ctx, user.ID, UserInput{
		Name: user.Name, Email: user.Email, Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	planes.resources.subjects = &flakySubjectRegistry{target: planes.keys, failSync: true}
	if _, err := planes.resources.UpdateUser(ctx, user.ID, UserInput{
		Name: disabled.Name, Email: disabled.Email, Status: StatusActive,
	}); err == nil {
		t.Fatal("injected activation synchronization failure must reach the caller")
	}
	canonical, err := planes.resources.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Status != StatusActive {
		t.Fatalf("activation ordering did not persist canonical state first: %+v", canonical)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("failed activation must remain unauthorized, got %v", err)
	}
	if _, err := planes.resources.UpdateUser(ctx, user.ID, UserInput{
		Name: canonical.Name, Email: canonical.Email, Status: StatusActive,
	}); err != nil {
		t.Fatalf("activation retry did not repair projection: %v", err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); err != nil {
		t.Fatalf("repaired activation did not restore authorization: %v", err)
	}
}

func TestAmbiguousFenceIsFailClosedAndDeleteRetryIsIdempotent(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, _, model := provisionTestRoute(t, planes)
	planes.resources.subjects = &flakySubjectRegistry{
		target: planes.keys, failFenceAfterCommit: true,
	}
	if err := planes.resources.DeleteUser(ctx, user.ID); err == nil {
		t.Fatal("ambiguous fence response must reach the caller")
	}
	if _, err := planes.resources.GetUser(ctx, user.ID); err != nil {
		t.Fatalf("canonical user should remain after ambiguous fence: %v", err)
	}
	if _, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "must fail", UserID: user.ID, ModelIDs: []string{model.ID},
	}); err == nil {
		t.Fatal("committed fence must reject new keys")
	}
	if err := planes.resources.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("delete retry was not idempotent: %v", err)
	}
	if _, err := planes.resources.GetUser(ctx, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user remains after successful delete retry: %v", err)
	}
}

func TestServicesRejectDuplicateProviderAndDisallowedModel(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	user, provider, model := provisionTestRoute(t, planes)
	if _, err := planes.resources.CreateProvider(ctx, ProviderInput{
		Slug: provider.Slug, Name: "Duplicate", Kind: ProviderKindAnthropic, AdapterAppID: "another-adapter",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate slug conflict, got %v", err)
	}
	if _, err := planes.resources.CreateProvider(ctx, ProviderInput{
		Slug: "another-provider", Name: "Duplicate app ID", Kind: ProviderKindAnthropic, AdapterAppID: provider.AdapterAppID,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate app ID conflict, got %v", err)
	}
	otherModel, err := planes.resources.CreateModel(ctx, ModelInput{
		Alias: "other-model", ProviderID: provider.ID, UpstreamModel: "claude-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := planes.keys.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "limited", UserID: user.ID, ModelIDs: []string{model.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, otherModel.Alias); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden model, got %v", err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, "missing-model"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing model, got %v", err)
	}
	if _, err := planes.gateway.Authorize(ctx, created.Key, model.Alias); err != nil {
		t.Fatalf("expected selected model to be allowed, got %v", err)
	}
}

func TestProviderSlugAndAdapterAppIDAreImmutable(t *testing.T) {
	planes := newTestControlPlanes()
	_, provider, _ := provisionTestRoute(t, planes)
	input := ProviderInput{
		Slug: provider.Slug, Name: provider.Name, Kind: provider.Kind,
		AdapterAppID: provider.AdapterAppID, Status: provider.Status,
	}
	input.Slug = "renamed"
	if _, err := planes.resources.UpdateProvider(context.Background(), provider.ID, input); err == nil {
		t.Fatal("expected immutable slug validation error")
	}
	input.Slug = provider.Slug
	input.AdapterAppID = "renamed-adapter"
	if _, err := planes.resources.UpdateProvider(context.Background(), provider.ID, input); err == nil {
		t.Fatal("expected immutable adapter_app_id validation error")
	}
}

func TestServiceRejectsDuplicateUserEmailCaseInsensitively(t *testing.T) {
	planes := newTestControlPlanes()
	ctx := context.Background()
	if _, err := planes.resources.CreateUser(ctx, UserInput{Name: "First", Email: "person@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := planes.resources.CreateUser(ctx, UserInput{Name: "Second", Email: "PERSON@example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate email conflict, got %v", err)
	}
}

func TestNormalizeProviderInputSupportsEveryAdapterKind(t *testing.T) {
	tests := []string{
		ProviderKindAnthropic,
		ProviderKindOpenAIChat,
		ProviderKindOpenAIResponses,
		ProviderKindGemini,
	}
	for _, kind := range tests {
		t.Run(kind, func(t *testing.T) {
			result, err := normalizeProviderInput(ProviderInput{
				Slug: "provider", Name: "Provider", Kind: kind, AdapterAppID: "provider-adapter",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Kind != kind || result.Status != StatusActive {
				t.Fatalf("unexpected normalized provider: %+v", result)
			}
		})
	}
}

func TestVirtualKeyRequiresKnownModelID(t *testing.T) {
	planes := newTestControlPlanes()
	user, _, _ := provisionTestRoute(t, planes)
	_, err := planes.keys.CreateVirtualKey(context.Background(), VirtualKeyInput{
		Name: "invalid", UserID: user.ID, ModelIDs: []string{"mdl_missing"},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "model_ids" {
		t.Fatalf("expected model_ids validation error, got %v", err)
	}
}
