package controlplane

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Albe83/gwai/internal/daprhttp"
)

type errorInvoker struct {
	err error
}

func (i errorInvoker) InvokeJSON(context.Context, string, string, any, any) error {
	return i.err
}

type captureInvoker struct {
	appID   string
	method  string
	payload any
}

func (i *captureInvoker) InvokeJSON(_ context.Context, appID, method string, input, _ any) error {
	i.appID, i.method, i.payload = appID, method, input
	return nil
}

func TestRemoteSubjectRegistryMapsDomainErrors(t *testing.T) {
	subject := KeySubject{UserID: "usr_test", Status: StatusDisabled, Revision: 2, Deleted: true}
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"conflict", http.StatusConflict, ErrConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewRemoteSubjectRegistry(errorInvoker{err: &daprhttp.HTTPError{StatusCode: test.status}}, "keys")
			if err := client.FenceSubject(context.Background(), subject); !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestRemoteSubjectRegistryDoesNotExposeDaprRoutingFailuresAsDomainNotFound(t *testing.T) {
	client := NewRemoteSubjectRegistry(errorInvoker{err: &daprhttp.HTTPError{StatusCode: http.StatusNotFound}}, "keys")
	err := client.SyncSubject(context.Background(), KeySubject{})
	if err == nil || errors.Is(err, ErrNotFound) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Dapr routing 404 must remain an internal invocation failure, got %v", err)
	}
}

func TestRemoteSubjectRegistryRequiresConfiguration(t *testing.T) {
	client := NewRemoteSubjectRegistry(nil, "")
	if err := client.SyncSubject(context.Background(), KeySubject{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing invoker and app ID returned %v, want unavailable", err)
	}
}

func TestRemoteSubjectRegistryInvokesModelProjectionEndpoints(t *testing.T) {
	invoker := &captureInvoker{}
	client := NewRemoteSubjectRegistry(invoker, "keys")
	subject := ModelSubject{ModelID: "mdl_test", Alias: "model", Status: StatusActive, Revision: 1}
	if err := client.SyncModel(context.Background(), subject); err != nil {
		t.Fatal(err)
	}
	if invoker.appID != "keys" || invoker.method != "/internal/v1/model-subjects/sync" || invoker.payload != subject {
		t.Fatalf("unexpected sync invocation: %+v", invoker)
	}
	subject.Status, subject.Deleted = StatusDisabled, true
	if err := client.FenceModel(context.Background(), subject); err != nil {
		t.Fatal(err)
	}
	if invoker.method != "/internal/v1/model-subjects/fence" || invoker.payload != subject {
		t.Fatalf("unexpected fence invocation: %+v", invoker)
	}
}

func TestRemoteSubjectRegistryMapsModelConflict(t *testing.T) {
	client := NewRemoteSubjectRegistry(errorInvoker{err: &daprhttp.HTTPError{StatusCode: http.StatusConflict}}, "keys")
	err := client.FenceModel(context.Background(), ModelSubject{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected model conflict, got %v", err)
	}
}
