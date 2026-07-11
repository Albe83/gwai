package controlplane

import (
	"context"
	"net/http"
	"testing"

	"github.com/Albe83/gwai/internal/daprhttp"
)

type retryInvoker struct {
	calls int
}

func (i *retryInvoker) InvokeJSON(_ context.Context, _, _ string, _, output any) error {
	i.calls++
	if i.calls == 1 {
		return &daprhttp.HTTPError{StatusCode: http.StatusInternalServerError, Body: "stale endpoint"}
	}
	*(output.(*Route)) = Route{Alias: "model", ProviderID: "provider"}
	return nil
}

func TestClientRetriesTransientDaprInvocation(t *testing.T) {
	invoker := &retryInvoker{}
	client := NewClient(invoker, "control-plane")
	route, err := client.ResolveRoute(context.Background(), "model")
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 2 || route.ProviderID != "provider" {
		t.Fatalf("unexpected retry result: calls=%d route=%+v", invoker.calls, route)
	}
}
