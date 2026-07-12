package controlplane

import (
	"time"

	"github.com/Albe83/gwai/internal/daprhttp"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

const (
	ProviderKindAnthropic       = "anthropic"
	ProviderKindOpenAIChat      = "openai-chat"
	ProviderKindOpenAIResponses = "openai-responses"
	ProviderKindGemini          = "gemini"
)

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Status    Status    `json:"status"`
	Revision  uint64    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Provider struct {
	ID           string             `json:"id"`
	Slug         string             `json:"slug"`
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	BaseURL      string             `json:"base_url"`
	APIVersion   string             `json:"api_version"`
	AdapterAppID string             `json:"adapter_app_id"`
	SecretRef    daprhttp.SecretRef `json:"secret_ref"`
	Status       Status             `json:"status"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

// Model is the stable, client-facing routing resource between virtual-key
// authorization and a concrete provider account. Alias is immutable after
// creation; ProviderID and UpstreamModel may be changed through a full PUT.
type Model struct {
	ID            string    `json:"id"`
	Alias         string    `json:"alias"`
	ProviderID    string    `json:"provider_id"`
	UpstreamModel string    `json:"upstream_model"`
	Status        Status    `json:"status"`
	Revision      uint64    `json:"revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ModelSubject is the authorization projection owned by the virtual-key
// service. It deliberately excludes provider routing data: gateways resolve
// the canonical Model directly from provider state.
type ModelSubject struct {
	ModelID   string    `json:"model_id"`
	Alias     string    `json:"alias"`
	Status    Status    `json:"status"`
	Revision  uint64    `json:"revision"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updated_at"`
}

type VirtualKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	UserID    string     `json:"user_id"`
	Prefix    string     `json:"prefix"`
	KeyHash   string     `json:"key_hash"`
	ModelIDs  []string   `json:"model_ids"`
	Status    Status     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// KeySubject is the authorization projection for the user that owns one or
// more virtual keys. It lives with the virtual-key records so gateways can
// fail closed without access to private control-plane state.
type KeySubject struct {
	UserID    string    `json:"user_id"`
	Status    Status    `json:"status"`
	Revision  uint64    `json:"revision"`
	Deleted   bool      `json:"deleted"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PublicVirtualKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	UserID    string     `json:"user_id"`
	Prefix    string     `json:"prefix"`
	ModelIDs  []string   `json:"model_ids"`
	Status    Status     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func (key VirtualKey) Public() PublicVirtualKey {
	return PublicVirtualKey{
		ID:        key.ID,
		Name:      key.Name,
		UserID:    key.UserID,
		Prefix:    key.Prefix,
		ModelIDs:  append([]string(nil), key.ModelIDs...),
		Status:    key.Status,
		ExpiresAt: key.ExpiresAt,
		CreatedAt: key.CreatedAt,
		UpdatedAt: key.UpdatedAt,
	}
}

type CreatedVirtualKey struct {
	VirtualKey PublicVirtualKey `json:"virtual_key"`
	Key        string           `json:"key"`
}

type Authorization struct {
	KeyID  string `json:"key_id"`
	UserID string `json:"user_id"`
}

type Route struct {
	ModelID       string `json:"model_id"`
	Alias         string `json:"alias"`
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
	AdapterAppID  string `json:"adapter_app_id"`
}
