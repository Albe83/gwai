package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Albe83/gwai/internal/ir"
)

func marshalRaw(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func imageURL(image *ir.Image) (string, error) {
	if image == nil {
		return "", fmt.Errorf("image content has no payload")
	}
	if image.URL != "" {
		return image.URL, nil
	}
	if image.MediaType == "" || image.Data == "" {
		return "", fmt.Errorf("inline image requires media_type and data")
	}
	if _, err := base64.StdEncoding.DecodeString(image.Data); err != nil {
		return "", fmt.Errorf("inline image contains invalid base64 data: %w", err)
	}
	return "data:" + image.MediaType + ";base64," + image.Data, nil
}

func ordinaryMessage(message ir.Message) (ChatMessage, error) {
	result := ChatMessage{Role: message.Role}
	parts := make([]ContentPart, 0, len(message.Content))
	var text strings.Builder
	hasImage := false
	for index, content := range message.Content {
		switch content.Type {
		case ir.ContentText:
			text.WriteString(content.Text)
			parts = append(parts, ContentPart{Type: "text", Text: content.Text})
		case ir.ContentImage:
			if message.Role != ir.RoleUser {
				return ChatMessage{}, fmt.Errorf("content[%d]: images are only supported in user messages by OpenAI Chat", index)
			}
			url, err := imageURL(content.Image)
			if err != nil {
				return ChatMessage{}, fmt.Errorf("content[%d]: %w", index, err)
			}
			hasImage = true
			parts = append(parts, ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: url}})
		case ir.ContentToolCall:
			if message.Role != ir.RoleAssistant || content.ToolCall == nil {
				return ChatMessage{}, fmt.Errorf("content[%d]: invalid assistant tool call", index)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID: content.ToolCall.ID, Type: "function",
				Function: ToolCallFunction{Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments)},
			})
		default:
			return ChatMessage{}, fmt.Errorf("content[%d]: unsupported %s content in %s message", index, content.Type, message.Role)
		}
	}
	var err error
	if hasImage {
		result.Content, err = marshalRaw(parts)
	} else if text.Len() > 0 || len(result.ToolCalls) == 0 {
		result.Content, err = marshalRaw(text.String())
	} else {
		result.Content = json.RawMessage("null")
	}
	return result, err
}

func toolMessages(message ir.Message) ([]ChatMessage, error) {
	result := make([]ChatMessage, 0, len(message.Content))
	for index, content := range message.Content {
		if content.Type != ir.ContentToolResult || content.ToolResult == nil {
			return nil, fmt.Errorf("content[%d]: tool messages may only contain tool results", index)
		}
		toolResult := content.ToolResult
		var text strings.Builder
		if len(toolResult.Result) > 0 {
			text.Write(toolResult.Result)
		}
		for nestedIndex, nested := range toolResult.Content {
			if nested.Type != ir.ContentText {
				return nil, fmt.Errorf("content[%d].tool_result.content[%d]: OpenAI Chat tool results only support text", index, nestedIndex)
			}
			text.WriteString(nested.Text)
		}
		encoded, err := marshalRaw(text.String())
		if err != nil {
			return nil, err
		}
		result = append(result, ChatMessage{
			Role: ir.RoleTool, Content: encoded, Name: toolResult.Name, ToolCallID: toolResult.ToolCallID,
		})
	}
	return result, nil
}

func ToChatRequest(request ir.Request) (ChatCompletionRequest, error) {
	if err := request.Validate(); err != nil {
		return ChatCompletionRequest{}, err
	}
	if request.Stream {
		return ChatCompletionRequest{}, fmt.Errorf("streaming IR requests are not supported")
	}
	if len(request.Metadata) > 0 {
		return ChatCompletionRequest{}, fmt.Errorf("IR metadata cannot be preserved by OpenAI Chat")
	}
	result := ChatCompletionRequest{
		Model: request.Route.UpstreamModel, MaxCompletionTokens: request.MaxOutputTokens,
		Temperature: request.Temperature, TopP: request.TopP,
	}
	if len(request.Stop) > 0 {
		var err error
		if len(request.Stop) == 1 {
			result.Stop, err = marshalRaw(request.Stop[0])
		} else {
			result.Stop, err = marshalRaw(request.Stop)
		}
		if err != nil {
			return ChatCompletionRequest{}, err
		}
	}
	for messageIndex, message := range request.Messages {
		if message.Role == ir.RoleTool {
			messages, err := toolMessages(message)
			if err != nil {
				return ChatCompletionRequest{}, fmt.Errorf("messages[%d]: %w", messageIndex, err)
			}
			result.Messages = append(result.Messages, messages...)
			continue
		}
		translated, err := ordinaryMessage(message)
		if err != nil {
			return ChatCompletionRequest{}, fmt.Errorf("messages[%d]: %w", messageIndex, err)
		}
		result.Messages = append(result.Messages, translated)
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, Tool{Type: "function", Function: FunctionTool{
			Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict,
		}})
	}
	if request.ToolChoice != nil {
		var value any
		switch request.ToolChoice.Mode {
		case "auto", "none", "required":
			value = request.ToolChoice.Mode
		case "named":
			value = map[string]any{"type": "function", "function": map[string]string{"name": request.ToolChoice.Name}}
		default:
			return ChatCompletionRequest{}, fmt.Errorf("unsupported tool choice mode %q", request.ToolChoice.Mode)
		}
		encoded, err := marshalRaw(value)
		if err != nil {
			return ChatCompletionRequest{}, err
		}
		result.ToolChoice = encoded
		if request.ToolChoice.DisableParallel {
			parallel := false
			result.ParallelToolCalls = &parallel
		}
	}
	return result, nil
}

func FromChatResponse(response ChatCompletionResponse, request ir.Request) (ir.Response, error) {
	if len(response.Choices) != 1 {
		return ir.Response{}, fmt.Errorf("OpenAI Chat returned %d choices; exactly one is required", len(response.Choices))
	}
	choice := response.Choices[0]
	if choice.Index != 0 {
		return ir.Response{}, fmt.Errorf("OpenAI Chat returned unexpected choice index %d", choice.Index)
	}
	if choice.Message.Role != "" && choice.Message.Role != ir.RoleAssistant {
		return ir.Response{}, fmt.Errorf("OpenAI Chat returned message role %q", choice.Message.Role)
	}
	result := ir.Response{
		Version: ir.Version, ID: request.ID, Model: request.Route.UpstreamModel,
		ProviderResponseID: response.ID,
		Usage: ir.Usage{
			InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens,
		},
	}
	if response.Usage.PromptTokensDetails != nil {
		result.Usage.CachedInputTokens = response.Usage.PromptTokensDetails.CachedTokens
	}
	switch choice.FinishReason {
	case "stop", "":
		result.FinishReason = ir.FinishStop
	case "length":
		result.FinishReason = ir.FinishLength
	case "tool_calls", "function_call":
		result.FinishReason = ir.FinishToolCalls
	case "content_filter":
		result.FinishReason = ir.FinishContent
	default:
		return ir.Response{}, fmt.Errorf("OpenAI Chat returned unsupported finish_reason %q", choice.FinishReason)
	}
	if choice.Message.Content != nil {
		result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: *choice.Message.Content})
	}
	if choice.Message.Refusal != nil {
		if choice.Message.Content != nil && *choice.Message.Content != "" {
			return ir.Response{}, fmt.Errorf("OpenAI Chat returned both content and refusal")
		}
		result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: *choice.Message.Refusal})
		result.FinishReason = ir.FinishContent
	}
	for index, call := range choice.Message.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || !isJSONObject(arguments) {
			return ir.Response{}, fmt.Errorf("OpenAI Chat tool_calls[%d] is invalid", index)
		}
		result.Content = append(result.Content, ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{
			ID: call.ID, Name: call.Function.Name, Arguments: arguments,
		}})
	}
	if err := result.Validate(); err != nil {
		return ir.Response{}, fmt.Errorf("invalid translated OpenAI Chat response: %w", err)
	}
	return result, nil
}
