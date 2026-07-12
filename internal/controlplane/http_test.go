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

	"github.com/Albe83/gwai/internal/daprhttp"
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

func controlRequestIfMatch(handler http.Handler, method, path, bearer, ifMatch string, body any) *httptest.ResponseRecorder {
	var input io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		input = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, input)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	request.Header.Set("If-Match", ifMatch)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func requireStrongETag(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	etag := response.Header().Get("ETag")
	if len(etag) != 66 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		t.Fatalf("response did not contain a SHA-256 strong ETag: status=%d etag=%q body=%s", response.Code, etag, response.Body.String())
	}
	return etag
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

func TestIfMatchDistinguishesMissingFromEmptyHeader(t *testing.T) {
	resources, _ := newControlPlaneHandlers()
	created := controlRequest(resources, http.MethodPost, "/v1/users", "admin-token", UserInput{
		Name: "Ada", Email: "ada@example.com", Status: StatusActive,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create user returned %d: %s", created.Code, created.Body.String())
	}
	var user User
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}

	updated := controlRequest(resources, http.MethodPut, "/v1/users/"+user.ID, "admin-token", UserInput{
		Name: "Ada Lovelace", Email: user.Email, Status: StatusActive,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("PUT without If-Match returned %d: %s", updated.Code, updated.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/users/"+user.ID, "admin-token", "", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("DELETE with empty If-Match returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequest(resources, http.MethodDelete, "/v1/users/"+user.ID, "admin-token", nil); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE without If-Match returned %d: %s", response.Code, response.Body.String())
	}
}

func TestPublicResourceETagsAndIfMatchAcrossAllDomains(t *testing.T) {
	resources, keys := newControlPlaneHandlers()

	createdUserResponse := controlRequest(resources, http.MethodPost, "/v1/users", "admin-token", UserInput{
		Name: "Ada", Email: "ada@example.com", Status: StatusActive,
	})
	if createdUserResponse.Code != http.StatusCreated {
		t.Fatalf("create user returned %d: %s", createdUserResponse.Code, createdUserResponse.Body.String())
	}
	userCreateTag := requireStrongETag(t, createdUserResponse)
	var user User
	if err := json.Unmarshal(createdUserResponse.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	userGetResponse := controlRequest(resources, http.MethodGet, "/v1/users/"+user.ID, "admin-token", nil)
	if userGetResponse.Code != http.StatusOK || requireStrongETag(t, userGetResponse) != userCreateTag {
		t.Fatal("user POST and GET did not expose the same entity version")
	}
	userUpdate := UserInput{Name: "Ada Lovelace", Email: user.Email, Status: StatusActive}
	userPutResponse := controlRequestIfMatch(resources, http.MethodPut, "/v1/users/"+user.ID, "admin-token", userCreateTag, userUpdate)
	if userPutResponse.Code != http.StatusOK {
		t.Fatalf("conditional user update returned %d: %s", userPutResponse.Code, userPutResponse.Body.String())
	}
	userUpdatedTag := requireStrongETag(t, userPutResponse)
	if userUpdatedTag == userCreateTag {
		t.Fatal("changed user retained its old ETag")
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/users/"+user.ID, "admin-token", userCreateTag, UserInput{Name: "Stale", Email: user.Email, Status: StatusActive}); response.Code != http.StatusConflict {
		t.Fatalf("stale user If-Match returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/users/"+user.ID, "admin-token", "not-an-etag", userUpdate); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed user If-Match returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/users/"+user.ID, "admin-token", "", userUpdate); response.Code != http.StatusBadRequest {
		t.Fatalf("empty user If-Match returned %d: %s", response.Code, response.Body.String())
	}

	providerInput := ProviderInput{
		Slug: "anthropic", Name: "Anthropic", Kind: ProviderKindAnthropic,
		AdapterAppID: "gwai-anthropic", SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic", Key: "api-key"},
		Status: StatusActive,
	}
	createdProviderResponse := controlRequest(resources, http.MethodPost, "/v1/providers", "admin-token", providerInput)
	if createdProviderResponse.Code != http.StatusCreated {
		t.Fatalf("create provider returned %d: %s", createdProviderResponse.Code, createdProviderResponse.Body.String())
	}
	providerCreateTag := requireStrongETag(t, createdProviderResponse)
	var provider Provider
	if err := json.Unmarshal(createdProviderResponse.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}
	providerGetResponse := controlRequest(resources, http.MethodGet, "/v1/providers/"+provider.ID, "admin-token", nil)
	if providerGetResponse.Code != http.StatusOK || requireStrongETag(t, providerGetResponse) != providerCreateTag {
		t.Fatal("provider POST and GET did not expose the same entity version")
	}
	providerUpdate := providerInput
	providerUpdate.Name = "Anthropic primary"
	providerPutResponse := controlRequestIfMatch(resources, http.MethodPut, "/v1/providers/"+provider.ID, "admin-token", providerCreateTag, providerUpdate)
	if providerPutResponse.Code != http.StatusOK {
		t.Fatalf("conditional provider update returned %d: %s", providerPutResponse.Code, providerPutResponse.Body.String())
	}
	providerUpdatedTag := requireStrongETag(t, providerPutResponse)
	if providerUpdatedTag == providerCreateTag {
		t.Fatal("changed provider retained its old ETag")
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/providers/"+provider.ID, "admin-token", providerCreateTag, providerUpdate); response.Code != http.StatusConflict {
		t.Fatalf("stale provider If-Match returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/providers/"+provider.ID, "admin-token", "not-an-etag", providerUpdate); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed provider If-Match returned %d: %s", response.Code, response.Body.String())
	}

	createdKeyResponse := controlRequest(keys, http.MethodPost, "/v1/virtual-keys", "admin-token", VirtualKeyInput{
		Name: "CLI", UserID: user.ID, AllowedModels: []string{"anthropic/claude"}, Status: StatusActive,
	})
	if createdKeyResponse.Code != http.StatusCreated {
		t.Fatalf("create key returned %d: %s", createdKeyResponse.Code, createdKeyResponse.Body.String())
	}
	keyCreateTag := requireStrongETag(t, createdKeyResponse)
	var createdKey CreatedVirtualKey
	if err := json.Unmarshal(createdKeyResponse.Body.Bytes(), &createdKey); err != nil {
		t.Fatal(err)
	}
	keyGetResponse := controlRequest(keys, http.MethodGet, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", nil)
	if keyGetResponse.Code != http.StatusOK || requireStrongETag(t, keyGetResponse) != keyCreateTag {
		t.Fatal("virtual-key POST ETag must validate the nested public resource returned by GET")
	}
	keyUpdate := VirtualKeyInput{
		Name: "CLI updated", UserID: user.ID, AllowedModels: []string{"anthropic/claude"}, Status: StatusActive,
	}
	keyPutResponse := controlRequestIfMatch(keys, http.MethodPut, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", keyCreateTag, keyUpdate)
	if keyPutResponse.Code != http.StatusOK {
		t.Fatalf("conditional key update returned %d: %s", keyPutResponse.Code, keyPutResponse.Body.String())
	}
	keyUpdatedTag := requireStrongETag(t, keyPutResponse)
	if keyUpdatedTag == keyCreateTag {
		t.Fatal("changed virtual key retained its old ETag")
	}
	if response := controlRequestIfMatch(keys, http.MethodPut, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", keyCreateTag, keyUpdate); response.Code != http.StatusConflict {
		t.Fatalf("stale key If-Match returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(keys, http.MethodPut, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", "not-an-etag", keyUpdate); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed key If-Match returned %d: %s", response.Code, response.Body.String())
	}

	if response := controlRequestIfMatch(keys, http.MethodDelete, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", keyCreateTag, nil); response.Code != http.StatusConflict {
		t.Fatalf("stale key delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(keys, http.MethodDelete, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", "not-an-etag", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed key delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(keys, http.MethodDelete, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", "", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("empty key delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(keys, http.MethodDelete, "/v1/virtual-keys/"+createdKey.VirtualKey.ID, "admin-token", keyUpdatedTag, nil); response.Code != http.StatusNoContent {
		t.Fatalf("conditional key delete returned %d: %s", response.Code, response.Body.String())
	}

	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/providers/"+provider.ID, "admin-token", providerCreateTag, nil); response.Code != http.StatusConflict {
		t.Fatalf("stale provider delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/providers/"+provider.ID, "admin-token", "not-an-etag", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed provider delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/providers/"+provider.ID, "admin-token", providerUpdatedTag, nil); response.Code != http.StatusNoContent {
		t.Fatalf("conditional provider delete returned %d: %s", response.Code, response.Body.String())
	}

	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/users/"+user.ID, "admin-token", userCreateTag, nil); response.Code != http.StatusConflict {
		t.Fatalf("stale user delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/users/"+user.ID, "admin-token", "not-an-etag", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed user delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/users/"+user.ID, "admin-token", userUpdatedTag, nil); response.Code != http.StatusNoContent {
		t.Fatalf("conditional user delete returned %d: %s", response.Code, response.Body.String())
	}
}
