package main

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAppQuitCommandLifecycle(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "app.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile := ProviderProfile{Name: "synthetic-provider", Model: "synthetic-model"}
	model := NewApp(store, NewAgent(store, &agentTestCompleter{}, agentTestCounter{}, profile), profile).(app)
	model.input.SetValue("/quit")

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(app)
	if !model.busy {
		t.Fatal("/quit submission must enter the busy state")
	}

	message := model.executeCommand(model.submittedInput)()
	finished, ok := message.(commandFinishedMsg)
	if !ok {
		t.Fatalf("command message = %T, want commandFinishedMsg", message)
	}
	if finished.err != nil || !finished.result.Quit {
		t.Fatalf("/quit result = (%+v, %v), want successful quit", finished.result, finished.err)
	}

	updated, quit := model.Update(finished)
	model = updated.(app)
	if model.busy {
		t.Fatal("app must leave the busy state when /quit finishes")
	}
	if quit == nil {
		t.Fatal("/quit completion must return a quit command")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("/quit command message = %T, want tea.QuitMsg", quit())
	}

	model.input.SetValue("/quit now")
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(app)
	message = model.executeCommand(model.submittedInput)()
	finished, ok = message.(commandFinishedMsg)
	if !ok {
		t.Fatalf("invalid command message = %T, want commandFinishedMsg", message)
	}
	if finished.err == nil {
		t.Fatal("/quit now must fail validation")
	}

	updated, active := model.Update(finished)
	model = updated.(app)
	if active != nil {
		t.Fatal("invalid command completion must not focus input before Escape")
	}
	if model.busy {
		t.Fatal("invalid command must leave the app active, not busy")
	}
	if model.input.Focused() {
		t.Fatal("invalid command result must not focus input before Escape")
	}
	if got := model.input.Value(); got != "/quit now" {
		t.Fatalf("input value = %q, want restored invalid command", got)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	model = updated.(app)
	view := model.View().Content
	if !strings.Contains(view, "Command failed.") || !strings.Contains(view, "usage: /quit") {
		t.Fatalf("visible command error = %q, want command failure and /quit usage", view)
	}
	if strings.Contains(view, "Transcript:") || strings.Contains(view, "Enter submit") {
		t.Fatalf("command result view must not reveal chat input surface: %q", view)
	}

	updated, focus := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(app)
	if focus == nil {
		t.Fatal("Escape from command result must return the input focus command")
	}
	if !model.input.Focused() {
		t.Fatal("Escape from command result must restore input focus")
	}
	if got := model.input.Value(); got != "/quit now" {
		t.Fatalf("input value after Escape = %q, want preserved invalid command", got)
	}
	if view := model.View().Content; !strings.Contains(view, "Transcript:") {
		t.Fatalf("chat view after Escape = %q, want transcript and input", view)
	}
}

func TestNewAppRestoresPersistedActiveTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active conversation: %v", err)
	}
	branch, err = store.SetBranchMode(branch.ID, strategySliding)
	if err != nil {
		t.Fatalf("set branch mode: %v", err)
	}
	branch, err = store.SetWindow(branch.ID, 3)
	if err != nil {
		t.Fatalf("set branch window: %v", err)
	}
	if _, err := store.SaveTurn(SaveTurnInput{
		ChatID:           chat.ID,
		BranchID:         branch.ID,
		UserContent:      "persisted synthetic question",
		AssistantContent: "persisted synthetic answer",
	}); err != nil {
		t.Fatalf("save synthetic turn: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	profile := ProviderProfile{Name: "synthetic-provider", Model: "synthetic-model"}
	completer := &agentTestCompleter{}
	model := NewApp(reopened, NewAgent(reopened, completer, agentTestCounter{}, profile), profile).(app)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	model = updated.(app)

	if model.chat.ID != chat.ID || model.branch.ID != branch.ID {
		t.Fatalf("active conversation = chat %d branch %d, want chat %d branch %d", model.chat.ID, model.branch.ID, chat.ID, branch.ID)
	}
	if model.branch.Strategy != strategySliding || model.branch.WindowSize != 3 {
		t.Fatalf("restored branch settings = (%q, %d), want (sliding, 3)", model.branch.Strategy, model.branch.WindowSize)
	}
	if got := model.viewport.View(); !strings.Contains(got, "persisted synthetic question") || !strings.Contains(got, "persisted synthetic answer") {
		t.Fatalf("visible transcript = %q, want persisted turn", got)
	}
	view := model.View().Content
	if !strings.Contains(view, "main · sliding") || !strings.Contains(view, "window 3") {
		t.Fatalf("visible active branch settings = %q, want main sliding window 3", view)
	}
	if len(completer.calls) != 0 {
		t.Fatalf("NewApp completion calls = %d, want 0", len(completer.calls))
	}
}
