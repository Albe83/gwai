package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

func rawJSON(value string) json.RawMessage { return json.RawMessage(value) }

func TestToIRRequestMapsMessagesImagesToolsAndResults(t *testing.T) {
	strict := true
	request := ClientMessageRequest{
		Model: " team/claude ", MaxTokens: 512, System: rawJSON(`"Be concise"`),
		Temperature: floatPointer(0.3), TopP: floatPointer(0.9), StopSequences: []string{"DONE"},
		Messages: []ClientMessage{
			{Role: "user", Content: rawJSON(`[{"type":"text","text":"Weather?"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]`)},
			{Role: "assistant", Content: rawJSON(`[{"type":"tool_use","id":"call_1","name":"weather","input":{"city":"Rome"}}]`)},
			{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]`)},
		},
		Tools:      []Tool{{Name: "weather", Description: "Weather", InputSchema: rawJSON(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: &ToolChoice{Type: "tool", Name: "weather", DisableParallelToolUse: true},
	}
	result, err := ToIRRequest(request, controlplane.Route{ProviderID: "prv_1", UpstreamModel: "claude-sonnet"}, "req_1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route.ProviderID != "prv_1" || result.Route.UpstreamModel != "claude-sonnet" || *result.MaxOutputTokens != 512 {
		t.Fatalf("unexpected IR route or token limit: %+v", result)
	}
	if len(result.Messages) != 4 || result.Messages[0].Role != ir.RoleSystem || result.Messages[3].Role != ir.RoleTool {
		t.Fatalf("unexpected IR messages: %+v", result.Messages)
	}
	toolResult := result.Messages[3].Content[0].ToolResult
	if toolResult == nil || toolResult.Name != "weather" || toolResult.Content[0].Text != "sunny" {
		t.Fatalf("tool result was not linked to its prior tool use: %+v", toolResult)
	}
	if result.ToolChoice == nil || result.ToolChoice.Mode != "named" || !result.ToolChoice.DisableParallel {
		t.Fatalf("unexpected IR tool choice: %+v", result.ToolChoice)
	}
	if result.Messages[1].Content[1].Image.MediaType != "image/png" {
		t.Fatalf("image was not translated: %+v", result.Messages[1].Content[1])
	}
}

func TestToIRRequestRejectsNonPortableFeatures(t *testing.T) {
	base := ClientMessageRequest{
		Model: "claude", MaxTokens: 10,
		Messages: []ClientMessage{{Role: "user", Content: rawJSON(`"hello"`)}},
	}
	topK := 2
	tests := []struct {
		name     string
		mutate   func(*ClientMessageRequest)
		contains string
	}{
		{name: "stream", mutate: func(r *ClientMessageRequest) { r.Stream = true }, contains: "streaming"},
		{name: "thinking", mutate: func(r *ClientMessageRequest) { r.Thinking = rawJSON(`{"type":"enabled","budget_tokens":1024}`) }, contains: "thinking"},
		{name: "top k", mutate: func(r *ClientMessageRequest) { r.TopK = &topK }, contains: "top_k"},
		{name: "metadata", mutate: func(r *ClientMessageRequest) { r.Metadata = rawJSON(`{"user_id":"u"}`) }, contains: "metadata"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			_, err := ToIRRequest(request, controlplane.Route{ProviderID: "p", UpstreamModel: "m"}, "req")
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("expected %q error, got %v", test.contains, err)
			}
		})
	}
}

func TestToIRRequestRejectsUnknownToolResultAndThinkingBlock(t *testing.T) {
	base := ClientMessageRequest{Model: "claude", MaxTokens: 10}
	base.Messages = []ClientMessage{{Role: "user", Content: rawJSON(`[{"type":"tool_result","tool_use_id":"missing","content":"x"}]`)}}
	if _, err := ToIRRequest(base, controlplane.Route{ProviderID: "p", UpstreamModel: "m"}, "req"); err == nil || !strings.Contains(err.Error(), "unknown prior tool_use") {
		t.Fatalf("expected unknown tool result error, got %v", err)
	}
	base.Messages = []ClientMessage{{Role: "assistant", Content: rawJSON(`[{"type":"thinking","thinking":"secret"}]`)}}
	if _, err := ToIRRequest(base, controlplane.Route{ProviderID: "p", UpstreamModel: "m"}, "req"); err == nil || !strings.Contains(err.Error(), "thinking") {
		t.Fatalf("expected thinking block error, got %v", err)
	}
}

func TestFromIRResponseMapsToolUseStopAndCacheUsage(t *testing.T) {
	result, err := FromIRResponse(ir.Response{
		Version: ir.Version, ID: "req", Model: "provider-model", FinishReason: ir.FinishToolCalls,
		Content: []ir.Content{{Type: ir.ContentText, Text: "Checking"}, {Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{
			ID: "call_1", Name: "weather", Arguments: rawJSON(`{"city":"Rome"}`),
		}}},
		Usage: ir.Usage{InputTokens: 22, CacheCreationInputTokens: 3, CachedInputTokens: 7, OutputTokens: 5},
	}, "team/claude", "msg_gateway")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "msg_gateway" || result.Model != "team/claude" || result.StopReason != "tool_use" {
		t.Fatalf("unexpected Anthropic response: %+v", result)
	}
	if result.Usage.InputTokens != 12 || result.Usage.CacheCreationInputTokens != 3 || result.Usage.CacheReadInputTokens != 7 {
		t.Fatalf("unexpected Anthropic cache usage: %+v", result.Usage)
	}
	if len(result.Content) != 2 || result.Content[1].Name != "weather" {
		t.Fatalf("unexpected response blocks: %+v", result.Content)
	}
}

func TestFromIRResponseRejectsImageOutput(t *testing.T) {
	_, err := FromIRResponse(ir.Response{
		Version: ir.Version, ID: "req", Model: "model", FinishReason: ir.FinishStop,
		Content: []ir.Content{{Type: ir.ContentImage, Image: &ir.Image{URL: "https://example.test/image.png"}}},
	}, "model", "msg")
	if err == nil || !strings.Contains(err.Error(), "cannot represent") {
		t.Fatalf("expected image output error, got %v", err)
	}
}

func floatPointer(value float64) *float64 { return &value }
