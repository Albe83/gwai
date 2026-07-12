package controlplane

import (
	"context"
	"errors"
	"slices"
	"time"
)

// GatewayRuntime exposes only the virtual-key and routing reads needed by
// client-facing gateways. Its two repositories are backed by separate Dapr
// state components; it has no access to private control-plane user state.
type GatewayRuntime struct {
	keys      *VirtualKeyRepository
	providers *ProviderRepository
	now       func() time.Time
}

func NewGatewayRuntime(keys *VirtualKeyRepository, providers *ProviderRepository) *GatewayRuntime {
	return &GatewayRuntime{
		keys:      keys,
		providers: providers,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (r *GatewayRuntime) Authorize(ctx context.Context, token, qualifiedModel string) (Authorization, error) {
	if token == "" {
		return Authorization{}, ErrUnauthorized
	}
	key, err := r.keys.GetVirtualKeyByHash(ctx, hashKey(token))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Authorization{}, ErrUnauthorized
		}
		return Authorization{}, err
	}
	if key.Status != StatusActive || (key.ExpiresAt != nil && !key.ExpiresAt.After(r.now())) {
		return Authorization{}, ErrUnauthorized
	}
	subject, err := r.keys.GetSubject(ctx, key.UserID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Authorization{}, ErrUnauthorized
		}
		return Authorization{}, err
	}
	if subject.UserID != key.UserID || subject.Status != StatusActive || subject.Deleted {
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

func (r *GatewayRuntime) ResolveRoute(ctx context.Context, qualifiedModel string) (Route, error) {
	model, err := ParseQualifiedModel(qualifiedModel)
	if err != nil {
		return Route{}, err
	}
	provider, err := resolveActiveProvider(ctx, r.providers, model.ProviderSlug)
	if err != nil {
		return Route{}, err
	}
	return Route{
		QualifiedModel: model.String(), ProviderID: provider.ID, UpstreamModel: model.UpstreamModel,
		AdapterAppID: provider.AdapterAppID,
	}, nil
}

// ProviderRuntime is the narrower adapter-side view: adapters can resolve only
// provider runtime records and receive no virtual-key repository.
type ProviderRuntime struct {
	providers *ProviderRepository
}

func NewProviderRuntime(providers *ProviderRepository) *ProviderRuntime {
	return &ProviderRuntime{providers: providers}
}

func (r *ProviderRuntime) ResolveProviderBySlug(ctx context.Context, slug string) (Provider, error) {
	return resolveActiveProvider(ctx, r.providers, slug)
}

func resolveActiveProvider(ctx context.Context, providers *ProviderRepository, slug string) (Provider, error) {
	provider, err := providers.GetProviderBySlug(ctx, slug)
	if err != nil {
		return Provider{}, err
	}
	if provider.Status != StatusActive {
		return Provider{}, ErrForbidden
	}
	return provider, nil
}
