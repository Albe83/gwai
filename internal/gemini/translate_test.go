package gemini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

func textPart(value string) Part { return Part{Text: &value} }

func TestToIRRequestPreservesPortableGeminiContent(t *testing.T) {
	request := GenerateContentRequest{
		SystemInstruction: &Content{Parts: []Part{textPart("be concise")}},
		Contents: []Content{
			{Role: "user", Parts: []Part{textPart("inspect"), {InlineData: &Blob{MIMEType: "image/png", Data: "AQ=="}}}},
			{Role: "model", Parts: []Part{{FunctionCall: &FunctionCall{Name: "lookup", Args: json.RawMessage(`{"city":"Rome"}`)}, ThoughtSignature: "signed-thought"}}},
			{Role: "user", Parts: []Part{{FunctionResponse: &FunctionResponse{Name: "lookup", Response: json.RawMessage(`{"temperature":27}`)}}}},
		},
		Tools: []Tool{{FunctionDeclarations: []FunctionDeclaration{{
			Name: "lookup", Description: "weather lookup", Parameters: json.RawMessage(`{"type":"object"}`),
		}}}},
		ToolConfig: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: "ANY", AllowedFunctionNames: []string{"lookup"}}},
		GenerationConfig: &GenerationConfig{
			MaxOutputTokens: intPointer(512), Temperature: floatPointer(0.4), TopP: floatPointer(0.8), StopSequences: []string{"done"},
		},
	}
	route := controlplane.Route{ProviderID: "prv_gemini", UpstreamModel: "gemini-3-flash"}

	got, err := ToIRRequest(request, "google/gemini-3-flash", route, "req_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 || got.Messages[0].Role != ir.RoleSystem || got.Messages[2].Role != ir.RoleAssistant || got.Messages[3].Role != ir.RoleTool {
		t.Fatalf("unexpected messages: %#v", got.Messages)
	}
	call := got.Messages[2].Content[0].ToolCall
	if call == nil || call.ID != "req_1-call-1-0" || call.Signature != "signed-thought" {
		t.Fatalf("unexpected tool call: %#v", call)
	}
	result := got.Messages[3].Content[0].ToolResult
	if result == nil || result.ToolCallID != call.ID || result.Name != "lookup" || string(result.Result) != `{"temperature":27}` {
		t.Fatalf("unexpected tool result: %#v", result)
	}
	if got.ToolChoice == nil || got.ToolChoice.Mode != "named" || got.ToolChoice.Name != "lookup" {
		t.Fatalf("unexpected tool choice: %#v", got.ToolChoice)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 512 || len(got.Stop) != 1 {
		t.Fatalf("generation config was not preserved: %#v", got)
	}
}

func TestToProviderRequestCoalescesRolesAndResolvesToolName(t *testing.T) {
	request := ir.Request{
		Version: ir.Version,
		ID:      "req_2",
		Route:   ir.Route{ProviderID: "prv_gemini", UpstreamModel: "gemini-3-flash"},
		Messages: []ir.Message{
			{Role: ir.RoleSystem, Content: []ir.Content{{Type: ir.ContentText, Text: "policy"}}},
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "one"}}},
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "two"}}},
			{Role: ir.RoleAssistant, Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "call_1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}}}},
			{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{ToolCallID: "call_1", Result: json.RawMessage(`{"answer":1}`)}}}},
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "continue"}}},
		},
		Tools: []ir.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}

	got, err := ToProviderRequest(request, 128, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.SystemInstruction == nil || len(got.Contents) != 3 {
		t.Fatalf("expected coalesced user/model/user contents, got %#v", got.Contents)
	}
	if got.Contents[0].Role != "user" || len(got.Contents[0].Parts) != 2 {
		t.Fatalf("adjacent user messages were not coalesced: %#v", got.Contents[0])
	}
	callPart := got.Contents[1].Parts[0]
	if callPart.ThoughtSignature != SkipThoughtSignatureValidator {
		t.Fatalf("missing Gemini 3 signature fallback: %#v", callPart)
	}
	if got.Contents[2].Role != "user" || len(got.Contents[2].Parts) != 2 {
		t.Fatalf("tool/user contents were not coalesced: %#v", got.Contents[2])
	}
	functionResponse := got.Contents[2].Parts[0].FunctionResponse
	if functionResponse == nil || functionResponse.Name != "lookup" || functionResponse.ID != "call_1" {
		t.Fatalf("tool name was not resolved from prior call: %#v", functionResponse)
	}
}

func TestToProviderRequestRejectsArbitraryImageURL(t *testing.T) {
	request := ir.Request{
		Version: ir.Version, ID: "req_3", Route: ir.Route{ProviderID: "p", UpstreamModel: "gemini-2.5-flash"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentImage, Image: &ir.Image{URL: "https://example.test/image.png"}}}}},
	}
	_, err := ToProviderRequest(request, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "arbitrary image URLs") {
		t.Fatalf("expected image URL rejection, got %v", err)
	}
}

func TestToIRRequestRejectsNonPortableFeatures(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*GenerateContentRequest)
		message string
	}{
		{"safety", func(r *GenerateContentRequest) { r.SafetySettings = []json.RawMessage{} }, "safety"},
		{"structured output", func(r *GenerateContentRequest) {
			r.GenerationConfig = &GenerationConfig{ResponseMIMEType: "application/json"}
		}, "structured output"},
		{"thinking", func(r *GenerateContentRequest) {
			r.GenerationConfig = &GenerationConfig{ThinkingConfig: json.RawMessage(`{}`)}
		}, "thinking"},
		{"hosted tool", func(r *GenerateContentRequest) { r.Tools = []Tool{{GoogleSearch: json.RawMessage(`{}`)}} }, "hosted provider tools"},
		{"file data", func(r *GenerateContentRequest) {
			r.Contents[0].Parts = []Part{{FileData: &FileData{FileURI: "https://example.test/a.png"}}}
		}, "fileData"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := GenerateContentRequest{Contents: []Content{{Role: "user", Parts: []Part{textPart("hello")}}}}
			test.mutate(&request)
			_, err := ToIRRequest(request, "google/gemini", controlplane.Route{ProviderID: "p", UpstreamModel: "gemini"}, "req")
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected error containing %q, got %v", test.message, err)
			}
		})
	}
}

func TestGeminiResponseRoundTripPreservesThoughtSignatureAndUsage(t *testing.T) {
	text := "calling"
	providerResponse := GenerateContentResponse{
		ResponseID: "provider-response", ModelVersion: "gemini-3-flash",
		Candidates: []Candidate{{Content: Content{Role: "model", Parts: []Part{
			{Text: &text},
			{FunctionCall: &FunctionCall{ID: "fc_1", Name: "lookup", Args: json.RawMessage(`{"city":"Rome"}`)}, ThoughtSignature: "signature-1"},
		}}, FinishReason: "STOP"}},
		UsageMetadata: UsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 4, ThoughtsTokenCount: 2, TotalTokenCount: 16, CachedContentTokenCount: 3},
	}
	request := ir.Request{Version: ir.Version, ID: "req_4", Route: ir.Route{ProviderID: "p", UpstreamModel: "gemini-3-flash"}, Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hello"}}}}}

	internal, err := ToIRResponse(providerResponse, request)
	if err != nil {
		t.Fatal(err)
	}
	if internal.FinishReason != ir.FinishToolCalls || internal.Usage.InputTokens != 10 || internal.Usage.OutputTokens != 6 || internal.Usage.CachedInputTokens != 3 {
		t.Fatalf("unexpected IR response: %#v", internal)
	}
	if internal.Content[1].ToolCall.Signature != "signature-1" {
		t.Fatalf("thought signature not preserved: %#v", internal.Content[1])
	}
	wire, err := FromIRResponse(internal)
	if err != nil {
		t.Fatal(err)
	}
	if wire.Candidates[0].Content.Parts[1].ThoughtSignature != "signature-1" || wire.ResponseID != "provider-response" {
		t.Fatalf("wire response lost Gemini metadata: %#v", wire)
	}
}

func intPointer(value int) *int           { return &value }
func floatPointer(value float64) *float64 { return &value }
