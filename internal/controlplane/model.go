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

type VirtualKey struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	UserID        string     `json:"user_id"`
	Prefix        string     `json:"prefix"`
	KeyHash       string     `json:"key_hash"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	Status        Status     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type PublicVirtualKey struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	UserID        string     `json:"user_id"`
	Prefix        string     `json:"prefix"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	Status        Status     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (key VirtualKey) Public() PublicVirtualKey {
	return PublicVirtualKey{
		ID:            key.ID,
		Name:          key.Name,
		UserID:        key.UserID,
		Prefix:        key.Prefix,
		AllowedModels: append([]string(nil), key.AllowedModels...),
		Status:        key.Status,
		ExpiresAt:     key.ExpiresAt,
		CreatedAt:     key.CreatedAt,
		UpdatedAt:     key.UpdatedAt,
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
	QualifiedModel string `json:"qualified_model"`
	ProviderID     string `json:"provider_id"`
	UpstreamModel  string `json:"upstream_model"`
	AdapterAppID   string `json:"adapter_app_id"`
}
