package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/platform"
)

type VirtualKeyInput struct {
	Name          string     `json:"name"`
	UserID        string     `json:"user_id"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	Status        Status     `json:"status,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// VirtualKeyService is the sole application writer for the virtual-key store.
// It also owns the minimal user projection consumed by gateway authorization.
type VirtualKeyService struct {
	keys      *VirtualKeyRepository
	providers *ProviderRepository
	now       func() time.Time
}

func NewVirtualKeyService(keys *VirtualKeyRepository, providers *ProviderRepository) *VirtualKeyService {
	return &VirtualKeyService{keys: keys, providers: providers, now: func() time.Time { return time.Now().UTC() }}
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

func (s *VirtualKeyService) normalizeInput(ctx context.Context, input VirtualKeyInput) (VirtualKeyInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.UserID = strings.TrimSpace(input.UserID)
	input.Status = normalizeStatus(input.Status)
	if err := validateName(input.Name); err != nil {
		return input, err
	}
	subject, err := s.keys.GetSubject(ctx, input.UserID)
	if err != nil || subject.Deleted {
		if err == nil || errors.Is(err, ErrNotFound) {
			return input, &ValidationError{Field: "user_id", Message: "does not reference an existing user"}
		}
		return input, err
	}
	if subject.Status != StatusActive && input.Status == StatusActive {
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
		if _, err = s.providers.GetProviderBySlug(ctx, model.ProviderSlug); err != nil {
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

func (s *VirtualKeyService) CreateVirtualKey(ctx context.Context, input VirtualKeyInput) (CreatedVirtualKey, error) {
	input, err := s.normalizeInput(ctx, input)
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
	if err := s.keys.CreateVirtualKey(ctx, key); err != nil {
		return CreatedVirtualKey{}, err
	}
	return CreatedVirtualKey{VirtualKey: key.Public(), Key: secret}, nil
}

func (s *VirtualKeyService) GetVirtualKey(ctx context.Context, id string) (PublicVirtualKey, error) {
	key, err := s.keys.GetVirtualKey(ctx, id)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	return key.Public(), nil
}

func (s *VirtualKeyService) ListVirtualKeys(ctx context.Context) ([]PublicVirtualKey, error) {
	keys, err := s.keys.ListVirtualKeys(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PublicVirtualKey, 0, len(keys))
	for _, key := range keys {
		result = append(result, key.Public())
	}
	return result, nil
}

func (s *VirtualKeyService) UpdateVirtualKey(ctx context.Context, id string, input VirtualKeyInput) (PublicVirtualKey, error) {
	current, err := s.keys.GetVirtualKey(ctx, id)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	input, err = s.normalizeInput(ctx, input)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	old := current
	current.Name = input.Name
	current.UserID = input.UserID
	current.AllowedModels = input.AllowedModels
	current.Status = input.Status
	current.ExpiresAt = input.ExpiresAt
	current.UpdatedAt = s.now()
	if err := s.keys.ReplaceVirtualKey(ctx, old, current); err != nil {
		return PublicVirtualKey{}, err
	}
	return current.Public(), nil
}

func (s *VirtualKeyService) DeleteVirtualKey(ctx context.Context, id string) error {
	key, err := s.keys.GetVirtualKey(ctx, id)
	if err != nil {
		return err
	}
	return s.keys.DeleteVirtualKey(ctx, key)
}

func (s *VirtualKeyService) SyncSubject(ctx context.Context, subject KeySubject) error {
	return s.keys.SyncSubject(ctx, subject)
}

func (s *VirtualKeyService) FenceSubject(ctx context.Context, subject KeySubject) error {
	if !subject.Deleted {
		return &ValidationError{Field: "deleted", Message: "must be true when fencing a subject"}
	}
	return s.keys.FenceSubject(ctx, subject)
}
