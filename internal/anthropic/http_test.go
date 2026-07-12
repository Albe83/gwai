package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type fixedProviderResolver struct {
	provider controlplane.Provider
	slug     string
}

func (r *fixedProviderResolver) ResolveProviderBySlug(_ context.Context, slug string) (controlplane.Provider, error) {
	r.slug = slug
	return r.provider, nil
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
