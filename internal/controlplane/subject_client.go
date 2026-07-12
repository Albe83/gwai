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
// control plane. The virtual-key service remains the sole state writer.
type RemoteSubjectRegistry struct {
	invoker JSONInvoker
	appID   string
}

func NewRemoteSubjectRegistry(invoker JSONInvoker, appID string) *RemoteSubjectRegistry {
	return &RemoteSubjectRegistry{invoker: invoker, appID: appID}
}

func (r *RemoteSubjectRegistry) SyncSubject(ctx context.Context, subject KeySubject) error {
	return r.invoke(ctx, "/internal/v1/subjects/sync", subject)
}

func (r *RemoteSubjectRegistry) FenceSubject(ctx context.Context, subject KeySubject) error {
	return r.invoke(ctx, "/internal/v1/subjects/fence", subject)
}

func (r *RemoteSubjectRegistry) invoke(ctx context.Context, method string, subject KeySubject) error {
	if r == nil || r.invoker == nil || r.appID == "" {
		return errors.New("virtual-key control-plane invocation is not configured")
	}
	if err := r.invoker.InvokeJSON(ctx, r.appID, method, subject, nil); err != nil {
		var httpErr *daprhttp.HTTPError
		if errors.As(err, &httpErr) {
			switch httpErr.StatusCode {
			case http.StatusConflict:
				return fmt.Errorf("%w: authorization subject", ErrConflict)
			}
		}
		return fmt.Errorf("%w: invoke virtual-key control-plane: %w", ErrUnavailable, err)
	}
	return nil
}
