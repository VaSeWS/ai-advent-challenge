package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type inspectTestCompleter struct {
	apiKey string
	calls  []CompletionRequest
}

func (c *inspectTestCompleter) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	c.calls = append(c.calls, request)
	return Completion{Content: "unexpected completion"}, nil
}

type promptComparisonCompleter struct {
	calls []CompletionRequest
}

func (c *promptComparisonCompleter) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	c.calls = append(c.calls, request)
	switch request.Kind {
	case "facts":
		return Completion{Content: `{"facts":{"subject":"updated"}}`}, nil
	case "summary":
		return Completion{Content: "updated summary"}, nil
	case "memory":
		return Completion{Content: "{}"}, nil
	default:
		return Completion{Content: "main response"}, nil
	}
}

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
			name: "slash validation error opens a result screen",
			run: func(t *testing.T, model app) {
				result, err := model.commands.Execute(context.Background(), model.chat.ID, model.branch.ID, "/new")
				if err == nil {
					t.Fatal("invalid slash command returned nil error")
				}
				model.busy = true
				model.input.Blur()
				err = errors.New("usage: /new <title>\nERROR_DETAIL_MUST_NOT_BE_CLIPPED")
				model, _ = updateInputTestApp(t, model, commandFinishedMsg{result: result, err: err})

				view := model.View().Content
				for _, line := range []string{"usage: /new <title>", "ERROR_DETAIL_MUST_NOT_BE_CLIPPED"} {
					if !strings.Contains(view, line) {
						t.Fatalf("result view = %q, want command validation error line %q", view, line)
					}
				}
				for _, chatSurface := range []string{"Transcript:", "You>", "Chats"} {
					if strings.Contains(view, chatSurface) {
						t.Fatalf("result view = %q, unexpectedly rendered %q", view, chatSurface)
					}
				}
			},
		},
		{
			name: "invalid command is restored after leaving the result screen",
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
				model, _ = updateInputTestApp(t, model, commandFinishedMsg{result: result, err: err})
				if got := model.View().Content; !strings.Contains(got, "usage: /quit") {
					t.Fatalf("result view = %q, want command validation error", got)
				}

				model, _ = updateInputTestApp(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
				if !model.input.Focused() {
					t.Fatal("Escape did not restore input focus")
				}
				if got := model.input.Value(); got != input {
					t.Fatalf("input value after Escape = %q, want preserved invalid command %q", got, input)
				}
				if got := model.View().Content; !strings.Contains(got, "Transcript:") || !strings.Contains(got, "You>") {
					t.Fatalf("chat view after Escape = %q, want transcript and input", got)
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

func TestProfileCommandsPersistActiveSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	commands := NewCommandService(store, nil)
	for _, input := range []string{
		"/profile   create Ada | English | concise | bullets | no emojis",
		"/profile create Bela | Russian | detailed | text | cite sources",
		"/profile select 1",
		"/profile select 2",
	} {
		if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, input); err != nil {
			t.Fatalf("execute %q: %v", input, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	active, err := reopened.ActiveProfile()
	if err != nil {
		t.Fatalf("load active profile: %v", err)
	}
	if active == nil || active.Name != "Bela" {
		t.Fatalf("active profile after restart = %#v, want Bela", active)
	}
	chat, branch, err = reopened.ActiveChat()
	if err != nil {
		t.Fatalf("load reopened active chat: %v", err)
	}
	result, err := NewCommandService(reopened, nil).Execute(context.Background(), chat.ID, branch.ID, "/profile show")
	if err != nil {
		t.Fatalf("show profiles: %v", err)
	}
	for _, line := range []string{"  1: Ada", "* 2: Bela"} {
		if !strings.Contains(result.Status, line) {
			t.Fatalf("profile output = %q, want %q", result.Status, line)
		}
	}
}

func TestInspectCommandShowsLayersWithoutCompletion(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "inspect.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	profile, err := store.CreateProfile(ProfileInput{
		Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "bullets", Constraints: "no emojis",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{
		Goal: "Ship inspector", Plan: "Show every layer", CurrentStep: "Render prompt", ExpectedAction: "Review output",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "architecture", Key: "storage", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user",
	}); err != nil {
		t.Fatalf("add invariant: %v", err)
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKindDecision, "format", "bullets"); err != nil {
		t.Fatalf("save profile memory: %v", err)
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeTask, MemoryKindKnowledge, "schema-version", "SQLite"); err != nil {
		t.Fatalf("save task memory: %v", err)
	}
	if _, err := store.SaveTurn(SaveTurnInput{
		ChatID: chat.ID, BranchID: branch.ID, UserContent: "raw user message", AssistantContent: "raw assistant message",
	}); err != nil {
		t.Fatalf("save raw dialogue: %v", err)
	}

	completer := &inspectTestCompleter{apiKey: "api-key-must-not-appear"}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, providerProfiles["groq"])
	result, err := NewCommandService(store, agent).Execute(context.Background(), chat.ID, branch.ID, "/inspect candidate message")
	if err != nil {
		t.Fatalf("inspect prompt: %v", err)
	}
	if len(completer.calls) != 0 {
		t.Fatalf("inspect made %d completions, want none", len(completer.calls))
	}
	for _, fragment := range []string{
		"profile:\nАктивный профиль:\nname: Ada",
		"task:\nАктивная задача:\ngoal: Ship inspector",
		"invariants:\nАктивные invariants:\narchitecture.storage = SQLite; rule: Use SQLite.; source: user",
		"long-term memory:\nДолгосрочная память:\nprofile decision.format: bullets\ntask knowledge.schema-version: SQLite",
		"raw dialogue:\n1. user:\nraw user message\n2. assistant:\nraw assistant message",
		"1. system:\n" + BaseSystemMessage,
		"2. system:\nАктивные invariants:\narchitecture.storage = SQLite; rule: Use SQLite.; source: user",
		"3. system:\nАктивный профиль:\nname: Ada",
		"4. system:\nДолгосрочная память:\nprofile decision.format: bullets\ntask knowledge.schema-version: SQLite",
		"5. system:\nАктивная задача:\ngoal: Ship inspector",
		"6. user:\nraw user message",
		"7. assistant:\nraw assistant message",
		"8. user:\ncandidate message",
	} {
		if !strings.Contains(result.Status, fragment) {
			t.Fatalf("inspect output = %q, want %q", result.Status, fragment)
		}
	}
	if strings.Contains(result.Status, completer.apiKey) {
		t.Fatalf("inspect output leaked API key material: %q", result.Status)
	}
}

func TestInspectMainPromptMatchesSendBeforeStrategyUpdates(t *testing.T) {
	for _, strategy := range []string{strategySummary, strategyFacts} {
		t.Run(strategy, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), strategy+".sqlite"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			chat, branch, err := store.ActiveChat()
			if err != nil {
				t.Fatalf("load active chat: %v", err)
			}
			if _, err := store.SetBranchMode(branch.ID, strategy); err != nil {
				t.Fatalf("set mode: %v", err)
			}
			if strategy == strategySummary {
				if _, err := store.SetWindow(branch.ID, 1); err != nil {
					t.Fatalf("set summary window: %v", err)
				}
				for range 6 {
					if _, err := store.SaveTurn(SaveTurnInput{
						ChatID: chat.ID, BranchID: branch.ID, UserContent: "raw user", AssistantContent: "raw assistant",
					}); err != nil {
						t.Fatalf("save summary history: %v", err)
					}
				}
			}

			completer := &promptComparisonCompleter{}
			agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, ProviderProfile{
				Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8,
			})
			_, inspected, err := agent.InspectMainPrompt(chat.ID, branch.ID, "candidate")
			if err != nil {
				t.Fatalf("inspect main prompt: %v", err)
			}
			if len(completer.calls) != 0 {
				t.Fatalf("inspect made %d completions, want none", len(completer.calls))
			}
			if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "candidate"); err != nil {
				t.Fatalf("send: %v", err)
			}
			var sent []CompletionMessage
			for _, call := range completer.calls {
				if call.Kind == "main" {
					sent = call.Messages
					break
				}
			}
			if !reflect.DeepEqual(sent, inspected) {
				t.Fatalf("sent main prompt = %#v, want inspected %#v", sent, inspected)
			}
		})
	}
}

func TestAppTextareaUsesExplicitReadableStyles(t *testing.T) {
	model := newInputTestApp(t)
	styles := model.input.Styles()

	if reflect.DeepEqual(styles, textarea.New().Styles()) {
		t.Fatal("textarea uses its unreadable default styles")
	}
	for name, style := range map[string]lipgloss.Style{
		"focused text":   styles.Focused.Text,
		"focused prompt": styles.Focused.Prompt,
		"blurred text":   styles.Blurred.Text,
		"blurred prompt": styles.Blurred.Prompt,
	} {
		if _, unset := style.GetForeground().(lipgloss.NoColor); unset {
			t.Fatalf("%s has no explicit foreground color", name)
		}
	}
}

func TestAppSuccessfulCommandResultUsesFullscreenView(t *testing.T) {
	model := newInputTestApp(t)
	model, _ = updateInputTestApp(t, model, tea.WindowSizeMsg{Width: 100, Height: 16})
	model.lineage = []Message{{Role: "user", Content: "CHAT_TRANSCRIPT_MUST_STAY_HIDDEN"}}
	model.refreshTranscript()
	model.layout()

	const output = "RESULT_LINE_ONE\nRESULT_LINE_TWO\nRESULT_LINE_THREE\nRESULT_LINE_FOUR"
	model, _ = updateInputTestApp(t, model, commandFinishedMsg{
		result: CommandResult{Status: output},
	})

	view := model.View().Content
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(view, line) {
			t.Fatalf("result view = %q, want complete output line %q", view, line)
		}
	}
	for _, chatSurface := range []string{"CHAT_TRANSCRIPT_MUST_STAY_HIDDEN", "Transcript:", "You>", "Chats"} {
		if strings.Contains(view, chatSurface) {
			t.Fatalf("result view = %q, unexpectedly rendered %q", view, chatSurface)
		}
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

func TestAppTextareaHeightTracksEnteredLines(t *testing.T) {
	model := newInputTestApp(t)
	if got := model.input.Height(); got != 1 {
		t.Fatalf("initial input height = %d, want 1", got)
	}
	if prompts := strings.Count(model.View().Content, "You>"); prompts != 1 {
		t.Fatalf("initial view prompts = %d, want 1", prompts)
	}

	var cmd tea.Cmd
	model, cmd = updateInputTestApp(t, model, inputTestTextKey("/"))
	if cmd == nil {
		t.Fatal("text input must keep the textarea interactive")
	}
	if got := model.input.Height(); got != 1 {
		t.Fatalf("single-line input height = %d, want 1", got)
	}

	model, _ = updateInputTestApp(t, model, inputTestCtrlJKey())
	if got := model.input.Height(); got != 2 {
		t.Fatalf("two-line input height = %d, want 2", got)
	}

	model.input.SetValue("one\ntwo\nthree\nfour")
	model.layout()
	if got := model.input.Height(); got != inputHeight {
		t.Fatalf("overlong input height = %d, want max %d", got, inputHeight)
	}

	model.input.SetValue("")
	model.layout()
	if got := model.input.Height(); got != 1 {
		t.Fatalf("cleared input height = %d, want 1", got)
	}
}
