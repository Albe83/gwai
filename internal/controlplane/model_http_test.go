package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestModelHTTPCRUDUsesStrongPreconditionsAndRestrictsProviderDelete(t *testing.T) {
	resources, _ := newControlPlaneHandlers()
	providerInput := ProviderInput{
		Slug: "anthropic", Name: "Anthropic", Kind: ProviderKindAnthropic, AdapterAppID: "anthropic-adapter",
		Status: StatusActive,
	}
	providerResponse := controlRequest(resources, http.MethodPost, "/v1/providers", "admin-token", providerInput)
	if providerResponse.Code != http.StatusCreated {
		t.Fatalf("create provider returned %d: %s", providerResponse.Code, providerResponse.Body.String())
	}
	providerTag := requireStrongETag(t, providerResponse)
	var provider Provider
	if err := json.Unmarshal(providerResponse.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}

	input := ModelInput{
		Alias: "claude", ProviderID: provider.ID, UpstreamModel: "claude-sonnet-4-6", Status: StatusActive,
	}
	if response := controlRequest(resources, http.MethodPost, "/v1/models", "wrong", input); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated model creation returned %d", response.Code)
	}
	createdResponse := controlRequest(resources, http.MethodPost, "/v1/models", "admin-token", input)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create model returned %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	createTag := requireStrongETag(t, createdResponse)
	var model Model
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Revision != 1 || model.Alias != input.Alias {
		t.Fatalf("unexpected model response: %+v", model)
	}
	if response := controlRequest(resources, http.MethodPost, "/v1/models", "admin-token", map[string]any{
		"alias": "other", "provider_id": provider.ID, "upstream_model": "other", "unexpected": true,
	}); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown model JSON field returned %d: %s", response.Code, response.Body.String())
	}
	getResponse := controlRequest(resources, http.MethodGet, "/v1/models/"+model.ID, "admin-token", nil)
	if getResponse.Code != http.StatusOK || requireStrongETag(t, getResponse) != createTag {
		t.Fatalf("model GET did not expose created entity version: %d %s", getResponse.Code, getResponse.Body.String())
	}
	listResponse := controlRequest(resources, http.MethodGet, "/v1/models", "admin-token", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list models returned %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var list struct {
		Data []Model `json:"data"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil || len(list.Data) != 1 || list.Data[0].ID != model.ID {
		t.Fatalf("unexpected model list: %+v err=%v", list, err)
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/providers/"+provider.ID, "admin-token", providerTag, nil); response.Code != http.StatusConflict {
		t.Fatalf("provider with model was not restricted: %d %s", response.Code, response.Body.String())
	}

	update := input
	update.UpstreamModel = "claude-sonnet-latest"
	withoutStatus := update
	withoutStatus.Status = ""
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/models/"+model.ID, "admin-token", createTag, withoutStatus); response.Code != http.StatusBadRequest {
		t.Fatalf("model PUT without status returned %d: %s", response.Code, response.Body.String())
	}
	putResponse := controlRequestIfMatch(resources, http.MethodPut, "/v1/models/"+model.ID, "admin-token", createTag, update)
	if putResponse.Code != http.StatusOK {
		t.Fatalf("conditional model update returned %d: %s", putResponse.Code, putResponse.Body.String())
	}
	updatedTag := requireStrongETag(t, putResponse)
	if updatedTag == createTag {
		t.Fatal("changed model retained its old ETag")
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/models/"+model.ID, "admin-token", createTag, update); response.Code != http.StatusConflict {
		t.Fatalf("stale model update returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/models/"+model.ID, "admin-token", "not-an-etag", update); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed model If-Match returned %d: %s", response.Code, response.Body.String())
	}
	update.Alias = "renamed"
	if response := controlRequestIfMatch(resources, http.MethodPut, "/v1/models/"+model.ID, "admin-token", updatedTag, update); response.Code != http.StatusBadRequest {
		t.Fatalf("mutable model alias returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/models/"+model.ID, "admin-token", createTag, nil); response.Code != http.StatusConflict {
		t.Fatalf("stale model delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/models/"+model.ID, "admin-token", "not-an-etag", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("malformed model delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/models/"+model.ID, "admin-token", updatedTag, nil); response.Code != http.StatusNoContent {
		t.Fatalf("conditional model delete returned %d: %s", response.Code, response.Body.String())
	}
	if response := controlRequestIfMatch(resources, http.MethodDelete, "/v1/providers/"+provider.ID, "admin-token", providerTag, nil); response.Code != http.StatusNoContent {
		t.Fatalf("provider delete after model removal returned %d: %s", response.Code, response.Body.String())
	}
}

func TestProviderAPIOmitsAdapterConnectionConfigAndModelAllowsNoUpstreamOverride(t *testing.T) {
	resources, _ := newControlPlaneHandlers()
	providerResponse := controlRequest(resources, http.MethodPost, "/v1/providers", "admin-token", ProviderInput{
		Slug: "openai", Name: "OpenAI", Kind: ProviderKindOpenAIResponses,
		AdapterAppID: "openai-adapter", Status: StatusActive,
	})
	if providerResponse.Code != http.StatusCreated {
		t.Fatalf("create provider returned %d: %s", providerResponse.Code, providerResponse.Body.String())
	}
	for _, removed := range []string{`"base_url"`, `"api_version"`, `"secret_ref"`} {
		if strings.Contains(providerResponse.Body.String(), removed) {
			t.Fatalf("provider response retained removed field %s: %s", removed, providerResponse.Body.String())
		}
	}
	var provider Provider
	if err := json.Unmarshal(providerResponse.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}

	legacyResponse := controlRequest(resources, http.MethodPost, "/v1/providers", "admin-token", map[string]any{
		"slug": "legacy", "name": "Legacy", "kind": ProviderKindOpenAIResponses,
		"adapter_app_id": "legacy-adapter", "base_url": "https://api.openai.com",
	})
	if legacyResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy provider connection field returned %d: %s", legacyResponse.Code, legacyResponse.Body.String())
	}

	modelResponse := controlRequest(resources, http.MethodPost, "/v1/models", "admin-token", map[string]any{
		"alias": "public-model", "provider_id": provider.ID,
	})
	if modelResponse.Code != http.StatusCreated {
		t.Fatalf("create model without upstream override returned %d: %s", modelResponse.Code, modelResponse.Body.String())
	}
	var model Model
	if err := json.Unmarshal(modelResponse.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.UpstreamModel != "" {
		t.Fatalf("model upstream override = %q, want empty", model.UpstreamModel)
	}
}
