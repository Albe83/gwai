package dataplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type runtimeStub struct {
	authorizedModel string
	resolvedModel   string
	route           controlplane.Route
	err             error
}

func (r *runtimeStub) Authorize(_ context.Context, _, model string) (controlplane.Authorization, error) {
	r.authorizedModel = model
	return controlplane.Authorization{}, r.err
}

func (r *runtimeStub) ResolveRoute(_ context.Context, model string) (controlplane.Route, error) {
	r.resolvedModel = model
	return r.route, r.err
}

type invokerStub struct {
	appID  string
	method string
	input  ir.Request
	output ir.Response
}

func (i *invokerStub) InvokeJSON(_ context.Context, appID, method string, input, output any) error {
	i.appID = appID
	i.method = method
	i.input = input.(ir.Request)
	*(output.(*ir.Response)) = i.output
	return nil
}

func TestDispatcherRoutesOnlyThroughProviderNeutralIR(t *testing.T) {
	runtime := &runtimeStub{route: controlplane.Route{ProviderID: "prv_1", UpstreamModel: "upstream", AdapterAppID: "adapter-a"}}
	invoker := &invokerStub{output: ir.Response{
		Version: ir.Version, ID: "req_1", Model: "upstream", FinishReason: ir.FinishStop,
		Content: []ir.Content{{Type: ir.ContentText, Text: "ok"}},
	}}
	dispatcher := NewDispatcher(runtime, invoker, time.Second)
	response, err := dispatcher.Generate(context.Background(), "gwai-key", "provider/model", "req_1", func(route controlplane.Route, id string) (ir.Request, error) {
		return ir.Request{
			Version: ir.Version, ID: id, Route: ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
			Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.authorizedModel != "provider/model" || runtime.resolvedModel != "provider/model" {
		t.Fatalf("unexpected runtime calls: %+v", runtime)
	}
	if invoker.appID != "adapter-a" || invoker.method != "/v1/generate" || invoker.input.Route.ProviderID != "prv_1" {
		t.Fatalf("unexpected adapter invocation: %+v", invoker)
	}
	if response.Content[0].Text != "ok" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestDispatcherStopsBeforeResolutionWhenAuthorizationFails(t *testing.T) {
	runtime := &runtimeStub{err: controlplane.ErrUnauthorized}
	dispatcher := NewDispatcher(runtime, &invokerStub{}, 0)
	_, err := dispatcher.Generate(context.Background(), "bad", "provider/model", "req_1", func(controlplane.Route, string) (ir.Request, error) {
		t.Fatal("translator must not run")
		return ir.Request{}, nil
	})
	if !errors.Is(err, controlplane.ErrUnauthorized) || runtime.resolvedModel != "" {
		t.Fatalf("expected authorization failure before route resolution, got %v", err)
	}
}

func TestDispatcherRejectsInvalidAdapterResponse(t *testing.T) {
	runtime := &runtimeStub{route: controlplane.Route{ProviderID: "prv_1", UpstreamModel: "upstream", AdapterAppID: "adapter-a"}}
	invoker := &invokerStub{output: ir.Response{Version: "old"}}
	dispatcher := NewDispatcher(runtime, invoker, 0)
	_, err := dispatcher.Generate(context.Background(), "key", "provider/model", "req_1", func(route controlplane.Route, id string) (ir.Request, error) {
		return ir.Request{
			Version: ir.Version, ID: id, Route: ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
			Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}},
		}, nil
	})
	if err == nil {
		t.Fatal("expected invalid adapter response to be rejected")
	}
}
