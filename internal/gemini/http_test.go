package gemini

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

	"github.com/Albe83/gwai/internal/adapterconfig"
	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/ir"
)

type fakeGatewayRuntime struct {
	authorizedToken string
	authorizedModel string
	route           controlplane.Route
	err             error
}

func (f *fakeGatewayRuntime) Authorize(_ context.Context, token, model string) (controlplane.Authorization, error) {
	f.authorizedToken = token
	f.authorizedModel = model
	return controlplane.Authorization{}, f.err
}

func (f *fakeGatewayRuntime) ResolveRoute(_ context.Context, _ string) (controlplane.Route, error) {
	return f.route, f.err
}

type fakeInvoker struct {
	appID   string
	method  string
	request ir.Request
	result  ir.Response
	err     error
}

func (f *fakeInvoker) InvokeJSON(_ context.Context, appID, method string, input, output any) error {
	f.appID = appID
	f.method = method
	f.request = input.(ir.Request)
	*(output.(*ir.Response)) = f.result
	return f.err
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestGatewayGenerateContentDispatchesThroughIR(t *testing.T) {
	runtime := &fakeGatewayRuntime{route: controlplane.Route{ProviderID: "prv_g", UpstreamModel: "gemini-3-flash", AdapterAppID: "gemini-team"}}
	invoker := &fakeInvoker{result: ir.Response{
		Version: ir.Version, ID: "req_internal", Model: "gemini-3-flash", ProviderResponseID: "resp_1",
		Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}, FinishReason: ir.FinishStop,
		Usage: ir.Usage{InputTokens: 3, OutputTokens: 1},
	}}
	handler := NewGatewayHTTPHandler(runtime, invoker, GatewayConfig{APIVersion: "v1beta", MaxBody: 1 << 20}, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/team/gemini-3-flash:generateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-goog-api-key", "gwai-key")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if runtime.authorizedToken != "gwai-key" || runtime.authorizedModel != "team/gemini-3-flash" {
		t.Fatalf("unexpected authorization input: token=%q model=%q", runtime.authorizedToken, runtime.authorizedModel)
	}
	if invoker.appID != "gemini-team" || invoker.method != "/v1/generate" || invoker.request.Route.UpstreamModel != "gemini-3-flash" {
		t.Fatalf("unexpected dispatch: %#v", invoker)
	}
	var response GenerateContentResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ResponseID != "resp_1" || response.ModelVersion != "team/gemini-3-flash" || *response.Candidates[0].Content.Parts[0].Text != "hello" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestGatewayRejectsStreamingAndUnknownFields(t *testing.T) {
	handler := NewGatewayHTTPHandler(&fakeGatewayRuntime{}, &fakeInvoker{}, GatewayConfig{APIVersion: "v1beta", MaxBody: 1 << 20}, testLogger())
	for _, test := range []struct {
		path string
		body string
	}{
		{"/v1beta/models/team/model:streamGenerateContent", `{"contents":[]}`},
		{"/v1beta/models/team/model:generateContent", `{"contents":[],"unknown":true}`},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("x-goog-api-key", "key")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", test.path, recorder.Code, recorder.Body.String())
		}
		var response ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Error.Status != "INVALID_ARGUMENT" {
			t.Fatalf("unexpected Gemini error: %#v (%v)", response, err)
		}
	}
}

type fakeProviderResolver struct {
	provider controlplane.Provider
	appID    string
}

func (f *fakeProviderResolver) ResolveProviderByAdapterAppID(_ context.Context, appID string) (controlplane.Provider, error) {
	f.appID = appID
	return f.provider, nil
}

type fakeSecretResolver struct {
	value string
	ref   daprhttp.SecretRef
}

func (f *fakeSecretResolver) Get(_ context.Context, ref daprhttp.SecretRef) (string, error) {
	f.ref = ref
	return f.value, nil
}

func TestAdapterCallsGeminiAndPreservesThoughtSignature(t *testing.T) {
	var received GenerateContentRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-3-flash:generateContent" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "provider-key" {
			t.Errorf("unexpected provider key %q", r.Header.Get("x-goog-api-key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"fc_provider","name":"lookup","args":{"q":"next"}},"thoughtSignature":"provider-signature"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":2},"modelVersion":"gemini-3-flash","responseId":"provider-response"}`)
	}))
	defer upstream.Close()
	provider := controlplane.Provider{
		ID: "prv_g", Slug: "team", Kind: controlplane.ProviderKindGemini,
		AdapterAppID: "gemini-team", Status: controlplane.StatusActive,
	}
	secretRef := daprhttp.SecretRef{Store: "secrets", Name: "gemini", Key: "api-key"}
	providers := &fakeProviderResolver{provider: provider}
	secrets := &fakeSecretResolver{value: "provider-key"}
	handler := NewAdapterHTTPHandler(providers, secrets, upstream.Client(), AdapterConfig{
		Runtime: adapterconfig.Config{
			AppID: "gemini-team", BaseURL: upstream.URL, APIVersion: "v1beta", SecretRef: secretRef,
		},
		MaxBody: 1 << 20, DefaultMaxOutputTokens: 256,
	}, testLogger())
	internalRequest := ir.Request{
		Version: ir.Version, ID: "req_adapter", Route: ir.Route{ProviderID: "prv_g", UpstreamModel: "gemini-3-flash"},
		Messages: []ir.Message{
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "start"}}},
			{Role: ir.RoleAssistant, Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"now"}`)}}}},
			{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{ToolCallID: "call_1", Result: json.RawMessage(`{"value":1}`)}}}},
		},
		Tools: []ir.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	body, err := json.Marshal(internalRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(received.Contents) < 2 || received.Contents[1].Parts[0].ThoughtSignature != SkipThoughtSignatureValidator {
		t.Fatalf("missing Gemini 3 fallback signature: %#v", received)
	}
	if received.Contents[2].Parts[0].FunctionResponse.Name != "lookup" {
		t.Fatalf("tool result name was not resolved: %#v", received.Contents[2])
	}
	var response ir.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ProviderResponseID != "provider-response" || response.Content[0].ToolCall.Signature != "provider-signature" || response.FinishReason != ir.FinishToolCalls {
		t.Fatalf("unexpected IR response: %#v", response)
	}
	if providers.appID != "gemini-team" || secrets.ref != secretRef {
		t.Fatalf("unexpected runtime resolution: appID=%q ref=%+v", providers.appID, secrets.ref)
	}
}

func TestAdapterRejectsRouteForAnotherInstance(t *testing.T) {
	provider := controlplane.Provider{ID: "prv_g", Kind: controlplane.ProviderKindGemini, AdapterAppID: "other", Status: controlplane.StatusActive}
	handler := NewAdapterHTTPHandler(&fakeProviderResolver{provider: provider}, &fakeSecretResolver{}, nil, AdapterConfig{
		Runtime: adapterconfig.Config{AppID: "gemini-team"}, MaxBody: 1 << 20,
	}, testLogger())
	requestBody, err := json.Marshal(ir.Request{
		Version: ir.Version, ID: "req", Route: ir.Route{ProviderID: "prv_g", UpstreamModel: "gemini"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/generate", bytes.NewReader(requestBody))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected route isolation failure, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
