package ir

import (
	"encoding/json"
	"fmt"
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
	if request.MaxOutputTokens <= 0 {
		return fmt.Errorf("max_output_tokens must be positive")
	}
	for messageIndex, message := range request.Messages {
		switch message.Role {
		case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		default:
			return fmt.Errorf("messages[%d] has unsupported role %q", messageIndex, message.Role)
		}
		if len(message.Content) == 0 {
			return fmt.Errorf("messages[%d] must contain at least one content part", messageIndex)
		}
		for contentIndex, content := range message.Content {
			if err := content.Validate(); err != nil {
				return fmt.Errorf("messages[%d].content[%d]: %w", messageIndex, contentIndex, err)
			}
		}
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
		return nil
	case ContentImage:
		if content.Image == nil {
			return fmt.Errorf("image payload is required")
		}
		if content.Image.URL == "" && (content.Image.MediaType == "" || content.Image.Data == "") {
			return fmt.Errorf("image requires a URL or media_type and base64 data")
		}
	case ContentToolCall:
		if content.ToolCall == nil || content.ToolCall.ID == "" || content.ToolCall.Name == "" {
			return fmt.Errorf("tool_call requires id and name")
		}
		if !isJSONObject(content.ToolCall.Arguments) {
			return fmt.Errorf("tool_call arguments must be a JSON object")
		}
	case ContentToolResult:
		if content.ToolResult == nil || content.ToolResult.ToolCallID == "" {
			return fmt.Errorf("tool_result requires tool_call_id")
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
