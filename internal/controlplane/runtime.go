package controlplane

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Runtime exposes the read-only registry operations needed by data-plane
// services. It shares persisted entities with the admin service but never
// performs a mutation.
type Runtime struct {
	repository *Repository
	now        func() time.Time
}

func NewRuntime(repository *Repository) *Runtime {
	return &Runtime{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func (r *Runtime) Authorize(ctx context.Context, token, qualifiedModel string) (Authorization, error) {
	if token == "" {
		return Authorization{}, ErrUnauthorized
	}
	key, err := r.repository.GetVirtualKeyByHash(ctx, hashKey(token))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Authorization{}, ErrUnauthorized
		}
		return Authorization{}, err
	}
	if key.Status != StatusActive || (key.ExpiresAt != nil && !key.ExpiresAt.After(r.now())) {
		return Authorization{}, ErrUnauthorized
	}
	user, err := r.repository.GetUser(ctx, key.UserID)
	if err != nil || user.Status != StatusActive {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Authorization{}, err
		}
		return Authorization{}, ErrUnauthorized
	}
	model, err := ParseQualifiedModel(qualifiedModel)
	if err != nil {
		return Authorization{}, err
	}
	if len(key.AllowedModels) > 0 && !slices.Contains(key.AllowedModels, model.String()) {
		return Authorization{}, ErrForbidden
	}
	return Authorization{KeyID: key.ID, UserID: key.UserID}, nil
}

func (r *Runtime) ResolveRoute(ctx context.Context, qualifiedModel string) (Route, error) {
	model, err := ParseQualifiedModel(qualifiedModel)
	if err != nil {
		return Route{}, err
	}
	provider, err := r.repository.GetProviderBySlug(ctx, model.ProviderSlug)
	if err != nil {
		return Route{}, err
	}
	if provider.Status != StatusActive {
		return Route{}, ErrForbidden
	}
	return Route{
		QualifiedModel: model.String(), ProviderID: provider.ID, UpstreamModel: model.UpstreamModel,
		AdapterAppID: provider.AdapterAppID,
	}, nil
}

func (r *Runtime) ResolveProviderBySlug(ctx context.Context, slug string) (Provider, error) {
	provider, err := r.repository.GetProviderBySlug(ctx, slug)
	if err != nil {
		return Provider{}, err
	}
	if provider.Status != StatusActive {
		return Provider{}, ErrForbidden
	}
	return provider, nil
}
