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
