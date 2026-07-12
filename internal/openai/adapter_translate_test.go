package openai

import (
	"encoding/json"
	"testing"

	"github.com/Albe83/gwai/internal/ir"
)

func TestToChatRequestPreservesPortableMultimodalTools(t *testing.T) {
	maxTokens := 321
	strict := true
	request := ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_1", UpstreamModel: "gpt-test"},
		Messages: []ir.Message{
			{Role: ir.RoleSystem, Content: []ir.Content{{Type: ir.ContentText, Text: "Be concise"}}},
			{Role: ir.RoleUser, Content: []ir.Content{
				{Type: ir.ContentText, Text: "inspect"},
				{Type: ir.ContentImage, Image: &ir.Image{MediaType: "image/png", Data: "aGVsbG8="}},
			}},
			{Role: ir.RoleAssistant, Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{
				ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Rome"}`),
			}}}},
			{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{
				ToolCallID: "call_1", Name: "weather", Result: json.RawMessage(`{"weather":"sunny"}`),
			}}}},
		},
		MaxOutputTokens: &maxTokens,
		Tools:           []ir.Tool{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice:      &ir.ToolChoice{Mode: "named", Name: "weather", DisableParallel: true},
	}
	result, err := ToChatRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "gpt-test" || result.MaxCompletionTokens == nil || *result.MaxCompletionTokens != maxTokens {
		t.Fatalf("generation controls were not preserved: %+v", result)
	}
	if len(result.Messages) != 4 || result.Messages[2].ToolCalls[0].Function.Name != "weather" || result.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("conversation was not preserved: %+v", result.Messages)
	}
	if string(result.Messages[1].Content) != `[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]` {
		t.Fatalf("unexpected multimodal content: %s", result.Messages[1].Content)
	}
	if result.ParallelToolCalls == nil || *result.ParallelToolCalls || result.Tools[0].Function.Strict == nil || !*result.Tools[0].Function.Strict {
		t.Fatalf("tool controls were not preserved: %+v", result)
	}
}

func TestFromChatResponseBuildsValidatedIR(t *testing.T) {
	content := "checking"
	response, err := FromChatResponse(ChatCompletionResponse{
		ID: "chatcmpl_provider",
		Choices: []Choice{{FinishReason: "tool_calls", Message: AssistantOutput{
			Content:   &content,
			ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "weather", Arguments: `{"city":"Rome"}`}}},
		}}},
		Usage: Usage{PromptTokens: 12, CompletionTokens: 4, PromptTokensDetails: &PromptTokensDetails{CachedTokens: 3}},
	}, ir.Request{Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_1", UpstreamModel: "gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.ProviderResponseID != "chatcmpl_provider" || response.FinishReason != ir.FinishToolCalls || response.Content[1].ToolCall.Name != "weather" {
		t.Fatalf("unexpected IR response: %+v", response)
	}
	if response.Usage.InputTokens != 12 || response.Usage.CachedInputTokens != 3 {
		t.Fatalf("usage was not preserved: %+v", response.Usage)
	}
}

func TestFromChatResponsePreservesRefusal(t *testing.T) {
	refusal := "I cannot help with that."
	response, err := FromChatResponse(ChatCompletionResponse{
		ID:      "chatcmpl_refusal",
		Choices: []Choice{{Index: 0, FinishReason: "stop", Message: AssistantOutput{Role: "assistant", Refusal: &refusal}}},
	}, ir.Request{Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "prv_1", UpstreamModel: "gpt-test"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.FinishReason != ir.FinishContent || len(response.Content) != 1 || response.Content[0].Text != refusal {
		t.Fatalf("refusal was not preserved: %+v", response)
	}
}
