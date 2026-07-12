package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/state"
)

func newControlPlaneHandlers() (http.Handler, http.Handler) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	users := NewUserRepository(state.NewMemoryStore())
	providers := NewProviderRepository(state.NewMemoryStore())
	keyRepo := NewVirtualKeyRepository(state.NewMemoryStore())
	keys := NewVirtualKeyService(keyRepo, providers)
	resources := NewResourceService(users, providers, keys)
	return NewResourceHTTPHandler(resources, "admin-token", 1<<20, logger),
		NewVirtualKeyHTTPHandler(keys, "admin-token", "app-token", 1<<20, logger)
}

func controlRequest(handler http.Handler, method, path, bearer string, body any) *httptest.ResponseRecorder {
	var input io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		input = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, input)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestControlPlaneHTTPDomainsAreIndependent(t *testing.T) {
	resources, keys := newControlPlaneHandlers()

	response := controlRequest(resources, http.MethodPost, "/v1/users", "admin-token", UserInput{Name: "Ada", Email: "ada@example.com"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create user returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequest(resources, http.MethodGet, "/v1/virtual-keys", "admin-token", nil); response.Code != http.StatusNotFound {
		t.Fatalf("resource control-plane must not expose virtual keys, got %d", response.Code)
	}
	if response := controlRequest(keys, http.MethodGet, "/v1/users", "admin-token", nil); response.Code != http.StatusNotFound {
		t.Fatalf("virtual-key control-plane must not expose users, got %d", response.Code)
	}
	if response := controlRequest(keys, http.MethodGet, "/v1/virtual-keys", "wrong", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("virtual-key admin endpoint must authenticate independently, got %d", response.Code)
	}
}

func TestVirtualKeyInternalSubjectEndpointRequiresAppToken(t *testing.T) {
	_, handler := newControlPlaneHandlers()
	subject := KeySubject{UserID: "usr_test", Status: StatusActive, Revision: 1, UpdatedAt: time.Now().UTC()}
	payload, err := json.Marshal(subject)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/internal/v1/subjects/sync", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("internal endpoint without app token returned %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/internal/v1/subjects/sync", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("dapr-api-token", "app-token")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("internal endpoint with app token returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestControlPlaneHTTPRejectsUnknownJSONFields(t *testing.T) {
	resources, _ := newControlPlaneHandlers()
	request := httptest.NewRequest(http.MethodPost, "/v1/users", bytes.NewBufferString(`{"name":"Ada","email":"ada@example.com","extra":true}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	resources.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON field returned %d", recorder.Code)
	}
}
