package adminui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
)

func TestDaprAPIUsesExplicitTargetsRoutesAndHTTPMethods(t *testing.T) {
	type observed struct {
		method, path, authorization, daprToken, contentType, ifMatch string
		body                                                         map[string]any
		contentLength                                                int64
		transferEncoding                                             []string
	}
	requests := make(chan observed, 12)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := observed{
			method: r.Method, path: r.URL.EscapedPath(), authorization: r.Header.Get("Authorization"),
			daprToken: r.Header.Get("dapr-api-token"), contentType: r.Header.Get("Content-Type"), ifMatch: r.Header.Get("If-Match"),
			contentLength: r.ContentLength, transferEncoding: slices.Clone(r.TransferEncoding),
		}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&entry.body)
		}
		requests <- entry
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"response-version"`)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/users"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/users"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"usr_new","name":"Ada","email":"ada@example.com","status":"active","revision":1,"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/v1/providers/"):
			_, _ = w.Write([]byte(`{"id":"prv_one","slug":"anthropic","name":"Primary","kind":"anthropic","base_url":"https://api.anthropic.com","api_version":"2023-06-01","adapter_app_id":"gwai-anthropic","secret_ref":{"store":"kubernetes","name":"anthropic"},"status":"disabled","created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/models"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/models"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"mdl_one","alias":"claude","provider_id":"prv_one","upstream_model":"claude-sonnet","status":"active","revision":1,"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/v1/models/"):
			_, _ = w.Write([]byte(`{"id":"mdl_one","alias":"claude","provider_id":"prv_one","upstream_model":"claude-sonnet-v2","status":"active","revision":2,"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:01:00Z"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/virtual-keys"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"virtual_key":{"id":"key_one","name":"CLI","user_id":"usr_one","prefix":"gwai_prefix","status":"active","created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"},"key":"gwai_secret"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected route", http.StatusNotFound)
		}
	}))
	defer server.Close()

	dapr := daprhttp.New(server.URL, "sidecar-token", server.Client())
	api, err := NewDaprAPI(dapr, "resource-plane", "key-plane", "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := api.ListUsers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateUser(ctx, controlplane.UserInput{Name: "Ada", Email: "ada@example.com", Status: controlplane.StatusActive}); err != nil {
		t.Fatal(err)
	}
	providerInput := controlplane.ProviderInput{
		Slug: "anthropic", Name: "Primary", Kind: controlplane.ProviderKindAnthropic,
		BaseURL: "https://api.anthropic.com", APIVersion: "2023-06-01", AdapterAppID: "gwai-anthropic",
		SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic"}, Status: controlplane.StatusDisabled,
	}
	updated, err := api.UpdateProvider(ctx, "prv_one", providerInput, `"provider-version"`)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ETag != `"response-version"` {
		t.Fatalf("updated provider ETag = %q", updated.ETag)
	}
	if _, err := api.ListModels(ctx); err != nil {
		t.Fatal(err)
	}
	modelInput := controlplane.ModelInput{Alias: "claude", ProviderID: "prv_one", UpstreamModel: "claude-sonnet", Status: controlplane.StatusActive}
	if _, err := api.CreateModel(ctx, modelInput); err != nil {
		t.Fatal(err)
	}
	modelInput.UpstreamModel = "claude-sonnet-v2"
	updatedModel, err := api.UpdateModel(ctx, "mdl_one", modelInput, `"model-version"`)
	if err != nil || updatedModel.ETag != `"response-version"` {
		t.Fatalf("model update = %+v, %v", updatedModel, err)
	}
	if _, err := api.CreateVirtualKey(ctx, controlplane.VirtualKeyInput{Name: "CLI", UserID: "usr_one", ModelIDs: []string{"mdl_one"}, Status: controlplane.StatusActive}); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteModel(ctx, "mdl_one", `"model-delete-version"`); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteVirtualKey(ctx, "key_one", `"key-version"`); err != nil {
		t.Fatal(err)
	}

	wants := []struct{ method, path, ifMatch string }{
		{http.MethodGet, "/v1.0/invoke/resource-plane/method/v1/users", ""},
		{http.MethodPost, "/v1.0/invoke/resource-plane/method/v1/users", ""},
		{http.MethodPut, "/v1.0/invoke/resource-plane/method/v1/providers/prv_one", `"provider-version"`},
		{http.MethodGet, "/v1.0/invoke/resource-plane/method/v1/models", ""},
		{http.MethodPost, "/v1.0/invoke/resource-plane/method/v1/models", ""},
		{http.MethodPut, "/v1.0/invoke/resource-plane/method/v1/models/mdl_one", `"model-version"`},
		{http.MethodPost, "/v1.0/invoke/key-plane/method/v1/virtual-keys", ""},
		{http.MethodDelete, "/v1.0/invoke/resource-plane/method/v1/models/mdl_one", `"model-delete-version"`},
		{http.MethodDelete, "/v1.0/invoke/key-plane/method/v1/virtual-keys/key_one", `"key-version"`},
	}
	for index, want := range wants {
		got := <-requests
		if got.method != want.method || got.path != want.path {
			t.Fatalf("request %d = %s %s, want %s %s", index, got.method, got.path, want.method, want.path)
		}
		if got.authorization != "Bearer admin-secret" || got.daprToken != "sidecar-token" {
			t.Fatalf("request %d authentication headers = auth %q dapr %q", index, got.authorization, got.daprToken)
		}
		if (got.method == http.MethodPost || got.method == http.MethodPut) && got.contentType != "application/json" {
			t.Fatalf("request %d content type = %q", index, got.contentType)
		}
		if got.ifMatch != want.ifMatch {
			t.Fatalf("request %d If-Match = %q", index, got.ifMatch)
		}
		if got.method != http.MethodGet {
			if got.contentLength != -1 || !slices.Contains(got.transferEncoding, "chunked") {
				t.Fatalf("mutation %s %s must be an unknown-length streaming invocation, got length=%d encoding=%v", got.method, got.path, got.contentLength, got.transferEncoding)
			}
		}
	}
}

func TestDaprAPIRequiresEntityTagForEditableResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"usr_one","name":"Ada","email":"ada@example.com","status":"active","revision":1,"created_at":"2026-07-12T00:00:00Z","updated_at":"2026-07-12T00:00:00Z"}`))
	}))
	defer server.Close()
	api, err := NewDaprAPI(daprhttp.New(server.URL, "", server.Client()), "resources", "keys", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.GetUser(context.Background(), "usr_one")
	var problem *APIError
	if !errors.As(err, &problem) || problem.Status != http.StatusBadGateway || !strings.Contains(problem.Detail, "resource version") {
		t.Fatalf("missing ETag returned %#v", err)
	}
}

func TestDaprAPIRetainsSafeProblemDetailsAndHidesRawFailures(t *testing.T) {
	response := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response++
		if response == 1 {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Conflict","status":409,"detail":"user still has virtual keys","instance":"req_123"}`))
			return
		}
		w.Header().Set("X-Request-ID", "req_raw")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream credential=must-not-leak"))
	}))
	defer server.Close()

	api, err := NewDaprAPI(daprhttp.New(server.URL, "", server.Client()), "resources", "keys", "admin")
	if err != nil {
		t.Fatal(err)
	}
	err = api.DeleteUser(context.Background(), "usr_one", `"user-version"`)
	var problem *APIError
	if !errors.As(err, &problem) || problem.Status != http.StatusConflict || problem.Detail != "user still has virtual keys" || problem.Instance != "req_123" {
		t.Fatalf("problem not preserved: %#v", err)
	}
	err = api.DeleteProvider(context.Background(), "prv_one", `"provider-version"`)
	if !errors.As(err, &problem) || problem.Status != http.StatusBadGateway || problem.Instance != "req_raw" || strings.Contains(problem.Error(), "credential") {
		t.Fatalf("raw failure leaked or was misclassified: %#v", err)
	}
}

func TestDaprAPIMarksFailedCreateOutcomeAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"about:blank","title":"Unavailable","status":503,"detail":"dependency failed"}`))
	}))
	defer server.Close()
	api, err := NewDaprAPI(daprhttp.New(server.URL, "", server.Client()), "resources", "keys", "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = api.CreateVirtualKey(context.Background(), controlplane.VirtualKeyInput{Name: "CLI", UserID: "usr_one"})
	var problem *APIError
	if !errors.As(err, &problem) || !problem.Ambiguous || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("failed key creation was not classified as ambiguous: %#v", err)
	}
}

func TestNewDaprAPIValidatesRequiredConfiguration(t *testing.T) {
	client := daprhttp.New("http://127.0.0.1:3500", "", nil)
	for _, test := range []struct {
		name, resource, keys, token string
	}{
		{"resource app", "", "keys", "token"},
		{"key app", "resources", "", "token"},
		{"admin token", "resources", "keys", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewDaprAPI(client, test.resource, test.keys, test.token); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
	if _, err := NewDaprAPI(nil, "resources", "keys", "token"); err == nil {
		t.Fatal("nil Dapr client was accepted")
	}
}
