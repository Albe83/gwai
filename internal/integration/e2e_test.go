package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/adapterconfig"
	"github.com/Albe83/gwai/internal/anthropic"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/dataplane"
	"github.com/Albe83/gwai/internal/gemini"
	"github.com/Albe83/gwai/internal/ir"
	openaichat "github.com/Albe83/gwai/internal/openai"
	"github.com/Albe83/gwai/internal/openairesponses"
	"github.com/Albe83/gwai/internal/state"
)

type localInvoker struct {
	mu          sync.RWMutex
	handlers    map[string]http.Handler
	invocations []localInvocation
}

type localInvocation struct {
	appID  string
	method string
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
	request.Header.Set("dapr-api-token", "internal-test-token")
	recorder := httptest.NewRecorder()
	i.mu.Lock()
	handler := i.handlers[appID]
	i.invocations = append(i.invocations, localInvocation{appID: appID, method: method})
	i.mu.Unlock()
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

func (i *localInvoker) invocationSnapshot() []localInvocation {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]localInvocation(nil), i.invocations...)
}

type staticSecrets struct {
	value string
}

func (s staticSecrets) Get(_ context.Context, _ daprhttp.SecretRef) (string, error) {
	return s.value, nil
}

type deploymentSecrets struct {
	mu     sync.Mutex
	values map[daprhttp.SecretRef]string
	used   []daprhttp.SecretRef
}

func (s *deploymentSecrets) Get(_ context.Context, ref daprhttp.SecretRef) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used = append(s.used, ref)
	value, ok := s.values[ref]
	if !ok {
		return "", fmt.Errorf("unexpected deployment secret reference: %+v", ref)
	}
	return value, nil
}

func (s *deploymentSecrets) usedSnapshot() []daprhttp.SecretRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]daprhttp.SecretRef(nil), s.used...)
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

type upstreamObservation struct {
	path        string
	model       string
	headers     http.Header
	decodeError error
}

func newConfiguredUpstream(kind string) (*httptest.Server, <-chan upstreamObservation) {
	observations := make(chan upstreamObservation, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		decodeError := json.NewDecoder(r.Body).Decode(&payload)
		model := payload.Model
		if kind == controlplane.ProviderKindGemini {
			const modelMarker = "/models/"
			if marker := strings.Index(r.URL.Path, modelMarker); marker >= 0 {
				model = strings.TrimSuffix(r.URL.Path[marker+len(modelMarker):], ":generateContent")
			}
		}
		observations <- upstreamObservation{
			path: r.URL.Path, model: model, headers: r.Header.Clone(), decodeError: decodeError,
		}

		w.Header().Set("Content-Type", "application/json")
		switch kind {
		case controlplane.ProviderKindAnthropic:
			_, _ = io.WriteString(w, `{"id":"msg_integration","type":"message","role":"assistant","content":[{"type":"text","text":"anthropic ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		case controlplane.ProviderKindOpenAIChat:
			_, _ = io.WriteString(w, `{"id":"chatcmpl_integration","object":"chat.completion","created":1,"model":"provider-response","choices":[{"index":0,"message":{"role":"assistant","content":"chat ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		case controlplane.ProviderKindOpenAIResponses:
			_, _ = io.WriteString(w, `{"id":"resp_integration","object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"responses ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
		case controlplane.ProviderKindGemini:
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"gemini ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"responseId":"gemini_integration"}`)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
	}))
	return server, observations
}

func TestFourGatewayProtocolsToAnthropicAdapter(t *testing.T) {
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

	userRepository := controlplane.NewUserRepository(state.NewMemoryStore())
	providerStore := state.NewMemoryStore()
	providerRepository := controlplane.NewProviderRepository(providerStore)
	modelRepository := controlplane.NewModelRepository(providerStore)
	keyRepository := controlplane.NewVirtualKeyRepository(state.NewMemoryStore())
	invoker := newLocalInvoker()
	keyService := controlplane.NewVirtualKeyService(keyRepository)
	keyHandler := controlplane.NewVirtualKeyHTTPHandler(keyService, "admin-test-token", "internal-test-token", 1<<20, logger)
	invoker.register("gwai-virtual-key-control-plane", keyHandler)
	registry := controlplane.NewRemoteSubjectRegistry(invoker, "gwai-virtual-key-control-plane")
	controlService := controlplane.NewResourceService(
		userRepository, providerRepository, modelRepository, registry, registry,
	)
	controlHandler := controlplane.NewResourceHTTPHandler(controlService, "admin-test-token", 1<<20, logger)
	gatewayRuntime := controlplane.NewGatewayRuntime(keyRepository, modelRepository, providerRepository)
	providerRuntime := controlplane.NewProviderRuntime(providerRepository)
	adapterHandler := anthropic.NewHTTPHandler(
		providerRuntime, staticSecrets{value: "anthropic-secret"}, providerServer.Client(),
		anthropic.Config{
			Runtime: adapterconfig.Config{
				AppID: "gwai-anthropic-test", BaseURL: providerServer.URL, APIVersion: "2023-06-01",
				SecretRef: daprhttp.SecretRef{Store: "kubernetes", Name: "anthropic", Key: "api-key"},
			},
			MaxBody:                10 << 20,
			DefaultMaxOutputTokens: 4096,
		}, logger,
	)
	invoker.register("gwai-anthropic-test", adapterHandler)
	gatewayHandler := openaichat.NewHTTPHandler(gatewayRuntime, invoker, 10<<20, time.Minute, logger)

	user := adminRequest[controlplane.User](t, controlHandler, http.MethodPost, "/v1/users", controlplane.UserInput{Name: "Ada", Email: "ada@example.com"})
	provider := adminRequest[controlplane.Provider](t, controlHandler, http.MethodPost, "/v1/providers", controlplane.ProviderInput{
		Slug: "anthropic-test", Name: "test Anthropic", Kind: "anthropic",
		AdapterAppID: "gwai-anthropic-test",
	})
	model := adminRequest[controlplane.Model](t, controlHandler, http.MethodPost, "/v1/models", controlplane.ModelInput{
		Alias: "assistant", ProviderID: provider.ID, UpstreamModel: "claude-test",
	})
	createdKey := adminRequest[controlplane.CreatedVirtualKey](t, keyHandler, http.MethodPost, "/v1/virtual-keys", controlplane.VirtualKeyInput{
		Name: "client", UserID: user.ID, ModelIDs: []string{model.ID},
	})

	requestBody := []byte(`{
		"model":"assistant",
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
	var completion openaichat.ChatCompletionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Model != model.Alias || completion.Choices[0].Message.Content == nil || *completion.Choices[0].Message.Content != "Ciao dal provider" {
		t.Fatalf("unexpected completion: %+v", completion)
	}
	if completion.Usage.PromptTokens != 8 || completion.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected usage: %+v", completion.Usage)
	}
	if captured.Model != "claude-test" || captured.MaxTokens != 64 || len(captured.System) != 1 || captured.Messages[0].Content[0].Text != "Saluta" {
		t.Fatalf("unexpected request received by Anthropic: %+v", captured)
	}

	responsesGateway := openairesponses.NewGatewayHTTPHandler(gatewayRuntime, invoker, 10<<20, time.Minute, logger)
	request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{
		"model":"assistant","input":"Saluta","max_output_tokens":64,"store":false
	}`)))
	request.Header.Set("Authorization", "Bearer "+createdKey.Key)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	responsesGateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Responses gateway returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var responsesOutput openairesponses.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &responsesOutput); err != nil {
		t.Fatal(err)
	}
	if responsesOutput.Output[0].Content[0].Text != "Ciao dal provider" || responsesOutput.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected Responses output: %+v", responsesOutput)
	}

	anthropicGateway := anthropic.NewGatewayHTTPHandler(dataplane.NewDispatcher(gatewayRuntime, invoker, time.Minute), 10<<20, logger)
	request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{
		"model":"assistant","max_tokens":64,"system":"Rispondi in italiano",
		"messages":[{"role":"user","content":"Saluta"}]
	}`)))
	request.Header.Set("x-api-key", createdKey.Key)
	request.Header.Set("anthropic-version", anthropic.PublicAPIVersion)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	anthropicGateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Anthropic gateway returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var anthropicOutput anthropic.MessageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &anthropicOutput); err != nil {
		t.Fatal(err)
	}
	if anthropicOutput.Content[0].Text != "Ciao dal provider" || anthropicOutput.Usage.InputTokens+anthropicOutput.Usage.OutputTokens != 12 {
		t.Fatalf("unexpected Anthropic output: %+v", anthropicOutput)
	}

	geminiGateway := gemini.NewGatewayHTTPHandler(gatewayRuntime, invoker, gemini.GatewayConfig{APIVersion: "v1beta", MaxBody: 10 << 20, RequestTimeout: time.Minute}, logger)
	request = httptest.NewRequest(http.MethodPost, "/v1beta/models/assistant:generateContent", bytes.NewReader([]byte(`{
		"systemInstruction":{"parts":[{"text":"Rispondi in italiano"}]},
		"contents":[{"role":"user","parts":[{"text":"Saluta"}]}],
		"generationConfig":{"maxOutputTokens":64}
	}`)))
	request.Header.Set("x-goog-api-key", createdKey.Key)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	geminiGateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Gemini gateway returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var geminiOutput gemini.GenerateContentResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &geminiOutput); err != nil {
		t.Fatal(err)
	}
	if *geminiOutput.Candidates[0].Content.Parts[0].Text != "Ciao dal provider" || geminiOutput.UsageMetadata.TotalTokenCount != 12 {
		t.Fatalf("unexpected Gemini output: %+v", geminiOutput)
	}
}

func TestDispatcherSelectsEachConfiguredAdapterAndRewritesUpstreamModel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	userRepository := controlplane.NewUserRepository(state.NewMemoryStore())
	providerStore := state.NewMemoryStore()
	providerRepository := controlplane.NewProviderRepository(providerStore)
	modelRepository := controlplane.NewModelRepository(providerStore)
	keyRepository := controlplane.NewVirtualKeyRepository(state.NewMemoryStore())
	invoker := newLocalInvoker()
	keyService := controlplane.NewVirtualKeyService(keyRepository)
	keyHandler := controlplane.NewVirtualKeyHTTPHandler(keyService, "admin-test-token", "internal-test-token", 1<<20, logger)
	invoker.register("gwai-virtual-key-control-plane", keyHandler)
	registry := controlplane.NewRemoteSubjectRegistry(invoker, "gwai-virtual-key-control-plane")
	controlService := controlplane.NewResourceService(
		userRepository, providerRepository, modelRepository, registry, registry,
	)
	controlHandler := controlplane.NewResourceHTTPHandler(controlService, "admin-test-token", 1<<20, logger)
	providerRuntime := controlplane.NewProviderRuntime(providerRepository)
	secrets := &deploymentSecrets{values: make(map[daprhttp.SecretRef]string)}

	type adapterCase struct {
		name          string
		kind          string
		slug          string
		appID         string
		alias         string
		upstreamModel string
		apiVersion    string
		secretRef     daprhttp.SecretRef
		secret        string
		expectedPath  string
		authHeader    string
		authValue     string
		observations  <-chan upstreamObservation
		model         controlplane.Model
	}
	cases := []adapterCase{
		{
			name: "anthropic", kind: controlplane.ProviderKindAnthropic, slug: "anthropic-deployment",
			appID: "adapter-anthropic-integration", alias: "public-anthropic", upstreamModel: "claude-private-integration",
			apiVersion: "anthropic-version-integration",
			secretRef:  daprhttp.SecretRef{Store: "store-anthropic", Name: "secret-anthropic", Key: "key-anthropic", Namespace: "ns-anthropic"},
			secret:     "credential-anthropic", expectedPath: "/v1/messages", authHeader: "x-api-key", authValue: "credential-anthropic",
		},
		{
			name: "openai-chat", kind: controlplane.ProviderKindOpenAIChat, slug: "chat-deployment",
			appID: "adapter-chat-integration", alias: "public-chat", upstreamModel: "gpt-chat-private-integration",
			apiVersion: "chat-api-integration",
			secretRef:  daprhttp.SecretRef{Store: "store-chat", Name: "secret-chat", Key: "key-chat", Namespace: "ns-chat"},
			secret:     "credential-chat", expectedPath: "/chat-api-integration/chat/completions", authHeader: "Authorization", authValue: "Bearer credential-chat",
		},
		{
			name: "openai-responses", kind: controlplane.ProviderKindOpenAIResponses, slug: "responses-deployment",
			appID: "adapter-responses-integration", alias: "public-responses", upstreamModel: "gpt-responses-private-integration",
			apiVersion: "responses-api-integration",
			secretRef:  daprhttp.SecretRef{Store: "store-responses", Name: "secret-responses", Key: "key-responses", Namespace: "ns-responses"},
			secret:     "credential-responses", expectedPath: "/responses-api-integration/responses", authHeader: "Authorization", authValue: "Bearer credential-responses",
		},
		{
			name: "gemini", kind: controlplane.ProviderKindGemini, slug: "gemini-deployment",
			appID: "adapter-gemini-integration", alias: "public-gemini", upstreamModel: "gemini-private-integration",
			apiVersion: "gemini-api-integration",
			secretRef:  daprhttp.SecretRef{Store: "store-gemini", Name: "secret-gemini", Key: "key-gemini", Namespace: "ns-gemini"},
			secret:     "credential-gemini", expectedPath: "/gemini-api-integration/models/gemini-private-integration:generateContent", authHeader: "x-goog-api-key", authValue: "credential-gemini",
		},
	}

	modelIDs := make([]string, 0, len(cases))
	for index := range cases {
		testCase := &cases[index]
		upstream, observations := newConfiguredUpstream(testCase.kind)
		t.Cleanup(upstream.Close)
		testCase.observations = observations
		secrets.values[testCase.secretRef] = testCase.secret

		provider := adminRequest[controlplane.Provider](t, controlHandler, http.MethodPost, "/v1/providers", controlplane.ProviderInput{
			Slug: testCase.slug, Name: testCase.name + " provider", Kind: testCase.kind, AdapterAppID: testCase.appID,
		})
		testCase.model = adminRequest[controlplane.Model](t, controlHandler, http.MethodPost, "/v1/models", controlplane.ModelInput{
			Alias: testCase.alias, ProviderID: provider.ID, UpstreamModel: testCase.upstreamModel,
		})
		modelIDs = append(modelIDs, testCase.model.ID)

		runtimeConfig := adapterconfig.Config{
			AppID: testCase.appID, BaseURL: upstream.URL, APIVersion: testCase.apiVersion, SecretRef: testCase.secretRef,
		}
		var handler http.Handler
		switch testCase.kind {
		case controlplane.ProviderKindAnthropic:
			handler = anthropic.NewHTTPHandler(providerRuntime, secrets, upstream.Client(), anthropic.Config{
				Runtime: runtimeConfig, MaxBody: 1 << 20, AppToken: "internal-test-token", DefaultMaxOutputTokens: 256,
			}, logger)
		case controlplane.ProviderKindOpenAIChat:
			handler = openaichat.NewAdapterHTTPHandler(providerRuntime, secrets, upstream.Client(), openaichat.AdapterConfig{
				Runtime: runtimeConfig, MaxBody: 1 << 20, AppToken: "internal-test-token",
			}, logger)
		case controlplane.ProviderKindOpenAIResponses:
			handler = openairesponses.NewAdapterHTTPHandler(providerRuntime, secrets, upstream.Client(), openairesponses.AdapterConfig{
				Runtime: runtimeConfig, MaxBody: 1 << 20, AppToken: "internal-test-token", MaxOutputTokens: 4096,
			}, logger)
		case controlplane.ProviderKindGemini:
			handler = gemini.NewAdapterHTTPHandler(providerRuntime, secrets, upstream.Client(), gemini.AdapterConfig{
				Runtime: runtimeConfig, MaxBody: 1 << 20, AppToken: "internal-test-token", DefaultMaxOutputTokens: 256,
			}, logger)
		default:
			t.Fatalf("unsupported test provider kind %q", testCase.kind)
		}
		invoker.register(testCase.appID, handler)
	}

	user := adminRequest[controlplane.User](t, controlHandler, http.MethodPost, "/v1/users", controlplane.UserInput{
		Name: "Multi Adapter User", Email: "multi-adapter@example.com",
	})
	createdKey := adminRequest[controlplane.CreatedVirtualKey](t, keyHandler, http.MethodPost, "/v1/virtual-keys", controlplane.VirtualKeyInput{
		Name: "all adapters", UserID: user.ID, ModelIDs: modelIDs,
	})
	gatewayRuntime := controlplane.NewGatewayRuntime(keyRepository, modelRepository, providerRepository)
	dispatcher := dataplane.NewDispatcher(gatewayRuntime, invoker, time.Second)

	for index := range cases {
		testCase := &cases[index]
		t.Run(testCase.name, func(t *testing.T) {
			before := len(invoker.invocationSnapshot())
			response, err := dispatcher.Generate(context.Background(), createdKey.Key, testCase.alias, "req-"+testCase.name,
				func(route controlplane.Route, requestID string) (ir.Request, error) {
					return ir.Request{
						Version: ir.Version, ID: requestID,
						Route:    ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
						Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
					}, nil
				})
			if err != nil {
				t.Fatalf("dispatch through %s: %v", testCase.appID, err)
			}
			if response.Model != testCase.upstreamModel {
				t.Fatalf("adapter returned model %q, want rewritten upstream model %q", response.Model, testCase.upstreamModel)
			}

			invocations := invoker.invocationSnapshot()
			if len(invocations) != before+1 {
				t.Fatalf("dispatcher made %d invocations, want exactly one", len(invocations)-before)
			}
			invocation := invocations[before]
			if invocation.appID != testCase.appID || invocation.method != "/v1/generate" {
				t.Fatalf("dispatcher invoked appID=%q method=%q, want appID=%q method=/v1/generate", invocation.appID, invocation.method, testCase.appID)
			}

			observation := <-testCase.observations
			if observation.decodeError != nil {
				t.Fatalf("decode upstream request: %v", observation.decodeError)
			}
			if observation.path != testCase.expectedPath {
				t.Fatalf("upstream path %q, want deployment-owned path %q", observation.path, testCase.expectedPath)
			}
			if observation.model != testCase.upstreamModel {
				t.Fatalf("upstream received model %q, want rewritten model %q", observation.model, testCase.upstreamModel)
			}
			if value := observation.headers.Get(testCase.authHeader); value != testCase.authValue {
				t.Fatalf("upstream auth header %q, want %q", value, testCase.authValue)
			}
			if testCase.kind == controlplane.ProviderKindAnthropic {
				if version := observation.headers.Get("anthropic-version"); version != testCase.apiVersion {
					t.Fatalf("Anthropic version %q, want deployment-owned version %q", version, testCase.apiVersion)
				}
			}
		})
	}

	usedSecrets := secrets.usedSnapshot()
	if len(usedSecrets) != len(cases) {
		t.Fatalf("adapters resolved %d secrets, want %d", len(usedSecrets), len(cases))
	}
	for index, testCase := range cases {
		if usedSecrets[index] != testCase.secretRef {
			t.Fatalf("adapter %s used secret ref %+v, want its deployment-owned ref %+v", testCase.name, usedSecrets[index], testCase.secretRef)
		}
	}
}
