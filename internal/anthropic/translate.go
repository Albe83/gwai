package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/Albe83/gwai/internal/ir"
)

func isJSONObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func contentToAnthropic(content ir.Content) (ContentBlock, error) {
	switch content.Type {
	case ir.ContentText:
		return ContentBlock{Type: "text", Text: content.Text}, nil
	case ir.ContentImage:
		if content.Image == nil {
			return ContentBlock{}, fmt.Errorf("image content has no payload")
		}
		if content.Image.URL != "" {
			return ContentBlock{Type: "image", Source: &ImageSource{Type: "url", URL: content.Image.URL}}, nil
		}
		return ContentBlock{Type: "image", Source: &ImageSource{Type: "base64", MediaType: content.Image.MediaType, Data: content.Image.Data}}, nil
	case ir.ContentToolCall:
		if content.ToolCall == nil {
			return ContentBlock{}, fmt.Errorf("tool_call content has no payload")
		}
		return ContentBlock{
			Type: "tool_use", ID: content.ToolCall.ID, Name: content.ToolCall.Name,
			Input: content.ToolCall.Arguments,
		}, nil
	case ir.ContentToolResult:
		if content.ToolResult == nil {
			return ContentBlock{}, fmt.Errorf("tool_result content has no payload")
		}
		nested := make([]ContentBlock, 0, len(content.ToolResult.Content))
		for _, item := range content.ToolResult.Content {
			block, err := contentToAnthropic(item)
			if err != nil {
				return ContentBlock{}, err
			}
			nested = append(nested, block)
		}
		return ContentBlock{
			Type: "tool_result", ToolUseID: content.ToolResult.ToolCallID,
			Content: nested, IsError: content.ToolResult.IsError,
		}, nil
	default:
		return ContentBlock{}, fmt.Errorf("unsupported IR content type %q", content.Type)
	}
}

func ToMessageRequest(request ir.Request) (MessageRequest, error) {
	if err := request.Validate(); err != nil {
		return MessageRequest{}, err
	}
	if request.Stream {
		return MessageRequest{}, fmt.Errorf("streaming IR requests are not supported")
	}
	result := MessageRequest{
		Model: request.Route.UpstreamModel, MaxTokens: request.MaxOutputTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		StopSequences: request.Stop, Stream: false,
	}
	for messageIndex, message := range request.Messages {
		blocks := make([]ContentBlock, 0, len(message.Content))
		for _, content := range message.Content {
			block, err := contentToAnthropic(content)
			if err != nil {
				return MessageRequest{}, fmt.Errorf("messages[%d]: %w", messageIndex, err)
			}
			blocks = append(blocks, block)
		}
		switch message.Role {
		case ir.RoleSystem:
			for _, block := range blocks {
				if block.Type != "text" {
					return MessageRequest{}, fmt.Errorf("system messages may only contain text")
				}
				result.System = append(result.System, block)
			}
		case ir.RoleUser:
			result.Messages = append(result.Messages, Message{Role: "user", Content: blocks})
		case ir.RoleAssistant:
			result.Messages = append(result.Messages, Message{Role: "assistant", Content: blocks})
		case ir.RoleTool:
			result.Messages = append(result.Messages, Message{Role: "user", Content: blocks})
		default:
			return MessageRequest{}, fmt.Errorf("unsupported IR message role %q", message.Role)
		}
	}
	if len(result.Messages) == 0 {
		return MessageRequest{}, fmt.Errorf("at least one non-system message is required")
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Strict: tool.Strict})
	}
	if request.ToolChoice != nil {
		switch request.ToolChoice.Mode {
		case "auto":
			result.ToolChoice = &ToolChoice{Type: "auto"}
		case "none":
			result.ToolChoice = &ToolChoice{Type: "none"}
		case "required":
			result.ToolChoice = &ToolChoice{Type: "any"}
		case "named":
			result.ToolChoice = &ToolChoice{Type: "tool", Name: request.ToolChoice.Name}
		default:
			return MessageRequest{}, fmt.Errorf("unsupported tool choice mode %q", request.ToolChoice.Mode)
		}
		if result.ToolChoice.Type != "none" {
			result.ToolChoice.DisableParallelToolUse = request.ToolChoice.DisableParallel
		}
	}
	return result, nil
}

func ToIRResponse(response MessageResponse, request ir.Request) (ir.Response, error) {
	result := ir.Response{
		Version: ir.Version, ID: request.ID, Model: request.Route.UpstreamModel,
		ProviderResponseID: response.ID,
		Usage: ir.Usage{
			InputTokens:       response.Usage.InputTokens + response.Usage.CacheCreationInputTokens + response.Usage.CacheReadInputTokens,
			OutputTokens:      response.Usage.OutputTokens,
			CachedInputTokens: response.Usage.CacheReadInputTokens,
		},
	}
	if response.StopSequence != nil {
		result.StopSequence = *response.StopSequence
	}
	switch response.StopReason {
	case "end_turn", "stop_sequence", "pause_turn", "":
		result.FinishReason = ir.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		result.FinishReason = ir.FinishLength
	case "tool_use":
		result.FinishReason = ir.FinishToolCalls
	case "refusal":
		result.FinishReason = ir.FinishContent
	default:
		result.FinishReason = ir.FinishStop
	}
	for index, block := range response.Content {
		switch block.Type {
		case "text":
			result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: block.Text})
		case "tool_use":
			if block.ID == "" || block.Name == "" || !isJSONObject(block.Input) {
				return ir.Response{}, fmt.Errorf("response content[%d] contains an invalid tool_use block", index)
			}
			result.Content = append(result.Content, ir.Content{
				Type:     ir.ContentToolCall,
				ToolCall: &ir.ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input},
			})
		case "thinking", "redacted_thinking":
			// Chat Completions does not expose provider chain-of-thought. Usage
			// still accounts for these tokens, so this block is intentionally
			// omitted from the client-visible IR response.
			continue
		default:
			return ir.Response{}, fmt.Errorf("response content[%d] has unsupported Anthropic type %q", index, block.Type)
		}
	}
	return result, nil
}
