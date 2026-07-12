package gemini

import "encoding/json"

const SkipThoughtSignatureValidator = "skip_thought_signature_validator"

type GenerateContentRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []json.RawMessage `json:"safetySettings,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

type Part struct {
	Text             *string           `json:"text,omitempty"`
	InlineData       *Blob             `json:"inlineData,omitempty"`
	FileData         *FileData         `json:"fileData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	ExecutableCode   json.RawMessage   `json:"executableCode,omitempty"`
	CodeExecution    json.RawMessage   `json:"codeExecutionResult,omitempty"`
	VideoMetadata    json.RawMessage   `json:"videoMetadata,omitempty"`
	Thought          *bool             `json:"thought,omitempty"`
}

type Blob struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type FileData struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type FunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type FunctionResponse struct {
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name"`
	Response     json.RawMessage `json:"response"`
	WillContinue *bool           `json:"willContinue,omitempty"`
	Scheduling   string          `json:"scheduling,omitempty"`
}

type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	GoogleSearch         json.RawMessage       `json:"googleSearch,omitempty"`
	CodeExecution        json.RawMessage       `json:"codeExecution,omitempty"`
	URLContext           json.RawMessage       `json:"urlContext,omitempty"`
	EnterpriseWebSearch  json.RawMessage       `json:"enterpriseWebSearch,omitempty"`
}

type FunctionDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
	Response             json.RawMessage `json:"response,omitempty"`
	ResponseJSONSchema   json.RawMessage `json:"responseJsonSchema,omitempty"`
	Behavior             string          `json:"behavior,omitempty"`
}

type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	RetrievalConfig       json.RawMessage        `json:"retrievalConfig,omitempty"`
}

type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type GenerationConfig struct {
	StopSequences      []string        `json:"stopSequences,omitempty"`
	MaxOutputTokens    *int            `json:"maxOutputTokens,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	TopP               *float64        `json:"topP,omitempty"`
	CandidateCount     *int            `json:"candidateCount,omitempty"`
	ResponseMIMEType   string          `json:"responseMimeType,omitempty"`
	ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
	ResponseModalities []string        `json:"responseModalities,omitempty"`
	ThinkingConfig     json.RawMessage `json:"thinkingConfig,omitempty"`
	TopK               *float64        `json:"topK,omitempty"`
	Seed               *int64          `json:"seed,omitempty"`
	PresencePenalty    *float64        `json:"presencePenalty,omitempty"`
	FrequencyPenalty   *float64        `json:"frequencyPenalty,omitempty"`
	ResponseLogprobs   *bool           `json:"responseLogprobs,omitempty"`
	Logprobs           *int            `json:"logprobs,omitempty"`
	MediaResolution    string          `json:"mediaResolution,omitempty"`
}

type GenerateContentResponse struct {
	Candidates     []Candidate     `json:"candidates,omitempty"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  UsageMetadata   `json:"usageMetadata,omitempty"`
	ModelVersion   string          `json:"modelVersion,omitempty"`
	ResponseID     string          `json:"responseId,omitempty"`
}

type Candidate struct {
	Content       Content           `json:"content"`
	FinishReason  string            `json:"finishReason,omitempty"`
	Index         int               `json:"index,omitempty"`
	SafetyRatings []json.RawMessage `json:"safetyRatings,omitempty"`
}

type PromptFeedback struct {
	BlockReason   string            `json:"blockReason,omitempty"`
	SafetyRatings []json.RawMessage `json:"safetyRatings,omitempty"`
}

type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Status  string            `json:"status"`
	Details []json.RawMessage `json:"details,omitempty"`
}
