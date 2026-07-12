package openairesponses

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

type gatewayRuntime struct {
	token string
	model string
	route controlplane.Route
	err   error
}

func (runtime *gatewayRuntime) Authorize(_ context.Context, token, model string) (controlplane.Authorization, error) {
	runtime.token = token
	runtime.model = model
	if runtime.err != nil {
		return controlplane.Authorization{}, runtime.err
	}
	return controlplane.Authorization{KeyID: "key"}, nil
}

func (runtime *gatewayRuntime) ResolveRoute(_ context.Context, model string) (controlplane.Route, error) {
	runtime.model = model
	if runtime.err != nil {
		return controlplane.Route{}, runtime.err
	}
	return runtime.route, nil
}

type gatewayInvoker struct {
	appID   string
	method  string
	request ir.Request
	called  bool
	err     error
}

func (invoker *gatewayInvoker) InvokeJSON(_ context.Context, appID, method string, input, output any) error {
	invoker.called = true
	invoker.appID = appID
	invoker.method = method
	invoker.request = input.(ir.Request)
	if invoker.err != nil {
		return invoker.err
	}
	response := output.(*ir.Response)
	*response = ir.Response{
		Version: ir.Version, ID: invoker.request.ID, Model: invoker.request.Route.UpstreamModel,
		Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}, FinishReason: ir.FinishStop,
		Usage: ir.Usage{InputTokens: 2, OutputTokens: 1},
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGatewayDispatchesThroughIRAndReturnsResponsesEnvelope(t *testing.T) {
	runtime := &gatewayRuntime{route: controlplane.Route{ProviderID: "prv", UpstreamModel: "gpt", AdapterAppID: "responses-adapter"}}
	invoker := &gatewayInvoker{}
	handler := NewGatewayHTTPHandler(runtime, invoker, 1<<20, 0, testLogger())
	body := []byte(`{"model":"virtual/gpt","input":"hello","store":false,"stream":false}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer vk_test")
	request.Header.Set("X-Request-ID", "req_client")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if runtime.token != "vk_test" || runtime.model != "virtual/gpt" || !invoker.called || invoker.appID != "responses-adapter" || invoker.method != "/v1/generate" {
		t.Fatalf("unexpected dispatch: runtime=%+v invoker=%+v", runtime, invoker)
	}
	if invoker.request.ID != "req_client" || invoker.request.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected IR request: %+v", invoker.request)
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Object != "response" || response.Model != "virtual/gpt" || response.Output[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestGatewayRejectsUnsupportedStateBeforeInvocation(t *testing.T) {
	runtime := &gatewayRuntime{route: controlplane.Route{ProviderID: "prv", UpstreamModel: "gpt", AdapterAppID: "adapter"}}
	invoker := &gatewayInvoker{}
	handler := NewGatewayHTTPHandler(runtime, invoker, 1<<20, 0, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m","input":"hello","previous_response_id":"resp_1"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || invoker.called {
		t.Fatalf("expected pre-invocation 400, got %d called=%v body=%s", recorder.Code, invoker.called, recorder.Body.String())
	}
	var response ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Param == nil || *response.Error.Param != "previous_response_id" || response.Error.Code != "unsupported_parameter" {
		t.Fatalf("unexpected API error: %+v", response)
	}
}

func TestGatewayRequiresBearerAuthentication(t *testing.T) {
	handler := NewGatewayHTTPHandler(&gatewayRuntime{}, &gatewayInvoker{}, 1<<20, 0, testLogger())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"m","input":"hi"}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("expected authentication error, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
