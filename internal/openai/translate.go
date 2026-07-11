package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type TranslationError struct {
	Param   string
	Code    string
	Message string
}

func (e *TranslationError) Error() string { return e.Message }

func invalid(param, code, format string, values ...any) error {
	return &TranslationError{Param: param, Code: code, Message: fmt.Sprintf(format, values...)}
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func parseContent(raw json.RawMessage, role string) ([]ir.Content, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return []ir.Content{{Type: ir.ContentText, Text: text}}, nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, fmt.Errorf("content must be a string or an array of content parts: %w", err)
	}
	result := make([]ir.Content, 0, len(parts))
	for index, part := range parts {
		switch part.Type {
		case "text":
			result = append(result, ir.Content{Type: ir.ContentText, Text: part.Text})
		case "image_url":
			if role != "user" && role != "tool" {
				return nil, fmt.Errorf("content[%d]: image_url is only supported for user and tool messages", index)
			}
			if part.ImageURL == nil || part.ImageURL.URL == "" {
				return nil, fmt.Errorf("content[%d].image_url.url is required", index)
			}
			if part.ImageURL.Detail != "" && part.ImageURL.Detail != "auto" {
				return nil, fmt.Errorf("content[%d].image_url.detail cannot be preserved by the selected provider", index)
			}
			image, err := parseImage(part.ImageURL.URL)
			if err != nil {
				return nil, fmt.Errorf("content[%d].image_url.url: %w", index, err)
			}
			result = append(result, ir.Content{Type: ir.ContentImage, Image: image})
		default:
			return nil, fmt.Errorf("content[%d] has unsupported type %q", index, part.Type)
		}
	}
	return result, nil
}

func parseImage(raw string) (*ir.Image, error) {
	if strings.HasPrefix(raw, "data:") {
		metadata, data, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
		if !ok || !strings.HasSuffix(metadata, ";base64") {
			return nil, fmt.Errorf("data URL must contain base64-encoded image data")
		}
		mediaType := strings.TrimSuffix(metadata, ";base64")
		switch mediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported image media type %q", mediaType)
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return nil, fmt.Errorf("invalid base64 data: %w", err)
		}
		return &ir.Image{MediaType: mediaType, Data: data}, nil
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("must be an HTTP(S) URL or a base64 data URL")
	}
	return &ir.Image{URL: raw}, nil
}

func parseStop(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var stop string
		if err := json.Unmarshal(trimmed, &stop); err != nil {
			return nil, err
		}
		return []string{stop}, nil
	}
	var stops []string
	if err := json.Unmarshal(trimmed, &stops); err != nil {
		return nil, fmt.Errorf("must be a string or array of strings")
	}
	return stops, nil
}

func parseToolChoice(raw json.RawMessage, hasTools bool) (*ir.ToolChoice, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var mode string
		if err := json.Unmarshal(trimmed, &mode); err != nil {
			return nil, err
		}
		switch mode {
		case "none":
			if !hasTools {
				return nil, nil
			}
			return &ir.ToolChoice{Mode: mode}, nil
		case "auto", "required":
			if !hasTools {
				return nil, fmt.Errorf("cannot use tool_choice %q without tools", mode)
			}
			return &ir.ToolChoice{Mode: mode}, nil
		default:
			return nil, fmt.Errorf("unsupported value %q", mode)
		}
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(trimmed, &choice); err != nil {
		return nil, err
	}
	if choice.Type != "function" || choice.Function.Name == "" {
		return nil, fmt.Errorf("named tool_choice must select a function")
	}
	if !hasTools {
		return nil, fmt.Errorf("cannot select a function without tools")
	}
	return &ir.ToolChoice{Mode: "named", Name: choice.Function.Name}, nil
}

func ToIR(request ChatCompletionRequest, route controlplane.Route, requestID string) (ir.Request, error) {
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		return ir.Request{}, invalid("model", "invalid_model", "model is required")
	}
	if len(request.Messages) == 0 {
		return ir.Request{}, invalid("messages", "invalid_messages", "messages must contain at least one item")
	}
	if request.N != nil && *request.N != 1 {
		return ir.Request{}, invalid("n", "unsupported_parameter", "only n=1 is supported")
	}
	if request.Stream {
		return ir.Request{}, invalid("stream", "unsupported_parameter", "streaming is not yet supported by this route")
	}
	if trimmed := bytes.TrimSpace(request.StreamOptions); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		return ir.Request{}, invalid("stream_options", "unsupported_parameter", "stream_options requires streaming, which is not supported by this route")
	}
	if len(request.Metadata) > 0 {
		return ir.Request{}, invalid("metadata", "unsupported_parameter", "metadata cannot be preserved by the selected provider")
	}
	if request.FrequencyPenalty != nil && *request.FrequencyPenalty != 0 {
		return ir.Request{}, invalid("frequency_penalty", "unsupported_parameter", "frequency_penalty is not supported by the selected provider")
	}
	if request.PresencePenalty != nil && *request.PresencePenalty != 0 {
		return ir.Request{}, invalid("presence_penalty", "unsupported_parameter", "presence_penalty is not supported by the selected provider")
	}
	if request.Logprobs != nil && *request.Logprobs {
		return ir.Request{}, invalid("logprobs", "unsupported_parameter", "logprobs is not supported by the selected provider")
	}
	if request.TopLogprobs != nil {
		return ir.Request{}, invalid("top_logprobs", "unsupported_parameter", "top_logprobs is not supported by the selected provider")
	}
	if request.Seed != nil {
		return ir.Request{}, invalid("seed", "unsupported_parameter", "seed is not supported by the selected provider")
	}
	if request.User != "" {
		return ir.Request{}, invalid("user", "unsupported_parameter", "user is not forwarded by the selected provider")
	}
	if len(bytes.TrimSpace(request.LogitBias)) > 0 && !bytes.Equal(bytes.TrimSpace(request.LogitBias), []byte("null")) {
		return ir.Request{}, invalid("logit_bias", "unsupported_parameter", "logit_bias is not supported by the selected provider")
	}
	if len(bytes.TrimSpace(request.ResponseFormat)) > 0 && !bytes.Equal(bytes.TrimSpace(request.ResponseFormat), []byte("null")) {
		return ir.Request{}, invalid("response_format", "unsupported_parameter", "response_format is not supported by the selected provider")
	}

	maxTokens := route.MaxOutputTokens
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return ir.Request{}, invalid("max_tokens", "invalid_request", "max_tokens and max_completion_tokens cannot both be set")
	}
	if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	if request.MaxCompletionTokens != nil {
		maxTokens = *request.MaxCompletionTokens
	}
	if maxTokens <= 0 || maxTokens > route.MaxOutputTokens {
		return ir.Request{}, invalid("max_completion_tokens", "invalid_value", "max completion tokens must be between 1 and %d", route.MaxOutputTokens)
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return ir.Request{}, invalid("temperature", "invalid_value", "temperature must be between 0 and 1 for the selected provider")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return ir.Request{}, invalid("top_p", "invalid_value", "top_p must be between 0 and 1")
	}
	stops, err := parseStop(request.Stop)
	if err != nil {
		return ir.Request{}, invalid("stop", "invalid_value", "invalid stop: %v", err)
	}

	messages := make([]ir.Message, 0, len(request.Messages))
	for messageIndex, message := range request.Messages {
		if message.Name != "" {
			return ir.Request{}, invalid(fmt.Sprintf("messages.%d.name", messageIndex), "unsupported_parameter", "named messages are not supported by the selected provider")
		}
		role := message.Role
		if role == "developer" {
			role = ir.RoleSystem
		}
		switch role {
		case ir.RoleSystem, ir.RoleUser, ir.RoleAssistant, ir.RoleTool:
		default:
			return ir.Request{}, invalid(fmt.Sprintf("messages.%d.role", messageIndex), "invalid_role", "unsupported message role %q", message.Role)
		}
		contents, err := parseContent(message.Content, role)
		if err != nil {
			return ir.Request{}, invalid(fmt.Sprintf("messages.%d.content", messageIndex), "invalid_content", "%v", err)
		}
		if role == ir.RoleAssistant {
			for toolIndex, toolCall := range message.ToolCalls {
				if toolCall.Type != "function" || toolCall.ID == "" || toolCall.Function.Name == "" {
					return ir.Request{}, invalid(fmt.Sprintf("messages.%d.tool_calls.%d", messageIndex, toolIndex), "invalid_tool_call", "tool call requires type=function, id, and function.name")
				}
				arguments := json.RawMessage(toolCall.Function.Arguments)
				if !isJSONObject(arguments) {
					return ir.Request{}, invalid(fmt.Sprintf("messages.%d.tool_calls.%d.function.arguments", messageIndex, toolIndex), "invalid_tool_arguments", "tool arguments must contain a JSON object")
				}
				contents = append(contents, ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: toolCall.ID, Name: toolCall.Function.Name, Arguments: arguments}})
			}
		}
		if role == ir.RoleTool {
			if message.ToolCallID == "" {
				return ir.Request{}, invalid(fmt.Sprintf("messages.%d.tool_call_id", messageIndex), "invalid_tool_result", "tool_call_id is required for tool messages")
			}
			contents = []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{ToolCallID: message.ToolCallID, Content: contents}}}
		}
		if len(contents) == 0 {
			return ir.Request{}, invalid(fmt.Sprintf("messages.%d.content", messageIndex), "invalid_content", "message content cannot be empty unless assistant tool_calls are present")
		}
		messages = append(messages, ir.Message{Role: role, Content: contents})
	}

	tools := make([]ir.Tool, 0, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for index, tool := range request.Tools {
		if tool.Type != "function" || strings.TrimSpace(tool.Function.Name) == "" {
			return ir.Request{}, invalid(fmt.Sprintf("tools.%d", index), "invalid_tool", "only named function tools are supported")
		}
		parameters := tool.Function.Parameters
		if len(bytes.TrimSpace(parameters)) == 0 || bytes.Equal(bytes.TrimSpace(parameters), []byte("null")) {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !isJSONObject(parameters) {
			return ir.Request{}, invalid(fmt.Sprintf("tools.%d.function.parameters", index), "invalid_tool_schema", "parameters must contain a JSON Schema object")
		}
		if _, duplicate := toolNames[tool.Function.Name]; duplicate {
			return ir.Request{}, invalid(fmt.Sprintf("tools.%d.function.name", index), "invalid_tool", "tool names must be unique")
		}
		toolNames[tool.Function.Name] = struct{}{}
		tools = append(tools, ir.Tool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: parameters, Strict: tool.Function.Strict})
	}
	toolChoice, err := parseToolChoice(request.ToolChoice, len(tools) > 0)
	if err != nil {
		return ir.Request{}, invalid("tool_choice", "invalid_tool_choice", "%v", err)
	}
	if toolChoice != nil && toolChoice.Mode == "named" {
		if _, exists := toolNames[toolChoice.Name]; !exists {
			return ir.Request{}, invalid("tool_choice", "invalid_tool_choice", "selected function %q is not present in tools", toolChoice.Name)
		}
	}
	if request.ParallelToolCalls != nil && !*request.ParallelToolCalls && len(tools) > 0 {
		if toolChoice == nil {
			toolChoice = &ir.ToolChoice{Mode: "auto"}
		}
		toolChoice.DisableParallel = true
	}

	result := ir.Request{
		Version: ir.Version, ID: requestID,
		Route:    ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
		Messages: messages, MaxOutputTokens: maxTokens,
		Temperature: request.Temperature, TopP: request.TopP, Stop: stops,
		Tools: tools, ToolChoice: toolChoice,
	}
	if err := result.Validate(); err != nil {
		return ir.Request{}, invalid("messages", "invalid_request", "%v", err)
	}
	return result, nil
}

func FromIR(response ir.Response, requestedModel, completionID string, created time.Time) (ChatCompletionResponse, error) {
	if response.Version != ir.Version {
		return ChatCompletionResponse{}, fmt.Errorf("adapter returned unsupported IR version %q", response.Version)
	}
	var text strings.Builder
	toolCalls := make([]ToolCall, 0)
	for index, content := range response.Content {
		switch content.Type {
		case ir.ContentText:
			text.WriteString(content.Text)
		case ir.ContentToolCall:
			if content.ToolCall == nil {
				return ChatCompletionResponse{}, fmt.Errorf("response content[%d] has no tool_call payload", index)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID: content.ToolCall.ID, Type: "function",
				Function: ToolCallFunction{Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments)},
			})
		default:
			return ChatCompletionResponse{}, fmt.Errorf("response content[%d] has unsupported type %q", index, content.Type)
		}
	}
	var content *string
	if text.Len() > 0 || len(toolCalls) == 0 {
		value := text.String()
		content = &value
	}
	finishReason := response.FinishReason
	switch finishReason {
	case ir.FinishStop, ir.FinishLength, ir.FinishToolCalls, ir.FinishContent:
	default:
		finishReason = ir.FinishStop
	}
	var promptTokensDetails *PromptTokensDetails
	if response.Usage.CachedInputTokens > 0 {
		promptTokensDetails = &PromptTokensDetails{CachedTokens: response.Usage.CachedInputTokens}
	}
	return ChatCompletionResponse{
		ID: completionID, Object: "chat.completion", Created: created.Unix(), Model: requestedModel,
		Choices: []Choice{{Index: 0, Message: AssistantOutput{Role: "assistant", Content: content, ToolCalls: toolCalls}, FinishReason: finishReason}},
		Usage: Usage{
			PromptTokens:        response.Usage.InputTokens,
			CompletionTokens:    response.Usage.OutputTokens,
			TotalTokens:         response.Usage.InputTokens + response.Usage.OutputTokens,
			PromptTokensDetails: promptTokensDetails,
		},
		SystemFingerprint: nil,
	}, nil
}
