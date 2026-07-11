package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

var (
	ErrUnauthorized = errors.New("authentication failed")
	ErrForbidden    = errors.New("access denied")
)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

type UserInput struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Status Status `json:"status,omitempty"`
}

type ProviderInput struct {
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	APIVersion   string             `json:"api_version,omitempty"`
	AdapterAppID string             `json:"adapter_app_id,omitempty"`
	SecretRef    daprhttp.SecretRef `json:"secret_ref"`
	Status       Status             `json:"status,omitempty"`
}

type ModelInput struct {
	Alias           string `json:"alias"`
	ProviderID      string `json:"provider_id"`
	UpstreamModel   string `json:"upstream_model"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Status          Status `json:"status,omitempty"`
}

type VirtualKeyInput struct {
	Name          string     `json:"name"`
	UserID        string     `json:"user_id"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	Status        Status     `json:"status,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type Service struct {
	repository *Repository
	now        func() time.Time
}

func NewService(repository *Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func validStatus(status Status) bool {
	return status == StatusActive || status == StatusDisabled
}

func normalizeStatus(status Status) Status {
	if status == "" {
		return StatusActive
	}
	return status
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return &ValidationError{Field: "name", Message: "must not be empty"}
	}
	if len(name) > 200 {
		return &ValidationError{Field: "name", Message: "must not exceed 200 bytes"}
	}
	return nil
}

func validateStatus(status Status) error {
	if !validStatus(status) {
		return &ValidationError{Field: "status", Message: "must be active or disabled"}
	}
	return nil
}

func (s *Service) CreateUser(ctx context.Context, input UserInput) (User, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return User{}, err
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(address.Address, input.Email) {
		return User{}, &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	if err := validateStatus(input.Status); err != nil {
		return User{}, err
	}
	id, err := platform.NewID("usr")
	if err != nil {
		return User{}, err
	}
	now := s.now()
	user := User{ID: id, Name: input.Name, Email: input.Email, Status: input.Status, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.CreateUser(ctx, user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Service) GetUser(ctx context.Context, id string) (User, error) {
	return s.repository.GetUser(ctx, id)
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	return s.repository.ListUsers(ctx)
}

func (s *Service) UpdateUser(ctx context.Context, id string, input UserInput) (User, error) {
	current, err := s.repository.GetUser(ctx, id)
	if err != nil {
		return User{}, err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	if err := validateName(input.Name); err != nil {
		return User{}, err
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(address.Address, input.Email) {
		return User{}, &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	if err := validateStatus(input.Status); err != nil {
		return User{}, err
	}
	old := current
	current.Name = input.Name
	current.Email = input.Email
	current.Status = input.Status
	current.UpdatedAt = s.now()
	if err := s.repository.ReplaceUser(ctx, old, current); err != nil {
		return User{}, err
	}
	return current, nil
}

func (s *Service) DeleteUser(ctx context.Context, id string) error {
	user, err := s.repository.GetUser(ctx, id)
	if err != nil {
		return err
	}
	keys, err := s.repository.ListVirtualKeys(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.UserID == id {
			return fmt.Errorf("%w: user still has virtual keys", ErrConflict)
		}
	}
	return s.repository.DeleteUser(ctx, user)
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

func normalizeProviderInput(input ProviderInput) (ProviderInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.APIVersion = strings.TrimSpace(input.APIVersion)
	input.AdapterAppID = strings.TrimSpace(input.AdapterAppID)
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return input, err
	}
	if input.Kind != "anthropic" {
		return input, &ValidationError{Field: "kind", Message: "only anthropic is currently supported"}
	}
	if input.BaseURL == "" {
		input.BaseURL = "https://api.anthropic.com"
	}
	parsed, err := url.ParseRequestURI(input.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return input, &ValidationError{Field: "base_url", Message: "must be an absolute HTTP(S) URL"}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return input, &ValidationError{Field: "base_url", Message: "must not contain credentials, a query, or a fragment"}
	}
	if input.APIVersion == "" {
		input.APIVersion = "2023-06-01"
	}
	if input.AdapterAppID == "" {
		input.AdapterAppID = "gwai-anthropic-adapter"
	}
	if !appIDPattern.MatchString(input.AdapterAppID) {
		return input, &ValidationError{Field: "adapter_app_id", Message: "must be a valid lowercase Dapr app ID"}
	}
	input.SecretRef.Store = strings.TrimSpace(input.SecretRef.Store)
	input.SecretRef.Name = strings.TrimSpace(input.SecretRef.Name)
	input.SecretRef.Key = strings.TrimSpace(input.SecretRef.Key)
	input.SecretRef.Namespace = strings.TrimSpace(input.SecretRef.Namespace)
	if input.SecretRef.Store == "" || input.SecretRef.Name == "" {
		return input, &ValidationError{Field: "secret_ref", Message: "store and name are required"}
	}
	if err := validateStatus(input.Status); err != nil {
		return input, err
	}
	return input, nil
}

func (s *Service) CreateProvider(ctx context.Context, input ProviderInput) (Provider, error) {
	input, err := normalizeProviderInput(input)
	if err != nil {
		return Provider{}, err
	}
	id, err := platform.NewID("prv")
	if err != nil {
		return Provider{}, err
	}
	now := s.now()
	provider := Provider{
		ID: id, Name: input.Name, Kind: input.Kind, BaseURL: input.BaseURL,
		APIVersion: input.APIVersion, AdapterAppID: input.AdapterAppID,
		SecretRef: input.SecretRef, Status: input.Status, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.CreateProvider(ctx, provider); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func (s *Service) GetProvider(ctx context.Context, id string) (Provider, error) {
	return s.repository.GetProvider(ctx, id)
}

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	return s.repository.ListProviders(ctx)
}

func (s *Service) UpdateProvider(ctx context.Context, id string, input ProviderInput) (Provider, error) {
	current, err := s.repository.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	input, err = normalizeProviderInput(input)
	if err != nil {
		return Provider{}, err
	}
	current.Name = input.Name
	current.Kind = input.Kind
	current.BaseURL = input.BaseURL
	current.APIVersion = input.APIVersion
	current.AdapterAppID = input.AdapterAppID
	current.SecretRef = input.SecretRef
	current.Status = input.Status
	current.UpdatedAt = s.now()
	if err := s.repository.ReplaceProvider(ctx, current); err != nil {
		return Provider{}, err
	}
	return current, nil
}

func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	if _, err := s.repository.GetProvider(ctx, id); err != nil {
		return err
	}
	models, err := s.repository.ListModels(ctx)
	if err != nil {
		return err
	}
	for _, model := range models {
		if model.ProviderID == id {
			return fmt.Errorf("%w: provider still has models", ErrConflict)
		}
	}
	return s.repository.DeleteProvider(ctx, id)
}

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

func (s *Service) normalizeModelInput(ctx context.Context, input ModelInput) (ModelInput, error) {
	input.Alias = strings.TrimSpace(input.Alias)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.UpstreamModel = strings.TrimSpace(input.UpstreamModel)
	input.Status = normalizeStatus(input.Status)
	if !aliasPattern.MatchString(input.Alias) {
		return input, &ValidationError{Field: "alias", Message: "contains unsupported characters or has an invalid length"}
	}
	if input.ProviderID == "" {
		return input, &ValidationError{Field: "provider_id", Message: "must not be empty"}
	}
	if _, err := s.repository.GetProvider(ctx, input.ProviderID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return input, &ValidationError{Field: "provider_id", Message: "does not reference an existing provider"}
		}
		return input, err
	}
	if input.UpstreamModel == "" || len(input.UpstreamModel) > 300 {
		return input, &ValidationError{Field: "upstream_model", Message: "must contain between 1 and 300 bytes"}
	}
	if input.MaxOutputTokens <= 0 || input.MaxOutputTokens > 1_000_000 {
		return input, &ValidationError{Field: "max_output_tokens", Message: "must be between 1 and 1000000"}
	}
	if err := validateStatus(input.Status); err != nil {
		return input, err
	}
	return input, nil
}

func (s *Service) CreateModel(ctx context.Context, input ModelInput) (Model, error) {
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
		MaxOutputTokens: input.MaxOutputTokens, Status: input.Status, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.CreateModel(ctx, model); err != nil {
		return Model{}, err
	}
	return model, nil
}

func (s *Service) GetModel(ctx context.Context, id string) (Model, error) {
	return s.repository.GetModel(ctx, id)
}

func (s *Service) ListModels(ctx context.Context) ([]Model, error) {
	return s.repository.ListModels(ctx)
}

func (s *Service) UpdateModel(ctx context.Context, id string, input ModelInput) (Model, error) {
	current, err := s.repository.GetModel(ctx, id)
	if err != nil {
		return Model{}, err
	}
	input, err = s.normalizeModelInput(ctx, input)
	if err != nil {
		return Model{}, err
	}
	if current.Alias != input.Alias {
		keys, err := s.repository.ListVirtualKeys(ctx)
		if err != nil {
			return Model{}, err
		}
		for _, key := range keys {
			if slices.Contains(key.AllowedModels, current.Alias) {
				return Model{}, fmt.Errorf("%w: model alias is referenced by a virtual key", ErrConflict)
			}
		}
	}
	old := current
	current.Alias = input.Alias
	current.ProviderID = input.ProviderID
	current.UpstreamModel = input.UpstreamModel
	current.MaxOutputTokens = input.MaxOutputTokens
	current.Status = input.Status
	current.UpdatedAt = s.now()
	if err := s.repository.ReplaceModel(ctx, old, current); err != nil {
		return Model{}, err
	}
	return current, nil
}

func (s *Service) DeleteModel(ctx context.Context, id string) error {
	model, err := s.repository.GetModel(ctx, id)
	if err != nil {
		return err
	}
	keys, err := s.repository.ListVirtualKeys(ctx)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if slices.Contains(key.AllowedModels, model.Alias) {
			return fmt.Errorf("%w: model is referenced by a virtual key", ErrConflict)
		}
	}
	return s.repository.DeleteModel(ctx, model)
}

func hashKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:])
}

func generateKey() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate virtual key: %w", err)
	}
	return "gwai_" + base64.RawURLEncoding.EncodeToString(random), nil
}

func (s *Service) normalizeVirtualKeyInput(ctx context.Context, input VirtualKeyInput) (VirtualKeyInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return input, err
	}
	user, err := s.repository.GetUser(ctx, input.UserID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return input, &ValidationError{Field: "user_id", Message: "does not reference an existing user"}
		}
		return input, err
	}
	if user.Status != StatusActive && input.Status == StatusActive {
		return input, &ValidationError{Field: "user_id", Message: "references a disabled user"}
	}
	if err := validateStatus(input.Status); err != nil {
		return input, err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.now()) && input.Status == StatusActive {
		return input, &ValidationError{Field: "expires_at", Message: "must be in the future for an active key"}
	}
	seen := make(map[string]struct{}, len(input.AllowedModels))
	normalized := make([]string, 0, len(input.AllowedModels))
	for _, alias := range input.AllowedModels {
		alias = strings.TrimSpace(alias)
		if _, duplicate := seen[alias]; duplicate {
			continue
		}
		model, err := s.repository.GetModelByAlias(ctx, alias)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return input, &ValidationError{Field: "allowed_models", Message: fmt.Sprintf("model alias %q does not exist", alias)}
			}
			return input, err
		}
		if model.Status != StatusActive && input.Status == StatusActive {
			return input, &ValidationError{Field: "allowed_models", Message: fmt.Sprintf("model alias %q is disabled", alias)}
		}
		seen[alias] = struct{}{}
		normalized = append(normalized, alias)
	}
	slices.Sort(normalized)
	input.AllowedModels = normalized
	return input, nil
}

func (s *Service) CreateVirtualKey(ctx context.Context, input VirtualKeyInput) (CreatedVirtualKey, error) {
	input, err := s.normalizeVirtualKeyInput(ctx, input)
	if err != nil {
		return CreatedVirtualKey{}, err
	}
	secret, err := generateKey()
	if err != nil {
		return CreatedVirtualKey{}, err
	}
	id, err := platform.NewID("key")
	if err != nil {
		return CreatedVirtualKey{}, err
	}
	now := s.now()
	key := VirtualKey{
		ID: id, Name: input.Name, UserID: input.UserID, Prefix: secret[:min(13, len(secret))],
		KeyHash: hashKey(secret), AllowedModels: input.AllowedModels, Status: input.Status,
		ExpiresAt: input.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.CreateVirtualKey(ctx, key); err != nil {
		return CreatedVirtualKey{}, err
	}
	return CreatedVirtualKey{VirtualKey: key.Public(), Key: secret}, nil
}

func (s *Service) GetVirtualKey(ctx context.Context, id string) (PublicVirtualKey, error) {
	key, err := s.repository.GetVirtualKey(ctx, id)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	return key.Public(), nil
}

func (s *Service) ListVirtualKeys(ctx context.Context) ([]PublicVirtualKey, error) {
	keys, err := s.repository.ListVirtualKeys(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PublicVirtualKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.Public())
	}
	return result, nil
}

func (s *Service) UpdateVirtualKey(ctx context.Context, id string, input VirtualKeyInput) (PublicVirtualKey, error) {
	current, err := s.repository.GetVirtualKey(ctx, id)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	input, err = s.normalizeVirtualKeyInput(ctx, input)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	current.Name = input.Name
	current.UserID = input.UserID
	current.AllowedModels = input.AllowedModels
	current.Status = input.Status
	current.ExpiresAt = input.ExpiresAt
	current.UpdatedAt = s.now()
	if err := s.repository.ReplaceVirtualKey(ctx, current); err != nil {
		return PublicVirtualKey{}, err
	}
	return current.Public(), nil
}

func (s *Service) DeleteVirtualKey(ctx context.Context, id string) error {
	key, err := s.repository.GetVirtualKey(ctx, id)
	if err != nil {
		return err
	}
	return s.repository.DeleteVirtualKey(ctx, key)
}

func (s *Service) Authorize(ctx context.Context, token, modelAlias string) (Authorization, error) {
	if token == "" {
		return Authorization{}, ErrUnauthorized
	}
	key, err := s.repository.GetVirtualKeyByHash(ctx, hashKey(token))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Authorization{}, ErrUnauthorized
		}
		return Authorization{}, err
	}
	if key.Status != StatusActive || (key.ExpiresAt != nil && !key.ExpiresAt.After(s.now())) {
		return Authorization{}, ErrUnauthorized
	}
	user, err := s.repository.GetUser(ctx, key.UserID)
	if err != nil || user.Status != StatusActive {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Authorization{}, err
		}
		return Authorization{}, ErrUnauthorized
	}
	model, err := s.repository.GetModelByAlias(ctx, modelAlias)
	if err != nil || model.Status != StatusActive {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Authorization{}, err
		}
		return Authorization{}, ErrForbidden
	}
	if len(key.AllowedModels) > 0 && !slices.Contains(key.AllowedModels, modelAlias) {
		return Authorization{}, ErrForbidden
	}
	return Authorization{KeyID: key.ID, UserID: key.UserID}, nil
}

func (s *Service) ResolveRoute(ctx context.Context, alias string) (Route, error) {
	model, err := s.repository.GetModelByAlias(ctx, alias)
	if err != nil {
		return Route{}, err
	}
	if model.Status != StatusActive {
		return Route{}, ErrForbidden
	}
	provider, err := s.repository.GetProvider(ctx, model.ProviderID)
	if err != nil {
		return Route{}, err
	}
	if provider.Status != StatusActive {
		return Route{}, ErrForbidden
	}
	return Route{
		Alias: model.Alias, ProviderID: provider.ID, UpstreamModel: model.UpstreamModel,
		MaxOutputTokens: model.MaxOutputTokens, AdapterAppID: provider.AdapterAppID,
	}, nil
}

func (s *Service) ResolveProvider(ctx context.Context, id string) (Provider, error) {
	provider, err := s.repository.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if provider.Status != StatusActive {
		return Provider{}, ErrForbidden
	}
	return provider, nil
}
