package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

type fixedTokenCounter struct{ tokens int }

func (c fixedTokenCounter) Count(string) int { return c.tokens }
func (fixedTokenCounter) Label() string      { return "fixed" }
func (fixedTokenCounter) Exact() bool        { return true }

func TestStoreReopenRestoresState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	branch, err = store.SetBranchMode(branch.ID, strategySummary)
	if err != nil {
		t.Fatalf("set branch mode: %v", err)
	}
	branch, err = store.SetWindow(branch.ID, 2)
	if err != nil {
		t.Fatalf("set window: %v", err)
	}
	assistant := saveTestTurn(t, store, chat.ID, branch.ID, "persisted question", "persisted answer")
	if err := store.SaveSummary(branch.ID, SummaryUpdate{
		ThroughMessageID: assistant.ID,
		Content:          "persisted summary",
	}); err != nil {
		t.Fatalf("save summary: %v", err)
	}
	if err := store.ReplaceFacts(branch.ID, map[string]string{"topic": "persistence"}); err != nil {
		t.Fatalf("replace facts: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	gotChat, gotBranch, err := reopened.ActiveChat()
	if err != nil {
		t.Fatalf("load reopened active chat: %v", err)
	}
	if gotChat.ID != chat.ID || gotBranch.ID != branch.ID {
		t.Fatalf("reopened active chat = (%d, %d), want (%d, %d)", gotChat.ID, gotBranch.ID, chat.ID, branch.ID)
	}
	if gotBranch.Strategy != strategySummary || gotBranch.WindowSize != 2 {
		t.Fatalf("reopened branch settings = (%q, %d), want (%q, %d)", gotBranch.Strategy, gotBranch.WindowSize, strategySummary, 2)
	}

	lineage, err := reopened.Lineage(gotBranch.ID)
	if err != nil {
		t.Fatalf("load reopened lineage: %v", err)
	}
	requireMessageSequence(t, lineage, []CompletionMessage{
		{Role: "user", Content: "persisted question"},
		{Role: "assistant", Content: "persisted answer"},
	})

	summary, err := reopened.Summary(gotBranch.ID)
	if err != nil {
		t.Fatalf("load reopened summary: %v", err)
	}
	if summary == nil || summary.ThroughMessageID != assistant.ID || summary.Content != "persisted summary" {
		t.Fatalf("reopened summary = %#v, want persisted summary through %d", summary, assistant.ID)
	}
	facts, err := reopened.Facts(gotBranch.ID)
	if err != nil {
		t.Fatalf("load reopened facts: %v", err)
	}
	if !reflect.DeepEqual(facts, []Fact{{Key: "topic", Value: "persistence"}}) {
		t.Fatalf("reopened facts = %#v, want persisted fact", facts)
	}
}

func TestSummaryCommandShowsStoredSummary(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "chat.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	commands := NewCommandService(store, nil)

	result, err := commands.Execute(t.Context(), chat.ID, branch.ID, "/summary")
	if err != nil {
		t.Fatalf("execute /summary: %v", err)
	}
	if got, want := result.Status, "summary:\n(no summary)"; got != want {
		t.Fatalf("empty summary status = %q, want %q", got, want)
	}

	assistant := saveTestTurn(t, store, chat.ID, branch.ID, "вопрос", "ответ")
	if err := store.SaveSummary(branch.ID, SummaryUpdate{
		ThroughMessageID: assistant.ID,
		Content:          "цель: собрать ТЗ",
	}); err != nil {
		t.Fatalf("save summary: %v", err)
	}

	result, err = commands.Execute(t.Context(), chat.ID, branch.ID, "/summary")
	if err != nil {
		t.Fatalf("execute /summary after save: %v", err)
	}
	want := fmt.Sprintf("summary (through message %d):\nцель: собрать ТЗ", assistant.ID)
	if result.Status != want {
		t.Fatalf("summary status = %q, want %q", result.Status, want)
	}
}

func TestCheckpointForkSharesPrefixAndIsolatesSuffix(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "chat.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	branch, err = store.SetBranchMode(branch.ID, strategyBranching)
	if err != nil {
		t.Fatalf("set branching mode: %v", err)
	}
	saveTestTurn(t, store, chat.ID, branch.ID, "prefix question", "prefix answer")
	if _, err := store.CreateCheckpoint(branch.ID, "prefix"); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	saveTestTurn(t, store, chat.ID, branch.ID, "source question", "source answer")

	fork, err := store.Fork(branch.ID, "prefix", "alternative")
	if err != nil {
		t.Fatalf("fork checkpoint: %v", err)
	}
	saveTestTurn(t, store, chat.ID, fork.ID, "fork question", "fork answer")

	sourceLineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load source lineage: %v", err)
	}
	forkLineage, err := store.Lineage(fork.ID)
	if err != nil {
		t.Fatalf("load fork lineage: %v", err)
	}
	requireMessageSequence(t, sourceLineage, []CompletionMessage{
		{Role: "user", Content: "prefix question"},
		{Role: "assistant", Content: "prefix answer"},
		{Role: "user", Content: "source question"},
		{Role: "assistant", Content: "source answer"},
	})
	requireMessageSequence(t, forkLineage, []CompletionMessage{
		{Role: "user", Content: "prefix question"},
		{Role: "assistant", Content: "prefix answer"},
		{Role: "user", Content: "fork question"},
		{Role: "assistant", Content: "fork answer"},
	})
	for i := range 2 {
		if sourceLineage[i].ID != forkLineage[i].ID {
			t.Fatalf("message %d in shared prefix has IDs %d and %d", i, sourceLineage[i].ID, forkLineage[i].ID)
		}
	}
	if sourceLineage[2].ID == forkLineage[2].ID || sourceLineage[3].ID == forkLineage[3].ID {
		t.Fatalf("fork suffix reused source message IDs: source=%v fork=%v", sourceLineage[2:], forkLineage[2:])
	}
}

func TestBuildMainPromptSelectsExactContextAtWindowBoundary(t *testing.T) {
	lineage := []Message{
		{ID: 1, Role: "user", Content: "first"},
		{ID: 2, Role: "assistant", Content: "second"},
		{ID: 3, Role: "user", Content: "third"},
	}
	summary := &Summary{ThroughMessageID: 1, Content: "earlier conversation"}
	facts := map[string]string{"zebra": "last", "apple": "first"}
	base := CompletionMessage{Role: "system", Content: BaseSystemMessage}
	input := CompletionMessage{Role: "user", Content: "next"}

	tests := []struct {
		name     string
		strategy string
		want     []CompletionMessage
	}{
		{
			name:     "full",
			strategy: strategyFull,
			want: []CompletionMessage{
				base,
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
				input,
			},
		},
		{
			name:     "sliding",
			strategy: strategySliding,
			want: []CompletionMessage{
				base,
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
				input,
			},
		},
		{
			name:     "summary",
			strategy: strategySummary,
			want: []CompletionMessage{
				base,
				{Role: "system", Content: "Краткое резюме предыдущего диалога:\n" + summary.Content},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
				input,
			},
		},
		{
			name:     "facts",
			strategy: strategyFacts,
			want: []CompletionMessage{
				base,
				{Role: "system", Content: "Важные факты:\napple: first\nzebra: last"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
				input,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := BuildMainPrompt(
				Branch{Strategy: test.strategy, WindowSize: 2},
				lineage,
				summary,
				facts,
				input.Content,
			)
			if err != nil {
				t.Fatalf("build prompt: %v", err)
			}
			if !reflect.DeepEqual(prompt, test.want) {
				t.Fatalf("prompt = %#v, want %#v", prompt, test.want)
			}
		})
	}
}

func TestParseFactsJSONRejectsMalformedInput(t *testing.T) {
	for _, content := range []string{
		"{",
		"[]",
		`{"facts": []}`,
		`{"facts": {"count": 1}}`,
		`{"facts": {}, "extra": true}`,
		`{"facts": {}} trailing`,
	} {
		t.Run(content, func(t *testing.T) {
			if _, err := ParseFactsJSON(content); err == nil {
				t.Fatalf("ParseFactsJSON(%q) succeeded", content)
			}
		})
	}
}

func TestCheckContextOverflowRejectsSuppliedProfileAndCounter(t *testing.T) {
	err := CheckContextOverflow(
		fixedTokenCounter{tokens: 3},
		ProviderProfile{ContextWindow: 11, MainMax: 3},
		[]CompletionMessage{
			{Role: "system", Content: "context"},
			{Role: "user", Content: "input"},
		},
	)
	if err == nil {
		t.Fatal("CheckContextOverflow accepted prompt exceeding supplied context window")
	}
}

func saveTestTurn(t *testing.T, store *Store, chatID, branchID int64, user, assistant string) Message {
	t.Helper()
	message, err := store.SaveTurn(SaveTurnInput{
		ChatID:           chatID,
		BranchID:         branchID,
		UserContent:      user,
		AssistantContent: assistant,
	})
	if err != nil {
		t.Fatalf("save turn: %v", err)
	}
	return message
}

func requireMessageSequence(t *testing.T, got []Message, want []CompletionMessage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("lineage length = %d, want %d", len(got), len(want))
	}
	for i, wantMessage := range want {
		if got[i].Role != wantMessage.Role || got[i].Content != wantMessage.Content {
			t.Fatalf("lineage[%d] = (%q, %q), want (%q, %q)", i, got[i].Role, got[i].Content, wantMessage.Role, wantMessage.Content)
		}
	}
}
