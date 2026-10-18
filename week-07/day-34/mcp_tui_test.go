package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPCommandsAddListConnectAndDisconnectWithoutExecutingList(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "commands.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	commands := NewCommandService(store, nil, manager)
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(t.TempDir(), "fixture")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	add := "/mcp add fixture --cwd " + cwd + " -- " + executable + " -test.run=^TestMCPStdioFixtureProcess$"
	if result, err := commands.Execute(context.Background(), chat.ID, branch.ID, add); err != nil || !strings.Contains(result.Status, "added MCP server \"fixture\"") {
		t.Fatalf("add command = %#v, %v", result, err)
	}
	listed, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp list")
	if err != nil || !strings.Contains(listed.Status, "fixture — disabled") {
		t.Fatalf("list before connect = %#v, %v", listed, err)
	}
	if len(manager.Tools()) != 0 {
		t.Fatal("/mcp list started the configured process")
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp connect fixture"); err != nil {
		t.Fatalf("connect command: %v", err)
	}
	listed, err = commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp list")
	if err != nil || !strings.Contains(listed.Status, "fixture_tool") || !strings.Contains(listed.Status, "connected") {
		t.Fatalf("list after connect = %#v, %v", listed, err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp disconnect fixture"); err != nil {
		t.Fatalf("disconnect command: %v", err)
	}
	listed, err = commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp list")
	if err != nil || !strings.Contains(listed.Status, "fixture — disabled") || strings.Contains(listed.Status, "fixture_tool") {
		t.Fatalf("list after disconnect = %#v, %v", listed, err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp remove fixture"); err != nil {
		t.Fatalf("remove command: %v", err)
	}
	listed, err = commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp list")
	if err != nil || listed.Status != "no MCP servers configured" {
		t.Fatalf("list after remove = %#v, %v", listed, err)
	}
}

func TestMCPCommandsReportConnectFailureAndKeepArgumentsPrivate(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "failure.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	commands := NewCommandService(store, nil, manager)
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	privateArgument := "argument-must-not-be-shown"
	cwd := filepath.Join(t.TempDir(), "failure")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	command := "/mcp add broken --cwd " + cwd + " -- /missing/mcp-server " + privateArgument
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, command); err != nil {
		t.Fatalf("add failure fixture: %v", err)
	}
	_, connectErr := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp connect broken")
	if connectErr == nil || !strings.Contains(connectErr.Error(), "connection failed") || strings.Contains(connectErr.Error(), privateArgument) {
		t.Fatalf("connect failure = %v; want safe failure status", connectErr)
	}
	listed, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/mcp list")
	if err != nil || !strings.Contains(listed.Status, "broken — disabled") {
		t.Fatalf("list after connect failure = %#v, %v", listed, err)
	}
	if strings.Contains(listed.Status, privateArgument) {
		t.Fatalf("server list exposed argv contents: %q", listed.Status)
	}
	configs, err := store.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || len(configs[0].Args) != 1 || configs[0].Args[0] != privateArgument {
		t.Fatalf("persisted argv = %#v", configs)
	}
}

func TestAppRendersPromptAndToolEventsForFailedAndSuccessfulTurns(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profile := providerProfiles["groq"]
	model := NewApp(store, NewAgent(store, nil, nil, profile), profile).(app)
	model.width = 120
	model.height = 40
	model.layout()
	model.submittedInput = "Составь план и отправь его"
	model.toolEvents = []ToolEvent{{Name: "build_review_plan", ServerName: "day19", Status: "completed"}, {Name: "send_message", ServerName: "telegram", Status: "failed"}}
	updated, _ := model.Update(turnFinishedMsg{result: TurnResult{ToolEvents: model.toolEvents}, err: context.DeadlineExceeded})
	model = updated.(app)
	view := model.viewport.View()
	for _, want := range []string{"Составь план и отправь его", "build_review_plan", "day19", "completed", "send_message", "telegram", "failed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("failed-turn view %q does not show %q", view, want)
		}
	}
	model.failedPrompt = ""
	model.lineage = []Message{{Role: "user", Content: "actual successful request"}, {Role: "assistant", Content: "Done"}}
	model.toolEvents = []ToolEvent{{Name: "find_due_questions", ServerName: "day19", Status: "completed (no matches)"}}
	model.refreshTranscript()
	view = model.viewport.View()
	for _, want := range []string{"actual successful request", "find_due_questions", "day19", "completed (no matches)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("successful-turn view %q does not show %q", view, want)
		}
	}
}

func TestAppSendPreservesToolEventsWhenAgentTurnFails(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "partial.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(t.TempDir(), "fixture")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "fixture", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Connect(context.Background(), "fixture"); err != nil {
		t.Fatalf("connect fixture: %v", err)
	}
	profile := ProviderProfile{Name: "test", Model: "test", ContextWindow: 10000, MainMax: 1000, AuxiliaryMax: 1000}
	agent := NewAgent(store, &toolCallTestCompleter{responses: []Completion{toolCall("failed-call", "fixture_tool", `{"unexpected":"private body"}`)}}, agentTestCounter{tokens: 1, label: "test"}, profile)
	agent.AttachMCPManager(manager)
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	model := app{store: store, agent: agent, chat: chat, branch: branch}
	message := model.send("request that partially called a tool")().(turnFinishedMsg)
	if message.err == nil || len(message.result.ToolEvents) != 1 || message.result.ToolEvents[0].Name != "fixture_tool" || message.result.ToolEvents[0].Status != "failed" {
		t.Fatalf("failed send result = %#v, error %v", message.result, message.err)
	}
}
