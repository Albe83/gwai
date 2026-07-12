package controlplane

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// GatewayRuntime exposes only the virtual-key and routing reads needed by
// client-facing gateways. Its two repositories are backed by separate Dapr
// state components; it has no access to private control-plane user state.
type GatewayRuntime struct {
	keys      *VirtualKeyRepository
	models    *ModelRepository
	providers *ProviderRepository
	now       func() time.Time
}

func NewGatewayRuntime(keys *VirtualKeyRepository, models *ModelRepository, providers *ProviderRepository) *GatewayRuntime {
	return &GatewayRuntime{
		keys:      keys,
		models:    models,
		providers: providers,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (r *GatewayRuntime) Authorize(ctx context.Context, token, requestedModel string) (Authorization, error) {
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
	model, err := r.resolveActiveModel(ctx, requestedModel)
	if err != nil {
		return Authorization{}, err
	}
	if !slices.Contains(key.ModelIDs, model.ID) {
		return Authorization{}, ErrForbidden
	}
	return Authorization{KeyID: key.ID, UserID: key.UserID}, nil
}

func (r *GatewayRuntime) ResolveRoute(ctx context.Context, requestedModel string) (Route, error) {
	model, err := r.resolveActiveModel(ctx, requestedModel)
	if err != nil {
		return Route{}, err
	}
	provider, err := resolveActiveProviderByID(ctx, r.providers, model.ProviderID)
	if err != nil {
		return Route{}, err
	}
	upstreamModel := model.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = model.Alias
	}
	return Route{
		ModelID: model.ID, Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: upstreamModel,
		AdapterAppID: provider.AdapterAppID,
	}, nil
}

func (r *GatewayRuntime) resolveActiveModel(ctx context.Context, requestedModel string) (Model, error) {
	alias := strings.TrimSpace(requestedModel)
	if alias == "" || alias != requestedModel {
		return Model{}, &ValidationError{Field: "model", Message: "must be a registered model alias without surrounding whitespace"}
	}
	model, err := r.models.GetModelByAlias(ctx, alias)
	if err != nil {
		return Model{}, err
	}
	if model.Status != StatusActive {
		return Model{}, ErrForbidden
	}
	subject, err := r.keys.GetModelSubject(ctx, model.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Model{}, ErrForbidden
		}
		return Model{}, err
	}
	if subject.ModelID != model.ID || subject.Alias != model.Alias || subject.Status != StatusActive ||
		subject.Deleted || subject.Revision != model.Revision {
		return Model{}, ErrForbidden
	}
	return model, nil
}

// ProviderRuntime is the narrower adapter-side view: adapters can resolve only
// provider runtime records and receive no virtual-key repository.
type ProviderRuntime struct {
	providers *ProviderRepository
}

func NewProviderRuntime(providers *ProviderRepository) *ProviderRuntime {
	return &ProviderRuntime{providers: providers}
}

func (r *ProviderRuntime) ResolveProviderByAdapterAppID(ctx context.Context, appID string) (Provider, error) {
	provider, err := r.providers.GetProviderByAdapterAppID(ctx, appID)
	if err != nil {
		return Provider{}, err
	}
	if provider.Status != StatusActive {
		return Provider{}, ErrForbidden
	}
	return provider, nil
}

func resolveActiveProviderByID(ctx context.Context, providers *ProviderRepository, id string) (Provider, error) {
	provider, err := providers.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if provider.Status != StatusActive {
		return Provider{}, ErrForbidden
	}
	return provider, nil
}
