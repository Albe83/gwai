package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Albe83/gwai/internal/daprhttp"
)

type Runtime interface {
	Authorize(context.Context, string, string) (Authorization, error)
	ResolveRoute(context.Context, string) (Route, error)
	ResolveProvider(context.Context, string) (Provider, error)
}

type Invoker interface {
	InvokeJSON(context.Context, string, string, any, any) error
}

type Client struct {
	invoker Invoker
	appID   string
}

func transientInvocationError(err error) bool {
	var httpError *daprhttp.HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode >= http.StatusInternalServerError
}

func (c *Client) invoke(ctx context.Context, method string, input, output any) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = c.invoker.InvokeJSON(ctx, c.appID, method, input, output)
		if err == nil || !transientInvocationError(err) || attempt == 1 {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func NewClient(invoker Invoker, appID string) *Client {
	return &Client{invoker: invoker, appID: appID}
}

func mapInvocationError(err error) error {
	var httpError *daprhttp.HTTPError
	if !errors.As(err, &httpError) {
		return err
	}
	switch httpError.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	default:
		return err
	}
}

func (c *Client) Authorize(ctx context.Context, token, model string) (Authorization, error) {
	var result Authorization
	err := c.invoke(ctx, "/internal/v1/authorize", map[string]string{"token": token, "model": model}, &result)
	if err != nil {
		return Authorization{}, fmt.Errorf("authorize virtual key: %w", mapInvocationError(err))
	}
	return result, nil
}

func (c *Client) ResolveRoute(ctx context.Context, alias string) (Route, error) {
	var result Route
	err := c.invoke(ctx, "/internal/v1/routes/resolve", map[string]string{"alias": alias}, &result)
	if err != nil {
		return Route{}, fmt.Errorf("resolve model route: %w", mapInvocationError(err))
	}
	return result, nil
}

func (c *Client) ResolveProvider(ctx context.Context, id string) (Provider, error) {
	var result Provider
	err := c.invoke(ctx, "/internal/v1/providers/resolve", map[string]string{"id": id}, &result)
	if err != nil {
		return Provider{}, fmt.Errorf("resolve provider: %w", mapInvocationError(err))
	}
	return result, nil
}
