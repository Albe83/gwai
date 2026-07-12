package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

type TranslationError struct {
	Field   string
	Message string
}

func (e *TranslationError) Error() string { return e.Message }

func invalid(field, format string, values ...any) error {
	return &TranslationError{Field: field, Message: fmt.Sprintf(format, values...)}
}

func rawPresent(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func validateInlineData(blob *Blob) (*ir.Image, error) {
	if blob == nil || blob.MIMEType == "" || blob.Data == "" {
		return nil, fmt.Errorf("inlineData requires mimeType and data")
	}
	switch blob.MIMEType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return nil, fmt.Errorf("unsupported inline image media type %q", blob.MIMEType)
	}
	if _, err := base64.StdEncoding.DecodeString(blob.Data); err != nil {
		return nil, fmt.Errorf("inlineData contains invalid base64: %w", err)
	}
	return &ir.Image{MediaType: blob.MIMEType, Data: blob.Data}, nil
}

func partVariantCount(part Part) int {
	count := 0
	if part.Text != nil {
		count++
	}
	if part.InlineData != nil {
		count++
	}
	if part.FileData != nil {
		count++
	}
	if part.FunctionCall != nil {
		count++
	}
	if part.FunctionResponse != nil {
		count++
	}
	if rawPresent(part.ExecutableCode) {
		count++
	}
	if rawPresent(part.CodeExecution) {
		count++
	}
	return count
}

func validateGenerationConfig(config *GenerationConfig) error {
	if config == nil {
		return nil
	}
	if config.MaxOutputTokens != nil && *config.MaxOutputTokens <= 0 {
		return invalid("generationConfig.maxOutputTokens", "maxOutputTokens must be positive")
	}
	if config.Temperature != nil && (*config.Temperature < 0 || *config.Temperature > 1) {
		return invalid("generationConfig.temperature", "temperature must be between 0 and 1 for the portable gateway contract")
	}
	if config.TopP != nil && (*config.TopP < 0 || *config.TopP > 1) {
		return invalid("generationConfig.topP", "topP must be between 0 and 1")
	}
	if config.CandidateCount != nil && *config.CandidateCount != 1 {
		return invalid("generationConfig.candidateCount", "only candidateCount=1 is supported")
	}
	if config.ResponseMIMEType != "" || rawPresent(config.ResponseSchema) || rawPresent(config.ResponseJSONSchema) {
		return invalid("generationConfig.responseSchema", "structured output is not supported by this route")
	}
	if config.ResponseModalities != nil {
		return invalid("generationConfig.responseModalities", "responseModalities is not supported by this route")
	}
	if rawPresent(config.ThinkingConfig) {
		return invalid("generationConfig.thinkingConfig", "thinking configuration is not supported by this route")
	}
	if config.TopK != nil || config.Seed != nil || config.PresencePenalty != nil || config.FrequencyPenalty != nil || config.ResponseLogprobs != nil || config.Logprobs != nil || config.MediaResolution != "" {
		return invalid("generationConfig", "the requested generation option is not in the portable subset")
	}
	for index, stop := range config.StopSequences {
		if stop == "" {
			return invalid(fmt.Sprintf("generationConfig.stopSequences.%d", index), "stop sequences must not be empty")
		}
	}
	return nil
}

func ToIRRequest(request GenerateContentRequest, requestedModel string, route controlplane.Route, requestID string) (ir.Request, error) {
	if strings.TrimSpace(requestedModel) == "" {
		return ir.Request{}, invalid("model", "model is required")
	}
	if len(request.Contents) == 0 {
		return ir.Request{}, invalid("contents", "contents must contain at least one item")
	}
	if request.SafetySettings != nil {
		return ir.Request{}, invalid("safetySettings", "safety settings are not supported by this route")
	}
	if request.CachedContent != "" {
		return ir.Request{}, invalid("cachedContent", "cached content is not supported by this route")
	}
	if request.Labels != nil {
		return ir.Request{}, invalid("labels", "labels cannot be preserved by this route")
	}
	if err := validateGenerationConfig(request.GenerationConfig); err != nil {
		return ir.Request{}, err
	}

	result := ir.Request{
		Version: ir.Version,
		ID:      requestID,
		Route:   ir.Route{ProviderID: route.ProviderID, UpstreamModel: route.UpstreamModel},
	}
	if request.GenerationConfig != nil {
		result.MaxOutputTokens = request.GenerationConfig.MaxOutputTokens
		result.Temperature = request.GenerationConfig.Temperature
		result.TopP = request.GenerationConfig.TopP
		result.Stop = append([]string(nil), request.GenerationConfig.StopSequences...)
	}
	if request.SystemInstruction != nil {
		if request.SystemInstruction.Role != "" && request.SystemInstruction.Role != "system" {
			return ir.Request{}, invalid("systemInstruction.role", "systemInstruction role must be omitted or system")
		}
		if len(request.SystemInstruction.Parts) == 0 {
			return ir.Request{}, invalid("systemInstruction.parts", "systemInstruction must contain text")
		}
		message := ir.Message{Role: ir.RoleSystem}
		for index, part := range request.SystemInstruction.Parts {
			if partVariantCount(part) != 1 || part.Text == nil || part.ThoughtSignature != "" || part.Thought != nil || rawPresent(part.VideoMetadata) {
				return ir.Request{}, invalid(fmt.Sprintf("systemInstruction.parts.%d", index), "systemInstruction only supports text parts")
			}
			message.Content = append(message.Content, ir.Content{Type: ir.ContentText, Text: *part.Text})
		}
		result.Messages = append(result.Messages, message)
	}

	pending := make(map[string][]string)
	for messageIndex, content := range request.Contents {
		role := content.Role
		if role == "" {
			role = "user"
		}
		if role != "user" && role != "model" {
			return ir.Request{}, invalid(fmt.Sprintf("contents.%d.role", messageIndex), "role must be user or model")
		}
		if len(content.Parts) == 0 {
			return ir.Request{}, invalid(fmt.Sprintf("contents.%d.parts", messageIndex), "content must contain at least one part")
		}
		containsResponse := false
		for _, part := range content.Parts {
			containsResponse = containsResponse || part.FunctionResponse != nil
		}
		if containsResponse && role != "user" {
			return ir.Request{}, invalid(fmt.Sprintf("contents.%d.role", messageIndex), "function responses require user role")
		}
		message := ir.Message{Role: ir.RoleUser}
		if role == "model" {
			message.Role = ir.RoleAssistant
		} else if containsResponse {
			message.Role = ir.RoleTool
		}
		for partIndex, part := range content.Parts {
			field := fmt.Sprintf("contents.%d.parts.%d", messageIndex, partIndex)
			if partVariantCount(part) != 1 {
				return ir.Request{}, invalid(field, "each part must contain exactly one supported data variant")
			}
			if part.Thought != nil {
				return ir.Request{}, invalid(field+".thought", "thought content is not supported by this route")
			}
			if rawPresent(part.VideoMetadata) {
				return ir.Request{}, invalid(field+".videoMetadata", "video metadata is not supported by this route")
			}
			if part.ThoughtSignature != "" && part.FunctionCall == nil {
				return ir.Request{}, invalid(field+".thoughtSignature", "thoughtSignature is only valid on functionCall parts")
			}
			switch {
			case part.Text != nil:
				if containsResponse {
					return ir.Request{}, invalid(field, "functionResponse cannot be mixed with other part types")
				}
				message.Content = append(message.Content, ir.Content{Type: ir.ContentText, Text: *part.Text})
			case part.InlineData != nil:
				if containsResponse {
					return ir.Request{}, invalid(field, "functionResponse cannot be mixed with other part types")
				}
				image, err := validateInlineData(part.InlineData)
				if err != nil {
					return ir.Request{}, invalid(field+".inlineData", "%v", err)
				}
				message.Content = append(message.Content, ir.Content{Type: ir.ContentImage, Image: image})
			case part.FileData != nil:
				return ir.Request{}, invalid(field+".fileData", "fileData and arbitrary image URLs are not supported by this route; use inlineData")
			case part.FunctionCall != nil:
				if role != "model" || containsResponse {
					return ir.Request{}, invalid(field+".functionCall", "functionCall requires model role")
				}
				call := part.FunctionCall
				if strings.TrimSpace(call.Name) == "" {
					return ir.Request{}, invalid(field+".functionCall.name", "function call name is required")
				}
				arguments := call.Args
				if !rawPresent(arguments) {
					arguments = json.RawMessage(`{}`)
				}
				if !jsonObject(arguments) {
					return ir.Request{}, invalid(field+".functionCall.args", "function call args must be an object")
				}
				callID := call.ID
				if callID == "" {
					callID = fmt.Sprintf("%s-call-%d-%d", requestID, messageIndex, partIndex)
				}
				pending[call.Name] = append(pending[call.Name], callID)
				message.Content = append(message.Content, ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{
					ID: callID, Name: call.Name, Arguments: arguments, Signature: part.ThoughtSignature,
				}})
			case part.FunctionResponse != nil:
				response := part.FunctionResponse
				if strings.TrimSpace(response.Name) == "" || !jsonObject(response.Response) {
					return ir.Request{}, invalid(field+".functionResponse", "functionResponse requires a name and object response")
				}
				if response.WillContinue != nil || response.Scheduling != "" {
					return ir.Request{}, invalid(field+".functionResponse", "streaming function responses are not supported")
				}
				callID := response.ID
				if callID == "" {
					calls := pending[response.Name]
					if len(calls) == 0 {
						return ir.Request{}, invalid(field+".functionResponse.name", "functionResponse has no preceding functionCall named %q", response.Name)
					}
					callID = calls[0]
					pending[response.Name] = calls[1:]
				} else {
					calls := pending[response.Name]
					matched := false
					for index, pendingID := range calls {
						if pendingID == callID {
							pending[response.Name] = append(calls[:index], calls[index+1:]...)
							matched = true
							break
						}
					}
					if !matched {
						return ir.Request{}, invalid(field+".functionResponse.id", "functionResponse id %q does not match a preceding functionCall named %q", callID, response.Name)
					}
				}
				message.Content = append(message.Content, ir.Content{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{
					ToolCallID: callID, Name: response.Name, Result: response.Response,
				}})
			default:
				return ir.Request{}, invalid(field, "the part type is not in the portable subset")
			}
		}
		result.Messages = append(result.Messages, message)
	}

	toolNames := make(map[string]struct{})
	for toolIndex, tool := range request.Tools {
		if rawPresent(tool.GoogleSearch) || rawPresent(tool.CodeExecution) || rawPresent(tool.URLContext) || rawPresent(tool.EnterpriseWebSearch) {
			return ir.Request{}, invalid(fmt.Sprintf("tools.%d", toolIndex), "hosted provider tools are not supported by this route")
		}
		if len(tool.FunctionDeclarations) == 0 {
			return ir.Request{}, invalid(fmt.Sprintf("tools.%d.functionDeclarations", toolIndex), "functionDeclarations must not be empty")
		}
		for declarationIndex, declaration := range tool.FunctionDeclarations {
			field := fmt.Sprintf("tools.%d.functionDeclarations.%d", toolIndex, declarationIndex)
			if declaration.Name == "" {
				return ir.Request{}, invalid(field+".name", "function name is required")
			}
			if _, exists := toolNames[declaration.Name]; exists {
				return ir.Request{}, invalid(field+".name", "function names must be unique")
			}
			if rawPresent(declaration.ParametersJSONSchema) || rawPresent(declaration.Response) || rawPresent(declaration.ResponseJSONSchema) || declaration.Behavior != "" {
				return ir.Request{}, invalid(field, "the function declaration uses unsupported non-portable fields")
			}
			parameters := declaration.Parameters
			if !rawPresent(parameters) {
				parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			if !jsonObject(parameters) {
				return ir.Request{}, invalid(field+".parameters", "parameters must be a JSON object")
			}
			toolNames[declaration.Name] = struct{}{}
			result.Tools = append(result.Tools, ir.Tool{Name: declaration.Name, Description: declaration.Description, InputSchema: parameters})
		}
	}
	if request.ToolConfig != nil {
		if rawPresent(request.ToolConfig.RetrievalConfig) {
			return ir.Request{}, invalid("toolConfig.retrievalConfig", "retrieval configuration is not supported")
		}
		config := request.ToolConfig.FunctionCallingConfig
		if config != nil {
			if len(result.Tools) == 0 {
				return ir.Request{}, invalid("toolConfig.functionCallingConfig", "function calling configuration requires function declarations")
			}
			switch strings.ToUpper(config.Mode) {
			case "", "AUTO":
				if len(config.AllowedFunctionNames) > 0 {
					return ir.Request{}, invalid("toolConfig.functionCallingConfig.allowedFunctionNames", "AUTO with an allow-list cannot be represented by the portable subset")
				}
				result.ToolChoice = &ir.ToolChoice{Mode: "auto"}
			case "NONE":
				if len(config.AllowedFunctionNames) > 0 {
					return ir.Request{}, invalid("toolConfig.functionCallingConfig.allowedFunctionNames", "NONE cannot include allowed function names")
				}
				result.ToolChoice = &ir.ToolChoice{Mode: "none"}
			case "ANY":
				switch len(config.AllowedFunctionNames) {
				case 0:
					result.ToolChoice = &ir.ToolChoice{Mode: "required"}
				case 1:
					if _, exists := toolNames[config.AllowedFunctionNames[0]]; !exists {
						return ir.Request{}, invalid("toolConfig.functionCallingConfig.allowedFunctionNames", "selected function is not declared")
					}
					result.ToolChoice = &ir.ToolChoice{Mode: "named", Name: config.AllowedFunctionNames[0]}
				default:
					return ir.Request{}, invalid("toolConfig.functionCallingConfig.allowedFunctionNames", "ANY supports zero or one allowed function in the portable subset")
				}
			default:
				return ir.Request{}, invalid("toolConfig.functionCallingConfig.mode", "unsupported function calling mode %q", config.Mode)
			}
		}
	}
	if err := result.Validate(); err != nil {
		return ir.Request{}, invalid("contents", "%v", err)
	}
	return result, nil
}

func appendContent(contents *[]Content, role string, parts []Part) {
	if len(*contents) > 0 && (*contents)[len(*contents)-1].Role == role {
		(*contents)[len(*contents)-1].Parts = append((*contents)[len(*contents)-1].Parts, parts...)
		return
	}
	*contents = append(*contents, Content{Role: role, Parts: parts})
}

func contentToProvider(content ir.Content, callNames map[string]string, upstreamModel string) (Part, error) {
	switch content.Type {
	case ir.ContentText:
		text := content.Text
		return Part{Text: &text}, nil
	case ir.ContentImage:
		if content.Image == nil {
			return Part{}, fmt.Errorf("image content has no payload")
		}
		if content.Image.URL != "" {
			return Part{}, fmt.Errorf("arbitrary image URLs cannot be sent to Gemini; use inline base64 image data")
		}
		image, err := validateInlineData(&Blob{MIMEType: content.Image.MediaType, Data: content.Image.Data})
		if err != nil {
			return Part{}, err
		}
		return Part{InlineData: &Blob{MIMEType: image.MediaType, Data: image.Data}}, nil
	case ir.ContentToolCall:
		if content.ToolCall == nil || !jsonObject(content.ToolCall.Arguments) {
			return Part{}, fmt.Errorf("tool_call requires an object arguments payload")
		}
		callNames[content.ToolCall.ID] = content.ToolCall.Name
		signature := content.ToolCall.Signature
		model := strings.TrimPrefix(strings.ToLower(upstreamModel), "models/")
		if signature == "" && strings.HasPrefix(model, "gemini-3") {
			signature = SkipThoughtSignatureValidator
		}
		return Part{FunctionCall: &FunctionCall{ID: content.ToolCall.ID, Name: content.ToolCall.Name, Args: content.ToolCall.Arguments}, ThoughtSignature: signature}, nil
	case ir.ContentToolResult:
		if content.ToolResult == nil {
			return Part{}, fmt.Errorf("tool_result content has no payload")
		}
		name := content.ToolResult.Name
		if name == "" {
			name = callNames[content.ToolResult.ToolCallID]
		}
		if name == "" {
			return Part{}, fmt.Errorf("tool_result %q has no function name and does not match a prior tool call", content.ToolResult.ToolCallID)
		}
		response := content.ToolResult.Result
		if !rawPresent(response) {
			var output strings.Builder
			for _, nested := range content.ToolResult.Content {
				if nested.Type != ir.ContentText {
					return Part{}, fmt.Errorf("Gemini tool results only support an object result or text content")
				}
				output.WriteString(nested.Text)
			}
			encoded, err := json.Marshal(map[string]any{"output": output.String(), "is_error": content.ToolResult.IsError})
			if err != nil {
				return Part{}, err
			}
			response = encoded
		}
		if !jsonObject(response) {
			return Part{}, fmt.Errorf("tool_result result must be a JSON object")
		}
		return Part{FunctionResponse: &FunctionResponse{ID: content.ToolResult.ToolCallID, Name: name, Response: response}}, nil
	default:
		return Part{}, fmt.Errorf("unsupported IR content type %q", content.Type)
	}
}

func ToProviderRequest(request ir.Request, defaultMaxOutputTokens, maxOutputTokens int) (GenerateContentRequest, error) {
	if err := request.Validate(); err != nil {
		return GenerateContentRequest{}, err
	}
	if request.Stream {
		return GenerateContentRequest{}, fmt.Errorf("streaming IR requests are not supported")
	}
	result := GenerateContentRequest{}
	config := &GenerationConfig{Temperature: request.Temperature, TopP: request.TopP, StopSequences: append([]string(nil), request.Stop...)}
	if request.MaxOutputTokens != nil {
		value := *request.MaxOutputTokens
		config.MaxOutputTokens = &value
	} else if defaultMaxOutputTokens > 0 {
		value := defaultMaxOutputTokens
		config.MaxOutputTokens = &value
	}
	if config.MaxOutputTokens != nil && maxOutputTokens > 0 && *config.MaxOutputTokens > maxOutputTokens {
		return GenerateContentRequest{}, fmt.Errorf("max_output_tokens must not exceed %d", maxOutputTokens)
	}
	if err := validateGenerationConfig(config); err != nil {
		return GenerateContentRequest{}, err
	}
	if config.MaxOutputTokens != nil || config.Temperature != nil || config.TopP != nil || len(config.StopSequences) > 0 {
		result.GenerationConfig = config
	}
	callNames := make(map[string]string)
	for messageIndex, message := range request.Messages {
		parts := make([]Part, 0, len(message.Content))
		for _, item := range message.Content {
			part, err := contentToProvider(item, callNames, request.Route.UpstreamModel)
			if err != nil {
				return GenerateContentRequest{}, fmt.Errorf("messages[%d]: %w", messageIndex, err)
			}
			parts = append(parts, part)
		}
		switch message.Role {
		case ir.RoleSystem:
			if result.SystemInstruction == nil {
				result.SystemInstruction = &Content{Parts: parts}
			} else {
				result.SystemInstruction.Parts = append(result.SystemInstruction.Parts, parts...)
			}
		case ir.RoleUser, ir.RoleTool:
			appendContent(&result.Contents, "user", parts)
		case ir.RoleAssistant:
			appendContent(&result.Contents, "model", parts)
		default:
			return GenerateContentRequest{}, fmt.Errorf("unsupported IR message role %q", message.Role)
		}
	}
	if len(result.Contents) == 0 {
		return GenerateContentRequest{}, fmt.Errorf("at least one non-system message is required")
	}
	if len(request.Tools) > 0 {
		declarations := make([]FunctionDeclaration, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.Strict != nil && *tool.Strict {
				return GenerateContentRequest{}, fmt.Errorf("strict function schemas are not supported by Gemini")
			}
			declarations = append(declarations, FunctionDeclaration{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
		}
		result.Tools = []Tool{{FunctionDeclarations: declarations}}
	}
	if request.ToolChoice != nil {
		functionConfig := &FunctionCallingConfig{}
		switch request.ToolChoice.Mode {
		case "auto":
			functionConfig.Mode = "AUTO"
		case "none":
			functionConfig.Mode = "NONE"
		case "required":
			functionConfig.Mode = "ANY"
		case "named":
			functionConfig.Mode = "ANY"
			functionConfig.AllowedFunctionNames = []string{request.ToolChoice.Name}
		default:
			return GenerateContentRequest{}, fmt.Errorf("unsupported tool choice mode %q", request.ToolChoice.Mode)
		}
		if request.ToolChoice.DisableParallel {
			return GenerateContentRequest{}, fmt.Errorf("disable_parallel cannot be represented by Gemini")
		}
		result.ToolConfig = &ToolConfig{FunctionCallingConfig: functionConfig}
	}
	return result, nil
}

func finishReasonToIR(reason string, hasToolCalls bool) (string, error) {
	switch reason {
	case "", "STOP":
		if hasToolCalls {
			return ir.FinishToolCalls, nil
		}
		return ir.FinishStop, nil
	case "MAX_TOKENS":
		return ir.FinishLength, nil
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return ir.FinishContent, nil
	case "OTHER", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "NO_IMAGE":
		return "", fmt.Errorf("Gemini stopped with unsupported finish reason %q", reason)
	default:
		return "", fmt.Errorf("Gemini returned unknown finish reason %q", reason)
	}
}

func ToIRResponse(response GenerateContentResponse, request ir.Request) (ir.Response, error) {
	if len(response.Candidates) > 1 {
		return ir.Response{}, fmt.Errorf("Gemini returned %d candidates; exactly one is supported", len(response.Candidates))
	}
	model := response.ModelVersion
	if model == "" {
		model = request.Route.UpstreamModel
	}
	result := ir.Response{
		Version: ir.Version,
		ID:      request.ID, Model: model, ProviderResponseID: response.ResponseID,
		Usage: ir.Usage{
			InputTokens:       response.UsageMetadata.PromptTokenCount,
			OutputTokens:      response.UsageMetadata.CandidatesTokenCount + response.UsageMetadata.ThoughtsTokenCount,
			CachedInputTokens: response.UsageMetadata.CachedContentTokenCount,
		},
	}
	if len(response.Candidates) == 0 {
		if response.PromptFeedback == nil || response.PromptFeedback.BlockReason == "" {
			return ir.Response{}, fmt.Errorf("Gemini returned no candidates")
		}
		result.FinishReason = ir.FinishContent
		return result, nil
	}
	candidate := response.Candidates[0]
	if candidate.Content.Role != "" && candidate.Content.Role != "model" {
		return ir.Response{}, fmt.Errorf("Gemini candidate role must be model")
	}
	hasToolCalls := false
	for partIndex, part := range candidate.Content.Parts {
		if partVariantCount(part) != 1 {
			return ir.Response{}, fmt.Errorf("candidate part[%d] must contain exactly one supported data variant", partIndex)
		}
		if part.ThoughtSignature != "" && part.FunctionCall == nil {
			return ir.Response{}, fmt.Errorf("candidate part[%d] has a thoughtSignature that cannot be represented outside a function call", partIndex)
		}
		if part.Thought != nil && *part.Thought {
			continue
		}
		switch {
		case part.Text != nil:
			result.Content = append(result.Content, ir.Content{Type: ir.ContentText, Text: *part.Text})
		case part.InlineData != nil:
			image, err := validateInlineData(part.InlineData)
			if err != nil {
				return ir.Response{}, fmt.Errorf("candidate part[%d]: %w", partIndex, err)
			}
			result.Content = append(result.Content, ir.Content{Type: ir.ContentImage, Image: image})
		case part.FunctionCall != nil:
			call := part.FunctionCall
			if call.Name == "" {
				return ir.Response{}, fmt.Errorf("candidate part[%d] has a function call without a name", partIndex)
			}
			arguments := call.Args
			if !rawPresent(arguments) {
				arguments = json.RawMessage(`{}`)
			}
			if !jsonObject(arguments) {
				return ir.Response{}, fmt.Errorf("candidate part[%d] function args must be an object", partIndex)
			}
			callID := call.ID
			if callID == "" {
				callID = fmt.Sprintf("%s-call-%d", request.ID, partIndex)
			}
			result.Content = append(result.Content, ir.Content{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: callID, Name: call.Name, Arguments: arguments, Signature: part.ThoughtSignature}})
			hasToolCalls = true
		default:
			return ir.Response{}, fmt.Errorf("candidate part[%d] has an unsupported part type", partIndex)
		}
	}
	finish, err := finishReasonToIR(candidate.FinishReason, hasToolCalls)
	if err != nil {
		return ir.Response{}, err
	}
	result.FinishReason = finish
	if err := result.Validate(); err != nil {
		return ir.Response{}, err
	}
	return result, nil
}

func FromIRResponse(response ir.Response, requestedModel string) (GenerateContentResponse, error) {
	if err := response.Validate(); err != nil {
		return GenerateContentResponse{}, err
	}
	if strings.TrimSpace(requestedModel) == "" {
		return GenerateContentResponse{}, fmt.Errorf("requested model is required")
	}
	result := GenerateContentResponse{
		ModelVersion: requestedModel,
		ResponseID:   response.ProviderResponseID,
		UsageMetadata: UsageMetadata{
			PromptTokenCount: response.Usage.InputTokens, CandidatesTokenCount: response.Usage.OutputTokens,
			TotalTokenCount:         response.Usage.InputTokens + response.Usage.OutputTokens,
			CachedContentTokenCount: response.Usage.CachedInputTokens,
		},
	}
	if result.ResponseID == "" {
		result.ResponseID = response.ID
	}
	candidate := Candidate{Content: Content{Role: "model"}}
	for index, content := range response.Content {
		part, err := contentToProvider(content, make(map[string]string), response.Model)
		if err != nil {
			return GenerateContentResponse{}, fmt.Errorf("response content[%d]: %w", index, err)
		}
		candidate.Content.Parts = append(candidate.Content.Parts, part)
	}
	switch response.FinishReason {
	case ir.FinishStop, ir.FinishToolCalls:
		candidate.FinishReason = "STOP"
	case ir.FinishLength:
		candidate.FinishReason = "MAX_TOKENS"
	case ir.FinishContent:
		candidate.FinishReason = "SAFETY"
	default:
		return GenerateContentResponse{}, fmt.Errorf("unsupported IR finish reason %q", response.FinishReason)
	}
	result.Candidates = []Candidate{candidate}
	return result, nil
}
