package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/state"
)

func newTestService() (*Service, *Repository) {
	repository := NewRepository(state.NewMemoryStore())
	service := NewService(repository)
	service.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	return service, repository
}

func provisionTestRoute(t *testing.T, service *Service) (User, Provider, Model) {
	t.Helper()
	ctx := context.Background()
	user, err := service.CreateUser(ctx, UserInput{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.CreateProvider(ctx, ProviderInput{
		Name: "Anthropic primary", Kind: "anthropic",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic", Key: "api-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	model, err := service.CreateModel(ctx, ModelInput{
		Alias: "claude", ProviderID: provider.ID, UpstreamModel: "claude-sonnet-test", MaxOutputTokens: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, provider, model
}

func TestServiceLifecycleAndAuthorization(t *testing.T) {
	service, repository := newTestService()
	ctx := context.Background()
	user, provider, model := provisionTestRoute(t, service)

	created, err := service.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "integration", UserID: user.ID, AllowedModels: []string{model.Alias},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.VirtualKey.Prefix == "" {
		t.Fatal("created key did not return its one-time secret and prefix")
	}
	persisted, err := repository.GetVirtualKey(ctx, created.VirtualKey.ID)
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

	authorization, err := service.Authorize(ctx, created.Key, model.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.UserID != user.ID || authorization.KeyID != created.VirtualKey.ID {
		t.Fatalf("unexpected authorization: %+v", authorization)
	}
	if _, err := service.Authorize(ctx, "gwai_wrong", model.Alias); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}

	route, err := service.ResolveRoute(ctx, model.Alias)
	if err != nil {
		t.Fatal(err)
	}
	if route.ProviderID != provider.ID || route.UpstreamModel != model.UpstreamModel || route.AdapterAppID != "gwai-anthropic-adapter" {
		t.Fatalf("unexpected route: %+v", route)
	}

	if err := service.DeleteUser(ctx, user.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected user dependency conflict, got %v", err)
	}
	if err := service.DeleteProvider(ctx, provider.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected provider dependency conflict, got %v", err)
	}
	if err := service.DeleteModel(ctx, model.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected model dependency conflict, got %v", err)
	}
	if err := service.DeleteVirtualKey(ctx, created.VirtualKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteModel(ctx, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteProvider(ctx, provider.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsDuplicateModelAliasAndDisallowedModel(t *testing.T) {
	service, _ := newTestService()
	ctx := context.Background()
	user, provider, model := provisionTestRoute(t, service)
	if _, err := service.CreateModel(ctx, ModelInput{
		Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: "other", MaxOutputTokens: 10,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate alias conflict, got %v", err)
	}
	other, err := service.CreateModel(ctx, ModelInput{
		Alias: "claude-other", ProviderID: provider.ID, UpstreamModel: "other", MaxOutputTokens: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateVirtualKey(ctx, VirtualKeyInput{Name: "limited", UserID: user.ID, AllowedModels: []string{model.Alias}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authorize(ctx, created.Key, other.Alias); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden model, got %v", err)
	}
}

func TestServiceRejectsDuplicateUserEmailCaseInsensitively(t *testing.T) {
	service, _ := newTestService()
	ctx := context.Background()
	if _, err := service.CreateUser(ctx, UserInput{Name: "First", Email: "person@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateUser(ctx, UserInput{Name: "Second", Email: "PERSON@example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate email conflict, got %v", err)
	}
}

func TestServiceRejectsAmbiguousProviderURL(t *testing.T) {
	service, _ := newTestService()
	_, err := service.CreateProvider(context.Background(), ProviderInput{
		Name: "Anthropic", Kind: "anthropic", BaseURL: "https://user@example.com/api?token=secret",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic"},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "base_url" {
		t.Fatalf("expected base_url validation error, got %v", err)
	}
}
