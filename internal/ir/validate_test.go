package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRequest() Request {
	maxOutputTokens := 10
	return Request{
		Version: Version,
		ID:      "req_1",
		Route:   Route{ProviderID: "provider", UpstreamModel: "model"},
		Messages: []Message{{
			Role: RoleUser, Content: []Content{{Type: ContentText, Text: "hello"}},
		}},
		MaxOutputTokens: &maxOutputTokens,
	}
}

func TestRequestValidationAllowsAdapterDefaultOutputTokens(t *testing.T) {
	request := validRequest()
	request.MaxOutputTokens = nil
	if err := request.Validate(); err != nil {
		t.Fatalf("expected omitted max_output_tokens to be valid, got %v", err)
	}
}

func TestRequestValidationRejectsNonObjectToolSchema(t *testing.T) {
	request := validRequest()
	request.Tools = []Tool{{Name: "lookup", InputSchema: json.RawMessage(`[]`)}}
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("expected object schema error, got %v", err)
	}
}

func TestRequestValidationRejectsUnknownNamedTool(t *testing.T) {
	request := validRequest()
	request.Tools = []Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	request.ToolChoice = &ToolChoice{Mode: "named", Name: "missing"}
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("expected named tool error, got %v", err)
	}
}

func TestRequestValidationEnforcesPortableSamplingRange(t *testing.T) {
	request := validRequest()
	temperature := 1.1
	request.Temperature = &temperature
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "portable IR") {
		t.Fatalf("expected portable temperature error, got %v", err)
	}
}

func TestRequestValidationRejectsUnknownToolResultReference(t *testing.T) {
	request := validRequest()
	request.Messages = append(request.Messages, Message{Role: RoleTool, Content: []Content{{Type: ContentToolResult, ToolResult: &ToolResult{
		ToolCallID: "missing", Content: []Content{{Type: ContentText, Text: "result"}},
	}}}})
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "unknown prior tool_call") {
		t.Fatalf("expected tool result reference error, got %v", err)
	}
}

func TestContentValidationRejectsAmbiguousImageAndInvalidBase64(t *testing.T) {
	content := Content{Type: ContentImage, Image: &Image{URL: "https://example.test/image.png", MediaType: "image/png", Data: "aGk="}}
	if err := content.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected ambiguous image error, got %v", err)
	}
	content.Image = &Image{MediaType: "image/png", Data: "not base64"}
	if err := content.Validate(); err == nil || !strings.Contains(err.Error(), "valid base64") {
		t.Fatalf("expected base64 error, got %v", err)
	}
}

func TestRequestValidationRequiresLeadingSystemMessages(t *testing.T) {
	request := validRequest()
	request.Messages = append(request.Messages, Message{Role: RoleSystem, Content: []Content{{Type: ContentText, Text: "late"}}})
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "must precede") {
		t.Fatalf("expected late system message error, got %v", err)
	}
}

func TestRequestValidationEnforcesPortableRoleContentPairs(t *testing.T) {
	request := validRequest()
	request.Messages[0] = Message{Role: RoleTool, Content: []Content{{Type: ContentText, Text: "not a tool result"}}}
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected role/content error, got %v", err)
	}
}

func TestResponseValidationRejectsNegativeUsageAndToolResults(t *testing.T) {
	response := Response{
		Version: Version, ID: "req_1", Model: "model", FinishReason: FinishStop,
		Content: []Content{{Type: ContentToolResult, ToolResult: &ToolResult{ToolCallID: "call_1", Content: []Content{{Type: ContentText, Text: "x"}}}}},
		Usage:   Usage{InputTokens: -1},
	}
	if err := response.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported response type") {
		t.Fatalf("expected response content error, got %v", err)
	}
	response.Content = []Content{{Type: ContentText, Text: "ok"}}
	if err := response.Validate(); err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("expected negative usage error, got %v", err)
	}
}
