package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/anthropic"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	openaiadapter "github.com/Albe83/gwai/internal/openai"
	"github.com/Albe83/gwai/internal/state"
)

type localInvoker struct {
	mu       sync.RWMutex
	handlers map[string]http.Handler
}

func newLocalInvoker() *localInvoker {
	return &localInvoker{handlers: make(map[string]http.Handler)}
}

func (i *localInvoker) register(appID string, handler http.Handler) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.handlers[appID] = handler
}

func (i *localInvoker) InvokeJSON(ctx context.Context, appID, method string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, method, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	i.mu.RLock()
	handler := i.handlers[appID]
	i.mu.RUnlock()
	if handler == nil {
		return &daprhttp.HTTPError{StatusCode: http.StatusNotFound, Body: "unknown app id"}
	}
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return &daprhttp.HTTPError{StatusCode: response.StatusCode, Body: string(body)}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

type staticSecrets struct {
	value string
}

func (s staticSecrets) Get(_ context.Context, _ daprhttp.SecretRef) (string, error) {
	return s.value, nil
}

func adminRequest[T any](t *testing.T, handler http.Handler, method, path string, input any) T {
	t.Helper()
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer admin-test-token")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("admin %s %s returned %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	var result T
	if recorder.Code != http.StatusNoContent {
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestOpenAIToAnthropicVerticalSlice(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var captured anthropic.MessageRequest
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-secret" {
			t.Errorf("unexpected provider API key")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("unexpected Anthropic version: %s", r.Header.Get("anthropic-version"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(anthropic.MessageResponse{
			ID: "msg_test", Type: "message", Role: "assistant", Model: "claude-test", StopReason: "end_turn",
			Content: []anthropic.ContentBlock{{Type: "text", Text: "Ciao dal provider"}},
			Usage:   anthropic.Usage{InputTokens: 8, OutputTokens: 4},
		})
	}))
	defer providerServer.Close()

	repository := controlplane.NewRepository(state.NewMemoryStore())
	controlService := controlplane.NewService(repository)
	runtime := controlplane.NewRuntime(repository)
	controlHandler := controlplane.NewHTTPHandler(controlService, "admin-test-token", 1<<20, logger)
	invoker := newLocalInvoker()
	adapterHandler := anthropic.NewHTTPHandler(
		runtime, staticSecrets{value: "anthropic-secret"}, providerServer.Client(),
		anthropic.Config{
			ProviderSlug: "anthropic-test", AppID: "gwai-anthropic-test", MaxBody: 10 << 20,
			DefaultMaxOutputTokens: 4096,
		}, logger,
	)
	invoker.register("gwai-anthropic-test", adapterHandler)
	gatewayHandler := openaiadapter.NewHTTPHandler(runtime, invoker, 10<<20, time.Minute, logger)

	user := adminRequest[controlplane.User](t, controlHandler, http.MethodPost, "/v1/users", controlplane.UserInput{Name: "Ada", Email: "ada@example.com"})
	provider := adminRequest[controlplane.Provider](t, controlHandler, http.MethodPost, "/v1/providers", controlplane.ProviderInput{
		Slug: "anthropic-test", Name: "test Anthropic", Kind: "anthropic", BaseURL: providerServer.URL,
		AdapterAppID: "gwai-anthropic-test",
		SecretRef:    daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic", Key: "api-key"},
	})
	qualifiedModel := provider.Slug + "/claude-test"
	createdKey := adminRequest[controlplane.CreatedVirtualKey](t, controlHandler, http.MethodPost, "/v1/virtual-keys", controlplane.VirtualKeyInput{
		Name: "client", UserID: user.ID, AllowedModels: []string{qualifiedModel},
	})

	requestBody := []byte(`{
		"model":"anthropic-test/claude-test",
		"messages":[
			{"role":"system","content":"Rispondi in italiano"},
			{"role":"user","content":"Saluta"}
		],
		"max_completion_tokens":64
	}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+createdKey.Key)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	gatewayHandler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("gateway returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var completion openaiadapter.ChatCompletionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Model != qualifiedModel || completion.Choices[0].Message.Content == nil || *completion.Choices[0].Message.Content != "Ciao dal provider" {
		t.Fatalf("unexpected completion: %+v", completion)
	}
	if completion.Usage.PromptTokens != 8 || completion.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected usage: %+v", completion.Usage)
	}
	if captured.Model != "claude-test" || captured.MaxTokens != 64 || len(captured.System) != 1 || captured.Messages[0].Content[0].Text != "Saluta" {
		t.Fatalf("unexpected request received by Anthropic: %+v", captured)
	}
}
