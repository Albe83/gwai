package ir

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

func isJSONObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func (request Request) Validate() error {
	if request.Version != Version {
		return fmt.Errorf("unsupported IR version %q", request.Version)
	}
	if request.ID == "" {
		return fmt.Errorf("id is required")
	}
	if request.Route.ProviderID == "" || request.Route.UpstreamModel == "" {
		return fmt.Errorf("route provider_id and upstream_model are required")
	}
	if len(request.Messages) == 0 {
		return fmt.Errorf("at least one message is required")
	}
	if request.MaxOutputTokens != nil && *request.MaxOutputTokens <= 0 {
		return fmt.Errorf("max_output_tokens must be positive")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return fmt.Errorf("temperature must be between 0 and 1 for the portable IR")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return fmt.Errorf("top_p must be between 0 and 1")
	}
	if request.Stream {
		return fmt.Errorf("streaming is not supported by IR version %s", Version)
	}
	for index, stop := range request.Stop {
		if stop == "" {
			return fmt.Errorf("stop[%d] must not be empty", index)
		}
	}
	seenNonSystem := false
	toolCalls := make(map[string]string)
	toolResults := make(map[string]struct{})
	for messageIndex, message := range request.Messages {
		switch message.Role {
		case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		default:
			return fmt.Errorf("messages[%d] has unsupported role %q", messageIndex, message.Role)
		}
		if len(message.Content) == 0 {
			return fmt.Errorf("messages[%d] must contain at least one content part", messageIndex)
		}
		if message.Role == RoleSystem {
			if seenNonSystem {
				return fmt.Errorf("messages[%d]: system messages must precede all other messages", messageIndex)
			}
		} else {
			seenNonSystem = true
		}
		for contentIndex, content := range message.Content {
			if err := content.Validate(); err != nil {
				return fmt.Errorf("messages[%d].content[%d]: %w", messageIndex, contentIndex, err)
			}
			allowed := false
			switch message.Role {
			case RoleSystem:
				allowed = content.Type == ContentText
			case RoleUser:
				allowed = content.Type == ContentText || content.Type == ContentImage
			case RoleAssistant:
				allowed = content.Type == ContentText || content.Type == ContentToolCall
			case RoleTool:
				allowed = content.Type == ContentToolResult
			}
			if !allowed {
				return fmt.Errorf("messages[%d].content[%d]: content type %q is not allowed for role %q", messageIndex, contentIndex, content.Type, message.Role)
			}
			if content.Type == ContentToolCall {
				if _, duplicate := toolCalls[content.ToolCall.ID]; duplicate {
					return fmt.Errorf("messages[%d].content[%d]: tool_call id %q must be unique", messageIndex, contentIndex, content.ToolCall.ID)
				}
				toolCalls[content.ToolCall.ID] = content.ToolCall.Name
			}
			if content.Type == ContentToolResult {
				callID := content.ToolResult.ToolCallID
				name, exists := toolCalls[callID]
				if !exists {
					return fmt.Errorf("messages[%d].content[%d]: tool_result references unknown prior tool_call %q", messageIndex, contentIndex, callID)
				}
				if content.ToolResult.Name != "" && content.ToolResult.Name != name {
					return fmt.Errorf("messages[%d].content[%d]: tool_result name %q does not match tool_call name %q", messageIndex, contentIndex, content.ToolResult.Name, name)
				}
				if _, duplicate := toolResults[callID]; duplicate {
					return fmt.Errorf("messages[%d].content[%d]: tool_call %q already has a result", messageIndex, contentIndex, callID)
				}
				toolResults[callID] = struct{}{}
			}
		}
	}
	if !seenNonSystem {
		return fmt.Errorf("at least one non-system message is required")
	}
	toolNames := make(map[string]struct{}, len(request.Tools))
	for index, tool := range request.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("tools[%d].name is required", index)
		}
		if !isJSONObject(tool.InputSchema) {
			return fmt.Errorf("tools[%d].input_schema must be a JSON object", index)
		}
		if _, duplicate := toolNames[tool.Name]; duplicate {
			return fmt.Errorf("tools[%d].name must be unique", index)
		}
		toolNames[tool.Name] = struct{}{}
	}
	if request.ToolChoice != nil {
		if len(request.Tools) == 0 {
			return fmt.Errorf("tool_choice requires at least one tool")
		}
		switch request.ToolChoice.Mode {
		case "auto", "none", "required":
		case "named":
			if request.ToolChoice.Name == "" {
				return fmt.Errorf("named tool_choice requires a name")
			}
			if _, exists := toolNames[request.ToolChoice.Name]; !exists {
				return fmt.Errorf("named tool_choice references an unknown tool %q", request.ToolChoice.Name)
			}
		default:
			return fmt.Errorf("unsupported tool_choice mode %q", request.ToolChoice.Mode)
		}
	}
	return nil
}

func (content Content) Validate() error {
	switch content.Type {
	case ContentText:
		if content.Image != nil || content.ToolCall != nil || content.ToolResult != nil {
			return fmt.Errorf("text content contains a payload for another content type")
		}
		return nil
	case ContentImage:
		if content.Text != "" || content.ToolCall != nil || content.ToolResult != nil {
			return fmt.Errorf("image content contains a payload for another content type")
		}
		if content.Image == nil {
			return fmt.Errorf("image payload is required")
		}
		hasURL := content.Image.URL != ""
		hasInlineField := content.Image.MediaType != "" || content.Image.Data != ""
		if hasURL == hasInlineField {
			return fmt.Errorf("image requires exactly one of URL or media_type and base64 data")
		}
		if hasURL {
			parsed, err := url.ParseRequestURI(content.Image.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
				return fmt.Errorf("image URL must be an absolute credential-free HTTP(S) URL")
			}
			return nil
		}
		if content.Image.MediaType == "" || content.Image.Data == "" {
			return fmt.Errorf("inline image requires media_type and base64 data")
		}
		switch content.Image.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return fmt.Errorf("unsupported image media_type %q", content.Image.MediaType)
		}
		if _, err := base64.StdEncoding.DecodeString(content.Image.Data); err != nil {
			return fmt.Errorf("image data is not valid base64: %w", err)
		}
	case ContentToolCall:
		if content.Text != "" || content.Image != nil || content.ToolResult != nil {
			return fmt.Errorf("tool_call content contains a payload for another content type")
		}
		if content.ToolCall == nil || content.ToolCall.ID == "" || content.ToolCall.Name == "" {
			return fmt.Errorf("tool_call requires id and name")
		}
		if !isJSONObject(content.ToolCall.Arguments) {
			return fmt.Errorf("tool_call arguments must be a JSON object")
		}
	case ContentToolResult:
		if content.Text != "" || content.Image != nil || content.ToolCall != nil {
			return fmt.Errorf("tool_result content contains a payload for another content type")
		}
		if content.ToolResult == nil || content.ToolResult.ToolCallID == "" {
			return fmt.Errorf("tool_result requires tool_call_id")
		}
		if len(content.ToolResult.Content) == 0 && len(content.ToolResult.Result) == 0 {
			return fmt.Errorf("tool_result requires content or result")
		}
		if len(content.ToolResult.Result) > 0 && !isJSONObject(content.ToolResult.Result) {
			return fmt.Errorf("tool_result result must be a JSON object")
		}
		for index, nested := range content.ToolResult.Content {
			if nested.Type != ContentText && nested.Type != ContentImage {
				return fmt.Errorf("tool_result content[%d] must be text or image", index)
			}
			if err := nested.Validate(); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported content type %q", content.Type)
	}
	return nil
}

func (response Response) Validate() error {
	if response.Version != Version {
		return fmt.Errorf("unsupported IR version %q", response.Version)
	}
	if response.ID == "" || response.Model == "" {
		return fmt.Errorf("id and model are required")
	}
	switch response.FinishReason {
	case FinishStop, FinishLength, FinishToolCalls, FinishContent:
	default:
		return fmt.Errorf("unsupported finish_reason %q", response.FinishReason)
	}
	for index, content := range response.Content {
		if content.Type != ContentText && content.Type != ContentImage && content.Type != ContentToolCall {
			return fmt.Errorf("content[%d] has unsupported response type %q", index, content.Type)
		}
		if err := content.Validate(); err != nil {
			return fmt.Errorf("content[%d]: %w", index, err)
		}
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.CacheCreationInputTokens < 0 || response.Usage.CachedInputTokens < 0 {
		return fmt.Errorf("usage token counts must not be negative")
	}
	return nil
}
