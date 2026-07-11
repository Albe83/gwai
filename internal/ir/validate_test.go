package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		Version: Version,
		ID:      "req_1",
		Route:   Route{ProviderID: "provider", UpstreamModel: "model"},
		Messages: []Message{{
			Role: RoleUser, Content: []Content{{Type: ContentText, Text: "hello"}},
		}},
		MaxOutputTokens: 10,
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
