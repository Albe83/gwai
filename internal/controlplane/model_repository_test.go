package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

func repositoryProvider(id, slug string, now time.Time) Provider {
	return Provider{
		ID: id, Slug: slug, Name: slug, Kind: ProviderKindAnthropic,
		AdapterAppID: slug + "-adapter", Status: StatusActive, CreatedAt: now, UpdatedAt: now,
	}
}

func TestModelRepositoryMaintainsAliasAndProviderIndexes(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	providers := NewProviderRepository(store)
	models := NewModelRepository(store)
	now := time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)
	first := repositoryProvider("prv_first", "first", now)
	second := repositoryProvider("prv_second", "second", now)
	for _, provider := range []Provider{first, second} {
		if err := providers.CreateProvider(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}
	model := Model{
		ID: "mdl_one", Alias: "client-model", ProviderID: first.ID, UpstreamModel: "upstream-one",
		Status: StatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := models.CreateModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	resolved, err := models.GetModelByAlias(ctx, model.Alias)
	if err != nil || resolved.ID != model.ID {
		t.Fatalf("alias lookup failed: model=%+v err=%v", resolved, err)
	}
	if err := models.CreateModel(ctx, Model{
		ID: "mdl_duplicate", Alias: model.Alias, ProviderID: first.ID, UpstreamModel: "other",
		Status: StatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate alias returned %v", err)
	}

	old := model
	model.ProviderID = second.ID
	model.UpstreamModel = "upstream-two"
	model.Revision++
	if err := models.ReplaceModel(ctx, old, model); err != nil {
		t.Fatal(err)
	}
	firstModels, _ := models.ListModelsByProvider(ctx, first.ID)
	secondModels, _ := models.ListModelsByProvider(ctx, second.ID)
	if len(firstModels) != 0 || len(secondModels) != 1 || secondModels[0].ID != model.ID {
		t.Fatalf("move corrupted provider indexes: first=%+v second=%+v", firstModels, secondModels)
	}
	stale := old
	stale.ProviderID = second.ID
	if err := models.ReplaceModel(ctx, stale, model); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected model returned %v", err)
	}
	if err := models.DeleteProviderIfNoModels(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("provider with a model was deleted: %v", err)
	}
	if err := models.DeleteProviderIfNoModels(ctx, first); err != nil {
		t.Fatalf("empty provider could not be deleted: %v", err)
	}
	if err := models.DeleteModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	if _, err := models.GetModelByAlias(ctx, model.Alias); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted alias lookup remains available: %v", err)
	}
}

func TestModelCreateAndProviderDeleteCannotCommitAnOrphan(t *testing.T) {
	ctx := context.Background()
	for iteration := 0; iteration < 50; iteration++ {
		store := state.NewMemoryStore()
		providers := NewProviderRepository(store)
		models := NewModelRepository(store)
		now := time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)
		provider := repositoryProvider(fmt.Sprintf("prv_%d", iteration), fmt.Sprintf("provider-%d", iteration), now)
		if err := providers.CreateProvider(ctx, provider); err != nil {
			t.Fatal(err)
		}
		model := Model{
			ID: fmt.Sprintf("mdl_%d", iteration), Alias: fmt.Sprintf("model-%d", iteration),
			ProviderID: provider.ID, UpstreamModel: "upstream", Status: StatusActive,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		var createErr, deleteErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			createErr = models.CreateModel(ctx, model)
		}()
		go func() {
			defer wait.Done()
			deleteErr = models.DeleteProviderIfNoModels(ctx, provider)
		}()
		wait.Wait()

		persistedModel, modelErr := models.GetModel(ctx, model.ID)
		persistedProvider, providerErr := providers.GetProvider(ctx, provider.ID)
		switch {
		case createErr == nil:
			if !errors.Is(deleteErr, ErrConflict) || modelErr != nil || providerErr != nil || persistedModel.ProviderID != persistedProvider.ID {
				t.Fatalf("create winner produced inconsistent state: create=%v delete=%v model=%+v/%v provider=%+v/%v", createErr, deleteErr, persistedModel, modelErr, persistedProvider, providerErr)
			}
		case deleteErr == nil:
			if !errors.Is(createErr, ErrNotFound) && !errors.Is(createErr, ErrConflict) {
				t.Fatalf("delete winner returned unexpected create error: %v", createErr)
			}
			if !errors.Is(modelErr, ErrNotFound) || !errors.Is(providerErr, ErrNotFound) {
				t.Fatalf("delete winner left state behind: model=%+v/%v provider=%+v/%v", persistedModel, modelErr, persistedProvider, providerErr)
			}
		default:
			t.Fatalf("neither concurrent operation committed: create=%v delete=%v", createErr, deleteErr)
		}
	}
}

func TestModelMoveAndTargetProviderDeleteCannotCommitAnOrphan(t *testing.T) {
	ctx := context.Background()
	for iteration := 0; iteration < 50; iteration++ {
		store := state.NewMemoryStore()
		providers := NewProviderRepository(store)
		models := NewModelRepository(store)
		now := time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)
		source := repositoryProvider(fmt.Sprintf("prv_source_%d", iteration), fmt.Sprintf("source-%d", iteration), now)
		target := repositoryProvider(fmt.Sprintf("prv_target_%d", iteration), fmt.Sprintf("target-%d", iteration), now)
		for _, provider := range []Provider{source, target} {
			if err := providers.CreateProvider(ctx, provider); err != nil {
				t.Fatal(err)
			}
		}
		model := Model{
			ID: fmt.Sprintf("mdl_move_%d", iteration), Alias: fmt.Sprintf("move-%d", iteration),
			ProviderID: source.ID, UpstreamModel: "upstream", Status: StatusActive,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := models.CreateModel(ctx, model); err != nil {
			t.Fatal(err)
		}
		moved := model
		moved.ProviderID = target.ID
		moved.Revision++
		var moveErr, deleteErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			moveErr = models.ReplaceModel(ctx, model, moved)
		}()
		go func() {
			defer wait.Done()
			deleteErr = models.DeleteProviderIfNoModels(ctx, target)
		}()
		wait.Wait()

		persisted, modelErr := models.GetModel(ctx, model.ID)
		_, targetErr := providers.GetProvider(ctx, target.ID)
		switch {
		case moveErr == nil:
			if !errors.Is(deleteErr, ErrConflict) || modelErr != nil || targetErr != nil || persisted.ProviderID != target.ID {
				t.Fatalf("move winner produced inconsistent state: move=%v delete=%v model=%+v/%v target=%v", moveErr, deleteErr, persisted, modelErr, targetErr)
			}
		case deleteErr == nil:
			if !errors.Is(moveErr, ErrNotFound) && !errors.Is(moveErr, ErrConflict) {
				t.Fatalf("delete winner returned unexpected move error: %v", moveErr)
			}
			if modelErr != nil || persisted.ProviderID != source.ID || !errors.Is(targetErr, ErrNotFound) {
				t.Fatalf("delete winner orphaned or moved model: model=%+v/%v target=%v", persisted, modelErr, targetErr)
			}
		default:
			t.Fatalf("neither concurrent operation committed: move=%v delete=%v", moveErr, deleteErr)
		}
	}
}
