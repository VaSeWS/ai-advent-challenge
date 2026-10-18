package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VaSeWS/ai-advent-challenge/week-07/day-34/workspace"
)

func TestProjectToolErrorIsReturnedToModelAndCanBeCorrected(t *testing.T) {
	root := t.TempDir()
	source := "# Known source\nThis exact line is from the workspace.\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close() })
	manager, err := newProjectToolManager(nil, project)
	if err != nil {
		t.Fatal(err)
	}
	badArguments := `{"path":"README.md","start_line":3,"end_line":1}`
	_, backendErr := project.CallTool(context.Background(), "project_read_file", json.RawMessage(badArguments))
	if backendErr == nil {
		t.Fatal("workspace accepted a read range whose start is after its end")
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("invalid-read", "project_read_file", badArguments),
		toolCall("corrected-read", "project_read_file", `{"path":"README.md"}`),
		{Content: "The README contains the known source."},
		{Content: `{}`},
	}}
	profile := ProviderProfile{Name: "synthetic", Model: "synthetic", ContextWindow: 8192, MainMax: 512}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, profile)
	agent.mcp = manager
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Read README.md and summarize it.")
	if err != nil {
		t.Fatalf("send turn after correcting a project read: %v", err)
	}
	if result.Assistant.Content != "The README contains the known source." {
		t.Fatalf("persisted assistant answer = %q, want final model answer", result.Assistant.Content)
	}

	var mainRequests []CompletionRequest
	for _, request := range completer.requests {
		if request.Kind == "main" {
			mainRequests = append(mainRequests, request)
		}
	}
	if len(mainRequests) != 3 {
		t.Fatalf("main completion requests = %d, want initial call, error-feedback continuation, and final answer", len(mainRequests))
	}
	if len(mainRequests[0].Messages) == 0 {
		t.Fatal("initial main request has no messages")
	}
	firstToolMessages := toolMessagesForCall(mainRequests[1].Messages, "invalid-read")
	if len(firstToolMessages) != 1 {
		t.Fatalf("model request after invalid read has tool messages %#v, want one result for invalid-read", firstToolMessages)
	}
	var failure struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(firstToolMessages[0].Content), &failure); err != nil {
		t.Fatalf("decode invalid-read feedback %q: %v", firstToolMessages[0].Content, err)
	}
	if !strings.Contains(failure.Error, backendErr.Error()) {
		t.Fatalf("model received invalid-read error %q, want backend error %q", failure.Error, backendErr)
	}
	if got := firstToolMessages[0].ToolCallID; got != "invalid-read" {
		t.Fatalf("invalid tool feedback call ID = %q, want invalid-read", got)
	}
	secondToolMessages := toolMessagesForCall(mainRequests[2].Messages, "corrected-read")
	if len(secondToolMessages) != 1 {
		t.Fatalf("model request after corrected read has tool messages %#v, want one result for corrected-read", secondToolMessages)
	}
	var corrected struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(secondToolMessages[0].Content), &corrected); err != nil {
		t.Fatal(err)
	}
	if corrected.Content != source {
		t.Fatalf("model received corrected source %q, want %q", corrected.Content, source)
	}
	if got := secondToolMessages[0].ToolCallID; got != "corrected-read" {
		t.Fatalf("corrected tool result call ID = %q, want corrected-read", got)
	}
	if len(completer.requests) != 4 {
		t.Fatalf("completion requests = %d, want three model-selected main calls and memory extraction", len(completer.requests))
	}
}

func TestRepeatedProjectToolErrorsRespectConfiguredRoundLimit(t *testing.T) {
	root := t.TempDir()
	original := "# Keep unchanged\n"
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(readme, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close() })
	manager, err := newProjectToolManager(nil, project)
	if err != nil {
		t.Fatal(err)
	}
	badArguments := `{"path":"README.md","start_line":3,"end_line":1}`
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("invalid-read-1", "project_read_file", badArguments),
		toolCall("invalid-read-2", "project_read_file", badArguments),
		toolCall("invalid-read-3", "project_read_file", badArguments),
	}}
	agent := &Agent{
		llm: completer, tokens: agentTestCounter{tokens: 1, label: "test"},
		profile: ProviderProfile{MainMax: 100}, mcp: manager, maxToolRounds: 2,
	}
	_, _, events, err := agent.completeMainWithTools(context.Background(), Branch{}, nil, "inspect the project", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "exceeded 2 rounds") {
		t.Fatalf("project tool error continuation did not stop at configured bound: events=%#v err=%v", events, err)
	}
	if len(completer.requests) != 3 {
		t.Fatalf("model completion requests = %d, want configured rounds plus the boundary decision", len(completer.requests))
	}
	firstFeedback := toolMessagesForCall(completer.requests[1].Messages, "invalid-read-1")
	secondFeedback := toolMessagesForCall(completer.requests[2].Messages, "invalid-read-2")
	if len(firstFeedback) != 1 || len(secondFeedback) != 1 {
		t.Fatalf("model-selected invalid-call feedback = %#v and %#v, want one message for each distinct call ID", firstFeedback, secondFeedback)
	}
	_, backendErr := project.CallTool(context.Background(), "project_read_file", json.RawMessage(badArguments))
	if backendErr == nil {
		t.Fatal("workspace accepted a read range whose start is after its end")
	}
	if !strings.Contains(firstFeedback[0].Content, backendErr.Error()) || !strings.Contains(secondFeedback[0].Content, backendErr.Error()) {
		t.Fatalf("repeated invalid project calls did not receive their backend error %q: %#v %#v", backendErr, firstFeedback, secondFeedback)
	}
	got, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("README.md changed after repeated invalid reads: got %q, want %q", got, original)
	}
}

func toolMessagesForCall(messages []CompletionMessage, callID string) []CompletionMessage {
	var found []CompletionMessage
	for _, message := range messages {
		if message.Role == "tool" && message.ToolCallID == callID {
			found = append(found, message)
		}
	}
	return found
}
