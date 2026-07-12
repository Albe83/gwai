package openai

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

func TestToIRTranslatesMessagesImagesAndTools(t *testing.T) {
	maxTokens := 500
	strict := true
	parallel := false
	request := ChatCompletionRequest{
		Model: "claude", MaxCompletionTokens: &maxTokens,
		Messages: []ChatMessage{
			{Role: "developer", Content: json.RawMessage(`"Be concise"`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]`)},
			{Role: "assistant", Content: json.RawMessage(`null`), ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "weather", Arguments: `{"city":"Rome"}`}}}},
			{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"sunny"`)},
		},
		Tools:             []Tool{{Type: "function", Function: FunctionTool{Name: "weather", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}}},
		ToolChoice:        json.RawMessage(`{"type":"function","function":{"name":"weather"}}`),
		ParallelToolCalls: &parallel,
	}
	route := controlplane.Route{ProviderID: "prv_1", UpstreamModel: "claude-test"}
	result, err := ToIR(request, route, "req_1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Messages[0].Role != ir.RoleSystem || result.Messages[1].Content[1].Image.MediaType != "image/png" {
		t.Fatalf("unexpected message translation: %+v", result.Messages)
	}
	if result.Messages[2].Content[0].ToolCall.Name != "weather" {
		t.Fatalf("tool call was not translated: %+v", result.Messages[2])
	}
	if result.Messages[3].Content[0].ToolResult.ToolCallID != "call_1" {
		t.Fatalf("tool result was not translated: %+v", result.Messages[3])
	}
	if result.ToolChoice == nil || result.ToolChoice.Mode != "named" || result.ToolChoice.Name != "weather" {
		t.Fatalf("unexpected tool choice: %+v", result.ToolChoice)
	}
	if result.Tools[0].Strict == nil || !*result.Tools[0].Strict || !result.ToolChoice.DisableParallel {
		t.Fatalf("strict/parallel tool controls were not preserved: tools=%+v choice=%+v", result.Tools, result.ToolChoice)
	}
	if result.MaxOutputTokens == nil || *result.MaxOutputTokens != maxTokens {
		t.Fatalf("max output tokens were not preserved: %+v", result.MaxOutputTokens)
	}
}

func TestToIRRejectsUnsupportedStreaming(t *testing.T) {
	request := ChatCompletionRequest{Model: "m", Stream: true, Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
	_, err := ToIR(request, controlplane.Route{ProviderID: "p", UpstreamModel: "m"}, "r")
	var translation *TranslationError
	if !errors.As(err, &translation) || translation.Param != "stream" {
		t.Fatalf("expected stream translation error, got %v", err)
	}
}

func TestToIRRejectsKnownUnmappedParameters(t *testing.T) {
	route := controlplane.Route{ProviderID: "p", UpstreamModel: "m"}
	tests := []struct {
		name  string
		param string
		apply func(*ChatCompletionRequest)
	}{
		{name: "metadata", param: "metadata", apply: func(request *ChatCompletionRequest) { request.Metadata = map[string]any{"trace": "x"} }},
		{name: "stream options", param: "stream_options", apply: func(request *ChatCompletionRequest) {
			request.StreamOptions = json.RawMessage(`{"include_usage":true}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := ChatCompletionRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}}
			test.apply(&request)
			_, err := ToIR(request, route, "r")
			var translation *TranslationError
			if !errors.As(err, &translation) || translation.Param != test.param {
				t.Fatalf("expected %s translation error, got %v", test.param, err)
			}
		})
	}
}

func TestToIRRejectsUnknownOrMisplacedMessageFields(t *testing.T) {
	route := controlplane.Route{ProviderID: "p", UpstreamModel: "m"}
	tests := []ChatCompletionRequest{
		{
			Model:    "m",
			Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hi","future":true}]`)}},
		},
		{
			Model:    "m",
			Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`), ToolCalls: []ToolCall{{ID: "call", Type: "function", Function: ToolCallFunction{Name: "f", Arguments: `{}`}}}}},
		},
	}
	for index, request := range tests {
		if _, err := ToIR(request, route, "r"); err == nil {
			t.Fatalf("case %d: expected invalid message fields to be rejected", index)
		}
	}
}

func TestToIRRejectsMissingNamedTool(t *testing.T) {
	request := ChatCompletionRequest{
		Model: "m", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
		Tools:      []Tool{{Type: "function", Function: FunctionTool{Name: "weather", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"unknown"}}`),
	}
	_, err := ToIR(request, controlplane.Route{ProviderID: "p", UpstreamModel: "m"}, "r")
	var translation *TranslationError
	if !errors.As(err, &translation) || translation.Param != "tool_choice" {
		t.Fatalf("expected tool_choice translation error, got %v", err)
	}
}

func TestToIROmitsAdapterOwnedOutputTokenDefault(t *testing.T) {
	request := ChatCompletionRequest{
		Model: "provider/model", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	result, err := ToIR(request, controlplane.Route{ProviderID: "p", UpstreamModel: "model"}, "r")
	if err != nil {
		t.Fatal(err)
	}
	if result.MaxOutputTokens != nil {
		t.Fatalf("expected adapter-owned token default, got %d", *result.MaxOutputTokens)
	}
}

func TestFromIRBuildsOpenAIToolResponse(t *testing.T) {
	response, err := FromIR(ir.Response{
		Version: ir.Version, FinishReason: ir.FinishToolCalls,
		Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "tool_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Rome"}`)}}},
		Usage:   ir.Usage{InputTokens: 10, OutputTokens: 3, CachedInputTokens: 4},
	}, "claude", "chatcmpl_1", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != nil || response.Choices[0].Message.ToolCalls[0].Function.Name != "weather" {
		t.Fatalf("unexpected OpenAI response: %+v", response)
	}
	if response.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
	if response.Usage.PromptTokensDetails == nil || response.Usage.PromptTokensDetails.CachedTokens != 4 {
		t.Fatalf("cached token usage was not preserved: %+v", response.Usage)
	}
}
