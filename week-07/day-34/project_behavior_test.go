package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/VaSeWS/ai-advent-challenge/week-07/day-34/workspace"
)

func TestProjectReadFileOutputReachesModelWithoutSanitizingSource(t *testing.T) {
	root := t.TempDir()
	source := "# content\n\nThe key token in this source is ordinary documentation.\n"
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	completer := &toolCallTestCompleter{responses: []Completion{
		toolCall("read-source", "project_read_file", `{"path":"README.md"}`),
		{Content: "The source says: " + source},
		{Content: `{}`},
	}}
	profile := ProviderProfile{Name: "synthetic", Model: "synthetic", ContextWindow: 8192, MainMax: 512}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, profile)
	agent.mcp = manager
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "Read README.md and summarize it."); err != nil {
		t.Fatalf("send turn with project_read_file: %v", err)
	}
	var mainRequests []CompletionRequest
	for _, request := range completer.requests {
		if request.Kind == "main" {
			mainRequests = append(mainRequests, request)
		}
	}
	if len(mainRequests) != 2 {
		t.Fatalf("main completion requests = %d, want tool request and final request", len(mainRequests))
	}
	var toolMessages []CompletionMessage
	for _, message := range mainRequests[1].Messages {
		if message.Role == "tool" && message.ToolCallID == "read-source" {
			toolMessages = append(toolMessages, message)
		}
	}
	if len(toolMessages) != 1 {
		t.Fatalf("final model request tool messages = %#v, want exactly the project_read_file result", toolMessages)
	}
	var result struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(toolMessages[0].Content), &result); err != nil {
		t.Fatalf("decode project read result %q: %v", toolMessages[0].Content, err)
	}
	if result.Path != "README.md" || result.Content != source || result.Truncated {
		t.Fatalf("model received project read result %#v, want complete source content", result)
	}
	if got := mainRequests[1].Messages[len(mainRequests[1].Messages)-1]; got.Role != "tool" || got.ToolCallID != "read-source" {
		t.Fatalf("last message to final completion = %#v, want project read tool output", got)
	}
}

func TestLegacyMCPOutputSanitizerStillRemovesSensitiveFields(t *testing.T) {
	got, structured, err := safeToolOutput("external_tool", map[string]any{
		"content": "private payload",
		"nested":  map[string]any{"api_key": "secret", "safe": "kept"},
	})
	if err != nil || !structured {
		t.Fatalf("structured=%t err=%v", structured, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("decode sanitized output %s: %v", got, err)
	}
	if _, ok := decoded["content"]; ok {
		t.Fatalf("legacy MCP content field was not sanitized: %s", got)
	}
	nested, ok := decoded["nested"].(map[string]any)
	if !ok || len(nested) != 1 || nested["safe"] != "kept" {
		t.Fatalf("legacy MCP nested output = %#v, want only safe field", decoded["nested"])
	}
}

func TestProjectDiffRequiresUnchangedApplyApprovalBeforeWriting(t *testing.T) {
	model, project, root, original := newProjectBehaviorFixture(t)
	stageProjectContent(t, project, "# First approved candidate\n")
	assertProjectCommand(t, &model, "/confirm")
	assertProjectDisk(t, root, original)
	assertProjectDiff(t, project, true)

	assertProjectCommand(t, &model, "/apply")
	assertProjectDisk(t, root, original)
	stageProjectContent(t, project, "# Changed after preview\n")
	assertProjectCommand(t, &model, "/confirm")
	assertProjectDisk(t, root, original)
	assertProjectDiff(t, project, true)

	assertProjectCommand(t, &model, "/apply")
	assertProjectDisk(t, root, original)
	assertProjectDiff(t, project, true)
	assertProjectCommand(t, &model, "/confirm")
	assertProjectDisk(t, root, "# Changed after preview\n")
	assertProjectDiff(t, project, false)
}

func TestApplyConflictIsVisibleWithoutLeavingResultScreen(t *testing.T) {
	model, project, root, _ := newProjectBehaviorFixture(t)
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	model = resized.(app)
	stageProjectContent(t, project, "# Candidate\n")
	assertProjectCommand(t, &model, "/apply")
	const external = "# External edit\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(external), 0o600); err != nil {
		t.Fatal(err)
	}
	model.input.SetValue("/confirm")
	updated, _ := model.submit()
	model = updated.(app)
	if model.screen != commandResultScreen || model.errText == "" ||
		!strings.Contains(model.View().Content, model.errText) {
		t.Fatalf("apply conflict not visible on result screen: %q", model.View().Content)
	}
	assertProjectDisk(t, root, external)
	assertProjectDiff(t, project, true)
}

func TestProjectRejectDiscardsStagedContentWithoutWriting(t *testing.T) {
	model, project, root, original := newProjectBehaviorFixture(t)
	stageProjectContent(t, project, "# Do not persist\n")
	assertProjectCommand(t, &model, "/reject")
	assertProjectDisk(t, root, original)
	assertProjectDiff(t, project, false)
}

func TestBusyCommandsAndCtrlCCannotLoseStagedProjectChanges(t *testing.T) {
	for _, command := range []string{"/apply", "/confirm", "/reject"} {
		t.Run(command, func(t *testing.T) {
			model, project, root, original := newProjectBehaviorFixture(t)
			stageProjectContent(t, project, "# Preserve while busy\n")
			if command == "/confirm" {
				assertProjectCommand(t, &model, "/apply")
			}
			before, err := project.Diff()
			if err != nil {
				t.Fatal(err)
			}
			model.busy = true
			model.input.SetValue(command)
			updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			model = updated.(app)
			after, err := project.Diff()
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("busy %s changed staged diff: before %q, after %q", command, before, after)
			}
			assertProjectDisk(t, root, original)
		})
	}

	t.Run("ctrl+c", func(t *testing.T) {
		model, project, root, original := newProjectBehaviorFixture(t)
		stageProjectContent(t, project, "# Preserve on attempted exit\n")
		before, err := project.Diff()
		if err != nil {
			t.Fatal(err)
		}
		model.busy = true
		updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
		model = updated.(app)
		if cmd != nil {
			t.Fatal("ctrl+c with staged changes returned a quit command")
		}
		after, err := project.Diff()
		if err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("ctrl+c changed staged diff: before %q, after %q", before, after)
		}
		assertProjectDisk(t, root, original)
	})

	t.Run("idle ctrl+c refuses pending changes until rejection", func(t *testing.T) {
		model, project, _, _ := newProjectBehaviorFixture(t)
		stageProjectContent(t, project, "# Reject before exit\n")
		updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
		model = updated.(app)
		if cmd != nil {
			t.Fatal("idle ctrl+c with pending changes returned a quit command")
		}
		assertProjectDiff(t, project, true)
		assertProjectCommand(t, &model, "/reject")
		assertProjectDiff(t, project, false)
		updated, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
		model = updated.(app)
		if cmd == nil {
			t.Fatal("idle ctrl+c with no pending changes returned no quit command")
		}
	})
}

func newProjectBehaviorFixture(t *testing.T) (app, *workspace.Workspace, string, string) {
	t.Helper()
	root := t.TempDir()
	original := "# Original\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(original), 0o600); err != nil {
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	model := NewAppWithProject(store, nil, providerProfiles["groq"], nil, nil, manager).(app)
	return model, project, root, original
}

func stageProjectContent(t *testing.T, project *workspace.Workspace, content string) {
	t.Helper()
	if _, err := project.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Stage("README.md", content); err != nil {
		t.Fatal(err)
	}
}

func assertProjectCommand(t *testing.T, model *app, command string) {
	t.Helper()
	if handled, _, err := model.executeProjectCommand(command); !handled || err != nil {
		t.Fatalf("execute project command %q: handled=%t err=%v", command, handled, err)
	}
}

func assertProjectDisk(t *testing.T, root, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("README.md on disk = %q, want %q", got, want)
	}
}

func assertProjectDiff(t *testing.T, project *workspace.Workspace, wantPending bool) {
	t.Helper()
	diff, err := project.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if got := diff != ""; got != wantPending {
		t.Fatalf("pending diff = %t, want %t (diff %q)", got, wantPending, diff)
	}
}
