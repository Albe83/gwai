package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/dataplane"
	"github.com/Albe83/gwai/internal/ir"
)

type gatewayRuntime struct {
	token string
	model string
	route controlplane.Route
	err   error
}

func (r *gatewayRuntime) Authorize(_ context.Context, token, model string) (controlplane.Authorization, error) {
	r.token, r.model = token, model
	if r.err != nil {
		return controlplane.Authorization{}, r.err
	}
	return controlplane.Authorization{KeyID: "key_1", UserID: "usr_1"}, nil
}

func (r *gatewayRuntime) ResolveRoute(_ context.Context, model string) (controlplane.Route, error) {
	r.model = model
	if r.err != nil {
		return controlplane.Route{}, r.err
	}
	return r.route, nil
}

type gatewayInvoker struct {
	appID  string
	method string
	input  ir.Request
	result ir.Response
	calls  int
}

func (i *gatewayInvoker) InvokeJSON(_ context.Context, appID, method string, input, output any) error {
	i.calls++
	i.appID, i.method = appID, method
	request, ok := input.(ir.Request)
	if !ok {
		return errors.New("input is not an IR request")
	}
	i.input = request
	response, ok := output.(*ir.Response)
	if !ok {
		return errors.New("output is not an IR response")
	}
	*response = i.result
	return nil
}

func gatewayTestHandler(runtime *gatewayRuntime, invoker *gatewayInvoker) http.Handler {
	dispatcher := dataplane.NewDispatcher(runtime, invoker, time.Second)
	return NewGatewayHTTPHandler(dispatcher, 1<<20, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestGatewayHTTPHandlerCreatesAnthropicMessage(t *testing.T) {
	runtime := &gatewayRuntime{route: controlplane.Route{
		QualifiedModel: "team/claude", ProviderID: "prv_1", UpstreamModel: "claude-sonnet", AdapterAppID: "adapter-1",
	}}
	invoker := &gatewayInvoker{result: ir.Response{
		Version: ir.Version, ID: "req_internal", Model: "claude-sonnet",
		Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}, FinishReason: ir.FinishStop,
		Usage: ir.Usage{InputTokens: 3, OutputTokens: 1},
	}}
	handler := gatewayTestHandler(runtime, invoker)
	body := `{"model":"team/claude","max_tokens":32,"system":"brief","messages":[{"role":"user","content":"hello"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", "gwai_secret")
	request.Header.Set("anthropic-version", PublicAPIVersion)
	request.Header.Set("X-Request-ID", "req_client")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected success, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if runtime.token != "gwai_secret" || runtime.model != "team/claude" || invoker.appID != "adapter-1" || invoker.method != "/v1/generate" {
		t.Fatalf("unexpected dispatch: runtime=%+v invoker=%+v", runtime, invoker)
	}
	if invoker.input.Route.ProviderID != "prv_1" || invoker.input.Messages[0].Role != ir.RoleSystem {
		t.Fatalf("unexpected translated request: %+v", invoker.input)
	}
	var response MessageResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.ID, "msg_") || response.Model != "team/claude" || response.Content[0].Text != "hello" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if recorder.Header().Get("request-id") != "req_client" {
		t.Fatalf("request-id header was not propagated: %q", recorder.Header().Get("request-id"))
	}
}

func TestGatewayHTTPHandlerRejectsAuthVersionBetaAndUnknownFields(t *testing.T) {
	runtime := &gatewayRuntime{route: controlplane.Route{ProviderID: "p", UpstreamModel: "m", AdapterAppID: "a"}}
	invoker := &gatewayInvoker{}
	tests := []struct {
		name    string
		headers map[string]string
		body    string
		status  int
		typeOf  string
	}{
		{name: "missing key", headers: map[string]string{"anthropic-version": PublicAPIVersion}, body: `{}`, status: 401, typeOf: "authentication_error"},
		{name: "missing version", headers: map[string]string{"x-api-key": "key"}, body: `{}`, status: 400, typeOf: "invalid_request_error"},
		{name: "unsupported version", headers: map[string]string{"x-api-key": "key", "anthropic-version": "2024-01-01"}, body: `{}`, status: 400, typeOf: "invalid_request_error"},
		{name: "beta", headers: map[string]string{"x-api-key": "key", "anthropic-version": PublicAPIVersion, "anthropic-beta": "extended-thinking"}, body: `{}`, status: 400, typeOf: "invalid_request_error"},
		{name: "unknown field", headers: map[string]string{"x-api-key": "key", "anthropic-version": PublicAPIVersion}, body: `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}],"unknown":true}`, status: 400, typeOf: "invalid_request_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := gatewayTestHandler(runtime, invoker)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("expected %d, got %d: %s", test.status, recorder.Code, recorder.Body.String())
			}
			var response ErrorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Type != "error" || response.Error.Type != test.typeOf {
				t.Fatalf("unexpected error response: %+v", response)
			}
		})
	}
	if invoker.calls != 0 {
		t.Fatalf("invalid requests reached adapter %d times", invoker.calls)
	}
}

func TestGatewayHTTPHandlerMapsAuthorizationError(t *testing.T) {
	runtime := &gatewayRuntime{err: controlplane.ErrUnauthorized}
	invoker := &gatewayInvoker{}
	handler := gatewayTestHandler(runtime, invoker)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`))
	request.Header.Set("x-api-key", "bad")
	request.Header.Set("anthropic-version", PublicAPIVersion)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || invoker.calls != 0 {
		t.Fatalf("unexpected authorization result %d calls=%d: %s", recorder.Code, invoker.calls, recorder.Body.String())
	}
}
