package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Albe83/gwai/internal/daprhttp"
)

type JSONInvoker interface {
	InvokeJSON(context.Context, string, string, any, any) error
}

// RemoteSubjectRegistry is the internal Dapr client used by the resource
// control plane for user and model authorization projections. The virtual-key
// service remains the sole writer for that projection state.
type RemoteSubjectRegistry struct {
	invoker JSONInvoker
	appID   string
}

func NewRemoteSubjectRegistry(invoker JSONInvoker, appID string) *RemoteSubjectRegistry {
	return &RemoteSubjectRegistry{invoker: invoker, appID: appID}
}

func (r *RemoteSubjectRegistry) SyncSubject(ctx context.Context, subject KeySubject) error {
	return r.invoke(ctx, "/internal/v1/subjects/sync", subject, "authorization subject")
}

func (r *RemoteSubjectRegistry) FenceSubject(ctx context.Context, subject KeySubject) error {
	return r.invoke(ctx, "/internal/v1/subjects/fence", subject, "authorization subject")
}

func (r *RemoteSubjectRegistry) SyncModel(ctx context.Context, subject ModelSubject) error {
	return r.invoke(ctx, "/internal/v1/model-subjects/sync", subject, "model subject")
}

func (r *RemoteSubjectRegistry) FenceModel(ctx context.Context, subject ModelSubject) error {
	return r.invoke(ctx, "/internal/v1/model-subjects/fence", subject, "model subject")
}

func (r *RemoteSubjectRegistry) invoke(ctx context.Context, method string, payload any, resource string) error {
	if r == nil || r.invoker == nil || r.appID == "" {
		return fmt.Errorf("%w: virtual-key control-plane invocation is not configured", ErrUnavailable)
	}
	if err := r.invoker.InvokeJSON(ctx, r.appID, method, payload, nil); err != nil {
		var httpErr *daprhttp.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusConflict:
				return fmt.Errorf("%w: %s", ErrConflict, resource)
			}
		}
		return fmt.Errorf("%w: invoke virtual-key control-plane: %w", ErrUnavailable, err)
	}
	return nil
}
