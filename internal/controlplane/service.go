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
	Slug         string             `json:"slug"`
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	BaseURL      string             `json:"base_url,omitempty"`
	APIVersion   string             `json:"api_version,omitempty"`
	AdapterAppID string             `json:"adapter_app_id"`
	SecretRef    daprhttp.SecretRef `json:"secret_ref"`
	Status       Status             `json:"status,omitempty"`
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
	input.Slug = strings.TrimSpace(input.Slug)
	input.Name = strings.TrimSpace(input.Name)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.BaseURL = strings.TrimRight(strings.TrimSpace(input.BaseURL), "/")
	input.APIVersion = strings.TrimSpace(input.APIVersion)
	input.AdapterAppID = strings.TrimSpace(input.AdapterAppID)
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return input, err
	}
	if err := validateProviderSlug(input.Slug); err != nil {
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
	if input.AdapterAppID == "" || !appIDPattern.MatchString(input.AdapterAppID) {
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
		ID: id, Slug: input.Slug, Name: input.Name, Kind: input.Kind, BaseURL: input.BaseURL,
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
	if input.Slug != current.Slug {
		return Provider{}, &ValidationError{Field: "slug", Message: "is immutable"}
	}
	if input.AdapterAppID != current.AdapterAppID {
		return Provider{}, &ValidationError{Field: "adapter_app_id", Message: "is immutable"}
	}
	old := current
	current.Name = input.Name
	current.Kind = input.Kind
	current.BaseURL = input.BaseURL
	current.APIVersion = input.APIVersion
	current.SecretRef = input.SecretRef
	current.Status = input.Status
	current.UpdatedAt = s.now()
	if err := s.repository.ReplaceProvider(ctx, old, current); err != nil {
		return Provider{}, err
	}
	return current, nil
}

func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	provider, err := s.repository.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	return s.repository.DeleteProvider(ctx, provider)
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
	for _, value := range input.AllowedModels {
		model, err := ParseQualifiedModel(value)
		if err != nil {
			return input, &ValidationError{Field: "allowed_models", Message: err.Error()}
		}
		qualified := model.String()
		if _, duplicate := seen[qualified]; duplicate {
			continue
		}
		_, err = s.repository.GetProviderBySlug(ctx, model.ProviderSlug)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return input, &ValidationError{Field: "allowed_models", Message: fmt.Sprintf("provider slug %q does not exist", model.ProviderSlug)}
			}
			return input, err
		}
		seen[qualified] = struct{}{}
		normalized = append(normalized, qualified)
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
