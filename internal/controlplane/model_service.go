package controlplane

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Albe83/gwai/internal/platform"
)

var modelAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

type ModelInput struct {
	Alias         string `json:"alias"`
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	Status        Status `json:"status,omitempty"`
}

// ModelRegistry is the cross-service contract that projects model lifecycle
// into the virtual-key domain. A fence both proves that no key references the
// model and prevents a concurrent reference from being created.
type ModelRegistry interface {
	SyncModel(context.Context, ModelSubject) error
	FenceModel(context.Context, ModelSubject) error
}

func modelSubjectFor(model Model, deleted bool) ModelSubject {
	status := model.Status
	if deleted {
		status = StatusDisabled
	}
	return ModelSubject{
		ModelID: model.ID, Alias: model.Alias, Status: status, Revision: model.Revision,
		Deleted: deleted, UpdatedAt: model.UpdatedAt,
	}
}

func (s *ResourceService) normalizeModelInput(ctx context.Context, input ModelInput) (ModelInput, error) {
	if strings.ContainsAny(input.UpstreamModel, "\r\n\x00") {
		return input, &ValidationError{Field: "upstream_model", Message: "contains unsupported control characters"}
	}
	input.Alias = strings.TrimSpace(input.Alias)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.UpstreamModel = strings.TrimSpace(input.UpstreamModel)
	input.Status = normalizeStatus(input.Status)
	if !modelAliasPattern.MatchString(input.Alias) {
		return input, &ValidationError{Field: "alias", Message: "contains unsupported characters or has an invalid length"}
	}
	if input.ProviderID == "" {
		return input, &ValidationError{Field: "provider_id", Message: "must not be empty"}
	}
	provider, err := s.providers.GetProvider(ctx, input.ProviderID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return input, &ValidationError{Field: "provider_id", Message: "does not reference an existing provider"}
		}
		return input, err
	}
	if len(input.UpstreamModel) > 300 {
		return input, &ValidationError{Field: "upstream_model", Message: "must not exceed 300 bytes"}
	}
	if err := validateStatus(input.Status); err != nil {
		return input, err
	}
	if input.Status == StatusActive && provider.Status != StatusActive {
		return input, &ValidationError{Field: "provider_id", Message: "an active model requires an active provider"}
	}
	return input, nil
}

func (s *ResourceService) CreateModel(ctx context.Context, input ModelInput) (Model, error) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()

	input, err := s.normalizeModelInput(ctx, input)
	if err != nil {
		return Model{}, err
	}
	id, err := platform.NewID("mdl")
	if err != nil {
		return Model{}, err
	}
	now := s.now()
	model := Model{
		ID: id, Alias: input.Alias, ProviderID: input.ProviderID, UpstreamModel: input.UpstreamModel,
		Status: input.Status, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.models.CreateModel(ctx, model); err != nil {
		return Model{}, err
	}
	if s.modelSubjects == nil {
		return Model{}, fmt.Errorf("sync model subject: %w", ErrUnavailable)
	}
	if err := s.modelSubjects.SyncModel(ctx, modelSubjectFor(model, false)); err != nil {
		// Retain the canonical model. A missing projection fails key creation and
		// authorization closed; a full PUT advances the revision and repairs it.
		return Model{}, fmt.Errorf("sync model subject: %w", err)
	}
	return model, nil
}

func (s *ResourceService) GetModel(ctx context.Context, id string) (Model, error) {
	return s.models.GetModel(ctx, id)
}

func (s *ResourceService) ListModels(ctx context.Context) ([]Model, error) {
	return s.models.ListModels(ctx)
}

func (s *ResourceService) UpdateModel(ctx context.Context, id string, input ModelInput) (Model, error) {
	return s.updateModel(ctx, id, input, ifMatchPrecondition{})
}

func (s *ResourceService) UpdateModelIfMatch(ctx context.Context, id string, input ModelInput, ifMatch string) (Model, error) {
	return s.updateModel(ctx, id, input, optionalIfMatch(ifMatch))
}

func (s *ResourceService) updateModel(ctx context.Context, id string, input ModelInput, precondition ifMatchPrecondition) (Model, error) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()

	current, err := s.models.GetModel(ctx, id)
	if err != nil {
		return Model{}, err
	}
	if err := enforceIfMatch(precondition, current); err != nil {
		return Model{}, err
	}
	if input.Status == "" {
		return Model{}, &ValidationError{Field: "status", Message: "must be active or disabled"}
	}
	input, err = s.normalizeModelInput(ctx, input)
	if err != nil {
		return Model{}, err
	}
	if input.Alias != current.Alias {
		return Model{}, &ValidationError{Field: "alias", Message: "is immutable"}
	}
	old := current
	current.ProviderID = input.ProviderID
	current.UpstreamModel = input.UpstreamModel
	current.Status = input.Status
	current.Revision++
	current.UpdatedAt = s.now()
	if s.modelSubjects == nil {
		return Model{}, fmt.Errorf("sync model subject: %w", ErrUnavailable)
	}
	// Disablement is projected before canonical state changes. Activation is
	// projected afterwards. Unchanged statuses are deliberately resynchronized
	// so a PUT repairs an earlier ambiguous create or activation response.
	if current.Status == StatusDisabled {
		if err := s.modelSubjects.SyncModel(ctx, modelSubjectFor(current, false)); err != nil {
			return Model{}, fmt.Errorf("disable model subject: %w", err)
		}
	}
	if err := s.models.ReplaceModel(ctx, old, current); err != nil {
		return Model{}, err
	}
	if current.Status == StatusActive {
		if err := s.modelSubjects.SyncModel(ctx, modelSubjectFor(current, false)); err != nil {
			return Model{}, fmt.Errorf("enable model subject: %w", err)
		}
	}
	return current, nil
}

func (s *ResourceService) DeleteModel(ctx context.Context, id string) error {
	return s.deleteModel(ctx, id, ifMatchPrecondition{})
}

func (s *ResourceService) DeleteModelIfMatch(ctx context.Context, id, ifMatch string) error {
	return s.deleteModel(ctx, id, optionalIfMatch(ifMatch))
}

func (s *ResourceService) deleteModel(ctx context.Context, id string, precondition ifMatchPrecondition) error {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()

	model, err := s.models.GetModel(ctx, id)
	if err != nil {
		return err
	}
	if err := enforceIfMatch(precondition, model); err != nil {
		return err
	}
	fence := model
	fence.Revision++
	fence.UpdatedAt = s.now()
	if s.modelSubjects == nil {
		return fmt.Errorf("fence model subject: %w", ErrUnavailable)
	}
	if err := s.modelSubjects.FenceModel(ctx, modelSubjectFor(fence, true)); err != nil {
		return err
	}
	return s.models.DeleteModel(ctx, model)
}
