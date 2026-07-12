package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Albe83/gwai/internal/daprhttp"
	"github.com/Albe83/gwai/internal/platform"
)

var (
	ErrUnauthorized = errors.New("authentication failed")
	ErrForbidden    = errors.New("access denied")
	ErrUnavailable  = errors.New("dependent service unavailable")
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

// SubjectRegistry is the only cross-service contract required by the
// user/provider control plane. Its implementation lives in the virtual-key
// control plane so that only that service writes authorization state.
type SubjectRegistry interface {
	SyncSubject(context.Context, KeySubject) error
	FenceSubject(context.Context, KeySubject) error
}

// ResourceService owns users and providers. It never reads or writes virtual
// keys directly; user lifecycle changes are coordinated through SubjectRegistry.
type ResourceService struct {
	users     *UserRepository
	providers *ProviderRepository
	subjects  SubjectRegistry
	now       func() time.Time
	userMu    sync.Mutex
}

func NewResourceService(users *UserRepository, providers *ProviderRepository, subjects SubjectRegistry) *ResourceService {
	return &ResourceService{
		users: users, providers: providers, subjects: subjects,
		now: func() time.Time { return time.Now().UTC() },
	}
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

func normalizeUserInput(input UserInput) (UserInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return input, err
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(address.Address, input.Email) {
		return input, &ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	if err := validateStatus(input.Status); err != nil {
		return input, err
	}
	return input, nil
}

func subjectForUser(user User, deleted bool) KeySubject {
	status := user.Status
	if deleted {
		status = StatusDisabled
	}
	return KeySubject{
		UserID: user.ID, Status: status, Revision: user.Revision,
		Deleted: deleted, UpdatedAt: user.UpdatedAt,
	}
}

func (s *ResourceService) CreateUser(ctx context.Context, input UserInput) (User, error) {
	s.userMu.Lock()
	defer s.userMu.Unlock()

	input, err := normalizeUserInput(input)
	if err != nil {
		return User{}, err
	}
	id, err := platform.NewID("usr")
	if err != nil {
		return User{}, err
	}
	now := s.now()
	user := User{
		ID: id, Name: input.Name, Email: input.Email, Status: input.Status,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.users.CreateUser(ctx, user); err != nil {
		return User{}, err
	}
	if err := s.subjects.SyncSubject(ctx, subjectForUser(user, false)); err != nil {
		// Keep the canonical user: an invocation error may be observed after
		// the remote commit. A subsequent PUT resynchronizes the projection;
		// a missing projection always fails authorization closed.
		return User{}, fmt.Errorf("sync authorization subject: %w", err)
	}
	return user, nil
}

func (s *ResourceService) GetUser(ctx context.Context, id string) (User, error) {
	return s.users.GetUser(ctx, id)
}

func (s *ResourceService) ListUsers(ctx context.Context) ([]User, error) {
	return s.users.ListUsers(ctx)
}

func (s *ResourceService) UpdateUser(ctx context.Context, id string, input UserInput) (User, error) {
	s.userMu.Lock()
	defer s.userMu.Unlock()

	current, err := s.users.GetUser(ctx, id)
	if err != nil {
		return User{}, err
	}
	if input.Status == "" {
		return User{}, &ValidationError{Field: "status", Message: "must be active or disabled"}
	}
	input, err = normalizeUserInput(input)
	if err != nil {
		return User{}, err
	}
	old := current
	current.Name = input.Name
	current.Email = input.Email
	current.Status = input.Status
	current.Revision++
	current.UpdatedAt = s.now()
	// Revocation is propagated before the private record changes; activation
	// is propagated afterwards. We also resync unchanged statuses so a retry repairs a failed create or
	// an earlier ambiguous cross-service response.
	if current.Status == StatusDisabled {
		if err := s.subjects.SyncSubject(ctx, subjectForUser(current, false)); err != nil {
			return User{}, fmt.Errorf("disable authorization subject: %w", err)
		}
	}
	if err := s.users.ReplaceUser(ctx, old, current); err != nil {
		return User{}, err
	}
	if current.Status == StatusActive {
		if err := s.subjects.SyncSubject(ctx, subjectForUser(current, false)); err != nil {
			return User{}, fmt.Errorf("enable authorization subject: %w", err)
		}
	}
	return current, nil
}

func (s *ResourceService) DeleteUser(ctx context.Context, id string) error {
	s.userMu.Lock()
	defer s.userMu.Unlock()

	user, err := s.users.GetUser(ctx, id)
	if err != nil {
		return err
	}
	fence := user
	fence.Revision++
	fence.UpdatedAt = s.now()
	if err := s.subjects.FenceSubject(ctx, subjectForUser(fence, true)); err != nil {
		return err
	}
	// FenceSubject atomically proves that no key exists and prevents a new key
	// from racing the private-store deletion. A retry is idempotent.
	return s.users.DeleteUser(ctx, user)
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)
var apiVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

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
	switch input.Kind {
	case ProviderKindAnthropic:
		if input.BaseURL == "" {
			input.BaseURL = "https://api.anthropic.com"
		}
		if input.APIVersion == "" {
			input.APIVersion = "2023-06-01"
		}
	case ProviderKindOpenAIChat, ProviderKindOpenAIResponses:
		if input.BaseURL == "" {
			input.BaseURL = "https://api.openai.com"
		}
		if input.APIVersion == "" {
			input.APIVersion = "v1"
		}
	case ProviderKindGemini:
		if input.BaseURL == "" {
			input.BaseURL = "https://generativelanguage.googleapis.com"
		}
		if input.APIVersion == "" {
			input.APIVersion = "v1beta"
		}
	default:
		return input, &ValidationError{Field: "kind", Message: "must be anthropic, openai-chat, openai-responses, or gemini"}
	}
	parsed, err := url.ParseRequestURI(input.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return input, &ValidationError{Field: "base_url", Message: "must be an absolute HTTP(S) URL"}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return input, &ValidationError{Field: "base_url", Message: "must not contain credentials, a query, or a fragment"}
	}
	if !apiVersionPattern.MatchString(input.APIVersion) {
		return input, &ValidationError{Field: "api_version", Message: "must be a valid API version token"}
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

func (s *ResourceService) CreateProvider(ctx context.Context, input ProviderInput) (Provider, error) {
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
	if err := s.providers.CreateProvider(ctx, provider); err != nil {
		return Provider{}, err
	}
	return provider, nil
}

func (s *ResourceService) GetProvider(ctx context.Context, id string) (Provider, error) {
	return s.providers.GetProvider(ctx, id)
}

func (s *ResourceService) ListProviders(ctx context.Context) ([]Provider, error) {
	return s.providers.ListProviders(ctx)
}

func (s *ResourceService) UpdateProvider(ctx context.Context, id string, input ProviderInput) (Provider, error) {
	current, err := s.providers.GetProvider(ctx, id)
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
	if err := s.providers.ReplaceProvider(ctx, old, current); err != nil {
		return Provider{}, err
	}
	return current, nil
}

func (s *ResourceService) DeleteProvider(ctx context.Context, id string) error {
	provider, err := s.providers.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	return s.providers.DeleteProvider(ctx, provider)
}
