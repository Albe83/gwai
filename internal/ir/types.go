// Package ir defines gwai's provider-neutral generation protocol. Ingress and
// egress adapters share this versioned contract instead of translating every
// client API directly to every provider API.
package ir

import "encoding/json"

const Version = "2026-07-01"

const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

const (
	ContentText       = "text"
	ContentImage      = "image"
	ContentToolCall   = "tool_call"
	ContentToolResult = "tool_result"
)

type Route struct {
	ProviderID    string `json:"provider_id"`
	UpstreamModel string `json:"upstream_model"`
}

type Request struct {
	Version         string         `json:"version"`
	ID              string         `json:"id"`
	Route           Route          `json:"route"`
	Messages        []Message      `json:"messages"`
	MaxOutputTokens int            `json:"max_output_tokens"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	Stop            []string       `json:"stop,omitempty"`
	Tools           []Tool         `json:"tools,omitempty"`
	ToolChoice      *ToolChoice    `json:"tool_choice,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

type Content struct {
	Type       string      `json:"type"`
	Text       string      `json:"text,omitempty"`
	Image      *Image      `json:"image,omitempty"`
	ToolCall   *ToolCall   `json:"tool_call,omitempty"`
	ToolResult *ToolResult `json:"tool_result,omitempty"`
}

type Image struct {
	URL       string `json:"url,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	ToolCallID string    `json:"tool_call_id"`
	Content    []Content `json:"content"`
	IsError    bool      `json:"is_error,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	Strict      *bool           `json:"strict,omitempty"`
}

type ToolChoice struct {
	Mode            string `json:"mode"`
	Name            string `json:"name,omitempty"`
	DisableParallel bool   `json:"disable_parallel,omitempty"`
}

type Response struct {
	Version            string    `json:"version"`
	ID                 string    `json:"id"`
	Model              string    `json:"model"`
	Content            []Content `json:"content"`
	FinishReason       string    `json:"finish_reason"`
	StopSequence       string    `json:"stop_sequence,omitempty"`
	Usage              Usage     `json:"usage"`
	ProviderResponseID string    `json:"provider_response_id,omitempty"`
}

type Usage struct {
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
}

const (
	FinishStop      = "stop"
	FinishLength    = "length"
	FinishToolCalls = "tool_calls"
	FinishContent   = "content_filter"
)
