// Package dataplane contains protocol-neutral request execution shared by
// client-facing gateways. Wire-format parsing and error envelopes remain in
// each protocol package.
package dataplane

import (
	"context"
	"fmt"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type Runtime interface {
	Authorize(context.Context, string, string) (controlplane.Authorization, error)
	ResolveRoute(context.Context, string) (controlplane.Route, error)
}

type Invoker interface {
	InvokeJSON(context.Context, string, string, any, any) error
}

type Translator func(controlplane.Route, string) (ir.Request, error)

type Dispatcher struct {
	runtime Runtime
	invoker Invoker
	timeout time.Duration
}

func NewDispatcher(runtime Runtime, invoker Invoker, timeout time.Duration) *Dispatcher {
	return &Dispatcher{runtime: runtime, invoker: invoker, timeout: timeout}
}

func (d *Dispatcher) Generate(ctx context.Context, token, model, requestID string, translate Translator) (ir.Response, error) {
	if d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.timeout)
		defer cancel()
	}
	if _, err := d.runtime.Authorize(ctx, token, model); err != nil {
		return ir.Response{}, err
	}
	route, err := d.runtime.ResolveRoute(ctx, model)
	if err != nil {
		return ir.Response{}, err
	}
	request, err := translate(route, requestID)
	if err != nil {
		return ir.Response{}, err
	}
	if err := request.Validate(); err != nil {
		return ir.Response{}, fmt.Errorf("validate translated IR request: %w", err)
	}
	var response ir.Response
	if err := d.invoker.InvokeJSON(ctx, route.AdapterAppID, "/v1/generate", request, &response); err != nil {
		return ir.Response{}, err
	}
	if err := response.Validate(); err != nil {
		return ir.Response{}, fmt.Errorf("validate adapter IR response: %w", err)
	}
	return response, nil
}
