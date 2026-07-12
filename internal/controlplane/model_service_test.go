package controlplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

type recordingModelRegistry struct {
	mu                   sync.Mutex
	subject              ModelSubject
	hasSubject           bool
	failNextSync         bool
	failFenceAfterCommit bool
}

func (r *recordingModelRegistry) SyncModel(_ context.Context, subject ModelSubject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNextSync {
		r.failNextSync = false
		return errors.New("injected model sync failure")
	}
	r.subject = subject
	r.hasSubject = true
	return nil
}

func (r *recordingModelRegistry) FenceModel(_ context.Context, subject ModelSubject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subject = subject
	r.hasSubject = true
	if r.failFenceAfterCommit {
		r.failFenceAfterCommit = false
		return errors.New("injected ambiguous model fence response")
	}
	return nil
}

func newModelTestService(t *testing.T) (*ResourceService, *ProviderRepository, *ModelRepository, *recordingModelRegistry, Provider) {
	t.Helper()
	providerStore := state.NewMemoryStore()
	providers := NewProviderRepository(providerStore)
	models := NewModelRepository(providerStore)
	registry := &recordingModelRegistry{}
	service := NewResourceService(NewUserRepository(state.NewMemoryStore()), providers, models, nil, registry)
	service.now = func() time.Time { return time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC) }
	provider, err := service.CreateProvider(context.Background(), ProviderInput{
		Slug: "anthropic", Name: "Anthropic", Kind: ProviderKindAnthropic, AdapterAppID: "anthropic-adapter",
		Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, providers, models, registry, provider
}

func TestModelServiceLifecycleAndProviderRestriction(t *testing.T) {
	service, _, models, registry, provider := newModelTestService(t)
	ctx := context.Background()
	created, err := service.CreateModel(ctx, ModelInput{
		Alias: " claude ", ProviderID: provider.ID, UpstreamModel: " claude-sonnet-4-6 ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Alias != "claude" || created.UpstreamModel != "claude-sonnet-4-6" ||
		created.Status != StatusActive || created.Revision != 1 {
		t.Fatalf("unexpected created model: %+v", created)
	}
	if !registry.hasSubject || registry.subject.ModelID != created.ID || registry.subject.Alias != created.Alias ||
		registry.subject.Status != StatusActive || registry.subject.Revision != 1 || registry.subject.Deleted {
		t.Fatalf("model projection was not synchronized: %+v", registry.subject)
	}
	if _, err := service.CreateModel(ctx, ModelInput{
		Alias: created.Alias, ProviderID: provider.ID, UpstreamModel: "other",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate alias returned %v", err)
	}
	if err := service.DeleteProvider(ctx, provider.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("provider with models must be restricted, got %v", err)
	}

	other, err := service.CreateProvider(ctx, ProviderInput{
		Slug: "backup", Name: "Backup", Kind: ProviderKindAnthropic, AdapterAppID: "backup-adapter",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.UpdateModel(ctx, created.ID, ModelInput{
		Alias: created.Alias, ProviderID: other.ID, UpstreamModel: "claude-backup", Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProviderID != other.ID || updated.Revision != 2 {
		t.Fatalf("model was not moved: %+v", updated)
	}
	oldModels, err := models.ListModelsByProvider(ctx, provider.ID)
	if err != nil || len(oldModels) != 0 {
		t.Fatalf("old provider index was not cleared: models=%+v err=%v", oldModels, err)
	}
	newModels, err := models.ListModelsByProvider(ctx, other.ID)
	if err != nil || len(newModels) != 1 || newModels[0].ID != created.ID {
		t.Fatalf("new provider index was not populated: models=%+v err=%v", newModels, err)
	}
	if _, err := service.UpdateModel(ctx, created.ID, ModelInput{
		Alias: "renamed", ProviderID: other.ID, UpstreamModel: updated.UpstreamModel, Status: StatusActive,
	}); err == nil {
		t.Fatal("model alias must be immutable")
	}
	if _, err := service.UpdateModel(ctx, created.ID, ModelInput{
		Alias: created.Alias, ProviderID: other.ID, UpstreamModel: "bad\nmodel", Status: StatusActive,
	}); err == nil {
		t.Fatal("upstream model with a newline must be rejected")
	}
	if _, err := service.UpdateModel(ctx, created.ID, ModelInput{
		Alias: created.Alias, ProviderID: other.ID, UpstreamModel: updated.UpstreamModel,
	}); err == nil {
		t.Fatal("model PUT must require an explicit status")
	}
	if err := service.DeleteModel(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if !registry.subject.Deleted || registry.subject.Status != StatusDisabled || registry.subject.Revision != 3 {
		t.Fatalf("model deletion was not fenced: %+v", registry.subject)
	}
	if _, err := service.GetModel(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted model remains available: %v", err)
	}
	if err := service.DeleteProvider(ctx, other.ID); err != nil {
		t.Fatalf("provider remained restricted after model deletion: %v", err)
	}
}

func TestActiveModelRequiresActiveProvider(t *testing.T) {
	service, _, _, _, _ := newModelTestService(t)
	disabled, err := service.CreateProvider(context.Background(), ProviderInput{
		Slug: "disabled", Name: "Disabled", Kind: ProviderKindAnthropic, AdapterAppID: "disabled-adapter",
		Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateModel(context.Background(), ModelInput{
		Alias: "active", ProviderID: disabled.ID, UpstreamModel: "upstream", Status: StatusActive,
	}); err == nil {
		t.Fatal("active model on a disabled provider must be rejected")
	}
	if _, err := service.CreateModel(context.Background(), ModelInput{
		Alias: "disabled", ProviderID: disabled.ID, UpstreamModel: "upstream", Status: StatusDisabled,
	}); err != nil {
		t.Fatalf("disabled model on a disabled provider should be valid: %v", err)
	}
}

func TestModelProjectionFailuresRemainFailClosedAndRepairable(t *testing.T) {
	service, _, _, registry, provider := newModelTestService(t)
	ctx := context.Background()
	registry.failNextSync = true
	if _, err := service.CreateModel(ctx, ModelInput{
		Alias: "claude", ProviderID: provider.ID, UpstreamModel: "claude", Status: StatusActive,
	}); err == nil {
		t.Fatal("injected create projection failure must reach the caller")
	}
	models, err := service.ListModels(ctx)
	if err != nil || len(models) != 1 {
		t.Fatalf("canonical model must remain available for repair: models=%+v err=%v", models, err)
	}
	repaired, err := service.UpdateModel(ctx, models[0].ID, ModelInput{
		Alias: models[0].Alias, ProviderID: provider.ID, UpstreamModel: models[0].UpstreamModel, Status: StatusActive,
	})
	if err != nil {
		t.Fatalf("PUT did not repair model projection: %v", err)
	}
	if repaired.Revision != 2 || registry.subject.Revision != 2 || registry.subject.Status != StatusActive {
		t.Fatalf("repair did not advance synchronized revision: model=%+v subject=%+v", repaired, registry.subject)
	}

	registry.failNextSync = true
	if _, err := service.UpdateModel(ctx, repaired.ID, ModelInput{
		Alias: repaired.Alias, ProviderID: provider.ID, UpstreamModel: repaired.UpstreamModel, Status: StatusDisabled,
	}); err == nil {
		t.Fatal("disable projection failure must reach the caller")
	}
	canonical, err := service.GetModel(ctx, repaired.ID)
	if err != nil || canonical.Status != StatusActive || canonical.Revision != 2 {
		t.Fatalf("failed disable changed canonical state: model=%+v err=%v", canonical, err)
	}
	disabled, err := service.UpdateModel(ctx, repaired.ID, ModelInput{
		Alias: repaired.Alias, ProviderID: provider.ID, UpstreamModel: repaired.UpstreamModel, Status: StatusDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.failNextSync = true
	if _, err := service.UpdateModel(ctx, disabled.ID, ModelInput{
		Alias: disabled.Alias, ProviderID: provider.ID, UpstreamModel: disabled.UpstreamModel, Status: StatusActive,
	}); err == nil {
		t.Fatal("activation projection failure must reach the caller")
	}
	canonical, err = service.GetModel(ctx, disabled.ID)
	if err != nil || canonical.Status != StatusActive || canonical.Revision != disabled.Revision+1 {
		t.Fatalf("activation must persist canonical state before sync: model=%+v err=%v", canonical, err)
	}
	if registry.subject.Status != StatusDisabled {
		t.Fatalf("failed activation unexpectedly widened projected access: %+v", registry.subject)
	}
}

func TestAmbiguousModelFenceCanBeRetried(t *testing.T) {
	service, _, _, registry, provider := newModelTestService(t)
	ctx := context.Background()
	model, err := service.CreateModel(ctx, ModelInput{
		Alias: "claude", ProviderID: provider.ID, UpstreamModel: "claude", Status: StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.failFenceAfterCommit = true
	if err := service.DeleteModel(ctx, model.ID); err == nil {
		t.Fatal("ambiguous fence response must reach the caller")
	}
	if _, err := service.GetModel(ctx, model.ID); err != nil {
		t.Fatalf("canonical model should remain after ambiguous fence: %v", err)
	}
	if err := service.DeleteModel(ctx, model.ID); err != nil {
		t.Fatalf("delete retry failed: %v", err)
	}
}
