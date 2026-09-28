package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolCallTestCompleter struct {
	responses []Completion
	requests  []CompletionRequest
}

func (c *toolCallTestCompleter) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	c.requests = append(c.requests, request)
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

type toolCallTestManager struct {
	tools        []MCPTool
	results      map[string]any
	fail         map[string]bool
	errorResults map[string]string
	calls   []struct {
		name string
		args json.RawMessage
	}
}

func (m *toolCallTestManager) Tools() []MCPTool { return m.tools }
func (m *toolCallTestManager) CallTool(_ context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	m.calls = append(m.calls, struct {
		name string
		args json.RawMessage
	}{name, append(json.RawMessage(nil), arguments...)})
	if m.fail[name] {
		return nil, context.DeadlineExceeded
	}
	if text, ok := m.errorResults[name]; ok {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
	return &mcp.CallToolResult{StructuredContent: m.results[name]}, nil
}

func newToolCallTestManager(names ...string) *toolCallTestManager {
	manager := &toolCallTestManager{results: map[string]any{}, fail: map[string]bool{}, errorResults: map[string]string{}}
	for _, name := range names {
		manager.tools = append(manager.tools, MCPTool{ServerName: "test-server", Definition: mcp.Tool{
			Name: name, Description: "tool description", InputSchema: map[string]any{"type": "object"},
		}})
	}
	return manager
}

func toolCall(id, name, args string) Completion {
	return Completion{ToolCalls: []CompletionToolCall{{ID: id, Type: "function", Function: CompletionToolCallFn{Name: name, Arguments: args}}}, FinishReason: "tool_calls"}
}

func TestToolCallContinuationBindsActualLookupAndPlan(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "save_plan", "send_message", "unrelated_tool")
	manager.tools[0].Definition.InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic": map[string]any{"type": "string"},
			"date":  map[string]any{"type": "string"},
		},
		"required": []string{"topic", "date"},
	}
	manager.tools[4].Definition.InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1", "topic": "go", "due": "2026-09-28", "path": "Q1.md"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "# Review\nQ1", "count": 1}
	manager.results["save_plan"] = map[string]any{"path": "week-04/plan.md"}
	manager.results["send_message"] = map[string]any{"sent": true}
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("call-1", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
		toolCall("call-2", "build_review_plan", `{}`),
		toolCall("call-3", "save_plan", `{}`),
		toolCall("call-4", "send_message", `{}`),
		{Content: "Plan saved and sent."},
	}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}}
	agent.mcp = manager
	input := "Составь план повторения темы, сохрани его и отправь мне в Telegram"
	completion, calls, events, err := agent.completeMainWithTools(context.Background(), Branch{Strategy: strategyFull}, []CompletionMessage{{Role: "user", Content: input}}, input, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if completion.Content != "Plan saved and sent." || len(calls) != 5 || len(events) != 4 || len(manager.calls) != 4 {
		t.Fatalf("completion=%q calls=%d events=%#v tool calls=%d", completion.Content, len(calls), events, len(manager.calls))
	}
	if events[1].Name != "build_review_plan" || events[1].Status != "completed" || events[1].ServerName != "test-server" {
		t.Fatalf("plan success event missing server identity: %#v", events[1])
	}
	for _, event := range events[2:] {
		if event.ServerName != "test-server" || event.Status != "completed" {
			t.Fatalf("external success event was not confirmed with server identity: %#v", event)
		}
	}
	var lookup, build, save, send map[string]json.RawMessage
	for index, target := range []*map[string]json.RawMessage{&lookup, &build, &save, &send} {
		if err := json.Unmarshal(manager.calls[index].args, target); err != nil {
			t.Fatal(err)
		}
	}
	var questions []map[string]string
	_ = json.Unmarshal(build["questions"], &questions)
	var savedPlan, sentText string
	_ = json.Unmarshal(save["plan"], &savedPlan)
	_ = json.Unmarshal(send["text"], &sentText)
	if string(lookup["topic"]) != `"go"` || string(build["topic"]) != `"go"` || string(lookup["date"]) != `"2026-09-28"` || string(build["date"]) != `"2026-09-28"` || len(questions) != 1 || questions[0]["title"] != "Q1" || savedPlan != "# Review\nQ1" || sentText != "# Review\nQ1" {
		t.Fatalf("source-bound args failed: lookup=%s build=%s save=%s send=%s", manager.calls[0].args, manager.calls[1].args, manager.calls[2].args, manager.calls[3].args)
	}
	for _, request := range completer.requests {
		if request.ToolChoice != "auto" || len(request.Tools) != 5 {
			t.Fatalf("main request did not contain dynamic auto tools: %#v", request)
		}
		projected := make(map[string]CompletionToolFunction, len(request.Tools))
		for _, tool := range request.Tools {
			projected[tool.Function.Name] = tool.Function
		}
		emptyObjectSchema := map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}
		for _, name := range []string{"build_review_plan", "save_plan", "send_message"} {
			function, ok := projected[name]
			if !ok || function.Description != "tool description" || !reflect.DeepEqual(function.Parameters, emptyObjectSchema) {
				t.Fatalf("%s did not expose its host-bound empty-object request schema: %#v", name, function)
			}
		}
		if function := projected["find_due_questions"]; function.Description != "tool description" || !reflect.DeepEqual(function.Parameters, manager.tools[0].Definition.InputSchema) {
			t.Fatalf("lookup schema was projected instead of preserving required user inputs: %#v", function)
		}
		if function := projected["unrelated_tool"]; function.Description != "tool description" || !reflect.DeepEqual(function.Parameters, manager.tools[4].Definition.InputSchema) {
			t.Fatalf("unrelated discovered tool schema was changed: %#v", function)
		}
	}
}

func TestToolCallEmptyLookupStopsDownstream(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "save_plan")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{}}
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("lookup", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
		toolCall("build", "build_review_plan", `{}`),
	}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	completion, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "make a review plan", 0, 0)
	if err != nil || !strings.Contains(completion.Content, "no review plan was created") || len(events) != 1 || events[0].Status != "completed (no matches)" || len(manager.calls) != 1 || len(completer.requests) != 1 {
		t.Fatalf("empty lookup did not stop downstream: completion=%#v events=%#v calls=%d requests=%d err=%v", completion, events, len(manager.calls), len(completer.requests), err)
	}
}

func TestToolCallUnrequestedSaveIsBlocked(t *testing.T) {
	manager := newToolCallTestManager("save_plan")
	completer := &toolCallTestCompleter{responses: []Completion{toolCall("save", "save_plan", `{}`)}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	_, _, _, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "make a review plan", 0, 0)
	if err == nil || len(manager.calls) != 0 {
		t.Fatalf("unrequested save was not blocked: err=%v calls=%d", err, len(manager.calls))
	}
}

func TestRequestedExternalEffectsRespectNegation(t *testing.T) {
	tests := []struct {
		name               string
		input              string
		wantSave, wantSend bool
	}{
		{name: "day 19 save", input: "Составь план повторения темы и сохрани его", wantSave: true},
		{name: "day 20 save and telegram", input: "Составь план повторения темы, сохрани его и отправь мне в Telegram", wantSave: true, wantSend: true},
		{name: "save negation", input: "Составь план, но не сохраняй его"},
		{name: "save negation imperative", input: "Составь план, но не сохрани его"},
		{name: "send negation", input: "Составь план, но не отправляй его в Telegram"},
		{name: "send negation past imperative", input: "Составь план, но не пришли его мне в Telegram"},
		{name: "both negated", input: "Составь план, не сохраняй и не отправляй его"},
		{name: "never send", input: "Never send it"},
		{name: "do not actually send", input: "Do not actually send it"},
		{name: "don't actually save", input: "Don't actually save it"},
		{name: "negation across punctuation", input: "Build a review plan, but do not, under any circumstances, send it to Telegram"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			save, send := requestedExternalEffects(test.input)
			if save != test.wantSave || send != test.wantSend {
				t.Fatalf("requestedExternalEffects(%q) = (%t, %t), want (%t, %t)", test.input, save, send, test.wantSave, test.wantSend)
			}
		})
	}
}

func TestToolCallFailedExternalConfirmationIsNotSuccess(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "save_plan")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "review", "count": 1}
	manager.results["save_plan"] = map[string]any{"path": ""}
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("lookup", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
		toolCall("build", "build_review_plan", `{}`),
		toolCall("save", "save_plan", `{}`),
	}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	_, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "сохрани план", 0, 0)
	if err == nil || len(events) != 3 || events[2].Status != "outcome unconfirmed" || events[2].ServerName != "test-server" || hasSuccessfulSideEffect(events) {
		t.Fatalf("failed confirmation was reported successful: events=%#v partial=%t err=%v", events, hasSuccessfulSideEffect(events), err)
	}
}
func TestNegatedEffectsNeverInvokeTools(t *testing.T) {
	tests := []struct {
		name  string
		input string
		tool  string
	}{
		{name: "save", input: "Составь план, но не сохрани его", tool: "save_plan"},
		{name: "send", input: "Составь план, но не пришли его мне в Telegram", tool: "send_message"},
		{name: "never save", input: "Never save it", tool: "save_plan"},
		{name: "never send", input: "Never send it", tool: "send_message"},
		{name: "don't actually save", input: "Don't actually save it", tool: "save_plan"},
		{name: "don't actually send", input: "Don't actually send it", tool: "send_message"},
		{name: "negation across punctuation blocks send", input: "Build a review plan, but do not, under any circumstances, send it to Telegram", tool: "send_message"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newToolCallTestManager("find_due_questions", "build_review_plan", test.tool)
			manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1"}}}
			manager.results["build_review_plan"] = map[string]any{"plan": "review", "count": 1}
			completer := &toolCallTestCompleter{responses: []Completion{
				toolCall("lookup", "find_due_questions", `{"date":"2026-09-28"}`),
				toolCall("build", "build_review_plan", `{}`),
				toolCall("effect", test.tool, `{}`),
			}}
			agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
			_, _, _, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, test.input, 0, 0)
			if err == nil || len(manager.calls) != 2 || manager.calls[1].name != "build_review_plan" {
				t.Fatalf("negated %s reached external tool: calls=%#v err=%v", test.tool, manager.calls, err)
			}
		})
	}
}

func TestUnconfirmedFinalSideEffectClaimsAreCorrected(t *testing.T) {
	tests := []struct {
		name, input, response, want string
	}{
		{
			name: "English save and send",
			input: "Save the plan and send it",
			response: "The plan was saved and sent.",
			want: "Saving was not confirmed. Sending was not confirmed.",
		},
		{
			name: "Russian save",
			input: "Сохрани план",
			response: "План сохранён.",
			want: "Saving was not confirmed.",
		},
		{
			name: "ordinary sentence is preserved",
			input: "Give me a plan",
			response: "Here is a sentence.",
			want: "Here is a sentence.",
		},
		{
			name: "text-only translation keeps sent",
			input: "Translate «Я отправил письмо» into English",
			response: "I sent a letter.",
			want: "I sent a letter.",
		},
		{
			name: "text-only explanation keeps stored",
			input: "Explain how the data is stored",
			response: "The data was stored in a database.",
			want: "The data was stored in a database.",
		},
		{
			name: "save request does not rewrite unrelated sending",
			input: "Save the plan",
			response: "The letter was sent by post.",
			want: "The letter was sent by post.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			completer := &toolCallTestCompleter{responses: []Completion{{Content: test.response}}}
			agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}}
			completion, _, _, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, test.input, 0, 0)
			if err != nil || completion.Content != test.want {
				t.Fatalf("completion=%q want %q err=%v", completion.Content, test.want, err)
			}
		})
	}
}

func TestSafeToolOutputPreservesLargeIntegers(t *testing.T) {
	got, structured, err := safeToolOutput("other_tool", map[string]any{
		"count": int64(9007199254740993),
	})
	if err != nil || !structured {
		t.Fatalf("structured=%t err=%v", structured, err)
	}
	if string(got) != `{"count":9007199254740993}` {
		t.Fatalf("large integer changed during sanitization: %s", got)
	}
}

func TestToolCallRejectsSecondSaveInTurn(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "save_plan")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "review", "count": 1}
	manager.results["save_plan"] = map[string]any{"path": "review.md"}
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("lookup", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
		toolCall("build", "build_review_plan", `{}`),
		toolCall("save-1", "save_plan", `{}`),
		toolCall("save-2", "save_plan", `{}`),
	}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	_, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "сохрани план", 0, 0)
	if err == nil || len(manager.calls) != 3 || len(events) != 3 || events[2].Status != "completed" {
		t.Fatalf("second save was not stopped: events=%#v calls=%d err=%v", events, len(manager.calls), err)
	}
}
func TestToolCallFailureStopsBeforeDownstream(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan")
	manager.fail["find_due_questions"] = true
	completer := &toolCallTestCompleter{responses: []Completion{toolCall("lookup", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`)}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	_, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "find due questions", 0, 0)
	if err == nil || len(events) != 1 || events[0].Status != "failed" || events[0].ServerName != "test-server" || len(manager.calls) != 1 {
		t.Fatalf("failure did not stop safely: err=%v events=%#v calls=%d", err, events, len(manager.calls))
	}
}

func TestToolCallTextOnlyCompatibility(t *testing.T) {
	completer := &toolCallTestCompleter{responses: []Completion{{Content: "plain response"}}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}}
	completion, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, []CompletionMessage{{Role: "user", Content: "hello"}}, "hello", 0, 0)
	if err != nil || completion.Content != "plain response" || len(events) != 0 || len(completer.requests[0].Tools) != 0 {
		t.Fatalf("text-only completion changed: %#v events=%#v err=%v", completion, events, err)
	}
}

func TestOpenAICompatibleToolMessageCodec(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages   []json.RawMessage `json:"messages"`
			Tools      []CompletionTool  `json:"tools"`
			ToolChoice string            `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if len(body.Tools) != 1 || body.ToolChoice != "auto" || !strings.Contains(string(body.Messages[1]), `"content":null`) || !strings.Contains(string(body.Messages[2]), `"tool_call_id":"call-1"`) {
			t.Errorf("invalid tool continuation encoding: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-2","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`))
	}))
	defer server.Close()
	client := NewOpenAICompatibleClient(ProviderProfile{Endpoint: server.URL, Model: "test", MaxTokensField: "max_tokens"}, "secret")
	got, err := client.Complete(context.Background(), CompletionRequest{
		Messages: []CompletionMessage{
			{Role: "system", Content: "system"},
			{Role: "assistant", ToolCalls: []CompletionToolCall{{ID: "call-1", Type: "function", Function: CompletionToolCallFn{Name: "lookup", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "call-1", Content: `{"ok":true}`},
		},
		Tools: []CompletionTool{{Type: "function", Function: CompletionToolFunction{Name: "lookup", Parameters: map[string]any{"type": "object"}}}},
	})
	if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "lookup" || got.Usage.TotalTokens != 11 {
		t.Fatalf("decoded completion=%#v err=%v", got, err)
	}
}

func TestToolCallCodecPreservesAssistantToolCallAndToolRole(t *testing.T) {
	messages := []CompletionMessage{
		{Role: "assistant", ToolCalls: []CompletionToolCall{{ID: "id", Type: "function", Function: CompletionToolCallFn{Name: "x", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "id", Content: `{"ok":true}`},
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(encoded), `"role":"tool"`) != 1 || !strings.Contains(string(encoded), `"content":null`) {
		t.Fatalf("messages lost valid tool-call encoding: %s", encoded)
	}
	var decoded []CompletionMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded[0].ToolCalls[0].Function.Arguments != `{}` || decoded[1].ToolCallID != "id" {
		t.Fatalf("round trip failed: %#v err=%v", decoded, err)
	}
}
func TestPartialTelegramDeliveryIsReportedWithoutRawError(t *testing.T) {
	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "send_message")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "review", "count": 1}
	manager.errorResults["send_message"] = "Telegram delivered 1 of 2 message chunks: secret Telegram failure details"
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("lookup", "find_due_questions", `{"date":"2026-09-28"}`),
		toolCall("build", "build_review_plan", `{}`),
		toolCall("send", "send_message", `{}`),
	}}
	agent := &Agent{llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"}, profile: ProviderProfile{MainMax: 100}, mcp: manager}
	_, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "send it to me", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 chunks") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("partial delivery error was not safely reported: %v", err)
	}
	if len(events) != 3 || events[2].Status != "partial delivery (1 of 2 chunks)" || !hasSuccessfulSideEffect(events) {
		t.Fatalf("partial delivery event not retained: %#v", events)
	}
	if len(manager.calls) != 3 || len(completer.requests) != 3 {
		t.Fatalf("partial delivery was retried: tool calls=%d model requests=%d", len(manager.calls), len(completer.requests))
	}
}
