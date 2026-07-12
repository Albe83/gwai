package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

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
			parsed, err := url.ParseRequestURI(content.Image.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return ContentBlock{}, fmt.Errorf("image URL must be an absolute HTTP(S) URL")
			}
			if content.Image.MediaType != "" || content.Image.Data != "" {
				return ContentBlock{}, fmt.Errorf("image cannot contain both URL and base64 fields")
			}
			return ContentBlock{Type: "image", Source: &ImageSource{Type: "url", URL: content.Image.URL}}, nil
		}
		switch content.Image.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return ContentBlock{}, fmt.Errorf("unsupported image media type %q", content.Image.MediaType)
		}
		if _, err := base64.StdEncoding.DecodeString(content.Image.Data); err != nil {
			return ContentBlock{}, fmt.Errorf("invalid image base64 data: %w", err)
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
		if len(content.ToolResult.Result) > 0 {
			nested = append(nested, ContentBlock{Type: "text", Text: string(content.ToolResult.Result)})
		}
		return ContentBlock{
			Type: "tool_result", ToolUseID: content.ToolResult.ToolCallID,
			Content: nested, IsError: content.ToolResult.IsError,
		}, nil
	default:
		return ContentBlock{}, fmt.Errorf("unsupported IR content type %q", content.Type)
	}
}

func ToMessageRequest(request ir.Request, defaultMaxOutputTokens, maxOutputTokens int) (MessageRequest, error) {
	if err := request.Validate(); err != nil {
		return MessageRequest{}, err
	}
	if request.Stream {
		return MessageRequest{}, fmt.Errorf("streaming IR requests are not supported")
	}
	if len(request.Metadata) > 0 {
		return MessageRequest{}, fmt.Errorf("metadata is not supported by the Anthropic adapter")
	}
	resolvedMaxOutputTokens := defaultMaxOutputTokens
	if request.MaxOutputTokens != nil {
		resolvedMaxOutputTokens = *request.MaxOutputTokens
	}
	if resolvedMaxOutputTokens <= 0 {
		return MessageRequest{}, fmt.Errorf("max_output_tokens is required when the adapter has no positive default")
	}
	if maxOutputTokens > 0 && resolvedMaxOutputTokens > maxOutputTokens {
		return MessageRequest{}, fmt.Errorf("max_output_tokens must not exceed %d", maxOutputTokens)
	}
	result := MessageRequest{
		Model: request.Route.UpstreamModel, MaxTokens: resolvedMaxOutputTokens,
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
		if strings.TrimSpace(tool.Name) == "" || !isJSONObject(tool.InputSchema) {
			return MessageRequest{}, fmt.Errorf("tools require a name and object input_schema")
		}
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
	if response.ID == "" {
		return ir.Response{}, fmt.Errorf("response id is required")
	}
	if response.Type != "message" || response.Role != "assistant" {
		return ir.Response{}, fmt.Errorf("response must be an assistant message")
	}
	if response.Model != "" && response.Model != request.Route.UpstreamModel {
		return ir.Response{}, fmt.Errorf("response model %q does not match requested model %q", response.Model, request.Route.UpstreamModel)
	}
	if response.Usage.InputTokens < 0 || response.Usage.OutputTokens < 0 || response.Usage.CacheCreationInputTokens < 0 || response.Usage.CacheReadInputTokens < 0 {
		return ir.Response{}, fmt.Errorf("response usage token counts must not be negative")
	}
	result := ir.Response{
		Version: ir.Version, ID: request.ID, Model: request.Route.UpstreamModel,
		ProviderResponseID: response.ID,
		Usage: ir.Usage{
			InputTokens:              response.Usage.InputTokens + response.Usage.CacheCreationInputTokens + response.Usage.CacheReadInputTokens,
			OutputTokens:             response.Usage.OutputTokens,
			CacheCreationInputTokens: response.Usage.CacheCreationInputTokens,
			CachedInputTokens:        response.Usage.CacheReadInputTokens,
		},
	}
	if response.StopSequence != nil {
		result.StopSequence = *response.StopSequence
	}
	switch response.StopReason {
	case "end_turn", "stop_sequence", "pause_turn":
		result.FinishReason = ir.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		result.FinishReason = ir.FinishLength
	case "tool_use":
		result.FinishReason = ir.FinishToolCalls
	case "refusal":
		result.FinishReason = ir.FinishContent
	default:
		return ir.Response{}, fmt.Errorf("unsupported Anthropic stop_reason %q", response.StopReason)
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
			return ir.Response{}, fmt.Errorf("response content[%d] contains unsupported thinking output", index)
		default:
			return ir.Response{}, fmt.Errorf("response content[%d] has unsupported Anthropic type %q", index, block.Type)
		}
	}
	if response.StopReason == "stop_sequence" && (response.StopSequence == nil || strings.TrimSpace(*response.StopSequence) == "") {
		return ir.Response{}, fmt.Errorf("stop_sequence stop reason requires a stop sequence")
	}
	if response.StopReason != "stop_sequence" && response.StopSequence != nil {
		return ir.Response{}, fmt.Errorf("stop_sequence must be null unless stop_reason is stop_sequence")
	}
	if err := result.Validate(); err != nil {
		return ir.Response{}, fmt.Errorf("invalid translated IR response: %w", err)
	}
	return result, nil
}
