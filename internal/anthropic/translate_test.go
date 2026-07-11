package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/Albe83/gwai/internal/ir"
)

func TestToMessageRequestMapsSystemAndToolMessages(t *testing.T) {
	strict := true
	request := ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "p", UpstreamModel: "claude-test"}, MaxOutputTokens: 100,
		Messages: []ir.Message{
			{Role: ir.RoleSystem, Content: []ir.Content{{Type: ir.ContentText, Text: "Be concise"}}},
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "Weather?"}}},
			{Role: ir.RoleAssistant, Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Rome"}`)}}}},
			{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{ToolCallID: "call_1", Content: []ir.Content{{Type: ir.ContentText, Text: "sunny"}}}}}},
		},
		Tools:      []ir.Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: &ir.ToolChoice{Mode: "required", DisableParallel: true},
	}
	result, err := ToMessageRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.System) != 1 || len(result.Messages) != 3 || result.Messages[2].Role != "user" {
		t.Fatalf("unexpected Anthropic messages: %+v", result)
	}
	if result.Messages[1].Content[0].Type != "tool_use" || result.Messages[2].Content[0].Type != "tool_result" {
		t.Fatalf("tool blocks not translated: %+v", result.Messages)
	}
	if result.ToolChoice == nil || result.ToolChoice.Type != "any" {
		t.Fatalf("unexpected tool choice: %+v", result.ToolChoice)
	}
	if result.Tools[0].Strict == nil || !*result.Tools[0].Strict || !result.ToolChoice.DisableParallelToolUse {
		t.Fatalf("strict/parallel controls were not preserved: tools=%+v choice=%+v", result.Tools, result.ToolChoice)
	}
}

func TestToIRResponseMapsStopReasonAndUsage(t *testing.T) {
	request := ir.Request{ID: "req_1", Route: ir.Route{UpstreamModel: "claude-test"}}
	result, err := ToIRResponse(MessageResponse{
		ID: "msg_1", StopReason: "tool_use", Usage: Usage{
			InputTokens: 12, OutputTokens: 5, CacheCreationInputTokens: 3, CacheReadInputTokens: 7,
		},
		Content: []ContentBlock{
			{Type: "thinking"},
			{Type: "tool_use", ID: "call_1", Name: "weather", Input: json.RawMessage(`{"city":"Rome"}`)},
		},
	}, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != ir.FinishToolCalls || result.Usage.InputTokens != 22 || result.Usage.CachedInputTokens != 7 || result.Content[0].ToolCall.Name != "weather" {
		t.Fatalf("unexpected IR response: %+v", result)
	}
}
