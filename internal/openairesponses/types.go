package openairesponses

import "encoding/json"

// CreateRequest is the stateless, non-streaming subset of the OpenAI
// Responses create request accepted by the gateway. Fields that cannot be
// represented by the IR are kept here so they can be rejected explicitly.
type CreateRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       json.RawMessage `json:"instructions,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Tools              []Tool          `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	Stream             *bool           `json:"stream,omitempty"`
	Store              *bool           `json:"store,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Conversation       json.RawMessage `json:"conversation,omitempty"`
	Reasoning          json.RawMessage `json:"reasoning,omitempty"`
	Text               json.RawMessage `json:"text,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
	Include            []string        `json:"include,omitempty"`
	Background         *bool           `json:"background,omitempty"`
	Truncation         string          `json:"truncation,omitempty"`
	Prompt             json.RawMessage `json:"prompt,omitempty"`
	MaxToolCalls       *int            `json:"max_tool_calls,omitempty"`
	SafetyIdentifier   string          `json:"safety_identifier,omitempty"`
	PromptCacheKey     string          `json:"prompt_cache_key,omitempty"`
	ServiceTier        string          `json:"service_tier,omitempty"`
	User               string          `json:"user,omitempty"`
	TopLogprobs        *int            `json:"top_logprobs,omitempty"`
	StreamOptions      json.RawMessage `json:"stream_options,omitempty"`
}

type InputItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Status    string          `json:"status,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
}

type InputContent struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	ImageURL    string            `json:"image_url,omitempty"`
	FileID      string            `json:"file_id,omitempty"`
	Detail      string            `json:"detail,omitempty"`
	Annotations []json.RawMessage `json:"annotations,omitempty"`
	Logprobs    []json.RawMessage `json:"logprobs,omitempty"`
}

type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ProviderRequest is deliberately narrower than CreateRequest. It is emitted
// by the upstream adapter only after the IR request has been validated.
type ProviderRequest struct {
	Model             string          `json:"model"`
	Input             []InputItem     `json:"input"`
	Instructions      string          `json:"instructions,omitempty"`
	MaxOutputTokens   *int            `json:"max_output_tokens,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Store             bool            `json:"store"`
	Stream            bool            `json:"stream"`
}

type Response struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	CreatedAt         int64              `json:"created_at"`
	Status            string             `json:"status"`
	Error             *ResponseError     `json:"error"`
	IncompleteDetails *IncompleteDetails `json:"incomplete_details"`
	Model             string             `json:"model"`
	Output            []OutputItem       `json:"output"`
	ParallelToolCalls bool               `json:"parallel_tool_calls"`
	Store             bool               `json:"store"`
	Usage             Usage              `json:"usage"`
}

type OutputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   []OutputContent `json:"content,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
}

type OutputContent struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Refusal     string            `json:"refusal,omitempty"`
	Annotations []json.RawMessage `json:"annotations,omitempty"`
	Logprobs    []json.RawMessage `json:"logprobs,omitempty"`
}

type IncompleteDetails struct {
	Reason string `json:"reason"`
}

type Usage struct {
	InputTokens        int                 `json:"input_tokens"`
	InputTokensDetails *InputTokenDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens       int                 `json:"output_tokens"`
	OutputTokenDetails *OutputTokenDetails `json:"output_tokens_details,omitempty"`
	TotalTokens        int                 `json:"total_tokens"`
}

type InputTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type OutputTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type ResponseError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Param   *string `json:"param,omitempty"`
	Type    string  `json:"type,omitempty"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}
