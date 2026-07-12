package openairesponses

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("must contain one JSON value")
		}
		return err
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
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

func imageURL(image *ir.Image) (string, error) {
	if image == nil {
		return "", fmt.Errorf("image payload is required")
	}
	if image.URL != "" {
		return image.URL, nil
	}
	if image.MediaType == "" || image.Data == "" {
		return "", fmt.Errorf("inline image requires media type and data")
	}
	return "data:" + image.MediaType + ";base64," + image.Data, nil
}

func parseInputContent(raw json.RawMessage, role, param string) ([]ir.Content, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, invalid(param, "invalid_input", "message content is required")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, invalid(param, "invalid_input", "message content must be valid text: %v", err)
		}
		return []ir.Content{{Type: ir.ContentText, Text: text}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, invalid(param, "invalid_input", "message content must be a string or array: %v", err)
	}
	if len(parts) == 0 {
		return nil, invalid(param, "invalid_input", "message content must not be empty")
	}
	result := make([]ir.Content, 0, len(parts))
	for index, rawPart := range parts {
		var part InputContent
		partParam := fmt.Sprintf("%s.%d", param, index)
		if err := decodeStrict(rawPart, &part); err != nil {
			return nil, invalid(partParam, "invalid_input", "invalid content part: %v", err)
		}
		switch part.Type {
		case "input_text":
			if role == ir.RoleAssistant && (len(part.Annotations) > 0 || len(part.Logprobs) > 0) {
				return nil, invalid(partParam, "unsupported_parameter", "annotations and logprobs cannot be preserved")
			}
			result = append(result, ir.Content{Type: ir.ContentText, Text: part.Text})
		case "output_text":
			if role != ir.RoleAssistant {
				return nil, invalid(partParam, "invalid_input", "output_text is only valid in assistant history")
			}
			if len(part.Annotations) > 0 || len(part.Logprobs) > 0 {
				return nil, invalid(partParam, "unsupported_parameter", "annotations and logprobs cannot be preserved")
			}
			result = append(result, ir.Content{Type: ir.ContentText, Text: part.Text})
		case "input_image":
			if role != ir.RoleUser && role != ir.RoleTool {
				return nil, invalid(partParam, "invalid_input", "input_image is only supported for user and function output content")
			}
			if part.FileID != "" {
				return nil, invalid(partParam+".file_id", "unsupported_parameter", "file-backed images are not supported")
			}
			if part.ImageURL == "" {
				return nil, invalid(partParam+".image_url", "invalid_input", "image_url is required")
			}
			if part.Detail != "" && part.Detail != "auto" {
				return nil, invalid(partParam+".detail", "unsupported_parameter", "image detail cannot be preserved by the selected provider")
			}
			image, err := parseImage(part.ImageURL)
			if err != nil {
				return nil, invalid(partParam+".image_url", "invalid_image", "%v", err)
			}
			result = append(result, ir.Content{Type: ir.ContentImage, Image: image})
		case "input_file":
			return nil, invalid(partParam, "unsupported_parameter", "file inputs are not supported")
		default:
			return nil, invalid(partParam+".type", "unsupported_parameter", "unsupported content type %q", part.Type)
		}
	}
	return result, nil
}

func parseToolResult(raw json.RawMessage, param string) (*ir.ToolResult, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, invalid(param, "invalid_tool_output", "function output is required")
	}
	if trimmed[0] == '"' {
		var output string
		if err := json.Unmarshal(trimmed, &output); err != nil {
			return nil, invalid(param, "invalid_tool_output", "invalid function output: %v", err)
		}
		asJSON := json.RawMessage(output)
		if isJSONObject(asJSON) {
			return &ir.ToolResult{Result: append(json.RawMessage(nil), asJSON...)}, nil
		}
		return &ir.ToolResult{Content: []ir.Content{{Type: ir.ContentText, Text: output}}}, nil
	}
	contents, err := parseInputContent(trimmed, ir.RoleTool, param)
	if err != nil {
		return nil, err
	}
	return &ir.ToolResult{Content: contents}, nil
}

func parseToolChoice(raw json.RawMessage, toolNames map[string]struct{}) (*ir.ToolChoice, error) {
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
			if len(toolNames) == 0 {
				return nil, nil
			}
		case "auto", "required":
			if len(toolNames) == 0 {
				return nil, fmt.Errorf("tool_choice %q requires at least one tool", mode)
			}
		default:
			return nil, fmt.Errorf("unsupported tool_choice %q", mode)
		}
		return &ir.ToolChoice{Mode: mode}, nil
	}
	var selected struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := decodeStrict(trimmed, &selected); err != nil {
		return nil, err
	}
	if selected.Type != "function" || selected.Name == "" {
		return nil, fmt.Errorf("named tool_choice must select a function")
	}
	if _, ok := toolNames[selected.Name]; !ok {
		return nil, fmt.Errorf("selected function %q is not present in tools", selected.Name)
	}
	return &ir.ToolChoice{Mode: "named", Name: selected.Name}, nil
}

func rejectUnsupported(request CreateRequest) error {
	checks := []struct {
		present bool
		param   string
		detail  string
	}{
		{request.Stream != nil && *request.Stream, "stream", "streaming is not supported"},
		{request.Store != nil && *request.Store, "store", "stored Responses are not supported; set store=false"},
		{request.PreviousResponseID != "", "previous_response_id", "stateful response chaining is not supported"},
		{rawPresent(request.Conversation), "conversation", "conversation state is not supported"},
		{rawPresent(request.Reasoning), "reasoning", "reasoning configuration cannot be preserved"},
		{rawPresent(request.Text), "text", "structured text output configuration is not supported"},
		{len(request.Metadata) > 0, "metadata", "metadata cannot be preserved by every provider"},
		{len(request.Include) > 0, "include", "additional response fields are not supported"},
		{request.Background != nil && *request.Background, "background", "background Responses are not supported"},
		{request.Truncation != "" && request.Truncation != "disabled", "truncation", "automatic truncation is not supported"},
		{rawPresent(request.Prompt), "prompt", "stored prompt templates are not supported"},
		{request.MaxToolCalls != nil, "max_tool_calls", "hosted tool call limits are not supported"},
		{request.SafetyIdentifier != "", "safety_identifier", "safety identifiers are not forwarded"},
		{request.PromptCacheKey != "", "prompt_cache_key", "prompt cache keys are not forwarded"},
		{request.ServiceTier != "", "service_tier", "service tier selection is not supported"},
		{request.User != "", "user", "deprecated user identifiers are not forwarded"},
		{request.TopLogprobs != nil, "top_logprobs", "log probabilities are not supported"},
		{rawPresent(request.StreamOptions), "stream_options", "stream options require streaming, which is not supported"},
	}
	for _, check := range checks {
		if check.present {
			return invalid(check.param, "unsupported_parameter", "%s", check.detail)
		}
	}
	return nil
}

func ToIR(request CreateRequest, route controlplane.Route, requestID string) (ir.Request, error) {
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		return ir.Request{}, invalid("model", "invalid_model", "model is required")
	}
	if err := rejectUnsupported(request); err != nil {
		return ir.Request{}, err
	}
	if request.MaxOutputTokens != nil && *request.MaxOutputTokens < 16 {
		return ir.Request{}, invalid("max_output_tokens", "invalid_value", "max_output_tokens must be at least 16")
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 1) {
		return ir.Request{}, invalid("temperature", "invalid_value", "temperature must be between 0 and 1 for the portable gateway contract")
	}
	if request.TopP != nil && (*request.TopP < 0 || *request.TopP > 1) {
		return ir.Request{}, invalid("top_p", "invalid_value", "top_p must be between 0 and 1")
	}

	messages := make([]ir.Message, 0)
	if rawPresent(request.Instructions) {
		var instructions string
		if err := json.Unmarshal(request.Instructions, &instructions); err != nil {
			return ir.Request{}, invalid("instructions", "unsupported_parameter", "only string instructions are supported")
		}
		if instructions != "" {
			messages = append(messages, ir.Message{Role: ir.RoleSystem, Content: []ir.Content{{Type: ir.ContentText, Text: instructions}}})
		}
	}

	input := bytes.TrimSpace(request.Input)
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return ir.Request{}, invalid("input", "invalid_input", "input is required")
	}
	if input[0] == '"' {
		var text string
		if err := json.Unmarshal(input, &text); err != nil {
			return ir.Request{}, invalid("input", "invalid_input", "input must be valid text: %v", err)
		}
		messages = append(messages, ir.Message{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: text}}})
	} else {
		var items []json.RawMessage
		if err := json.Unmarshal(input, &items); err != nil {
			return ir.Request{}, invalid("input", "invalid_input", "input must be a string or array of items: %v", err)
		}
		if len(items) == 0 {
			return ir.Request{}, invalid("input", "invalid_input", "input must contain at least one item")
		}
		callNames := make(map[string]string)
		for index, rawItem := range items {
			var item InputItem
			param := fmt.Sprintf("input.%d", index)
			if err := decodeStrict(rawItem, &item); err != nil {
				return ir.Request{}, invalid(param, "invalid_input", "invalid input item: %v", err)
			}
			switch item.Type {
			case "", "message":
				if item.Phase != "" {
					return ir.Request{}, invalid(param+".phase", "unsupported_parameter", "assistant phases cannot be preserved")
				}
				if item.Status != "" && item.Status != "completed" {
					return ir.Request{}, invalid(param+".status", "unsupported_parameter", "only completed history items are supported")
				}
				role := item.Role
				if role == "developer" {
					role = ir.RoleSystem
				}
				switch role {
				case ir.RoleSystem, ir.RoleUser, ir.RoleAssistant:
				default:
					return ir.Request{}, invalid(param+".role", "invalid_role", "unsupported message role %q", item.Role)
				}
				content, err := parseInputContent(item.Content, role, param+".content")
				if err != nil {
					return ir.Request{}, err
				}
				messages = append(messages, ir.Message{Role: role, Content: content})
			case "function_call":
				arguments := json.RawMessage(item.Arguments)
				if item.CallID == "" || item.Name == "" || !isJSONObject(arguments) {
					return ir.Request{}, invalid(param, "invalid_tool_call", "function_call requires call_id, name, and JSON object arguments")
				}
				if _, duplicate := callNames[item.CallID]; duplicate {
					return ir.Request{}, invalid(param+".call_id", "invalid_tool_call", "call_id values must be unique")
				}
				if item.Namespace != "" {
					return ir.Request{}, invalid(param+".namespace", "unsupported_parameter", "function namespaces are not supported")
				}
				callNames[item.CallID] = item.Name
				messages = append(messages, ir.Message{Role: ir.RoleAssistant, Content: []ir.Content{{
					Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: item.CallID, Name: item.Name, Arguments: arguments},
				}}})
			case "function_call_output":
				if item.CallID == "" {
					return ir.Request{}, invalid(param+".call_id", "invalid_tool_output", "call_id is required")
				}
				name, exists := callNames[item.CallID]
				if !exists {
					return ir.Request{}, invalid(param+".call_id", "invalid_tool_output", "function output must reference a preceding function_call")
				}
				toolResult, err := parseToolResult(item.Output, param+".output")
				if err != nil {
					return ir.Request{}, err
				}
				toolResult.ToolCallID = item.CallID
				toolResult.Name = name
				messages = append(messages, ir.Message{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: toolResult}}})
			case "reasoning":
				return ir.Request{}, invalid(param+".type", "unsupported_parameter", "reasoning items cannot be preserved")
			default:
				return ir.Request{}, invalid(param+".type", "unsupported_parameter", "unsupported input item type %q; only messages and function calls/results are supported", item.Type)
			}
		}
	}

	tools := make([]ir.Tool, 0, len(request.Tools))
	toolNames := make(map[string]struct{}, len(request.Tools))
	for index, tool := range request.Tools {
		param := fmt.Sprintf("tools.%d", index)
		if tool.Type != "function" {
			return ir.Request{}, invalid(param+".type", "unsupported_parameter", "hosted and custom tools are not supported")
		}
		if strings.TrimSpace(tool.Name) == "" {
			return ir.Request{}, invalid(param+".name", "invalid_tool", "function name is required")
		}
		parameters := bytes.TrimSpace(tool.Parameters)
		if len(parameters) == 0 || bytes.Equal(parameters, []byte("null")) {
			parameters = []byte(`{"type":"object","properties":{}}`)
		}
		if !isJSONObject(parameters) {
			return ir.Request{}, invalid(param+".parameters", "invalid_tool_schema", "parameters must contain a JSON Schema object")
		}
		if _, duplicate := toolNames[tool.Name]; duplicate {
			return ir.Request{}, invalid(param+".name", "invalid_tool", "function names must be unique")
		}
		toolNames[tool.Name] = struct{}{}
		tools = append(tools, ir.Tool{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), parameters...), Strict: tool.Strict})
	}
	toolChoice, err := parseToolChoice(request.ToolChoice, toolNames)
	if err != nil {
		return ir.Request{}, invalid("tool_choice", "invalid_tool_choice", "%v", err)
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
		Messages: messages, MaxOutputTokens: request.MaxOutputTokens,
		Temperature: request.Temperature, TopP: request.TopP,
		Tools: tools, ToolChoice: toolChoice,
	}
	if err := result.Validate(); err != nil {
		return ir.Request{}, invalid("input", "invalid_input", "%v", err)
	}
	return result, nil
}

func toolChoiceJSON(choice *ir.ToolChoice) (json.RawMessage, error) {
	if choice == nil {
		return nil, nil
	}
	switch choice.Mode {
	case "auto", "none", "required":
		return json.Marshal(choice.Mode)
	case "named":
		return json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: choice.Name})
	default:
		return nil, fmt.Errorf("unsupported tool choice mode %q", choice.Mode)
	}
}

func encodeMessageContent(contents []ir.Content) (json.RawMessage, []InputItem, error) {
	parts := make([]InputContent, 0, len(contents))
	calls := make([]InputItem, 0)
	for _, content := range contents {
		switch content.Type {
		case ir.ContentText:
			parts = append(parts, InputContent{Type: "input_text", Text: content.Text})
		case ir.ContentImage:
			value, err := imageURL(content.Image)
			if err != nil {
				return nil, nil, err
			}
			parts = append(parts, InputContent{Type: "input_image", ImageURL: value, Detail: "auto"})
		case ir.ContentToolCall:
			if content.ToolCall == nil {
				return nil, nil, fmt.Errorf("tool call payload is required")
			}
			calls = append(calls, InputItem{Type: "function_call", CallID: content.ToolCall.ID, Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments)})
		default:
			return nil, nil, fmt.Errorf("unsupported message content type %q", content.Type)
		}
	}
	if len(parts) == 0 {
		return nil, calls, nil
	}
	raw, err := json.Marshal(parts)
	return raw, calls, err
}

func encodeToolOutput(result *ir.ToolResult) (json.RawMessage, error) {
	if result == nil {
		return nil, fmt.Errorf("tool result payload is required")
	}
	if result.IsError {
		return nil, fmt.Errorf("OpenAI Responses has no portable is_error function output flag")
	}
	if len(result.Result) > 0 {
		return json.Marshal(string(result.Result))
	}
	if len(result.Content) == 1 && result.Content[0].Type == ir.ContentText {
		return json.Marshal(result.Content[0].Text)
	}
	parts := make([]InputContent, 0, len(result.Content))
	for _, content := range result.Content {
		switch content.Type {
		case ir.ContentText:
			parts = append(parts, InputContent{Type: "input_text", Text: content.Text})
		case ir.ContentImage:
			value, err := imageURL(content.Image)
			if err != nil {
				return nil, err
			}
			parts = append(parts, InputContent{Type: "input_image", ImageURL: value, Detail: "auto"})
		default:
			return nil, fmt.Errorf("unsupported function output content type %q", content.Type)
		}
	}
	return json.Marshal(parts)
}

func ToProviderRequest(request ir.Request) (ProviderRequest, error) {
	if err := request.Validate(); err != nil {
		return ProviderRequest{}, err
	}
	if request.Stream {
		return ProviderRequest{}, fmt.Errorf("streaming IR requests are not supported")
	}
	if len(request.Stop) > 0 {
		return ProviderRequest{}, fmt.Errorf("OpenAI Responses does not support stop sequences")
	}
	if len(request.Metadata) > 0 {
		return ProviderRequest{}, fmt.Errorf("metadata is not supported by this adapter")
	}
	if request.MaxOutputTokens != nil && *request.MaxOutputTokens < 16 {
		return ProviderRequest{}, fmt.Errorf("max_output_tokens must be at least 16")
	}
	result := ProviderRequest{
		Model: request.Route.UpstreamModel, MaxOutputTokens: request.MaxOutputTokens,
		Temperature: request.Temperature, TopP: request.TopP, Store: false, Stream: false,
	}
	var instructions []string
	for messageIndex, message := range request.Messages {
		switch message.Role {
		case ir.RoleSystem:
			for _, content := range message.Content {
				if content.Type != ir.ContentText {
					return ProviderRequest{}, fmt.Errorf("messages[%d]: system messages may only contain text", messageIndex)
				}
				instructions = append(instructions, content.Text)
			}
		case ir.RoleUser, ir.RoleAssistant:
			content, calls, err := encodeMessageContent(message.Content)
			if err != nil {
				return ProviderRequest{}, fmt.Errorf("messages[%d]: %w", messageIndex, err)
			}
			if len(content) > 0 {
				result.Input = append(result.Input, InputItem{Type: "message", Role: message.Role, Content: content})
			}
			result.Input = append(result.Input, calls...)
		case ir.RoleTool:
			for contentIndex, content := range message.Content {
				if content.Type != ir.ContentToolResult || content.ToolResult == nil {
					return ProviderRequest{}, fmt.Errorf("messages[%d].content[%d]: tool messages require tool_result content", messageIndex, contentIndex)
				}
				output, err := encodeToolOutput(content.ToolResult)
				if err != nil {
					return ProviderRequest{}, fmt.Errorf("messages[%d].content[%d]: %w", messageIndex, contentIndex, err)
				}
				result.Input = append(result.Input, InputItem{Type: "function_call_output", CallID: content.ToolResult.ToolCallID, Output: output})
			}
		default:
			return ProviderRequest{}, fmt.Errorf("messages[%d]: unsupported role %q", messageIndex, message.Role)
		}
	}
	result.Instructions = strings.Join(instructions, "\n")
	if len(result.Input) == 0 {
		return ProviderRequest{}, fmt.Errorf("at least one non-system input item is required")
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, Tool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema, Strict: tool.Strict})
	}
	choice, err := toolChoiceJSON(request.ToolChoice)
	if err != nil {
		return ProviderRequest{}, err
	}
	result.ToolChoice = choice
	if request.ToolChoice != nil && request.ToolChoice.DisableParallel {
		parallel := false
		result.ParallelToolCalls = &parallel
	}
	return result, nil
}

func ToIRResponse(response Response, request ir.Request) (ir.Response, error) {
	if response.Error != nil {
		return ir.Response{}, fmt.Errorf("OpenAI response failed: %s", response.Error.Message)
	}
	if response.Status != "completed" && response.Status != "incomplete" {
		return ir.Response{}, fmt.Errorf("OpenAI response has non-terminal status %q", response.Status)
	}
	model := response.Model
	if model == "" {
		model = request.Route.UpstreamModel
	}
	result := ir.Response{
		Version: ir.Version, ID: request.ID, Model: model,
		ProviderResponseID: response.ID,
		Usage:              ir.Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens},
		FinishReason:       ir.FinishStop,
	}
	if response.Usage.InputTokensDetails != nil {
		result.Usage.CachedInputTokens = response.Usage.InputTokensDetails.CachedTokens
	}
	refused := false
	for itemIndex, item := range response.Output {
		switch item.Type {
		case "message":
			if item.Role != "" && item.Role != "assistant" {
				return ir.Response{}, fmt.Errorf("output[%d] has unsupported message role %q", itemIndex, item.Role)
			}
			for contentIndex, content := range item.Content {
				switch content.Type {
				case "output_text":
					if len(content.Annotations) > 0 || len(content.Logprobs) > 0 {
						return ir.Response{}, fmt.Errorf("output[%d].content[%d] contains annotations or logprobs that cannot be preserved", itemIndex, contentIndex)
					}
					result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: content.Text})
				case "refusal":
					refused = true
					result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: content.Refusal})
				default:
					return ir.Response{}, fmt.Errorf("output[%d].content[%d] has unsupported type %q", itemIndex, contentIndex, content.Type)
				}
			}
		case "function_call":
			arguments := json.RawMessage(item.Arguments)
			if item.CallID == "" || item.Name == "" || !isJSONObject(arguments) {
				return ir.Response{}, fmt.Errorf("output[%d] contains an invalid function call", itemIndex)
			}
			result.Content = append(result.Content, ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: item.CallID, Name: item.Name, Arguments: arguments}})
		case "reasoning":
			// Reasoning items are intentionally not exposed in the portable IR.
			continue
		default:
			return ir.Response{}, fmt.Errorf("output[%d] has unsupported hosted item type %q", itemIndex, item.Type)
		}
	}
	if response.Status == "incomplete" {
		if response.IncompleteDetails == nil {
			return ir.Response{}, fmt.Errorf("incomplete OpenAI response has no reason")
		}
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			result.FinishReason = ir.FinishLength
		case "content_filter":
			result.FinishReason = ir.FinishContent
		default:
			return ir.Response{}, fmt.Errorf("unsupported incomplete reason %q", response.IncompleteDetails.Reason)
		}
	} else if refused {
		result.FinishReason = ir.FinishContent
	} else {
		for _, content := range result.Content {
			if content.Type == ir.ContentToolCall {
				result.FinishReason = ir.FinishToolCalls
				break
			}
		}
	}
	if err := result.Validate(); err != nil {
		return ir.Response{}, fmt.Errorf("invalid translated response: %w", err)
	}
	return result, nil
}

func FromIR(response ir.Response, requestedModel, responseID string, created time.Time, parallel bool) (Response, error) {
	if err := response.Validate(); err != nil {
		return Response{}, err
	}
	result := Response{
		ID: responseID, Object: "response", CreatedAt: created.Unix(), Status: "completed",
		Model: requestedModel, Output: make([]OutputItem, 0), ParallelToolCalls: parallel, Store: false,
		Usage: Usage{
			InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens,
			TotalTokens:        response.Usage.InputTokens + response.Usage.OutputTokens,
			InputTokensDetails: &InputTokenDetails{CachedTokens: response.Usage.CachedInputTokens},
			OutputTokenDetails: &OutputTokenDetails{},
		},
	}
	message := OutputItem{Type: "message", ID: "msg_" + strings.TrimPrefix(responseID, "resp_"), Status: "completed", Role: "assistant"}
	callIndex := 0
	for index, content := range response.Content {
		switch content.Type {
		case ir.ContentText:
			message.Content = append(message.Content, OutputContent{Type: "output_text", Text: content.Text, Annotations: []json.RawMessage{}})
		case ir.ContentToolCall:
			if content.ToolCall == nil {
				return Response{}, fmt.Errorf("content[%d] has no tool_call payload", index)
			}
			callIndex++
			result.Output = append(result.Output, OutputItem{
				Type: "function_call", ID: fmt.Sprintf("fc_%s_%d", strings.TrimPrefix(responseID, "resp_"), callIndex),
				Status: "completed", CallID: content.ToolCall.ID, Name: content.ToolCall.Name, Arguments: string(content.ToolCall.Arguments),
			})
		case ir.ContentImage:
			return Response{}, fmt.Errorf("content[%d]: Responses text generation has no portable image output item", index)
		default:
			return Response{}, fmt.Errorf("content[%d] has unsupported type %q", index, content.Type)
		}
	}
	if len(message.Content) > 0 {
		result.Output = append([]OutputItem{message}, result.Output...)
	}
	switch response.FinishReason {
	case ir.FinishLength:
		result.Status = "incomplete"
		result.IncompleteDetails = &IncompleteDetails{Reason: "max_output_tokens"}
	case ir.FinishContent:
		result.Status = "incomplete"
		result.IncompleteDetails = &IncompleteDetails{Reason: "content_filter"}
	case ir.FinishStop, ir.FinishToolCalls:
	default:
		return Response{}, fmt.Errorf("unsupported finish reason %q", response.FinishReason)
	}
	return result, nil
}
