package anthropic

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type ClientTranslationError struct {
	Field   string
	Message string
}

func (e *ClientTranslationError) Error() string { return e.Message }

func clientInvalid(field, format string, values ...any) error {
	return &ClientTranslationError{Field: field, Message: fmt.Sprintf(format, values...)}
}

func rawPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func decodeStrict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("must contain a single JSON value")
		}
		return err
	}
	return nil
}

func parseClientImage(source *ImageSource) (*ir.Image, error) {
	if source == nil {
		return nil, fmt.Errorf("source is required")
	}
	switch source.Type {
	case "base64":
		if source.URL != "" {
			return nil, fmt.Errorf("base64 source cannot contain url")
		}
		switch source.MediaType {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported base64 media_type %q", source.MediaType)
		}
		if source.Data == "" {
			return nil, fmt.Errorf("base64 data is required")
		}
		if _, err := base64.StdEncoding.DecodeString(source.Data); err != nil {
			return nil, fmt.Errorf("invalid base64 data: %w", err)
		}
		return &ir.Image{MediaType: source.MediaType, Data: source.Data}, nil
	case "url":
		if source.MediaType != "" || source.Data != "" {
			return nil, fmt.Errorf("URL source cannot contain media_type or data")
		}
		parsed, err := url.ParseRequestURI(source.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("url must be an absolute HTTP(S) URL")
		}
		return &ir.Image{URL: source.URL}, nil
	default:
		return nil, fmt.Errorf("unsupported image source type %q", source.Type)
	}
}

func parseClientBlocks(raw json.RawMessage, role string, nested bool) ([]ir.Content, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, fmt.Errorf("content is required")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, false, fmt.Errorf("content must be a valid string: %w", err)
		}
		return []ir.Content{{Type: ir.ContentText, Text: text}}, false, nil
	}
	var blocks []ClientContentBlock
	if err := decodeStrict(trimmed, &blocks); err != nil {
		return nil, false, fmt.Errorf("content must be a string or an array of content blocks: %w", err)
	}
	if len(blocks) == 0 {
		return nil, false, fmt.Errorf("content blocks must not be empty")
	}
	result := make([]ir.Content, 0, len(blocks))
	hasToolResult := false
	for index, block := range blocks {
		var content ir.Content
		switch block.Type {
		case "text":
			if block.Source != nil || block.ID != "" || block.Name != "" || rawPresent(block.Input) || block.ToolUseID != "" || rawPresent(block.Content) || block.IsError {
				return nil, false, fmt.Errorf("content[%d]: text block contains fields for another block type", index)
			}
			content = ir.Content{Type: ir.ContentText, Text: block.Text}
		case "image":
			if role != "user" {
				return nil, false, fmt.Errorf("content[%d]: image blocks require a user message", index)
			}
			if block.Text != "" || block.ID != "" || block.Name != "" || rawPresent(block.Input) || block.ToolUseID != "" || rawPresent(block.Content) || block.IsError {
				return nil, false, fmt.Errorf("content[%d]: image block contains fields for another block type", index)
			}
			image, err := parseClientImage(block.Source)
			if err != nil {
				return nil, false, fmt.Errorf("content[%d].source: %w", index, err)
			}
			content = ir.Content{Type: ir.ContentImage, Image: image}
		case "tool_use":
			if nested {
				return nil, false, fmt.Errorf("content[%d]: nested tool_use blocks are not supported", index)
			}
			if role != "assistant" {
				return nil, false, fmt.Errorf("content[%d]: tool_use blocks require an assistant message", index)
			}
			if block.Text != "" || block.Source != nil || block.ToolUseID != "" || rawPresent(block.Content) || block.IsError {
				return nil, false, fmt.Errorf("content[%d]: tool_use block contains fields for another block type", index)
			}
			if block.ID == "" || strings.TrimSpace(block.Name) == "" || !isJSONObject(block.Input) {
				return nil, false, fmt.Errorf("content[%d]: tool_use requires id, name, and an object input", index)
			}
			content = ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{
				ID: block.ID, Name: strings.TrimSpace(block.Name), Arguments: block.Input,
			}}
		case "tool_result":
			if nested {
				return nil, false, fmt.Errorf("content[%d]: nested tool_result blocks are not supported", index)
			}
			if role != "user" {
				return nil, false, fmt.Errorf("content[%d]: tool_result blocks require a user message", index)
			}
			if block.Text != "" || block.Source != nil || block.ID != "" || block.Name != "" || rawPresent(block.Input) {
				return nil, false, fmt.Errorf("content[%d]: tool_result block contains fields for another block type", index)
			}
			if block.ToolUseID == "" {
				return nil, false, fmt.Errorf("content[%d].tool_use_id is required", index)
			}
			nestedContent, _, err := parseClientBlocks(block.Content, "user", true)
			if err != nil {
				return nil, false, fmt.Errorf("content[%d].content: %w", index, err)
			}
			content = ir.Content{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{
				ToolCallID: block.ToolUseID, Content: nestedContent, IsError: block.IsError,
			}}
			hasToolResult = true
		case "thinking", "redacted_thinking":
			return nil, false, fmt.Errorf("content[%d]: thinking blocks are not supported", index)
		default:
			return nil, false, fmt.Errorf("content[%d] has unsupported type %q", index, block.Type)
		}
		result = append(result, content)
	}
	return result, hasToolResult, nil
}

func parseSystem(raw json.RawMessage) ([]ir.Content, error) {
	if !rawPresent(raw) {
		return nil, nil
	}
	contents, hasToolResult, err := parseClientBlocks(raw, "system", false)
	if err != nil {
		return nil, err
	}
	if hasToolResult {
		return nil, fmt.Errorf("system cannot contain tool results")
	}
	for index, content := range contents {
		if content.Type != ir.ContentText {
			return nil, fmt.Errorf("system content[%d] must be text", index)
		}
	}
	return contents, nil
}

// ToIRRequest translates the public Anthropic Messages request into the
// provider-neutral contract. It does not depend on the Anthropic provider
// adapter translation.
func ToIRRequest(request ClientMessageRequest, route controlplane.Route, requestID string) (ir.Request, error) {
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		return ir.Request{}, clientInvalid("model", "model is required")
	}
	if request.MaxTokens <= 0 {
		return ir.Request{}, clientInvalid("max_tokens", "max_tokens must be positive")
	}
	if len(request.Messages) == 0 {
		return ir.Request{}, clientInvalid("messages", "messages must contain at least one item")
	}
	if request.Stream {
		return ir.Request{}, clientInvalid("stream", "streaming is not supported")
	}
	if request.TopK != nil {
		return ir.Request{}, clientInvalid("top_k", "top_k is not supported by the portable gateway contract")
	}
	if rawPresent(request.Metadata) {
		return ir.Request{}, clientInvalid("metadata", "metadata is not supported by the portable gateway contract")
	}
	if request.ServiceTier != "" {
		return ir.Request{}, clientInvalid("service_tier", "service_tier is not supported by the portable gateway contract")
	}
	if rawPresent(request.Thinking) {
		return ir.Request{}, clientInvalid("thinking", "thinking is not supported by the portable gateway contract")
	}
	if rawPresent(request.OutputConfig) {
		return ir.Request{}, clientInvalid("output_config", "output_config is not supported by the portable gateway contract")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return ir.Request{}, clientInvalid("temperature", "temperature must be between 0 and 1")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return ir.Request{}, clientInvalid("top_p", "top_p must be between 0 and 1")
	}
	for index, stop := range request.StopSequences {
		if stop == "" {
			return ir.Request{}, clientInvalid(fmt.Sprintf("stop_sequences.%d", index), "stop sequences must not be empty")
		}
	}

	messages := make([]ir.Message, 0, len(request.Messages)+1)
	toolUseNames := make(map[string]string)
	system, err := parseSystem(request.System)
	if err != nil {
		return ir.Request{}, clientInvalid("system", "invalid system prompt: %v", err)
	}
	if len(system) > 0 {
		messages = append(messages, ir.Message{Role: ir.RoleSystem, Content: system})
	}
	for index, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			return ir.Request{}, clientInvalid(fmt.Sprintf("messages.%d.role", index), "message role must be user or assistant")
		}
		content, hasToolResult, err := parseClientBlocks(message.Content, message.Role, false)
		if err != nil {
			return ir.Request{}, clientInvalid(fmt.Sprintf("messages.%d.content", index), "%v", err)
		}
		role := message.Role
		if hasToolResult {
			role = ir.RoleTool
		}
		for contentIndex := range content {
			item := &content[contentIndex]
			switch item.Type {
			case ir.ContentToolCall:
				if _, duplicate := toolUseNames[item.ToolCall.ID]; duplicate {
					return ir.Request{}, clientInvalid(fmt.Sprintf("messages.%d.content.%d.id", index, contentIndex), "tool_use ids must be unique")
				}
				toolUseNames[item.ToolCall.ID] = item.ToolCall.Name
			case ir.ContentToolResult:
				name, exists := toolUseNames[item.ToolResult.ToolCallID]
				if !exists {
					return ir.Request{}, clientInvalid(fmt.Sprintf("messages.%d.content.%d.tool_use_id", index, contentIndex), "tool_result references unknown prior tool_use id %q", item.ToolResult.ToolCallID)
				}
				item.ToolResult.Name = name
			}
		}
		messages = append(messages, ir.Message{Role: role, Content: content})
	}

	tools := make([]ir.Tool, 0, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for index, tool := range request.Tools {
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" || !isJSONObject(tool.InputSchema) {
			return ir.Request{}, clientInvalid(fmt.Sprintf("tools.%d", index), "tools require a name and object input_schema")
		}
		if _, duplicate := toolNames[tool.Name]; duplicate {
			return ir.Request{}, clientInvalid(fmt.Sprintf("tools.%d.name", index), "tool names must be unique")
		}
		toolNames[tool.Name] = struct{}{}
		tools = append(tools, ir.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Strict: tool.Strict})
	}
	var toolChoice *ir.ToolChoice
	if request.ToolChoice != nil {
		switch request.ToolChoice.Type {
		case "none":
			if request.ToolChoice.DisableParallelToolUse {
				return ir.Request{}, clientInvalid("tool_choice.disable_parallel_tool_use", "disable_parallel_tool_use cannot be used with tool_choice none")
			}
			if len(tools) > 0 {
				toolChoice = &ir.ToolChoice{Mode: "none"}
			}
		case "auto":
			toolChoice = &ir.ToolChoice{Mode: "auto"}
		case "any":
			toolChoice = &ir.ToolChoice{Mode: "required"}
		case "tool":
			if request.ToolChoice.Name == "" {
				return ir.Request{}, clientInvalid("tool_choice.name", "named tool_choice requires a name")
			}
			toolChoice = &ir.ToolChoice{Mode: "named", Name: request.ToolChoice.Name}
		default:
			return ir.Request{}, clientInvalid("tool_choice.type", "unsupported tool_choice type %q", request.ToolChoice.Type)
		}
		if request.ToolChoice.Type != "none" && len(tools) == 0 {
			return ir.Request{}, clientInvalid("tool_choice", "tool_choice requires at least one tool")
		}
		if toolChoice != nil {
			toolChoice.DisableParallel = request.ToolChoice.DisableParallelToolUse
		}
	}
	if toolChoice != nil && toolChoice.Mode == "named" {
		if _, exists := toolNames[toolChoice.Name]; !exists {
			return ir.Request{}, clientInvalid("tool_choice.name", "selected tool %q is not present in tools", toolChoice.Name)
		}
	}

	maxTokens := request.MaxTokens
	result := ir.Request{
		Version: ir.Version, ID: requestID,
		Route:           ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
		Messages:        messages,
		MaxOutputTokens: &maxTokens,
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		Stop:            append([]string(nil), request.StopSequences...),
		Tools:           tools,
		ToolChoice:      toolChoice,
	}
	if err := result.Validate(); err != nil {
		return ir.Request{}, clientInvalid("messages", "invalid request: %v", err)
	}
	return result, nil
}

// FromIRResponse translates a provider-neutral response into an Anthropic
// Messages response without knowing which provider produced it.
func FromIRResponse(response ir.Response, requestedModel, messageID string) (MessageResponse, error) {
	if err := response.Validate(); err != nil {
		return MessageResponse{}, fmt.Errorf("invalid IR response: %w", err)
	}
	if strings.TrimSpace(requestedModel) == "" || messageID == "" {
		return MessageResponse{}, fmt.Errorf("requested model and message ID are required")
	}
	result := MessageResponse{
		ID: messageID, Type: "message", Role: "assistant", Model: requestedModel,
		Usage: Usage{
			OutputTokens:             response.Usage.OutputTokens,
			CacheCreationInputTokens: response.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     response.Usage.CachedInputTokens,
		},
	}
	uncachedInput := response.Usage.InputTokens - response.Usage.CacheCreationInputTokens - response.Usage.CachedInputTokens
	if uncachedInput < 0 {
		return MessageResponse{}, fmt.Errorf("IR input token total is smaller than its cache token details")
	}
	result.Usage.InputTokens = uncachedInput
	for index, content := range response.Content {
		switch content.Type {
		case ir.ContentText:
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: content.Text})
		case ir.ContentToolCall:
			if content.ToolCall == nil {
				return MessageResponse{}, fmt.Errorf("response content[%d] has no tool_call payload", index)
			}
			result.Content = append(result.Content, ContentBlock{
				Type: "tool_use", ID: content.ToolCall.ID, Name: content.ToolCall.Name, Input: content.ToolCall.Arguments,
			})
		case ir.ContentImage:
			return MessageResponse{}, fmt.Errorf("response content[%d]: Anthropic Messages cannot represent model image output", index)
		default:
			return MessageResponse{}, fmt.Errorf("response content[%d] has unsupported type %q", index, content.Type)
		}
	}
	switch response.FinishReason {
	case ir.FinishStop:
		if response.StopSequence != "" {
			result.StopReason = "stop_sequence"
			stop := response.StopSequence
			result.StopSequence = &stop
		} else {
			result.StopReason = "end_turn"
		}
	case ir.FinishLength:
		result.StopReason = "max_tokens"
	case ir.FinishToolCalls:
		result.StopReason = "tool_use"
	case ir.FinishContent:
		result.StopReason = "refusal"
	default:
		return MessageResponse{}, fmt.Errorf("unsupported IR finish reason %q", response.FinishReason)
	}
	return result, nil
}
