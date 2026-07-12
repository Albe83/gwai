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
	Name      string     `json:"name"`
	UserID    string     `json:"user_id"`
	ModelIDs  []string   `json:"model_ids"`
	Status    Status     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// VirtualKeyService is the sole application writer for the virtual-key store.
// It also owns the minimal user projection consumed by gateway authorization.
type VirtualKeyService struct {
	keys *VirtualKeyRepository
	now  func() time.Time
}

func NewVirtualKeyService(keys *VirtualKeyRepository) *VirtualKeyService {
	return &VirtualKeyService{keys: keys, now: func() time.Time { return time.Now().UTC() }}
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
	seen := make(map[string]struct{}, len(input.ModelIDs))
	normalized := make([]string, 0, len(input.ModelIDs))
	for _, value := range input.ModelIDs {
		modelID := strings.TrimSpace(value)
		if modelID == "" {
			return input, &ValidationError{Field: "model_ids", Message: "must not contain empty IDs"}
		}
		if _, duplicate := seen[modelID]; duplicate {
			continue
		}
		model, err := s.keys.GetModelSubject(ctx, modelID)
		if err != nil || model.Deleted {
			if err == nil || errors.Is(err, ErrNotFound) {
				return input, &ValidationError{Field: "model_ids", Message: fmt.Sprintf("model ID %q does not exist", modelID)}
			}
			return input, err
		}
		if input.Status == StatusActive && model.Status != StatusActive {
			return input, &ValidationError{Field: "model_ids", Message: fmt.Sprintf("model ID %q is disabled", modelID)}
		}
		seen[modelID] = struct{}{}
		normalized = append(normalized, modelID)
	}
	if len(normalized) == 0 {
		return input, &ValidationError{Field: "model_ids", Message: "must contain at least one model ID"}
	}
	slices.Sort(normalized)
	input.ModelIDs = normalized
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
		KeyHash: hashKey(secret), ModelIDs: input.ModelIDs, Status: input.Status,
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
	return s.updateVirtualKey(ctx, id, input, ifMatchPrecondition{})
}

// UpdateVirtualKeyIfMatch optionally enforces the strong ETag of the public
// key representation after current state is loaded. The repository's entity
// and subject ETag transaction still rejects a race before the write commits.
func (s *VirtualKeyService) UpdateVirtualKeyIfMatch(ctx context.Context, id string, input VirtualKeyInput, ifMatch string) (PublicVirtualKey, error) {
	return s.updateVirtualKey(ctx, id, input, optionalIfMatch(ifMatch))
}

func (s *VirtualKeyService) updateVirtualKey(ctx context.Context, id string, input VirtualKeyInput, precondition ifMatchPrecondition) (PublicVirtualKey, error) {
	current, err := s.keys.GetVirtualKey(ctx, id)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	if err := enforceIfMatch(precondition, current.Public()); err != nil {
		return PublicVirtualKey{}, err
	}
	input, err = s.normalizeInput(ctx, input)
	if err != nil {
		return PublicVirtualKey{}, err
	}
	old := current
	current.Name = input.Name
	current.UserID = input.UserID
	current.ModelIDs = input.ModelIDs
	current.Status = input.Status
	current.ExpiresAt = input.ExpiresAt
	current.UpdatedAt = s.now()
	if err := s.keys.ReplaceVirtualKey(ctx, old, current); err != nil {
		return PublicVirtualKey{}, err
	}
	return current.Public(), nil
}

func (s *VirtualKeyService) DeleteVirtualKey(ctx context.Context, id string) error {
	return s.deleteVirtualKey(ctx, id, ifMatchPrecondition{})
}

// DeleteVirtualKeyIfMatch deletes a key only when a non-empty validator
// matches the current public representation, which never includes the hash.
func (s *VirtualKeyService) DeleteVirtualKeyIfMatch(ctx context.Context, id, ifMatch string) error {
	return s.deleteVirtualKey(ctx, id, optionalIfMatch(ifMatch))
}

func (s *VirtualKeyService) deleteVirtualKey(ctx context.Context, id string, precondition ifMatchPrecondition) error {
	key, err := s.keys.GetVirtualKey(ctx, id)
	if err != nil {
		return err
	}
	if err := enforceIfMatch(precondition, key.Public()); err != nil {
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

func (s *VirtualKeyService) SyncModel(ctx context.Context, subject ModelSubject) error {
	return s.keys.SyncModelSubject(ctx, subject)
}

func (s *VirtualKeyService) FenceModel(ctx context.Context, subject ModelSubject) error {
	if !subject.Deleted {
		return &ValidationError{Field: "deleted", Message: "must be true when fencing a model subject"}
	}
	return s.keys.FenceModelSubject(ctx, subject)
}
