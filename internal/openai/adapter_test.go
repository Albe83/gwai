package openai

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
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
)

type openAIProviderResolver struct{ provider controlplane.Provider }

func (r openAIProviderResolver) ResolveProviderBySlug(_ context.Context, _ string) (controlplane.Provider, error) {
	return r.provider, nil
}

type openAISecretResolver struct{ value string }

func (r openAISecretResolver) Get(_ context.Context, _ daprhttp.SecretRef) (string, error) {
	return r.value, nil
}

func TestAdapterHTTPHandlerCallsConfiguredOpenAIChatProvider(t *testing.T) {
	var received ChatCompletionRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		content := "hello"
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			ID: "chatcmpl_1", Choices: []Choice{{FinishReason: "stop", Message: AssistantOutput{Content: &content}}},
			Usage: Usage{PromptTokens: 2, CompletionTokens: 1},
		})
	}))
	defer upstream.Close()
	provider := controlplane.Provider{
		ID: "prv_1", Slug: "openai", Kind: controlplane.ProviderKindOpenAIChat,
		BaseURL: upstream.URL, APIVersion: "v1", AdapterAppID: "openai-chat-adapter",
	}
	handler := NewAdapterHTTPHandler(openAIProviderResolver{provider}, openAISecretResolver{"sk-test"}, upstream.Client(), AdapterConfig{
		ProviderSlug: "openai", AppID: "openai-chat-adapter", MaxBody: 1 << 20, AppToken: "sidecar-token",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	input := ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_1", UpstreamModel: "gpt-test"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hi"}}}},
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("dapr-api-token", "sidecar-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected success, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if received.Model != "gpt-test" || len(received.Messages) != 1 {
		t.Fatalf("unexpected upstream request: %+v", received)
	}
	var output ir.Response
	if err := json.NewDecoder(recorder.Body).Decode(&output); err != nil {
		t.Fatal(err)
	}
	if output.ProviderResponseID != "chatcmpl_1" || output.Content[0].Text != "hello" {
		t.Fatalf("unexpected IR response: %+v", output)
	}
}
