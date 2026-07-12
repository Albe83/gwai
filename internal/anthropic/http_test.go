package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
)

type fixedProviderResolver struct {
	provider controlplane.Provider
	slug     string
}

type fixedSecretResolver struct {
	value string
	ref   daprhttp.SecretRef
}

func (r *fixedSecretResolver) Get(_ context.Context, ref daprhttp.SecretRef) (string, error) {
	r.ref = ref
	return r.value, nil
}

func (r *fixedProviderResolver) ResolveProviderBySlug(_ context.Context, slug string) (controlplane.Provider, error) {
	r.slug = slug
	return r.provider, nil
}

func TestHTTPHandlerCallsAnthropicWithOfficialAuthAndVersion(t *testing.T) {
	var providerRequest MessageRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-secret" || r.Header.Get("anthropic-version") != PublicAPIVersion {
			t.Errorf("unexpected Anthropic headers: %v", r.Header)
		}
		if r.Header.Get("X-Request-ID") != "req_external" {
			t.Errorf("request ID was not propagated: %q", r.Header.Get("X-Request-ID"))
		}
		if err := json.NewDecoder(r.Body).Decode(&providerRequest); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		platformResponse := MessageResponse{
			ID: "msg_provider", Type: "message", Role: "assistant", Model: "claude-sonnet", StopReason: "end_turn",
			Content: []ContentBlock{{Type: "text", Text: "hello"}},
			Usage:   Usage{InputTokens: 11, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 7},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(platformResponse)
	}))
	defer upstream.Close()

	secretRef := daprhttp.SecretRef{Store: "secrets", Name: "anthropic", Key: "api-key"}
	resolver := &fixedProviderResolver{provider: controlplane.Provider{
		ID: "prv_a", Slug: "team-a", Kind: controlplane.ProviderKindAnthropic,
		BaseURL: upstream.URL, APIVersion: PublicAPIVersion, AdapterAppID: "anthropic-a", SecretRef: secretRef,
		Status: controlplane.StatusActive,
	}}
	secrets := &fixedSecretResolver{value: "anthropic-secret"}
	handler := NewHTTPHandler(resolver, secrets, upstream.Client(), Config{
		ProviderSlug: "team-a", AppID: "anthropic-a", MaxBody: 1 << 20, AppToken: "internal-token",
		DefaultMaxOutputTokens: 4096,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	requestBody, err := json.Marshal(ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_a", UpstreamModel: "claude-sonnet"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("dapr-api-token", "internal-token")
	request.Header.Set("X-Request-ID", "req_external")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected success, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if providerRequest.Model != "claude-sonnet" || providerRequest.MaxTokens != 4096 || providerRequest.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected provider request: %+v", providerRequest)
	}
	if secrets.ref != secretRef {
		t.Fatalf("unexpected secret reference: %+v", secrets.ref)
	}
	var response ir.Response
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ProviderResponseID != "msg_provider" || response.Usage.InputTokens != 21 || response.Usage.CacheCreationInputTokens != 3 || response.Usage.CachedInputTokens != 7 {
		t.Fatalf("unexpected IR response: %+v", response)
	}
}

func TestHTTPHandlerRequiresAuthenticatedDaprInvocation(t *testing.T) {
	handler := NewHTTPHandler(&fixedProviderResolver{}, nil, nil, Config{
		ProviderSlug: "team-a", AppID: "anthropic-a", MaxBody: 1 << 20, AppToken: "internal-token", DefaultMaxOutputTokens: 1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected Dapr token rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPHandlerRejectsRouteForAnotherProviderInstance(t *testing.T) {
	resolver := &fixedProviderResolver{provider: controlplane.Provider{
		ID: "prv_a", Slug: "team-a", Kind: "anthropic", AdapterAppID: "gwai-team-b",
		Status: controlplane.StatusActive,
	}}
	handler := NewHTTPHandler(resolver, nil, nil, Config{
		ProviderSlug: "team-a", AppID: "gwai-team-a", MaxBody: 1 << 20,
		DefaultMaxOutputTokens: 4096,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	requestBody, err := json.Marshal(ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_a", UpstreamModel: "claude"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected route isolation failure, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if resolver.slug != "team-a" {
		t.Fatalf("adapter resolved unexpected provider slug %q", resolver.slug)
	}
}
