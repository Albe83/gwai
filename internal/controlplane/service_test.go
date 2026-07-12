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

func newTestService() (*Service, *Runtime, *Repository) {
	repository := NewRepository(state.NewMemoryStore())
	service := NewService(repository)
	service.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	runtime := NewRuntime(repository)
	runtime.now = service.now
	return service, runtime, repository
}

func provisionTestRoute(t *testing.T, service *Service) (User, Provider, string) {
	t.Helper()
	ctx := context.Background()
	user, err := service.CreateUser(ctx, UserInput{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := service.CreateProvider(ctx, ProviderInput{
		Slug: "anthropic-primary", Name: "Anthropic primary", Kind: "anthropic",
		AdapterAppID: "gwai-anthropic-primary",
		SecretRef:    daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic", Key: "api-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return user, provider, "anthropic-primary/claude/sonnet-test"
}

func TestServiceLifecycleAndLocalAuthorization(t *testing.T) {
	service, runtime, repository := newTestService()
	ctx := context.Background()
	user, provider, qualifiedModel := provisionTestRoute(t, service)

	created, err := service.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "integration", UserID: user.ID, AllowedModels: []string{qualifiedModel},
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

	authorization, err := runtime.Authorize(ctx, created.Key, qualifiedModel)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.UserID != user.ID || authorization.KeyID != created.VirtualKey.ID {
		t.Fatalf("unexpected authorization: %+v", authorization)
	}
	if _, err := runtime.Authorize(ctx, "gwai_wrong", qualifiedModel); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}

	route, err := runtime.ResolveRoute(ctx, qualifiedModel)
	if err != nil {
		t.Fatal(err)
	}
	if route.ProviderID != provider.ID || route.UpstreamModel != "claude/sonnet-test" || route.AdapterAppID != provider.AdapterAppID {
		t.Fatalf("unexpected route: %+v", route)
	}

	if err := service.DeleteUser(ctx, user.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected user dependency conflict, got %v", err)
	}
	if err := service.DeleteProvider(ctx, provider.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ResolveRoute(ctx, qualifiedModel); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted provider to be unavailable, got %v", err)
	}
	if err := service.DeleteVirtualKey(ctx, created.VirtualKey.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRejectsDuplicateProviderSlugAndDisallowedModel(t *testing.T) {
	service, runtime, _ := newTestService()
	ctx := context.Background()
	user, provider, qualifiedModel := provisionTestRoute(t, service)
	if _, err := service.CreateProvider(ctx, ProviderInput{
		Slug: provider.Slug, Name: "Duplicate", Kind: "anthropic", AdapterAppID: "another-adapter",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "other"},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate slug conflict, got %v", err)
	}
	if _, err := service.CreateProvider(ctx, ProviderInput{
		Slug: "another-provider", Name: "Duplicate app ID", Kind: "anthropic", AdapterAppID: provider.AdapterAppID,
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "other"},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate adapter app ID conflict, got %v", err)
	}
	created, err := service.CreateVirtualKey(ctx, VirtualKeyInput{
		Name: "limited", UserID: user.ID, AllowedModels: []string{qualifiedModel},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Authorize(ctx, created.Key, provider.Slug+"/another-model"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected forbidden model, got %v", err)
	}
	if _, err := runtime.Authorize(ctx, created.Key, qualifiedModel); err != nil {
		t.Fatalf("expected exact qualified model to be allowed, got %v", err)
	}
}

func TestProviderSlugAndAdapterAppIDAreImmutable(t *testing.T) {
	service, _, _ := newTestService()
	_, provider, _ := provisionTestRoute(t, service)
	input := ProviderInput{
		Slug: provider.Slug, Name: provider.Name, Kind: provider.Kind, BaseURL: provider.BaseURL,
		APIVersion: provider.APIVersion, AdapterAppID: provider.AdapterAppID, SecretRef: provider.SecretRef,
		Status: provider.Status,
	}
	input.Slug = "renamed"
	if _, err := service.UpdateProvider(context.Background(), provider.ID, input); err == nil {
		t.Fatal("expected immutable slug validation error")
	}
	input.Slug = provider.Slug
	input.AdapterAppID = "renamed-adapter"
	if _, err := service.UpdateProvider(context.Background(), provider.ID, input); err == nil {
		t.Fatal("expected immutable adapter_app_id validation error")
	}
}

func TestServiceRejectsDuplicateUserEmailCaseInsensitively(t *testing.T) {
	service, _, _ := newTestService()
	ctx := context.Background()
	if _, err := service.CreateUser(ctx, UserInput{Name: "First", Email: "person@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateUser(ctx, UserInput{Name: "Second", Email: "PERSON@example.com"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate email conflict, got %v", err)
	}
}

func TestServiceRejectsAmbiguousProviderURL(t *testing.T) {
	service, _, _ := newTestService()
	_, err := service.CreateProvider(context.Background(), ProviderInput{
		Slug: "anthropic", Name: "Anthropic", Kind: "anthropic", AdapterAppID: "anthropic-adapter",
		BaseURL:   "https://user@example.com/api?token=secret",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic"},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "base_url" {
		t.Fatalf("expected base_url validation error, got %v", err)
	}
}

func TestNormalizeProviderInputSupportsEveryAdapterKind(t *testing.T) {
	tests := []struct {
		kind       string
		baseURL    string
		apiVersion string
	}{
		{ProviderKindAnthropic, "https://api.anthropic.com", "2023-06-01"},
		{ProviderKindOpenAIChat, "https://api.openai.com", "v1"},
		{ProviderKindOpenAIResponses, "https://api.openai.com", "v1"},
		{ProviderKindGemini, "https://generativelanguage.googleapis.com", "v1beta"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			result, err := normalizeProviderInput(ProviderInput{
				Slug: "provider", Name: "Provider", Kind: test.kind, AdapterAppID: "provider-adapter",
				SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "provider-key"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.BaseURL != test.baseURL || result.APIVersion != test.apiVersion {
				t.Fatalf("unexpected defaults: %+v", result)
			}
		})
	}
}

func TestNormalizeProviderInputRejectsUnknownKindAndInvalidVersion(t *testing.T) {
	base := ProviderInput{
		Slug: "provider", Name: "Provider", Kind: "unknown", AdapterAppID: "provider-adapter",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "provider-key"},
	}
	if _, err := normalizeProviderInput(base); err == nil {
		t.Fatal("expected unknown provider kind to be rejected")
	}
	base.Kind = ProviderKindGemini
	base.APIVersion = "../../secrets"
	if _, err := normalizeProviderInput(base); err == nil {
		t.Fatal("expected invalid API version to be rejected")
	}
}

func TestQualifiedModelRequiresKnownProviderInVirtualKey(t *testing.T) {
	service, _, _ := newTestService()
	user, _, _ := provisionTestRoute(t, service)
	_, err := service.CreateVirtualKey(context.Background(), VirtualKeyInput{
		Name: "invalid", UserID: user.ID, AllowedModels: []string{"missing/model"},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "allowed_models" {
		t.Fatalf("expected allowed_models validation error, got %v", err)
	}
}
