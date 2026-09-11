package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAppInputTransitions(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, app)
	}{
		{
			name: "unicode text input",
			run: func(t *testing.T, model app) {
				const input = "Привет, 世界 👋"
				model, _ = updateInputTestApp(t, model, inputTestTextKey(input))
				if got := model.input.Value(); got != input {
					t.Fatalf("input value = %q, want %q", got, input)
				}
			},
		},
		{
			name: "Ctrl+J inserts newline",
			run: func(t *testing.T, model app) {
				model.input.SetValue("first")
				model, command := updateInputTestApp(t, model, inputTestCtrlJKey())
				if command != nil {
					t.Fatal("Ctrl+J returned a command")
				}
				if got, want := model.input.Value(), "first\n"; got != want {
					t.Fatalf("input value = %q, want %q", got, want)
				}
			},
		},
		{
			name: "empty Enter does not submit",
			run: func(t *testing.T, model app) {
				model, command := updateInputTestApp(t, model, inputTestEnterKey())
				if command != nil {
					t.Fatal("empty Enter returned a command")
				}
				if model.busy {
					t.Fatal("empty Enter set app busy")
				}
				if !model.input.Focused() {
					t.Fatal("empty Enter unfocused input")
				}
				if got := model.View().Content; !strings.Contains(got, "Enter a message or slash command.") {
					t.Fatalf("view = %q, want empty-input notice", got)
				}
			},
		},
		{
			name: "message submit clears and blurs before completion",
			run: func(t *testing.T, model app) {
				const input = "send this"
				model.input.SetValue(input)
				model, command := updateInputTestApp(t, model, inputTestEnterKey())
				if command == nil {
					t.Fatal("message submit did not return completion command")
				}
				if !model.busy {
					t.Fatal("message submit did not set app busy")
				}
				if model.input.Focused() {
					t.Fatal("message submit left input focused")
				}
				if got := model.input.Value(); got != "" {
					t.Fatalf("input value = %q, want cleared", got)
				}
				if got := model.submittedInput; got != input {
					t.Fatalf("submitted input = %q, want %q", got, input)
				}
				if got := model.View().Content; !strings.Contains(got, "Waiting for response...") {
					t.Fatalf("view = %q, want waiting status", got)
				}
			},
		},
		{
			name: "busy state blocks input while allowing transcript paging",
			run: func(t *testing.T, model app) {
				model.busy = true
				model.input.SetValue("unchanged")
				model.viewport.SetContent(strings.Repeat("transcript line\n", 40))
				model.viewport.GotoBottom()
				atBottom := model.viewport.View()

				model, _ = updateInputTestApp(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
				if got := model.viewport.View(); got == atBottom {
					t.Fatal("PgUp did not page the transcript while busy")
				}

				model, _ = updateInputTestApp(t, model, inputTestTextKey(" blocked"))
				model, _ = updateInputTestApp(t, model, inputTestEnterKey())
				model, _ = updateInputTestApp(t, model, inputTestCtrlJKey())
				if got, want := model.input.Value(), "unchanged"; got != want {
					t.Fatalf("busy input value = %q, want %q", got, want)
				}
				if !model.busy {
					t.Fatal("busy state ended after blocked input")
				}
			},
		},
		{
			name: "successful turn restores focus",
			run: func(t *testing.T, model app) {
				model.busy = true
				model.submittedInput = "sent message"
				lineage := []Message{{Role: "user", Content: "sent message"}, {Role: "assistant", Content: "synthetic answer"}}
				model, command := updateInputTestApp(t, model, turnFinishedMsg{
					result:  TurnResult{Metrics: "synthetic metrics"},
					lineage: lineage,
					chat:    model.chat,
					branch:  model.branch,
				})
				if command == nil {
					t.Fatal("successful turn did not return focus command")
				}
				if model.busy {
					t.Fatal("successful turn left app busy")
				}
				if !model.input.Focused() {
					t.Fatal("successful turn did not restore input focus")
				}
				if got := model.input.Value(); got != "" {
					t.Fatalf("input value = %q, want cleared after success", got)
				}
				if got := model.View().Content; !strings.Contains(got, "Response received.") || !strings.Contains(got, "synthetic answer") {
					t.Fatalf("view = %q, want response status and transcript", got)
				}
			},
		},
		{
			name: "failed turn restores focus and text",
			run: func(t *testing.T, model app) {
				model.busy = true
				model.submittedInput = "retry this"
				model.input.Blur()
				model, command := updateInputTestApp(t, model, turnFinishedMsg{err: errors.New("synthetic request failure")})
				if command == nil {
					t.Fatal("failed turn did not return focus command")
				}
				if !model.input.Focused() {
					t.Fatal("failed turn did not restore input focus")
				}
				if got, want := model.input.Value(), "retry this"; got != want {
					t.Fatalf("input value = %q, want %q", got, want)
				}
				if got := model.View().Content; !strings.Contains(got, "Request failed.") || !strings.Contains(got, "synthetic request failure") {
					t.Fatalf("view = %q, want request error", got)
				}
			},
		},
		{
			name: "slash validation error is visible",
			run: func(t *testing.T, model app) {
				result, err := model.commands.Execute(context.Background(), model.chat.ID, model.branch.ID, "/new")
				if err == nil {
					t.Fatal("invalid slash command returned nil error")
				}
				model.busy = true
				model.input.Blur()
				model, command := updateInputTestApp(t, model, commandFinishedMsg{result: result, err: err})
				if command == nil {
					t.Fatal("invalid slash command did not return focus command")
				}
				if !model.input.Focused() {
					t.Fatal("invalid slash command did not restore input focus")
				}
				if got := model.View().Content; !strings.Contains(got, "Command failed.") || !strings.Contains(got, "usage: /new <title>") {
					t.Fatalf("view = %q, want command validation error", got)
				}
			},
		},
		{
			name: "invalid command preserves submitted text",
			run: func(t *testing.T, model app) {
				const input = "/quit later"
				model.input.SetValue(input)
				model, command := updateInputTestApp(t, model, inputTestEnterKey())
				if command == nil || !model.busy {
					t.Fatal("command submit did not enter busy state")
				}

				result, err := model.commands.Execute(context.Background(), model.chat.ID, model.branch.ID, input)
				if err == nil {
					t.Fatal("invalid command returned nil error")
				}
				model, command = updateInputTestApp(t, model, commandFinishedMsg{result: result, err: err})
				if command == nil {
					t.Fatal("invalid command did not return focus command")
				}
				if !model.input.Focused() {
					t.Fatal("invalid command did not restore input focus")
				}
				if got := model.input.Value(); got != input {
					t.Fatalf("input value = %q, want preserved invalid command %q", got, input)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, newInputTestApp(t))
		})
	}
}

func newInputTestApp(t *testing.T) app {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "app-input.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile := providerProfiles["groq"]
	completer := &agentTestCompleter{completion: Completion{Content: "synthetic answer"}}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, profile)
	model := NewApp(store, agent, profile).(app)
	model, _ = updateInputTestApp(t, model, tea.WindowSizeMsg{Width: 72, Height: 16})
	return model
}

func updateInputTestApp(t *testing.T, model app, msg tea.Msg) (app, tea.Cmd) {
	t.Helper()

	updated, command := model.Update(msg)
	result, ok := updated.(app)
	if !ok {
		t.Fatalf("updated model type = %T, want app", updated)
	}
	return result, command
}

func inputTestTextKey(text string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: text})
}

func inputTestEnterKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
}

func inputTestCtrlJKey() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl})
}
