package openairesponses

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Albe83/gwai/internal/controlplane"
	"github.com/Albe83/gwai/internal/ir"
)

func testRoute() controlplane.Route {
	return controlplane.Route{ProviderID: "prv_1", UpstreamModel: "gpt-test", AdapterAppID: "adapter"}
}

func TestToIRTranslatesResponsesItemsImagesAndFunctions(t *testing.T) {
	maxTokens := 256
	parallel := false
	strict := true
	request := CreateRequest{
		Model: "openai/gpt-test", Instructions: json.RawMessage(`"Be concise"`),
		Input: json.RawMessage(`[
            {"type":"message","role":"user","content":[{"type":"input_text","text":"weather"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8=","detail":"auto"}]},
            {"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Rome\"}"},
            {"type":"function_call_output","call_id":"call_1","output":"{\"temperature\":21}"}
        ]`),
		MaxOutputTokens: &maxTokens, ParallelToolCalls: &parallel,
		Tools:      []Tool{{Type: "function", Name: "weather", Description: "Weather", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: json.RawMessage(`{"type":"function","name":"weather"}`),
	}
	result, err := ToIR(request, testRoute(), "req_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 4 || result.Messages[0].Role != ir.RoleSystem || result.Messages[1].Role != ir.RoleUser {
		t.Fatalf("unexpected messages: %+v", result.Messages)
	}
	if image := result.Messages[1].Content[1].Image; image == nil || image.MediaType != "image/png" || image.Data != "aGVsbG8=" {
		t.Fatalf("image was not translated: %+v", image)
	}
	call := result.Messages[2].Content[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "weather" {
		t.Fatalf("function call was not translated: %+v", call)
	}
	toolResult := result.Messages[3].Content[0].ToolResult
	if toolResult == nil || toolResult.Name != "weather" || string(toolResult.Result) != `{"temperature":21}` {
		t.Fatalf("structured function output was not translated: %+v", toolResult)
	}
	if result.ToolChoice == nil || result.ToolChoice.Mode != "named" || result.ToolChoice.Name != "weather" || !result.ToolChoice.DisableParallel {
		t.Fatalf("unexpected tool choice: %+v", result.ToolChoice)
	}
	if result.Tools[0].Strict == nil || !*result.Tools[0].Strict || result.MaxOutputTokens == nil || *result.MaxOutputTokens != 256 {
		t.Fatalf("tool/token settings were not preserved: %+v", result)
	}
}

func TestToIRRequiresFunctionOutputToReferencePrecedingCall(t *testing.T) {
	request := CreateRequest{Model: "m", Input: json.RawMessage(`[{"type":"function_call_output","call_id":"missing","output":"ok"}]`)}
	_, err := ToIR(request, testRoute(), "req")
	var translation *TranslationError
	if !errors.As(err, &translation) || translation.Param != "input.0.call_id" {
		t.Fatalf("expected call reference error, got %v", err)
	}
}

func TestToIRRejectsStatefulAndNonPortableFeatures(t *testing.T) {
	trueValue := true
	tests := []struct {
		name  string
		param string
		apply func(*CreateRequest)
	}{
		{name: "stream", param: "stream", apply: func(r *CreateRequest) { r.Stream = &trueValue }},
		{name: "store", param: "store", apply: func(r *CreateRequest) { r.Store = &trueValue }},
		{name: "previous response", param: "previous_response_id", apply: func(r *CreateRequest) { r.PreviousResponseID = "resp_old" }},
		{name: "conversation", param: "conversation", apply: func(r *CreateRequest) { r.Conversation = json.RawMessage(`"conv_1"`) }},
		{name: "reasoning", param: "reasoning", apply: func(r *CreateRequest) { r.Reasoning = json.RawMessage(`{"effort":"high"}`) }},
		{name: "structured output", param: "text", apply: func(r *CreateRequest) { r.Text = json.RawMessage(`{"format":{"type":"json_schema"}}`) }},
		{name: "background", param: "background", apply: func(r *CreateRequest) { r.Background = &trueValue }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := CreateRequest{Model: "m", Input: json.RawMessage(`"hello"`)}
			test.apply(&request)
			_, err := ToIR(request, testRoute(), "req")
			var translation *TranslationError
			if !errors.As(err, &translation) || translation.Param != test.param {
				t.Fatalf("expected %s error, got %v", test.param, err)
			}
		})
	}
}

func TestToIRRejectsHostedTools(t *testing.T) {
	request := CreateRequest{Model: "m", Input: json.RawMessage(`"hello"`), Tools: []Tool{{Type: "web_search"}}}
	_, err := ToIR(request, testRoute(), "req")
	var translation *TranslationError
	if !errors.As(err, &translation) || translation.Param != "tools.0.type" {
		t.Fatalf("expected hosted tool error, got %v", err)
	}
}

func TestToProviderRequestBuildsStatelessResponsesPayload(t *testing.T) {
	maxTokens := 128
	parallel := true
	request := ir.Request{
		Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "p", UpstreamModel: "gpt-4.1"}, MaxOutputTokens: &maxTokens,
		Messages: []ir.Message{
			{Role: ir.RoleSystem, Content: []ir.Content{{Type: ir.ContentText, Text: "system"}}},
			{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hi"}}},
			{Role: ir.RoleAssistant, Content: []ir.Content{{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "call_1", Name: "f", Arguments: json.RawMessage(`{"x":1}`)}}}},
			{Role: ir.RoleTool, Content: []ir.Content{{Type: ir.ContentToolResult, ToolResult: &ir.ToolResult{ToolCallID: "call_1", Name: "f", Result: json.RawMessage(`{"y":2}`)}}}},
		},
		Tools:      []ir.Tool{{Name: "f", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &ir.ToolChoice{Mode: "required", DisableParallel: parallel},
	}
	result, err := ToProviderRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Store || result.Stream || result.Instructions != "system" || len(result.Input) != 3 {
		t.Fatalf("unexpected provider request: %+v", result)
	}
	if result.Input[1].Type != "function_call" || result.Input[2].Type != "function_call_output" || string(result.Input[2].Output) != `"{\"y\":2}"` {
		t.Fatalf("function history was not preserved: %+v", result.Input)
	}
	if string(result.ToolChoice) != `"required"` || result.ParallelToolCalls == nil || *result.ParallelToolCalls {
		t.Fatalf("unexpected tool controls: choice=%s parallel=%v", result.ToolChoice, result.ParallelToolCalls)
	}
}

func TestToProviderRequestRejectsStopSequences(t *testing.T) {
	request := ir.Request{
		Version: ir.Version, ID: "req", Route: ir.Route{ProviderID: "p", UpstreamModel: "m"}, Stop: []string{"STOP"},
		Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hi"}}}},
	}
	if _, err := ToProviderRequest(request); err == nil {
		t.Fatal("expected stop sequence rejection")
	}
}

func TestToIRResponsePreservesUsageCallsAndIncompleteReason(t *testing.T) {
	request := ir.Request{Version: ir.Version, ID: "req_1", Route: ir.Route{ProviderID: "p", UpstreamModel: "gpt"}, Messages: []ir.Message{{Role: ir.RoleUser, Content: []ir.Content{{Type: ir.ContentText, Text: "hi"}}}}}
	response := Response{
		ID: "resp_up", Status: "incomplete", Model: "gpt-snapshot", IncompleteDetails: &IncompleteDetails{Reason: "max_output_tokens"},
		Output: []OutputItem{
			{Type: "reasoning", ID: "rs_1"},
			{Type: "message", Role: "assistant", Content: []OutputContent{{Type: "output_text", Text: "partial"}}},
			{Type: "function_call", CallID: "call_1", Name: "weather", Arguments: `{"city":"Rome"}`},
		},
		Usage: Usage{InputTokens: 20, OutputTokens: 4, TotalTokens: 24, InputTokensDetails: &InputTokenDetails{CachedTokens: 7}},
	}
	result, err := ToIRResponse(response, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != ir.FinishLength || result.ProviderResponseID != "resp_up" || result.Model != "gpt-snapshot" {
		t.Fatalf("unexpected response metadata: %+v", result)
	}
	if result.Usage.InputTokens != 20 || result.Usage.CachedInputTokens != 7 || len(result.Content) != 2 {
		t.Fatalf("usage/content not preserved: %+v", result)
	}
}

func TestFromIRBuildsResponsesEnvelope(t *testing.T) {
	response, err := FromIR(ir.Response{
		Version: ir.Version, ID: "req", Model: "provider-model", FinishReason: ir.FinishToolCalls,
		Content: []ir.Content{
			{Type: ir.ContentText, Text: "calling"},
			{Type: ir.ContentToolCall, ToolCall: &ir.ToolCall{ID: "call_1", Name: "weather", Arguments: json.RawMessage(`{"city":"Rome"}`)}},
		},
		Usage: ir.Usage{InputTokens: 10, OutputTokens: 3, CachedInputTokens: 4},
	}, "client-model", "resp_1", time.Unix(100, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" || response.CreatedAt != 100 || response.Store || response.ParallelToolCalls || len(response.Output) != 2 {
		t.Fatalf("unexpected envelope: %+v", response)
	}
	if response.Output[0].Content[0].Text != "calling" || response.Output[1].CallID != "call_1" {
		t.Fatalf("unexpected output: %+v", response.Output)
	}
	if response.Usage.InputTokensDetails == nil || response.Usage.InputTokensDetails.CachedTokens != 4 || response.Usage.TotalTokens != 13 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
}
