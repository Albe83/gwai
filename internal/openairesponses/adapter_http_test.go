package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Albe83/gwai/internal/adapterconfig"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
)

type adapterProviderResolver struct {
	provider controlplane.Provider
	appID    string
	err      error
}

func (resolver *adapterProviderResolver) ResolveProviderByAdapterAppID(_ context.Context, appID string) (controlplane.Provider, error) {
	resolver.appID = appID
	return resolver.provider, resolver.err
}

type adapterSecretResolver struct {
	value string
	ref   daprhttp.SecretRef
	err   error
}

func (resolver *adapterSecretResolver) Get(_ context.Context, ref daprhttp.SecretRef) (string, error) {
	resolver.ref = ref
	return resolver.value, resolver.err
}

func adapterIRRequest(t *testing.T) []byte {
	t.Helper()
	maxTokens := 128
	payload, err := json.Marshal(ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_1", UpstreamModel: "gpt-4.1"}, MaxOutputTokens: &maxTokens,
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAdapterCallsVersionedResponsesEndpointWithBearerCredential(t *testing.T) {
	var captured ProviderRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer provider-secret" {
			t.Errorf("unexpected authorization header %q", authorization)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(Response{
			ID: "resp_provider", Object: "response", Status: "completed", Model: "gpt-4.1",
			Output: []OutputItem{{Type: "message", Role: "assistant", Status: "completed", Content: []OutputContent{{Type: "output_text", Text: "hello"}}}},
			Usage:  Usage{InputTokens: 5, OutputTokens: 1, TotalTokens: 6, InputTokensDetails: &InputTokenDetails{CachedTokens: 2}},
		})
	}))
	defer upstream.Close()

	secretRef := daprhttp.SecretRef{Store: "secrets", Name: "openai", Key: "api-key"}
	providers := &adapterProviderResolver{provider: controlplane.Provider{
		ID: "prv_1", Slug: "openai", Kind: controlplane.ProviderKindOpenAIResponses,
		AdapterAppID: "openai-responses-adapter",
	}}
	secrets := &adapterSecretResolver{value: "provider-secret"}
	handler := NewAdapterHTTPHandler(providers, secrets, upstream.Client(), AdapterConfig{
		Runtime: adapterconfig.Config{
			AppID: "openai-responses-adapter", BaseURL: upstream.URL, APIVersion: "v1", SecretRef: secretRef,
		},
		MaxBody: 1 << 20, AppToken: "sidecar-token", MaxOutputTokens: 1024,
	}, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(adapterIRRequest(t)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("dapr-api-token", "sidecar-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if providers.appID != "openai-responses-adapter" || secrets.ref != secretRef {
		t.Fatalf("unexpected provider/secret resolution: appID=%q ref=%+v", providers.appID, secrets.ref)
	}
	if captured.Model != "gpt-4.1" || captured.Store || captured.Stream || len(captured.Input) != 1 {
		t.Fatalf("unexpected provider payload: %+v", captured)
	}
	var response ir.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProviderResponseID != "resp_provider" || response.Content[0].Text != "hello" || response.Usage.CachedInputTokens != 2 {
		t.Fatalf("unexpected IR response: %+v", response)
	}
}

func TestAdapterRejectsWrongProviderKindAndRoute(t *testing.T) {
	tests := []struct {
		name     string
		provider controlplane.Provider
	}{
		{name: "kind", provider: controlplane.Provider{ID: "prv_1", Kind: controlplane.ProviderKindOpenAIChat, AdapterAppID: "adapter"}},
		{name: "provider id", provider: controlplane.Provider{ID: "prv_other", Kind: controlplane.ProviderKindOpenAIResponses, AdapterAppID: "adapter"}},
		{name: "app id", provider: controlplane.Provider{ID: "prv_1", Kind: controlplane.ProviderKindOpenAIResponses, AdapterAppID: "another-adapter"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providers := &adapterProviderResolver{provider: test.provider}
			handler := NewAdapterHTTPHandler(providers, &adapterSecretResolver{}, nil, AdapterConfig{
				Runtime: adapterconfig.Config{AppID: "adapter"}, MaxBody: 1 << 20,
			}, testLogger())
			request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(adapterIRRequest(t)))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected route isolation error, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestAdapterRequiresConfiguredSidecarToken(t *testing.T) {
	handler := NewAdapterHTTPHandler(&adapterProviderResolver{}, &adapterSecretResolver{}, nil, AdapterConfig{
		Runtime: adapterconfig.Config{AppID: "adapter"}, MaxBody: 1 << 20, AppToken: "required",
	}, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(adapterIRRequest(t)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
