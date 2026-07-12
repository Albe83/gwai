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
	"github.com/Albe83/gwai/internal/ir"
)

type chatGatewayRuntime struct {
	token string
	model string
	route controlplane.Route
}

func (r *chatGatewayRuntime) Authorize(_ context.Context, token, model string) (controlplane.Authorization, error) {
	r.token, r.model = token, model
	return controlplane.Authorization{}, nil
}

func (r *chatGatewayRuntime) ResolveRoute(_ context.Context, _ string) (controlplane.Route, error) {
	return r.route, nil
}

type chatGatewayInvoker struct{ request ir.Request }

func (i *chatGatewayInvoker) InvokeJSON(_ context.Context, _, _ string, input, output any) error {
	i.request = input.(ir.Request)
	*(output.(*ir.Response)) = ir.Response{
		Version: ir.Version, ID: i.request.ID, Model: i.request.Route.UpstreamModel, FinishReason: ir.FinishStop,
		Content: []ir.Content{{Type: ir.ContentText, Text: "ok"}}, Usage: ir.Usage{InputTokens: 2, OutputTokens: 1},
	}
	return nil
}

func TestChatGatewayDispatchesThroughIR(t *testing.T) {
	runtime := &chatGatewayRuntime{route: controlplane.Route{ProviderID: "prv_1", UpstreamModel: "upstream", AdapterAppID: "adapter"}}
	invoker := &chatGatewayInvoker{}
	handler := NewHTTPHandler(runtime, invoker, 1<<20, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"provider/model","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer gwai-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected success, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if runtime.token != "gwai-key" || runtime.model != "provider/model" || invoker.request.Route.ProviderID != "prv_1" {
		t.Fatalf("unexpected dispatch: runtime=%+v request=%+v", runtime, invoker.request)
	}
	var response ChatCompletionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content == nil || *response.Choices[0].Message.Content != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestChatGatewayRejectsUnknownFields(t *testing.T) {
	handler := NewHTTPHandler(&chatGatewayRuntime{}, &chatGatewayInvoker{}, 1<<20, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"p/m","messages":[{"role":"user","content":"hi"}],"unknown":true}`))
	request.Header.Set("Authorization", "Bearer gwai-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown field rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
