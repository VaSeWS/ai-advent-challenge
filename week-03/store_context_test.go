package main

import (
	"context"
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

func TestBuildMainPromptOrdersLayersBeforeEveryStrategy(t *testing.T) {
	profileID, taskID := int64(1), int64(2)
	state := PromptState{
		Lineage: []Message{
			{ID: 1, Role: "user", Content: "first"},
			{ID: 2, Role: "assistant", Content: "second"},
			{ID: 3, Role: "user", Content: "third"},
		},
		Summary: &Summary{ThroughMessageID: 1, Content: "earlier conversation"},
		Facts:   map[string]string{"zebra": "last", "apple": "first"},
		Invariants: []Invariant{
			{ID: 2, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user", Active: true},
			{ID: 1, Category: "architecture", Key: "transport", RequiredValue: "HTTP", Rule: "Use HTTP.", Source: "design", Active: true},
		},
		Profile: &Profile{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "bullets", Constraints: "no emojis"},
		LongTermMemories: []LongTermMemory{
			{ID: 3, Kind: MemoryKindKnowledge, Key: "schema-version", Value: "2", TaskID: &taskID},
			{ID: 1, Kind: MemoryKindDecision, Key: "format", Value: "bullets", ProfileID: &profileID},
			{ID: 2, Kind: MemoryKindDecision, Key: "storage", Value: "SQLite", TaskID: &taskID},
		},
		Task: &Task{Goal: "Ship parser", Plan: "Write strict decoder", Phase: TaskPhasePlanning, CurrentStep: "Implement parser", ExpectedAction: "Run offline tests", Paused: false},
	}
	base := CompletionMessage{Role: "system", Content: BaseSystemMessage}
	layers := []CompletionMessage{
		{Role: "system", Content: "Активные invariants:\narchitecture.transport = HTTP; rule: Use HTTP.; source: design\nstorage.database = SQLite; rule: Use SQLite.; source: user"},
		{Role: "system", Content: "Активный профиль:\nname: Ada\nlanguage: English\nresponse_style: concise\nresponse_format: bullets\nconstraints: no emojis"},
		{Role: "system", Content: "Долгосрочная память:\nprofile decision.format: bullets\ntask decision.storage: SQLite\ntask knowledge.schema-version: 2"},
		{Role: "system", Content: ActiveTaskSystemBlock(state.Task)},
	}
	input := CompletionMessage{Role: "user", Content: "next"}

	tests := []struct {
		name     string
		strategy string
		context  []CompletionMessage
	}{
		{
			name:     "full",
			strategy: strategyFull,
			context: []CompletionMessage{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
			},
		},
		{
			name:     "branching",
			strategy: strategyBranching,
			context: []CompletionMessage{
				{Role: "user", Content: "first"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
			},
		},
		{
			name:     "sliding",
			strategy: strategySliding,
			context: []CompletionMessage{
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
			},
		},
		{
			name:     "summary",
			strategy: strategySummary,
			context: []CompletionMessage{
				{Role: "system", Content: "Краткое резюме предыдущего диалога:\nearlier conversation"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
			},
		},
		{
			name:     "facts",
			strategy: strategyFacts,
			context: []CompletionMessage{
				{Role: "system", Content: "Важные факты:\napple: first\nzebra: last"},
				{Role: "assistant", Content: "second"},
				{Role: "user", Content: "third"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state.Branch = Branch{Strategy: test.strategy, WindowSize: 2}
			prompt, err := BuildMainPrompt(state, input.Content)
			if err != nil {
				t.Fatalf("build prompt: %v", err)
			}
			want := append([]CompletionMessage{base}, layers...)
			want = append(want, test.context...)
			want = append(want, input)
			if !reflect.DeepEqual(prompt, want) {
				t.Fatalf("prompt = %#v, want %#v", prompt, want)
			}
		})
	}
}

func TestBuildMainPromptOmitsAbsentLayersBeforeSummaryAndFacts(t *testing.T) {
	tests := []struct {
		name     string
		strategy string
		summary  *Summary
		facts    map[string]string
		want     []CompletionMessage
	}{
		{
			name:     "summary",
			strategy: strategySummary,
			summary:  &Summary{Content: "summary"},
			want: []CompletionMessage{
				{Role: "system", Content: BaseSystemMessage},
				{Role: "system", Content: "Краткое резюме предыдущего диалога:\nsummary"},
				{Role: "user", Content: "next"},
			},
		},
		{
			name:     "facts",
			strategy: strategyFacts,
			facts:    map[string]string{"topic": "context"},
			want: []CompletionMessage{
				{Role: "system", Content: BaseSystemMessage},
				{Role: "system", Content: "Важные факты:\ntopic: context"},
				{Role: "user", Content: "next"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt, err := BuildMainPrompt(PromptState{
				Branch:  Branch{Strategy: test.strategy, WindowSize: 2},
				Summary: test.summary,
				Facts:   test.facts,
			}, "next")
			if err != nil {
				t.Fatalf("build prompt: %v", err)
			}
			if !reflect.DeepEqual(prompt, test.want) {
				t.Fatalf("prompt = %#v, want %#v", prompt, test.want)
			}
		})
	}
}

func TestAgentSendLoadsActiveLayersIntoMainPrompt(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "layered-prompt.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "bullets", Constraints: "no emojis"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "Ship parser", Plan: "Write strict decoder", CurrentStep: "Implement parser", ExpectedAction: "Run offline tests"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user"}); err != nil {
		t.Fatalf("add invariant: %v", err)
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKindDecision, "format", "bullets"); err != nil {
		t.Fatalf("save profile memory: %v", err)
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeTask, MemoryKindKnowledge, "schema-version", "2"); err != nil {
		t.Fatalf("save task memory: %v", err)
	}

	completer := &agentTestCompleter{completion: Completion{Content: "answer"}}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "next"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(completer.calls) != 2 {
		t.Fatalf("completion calls = %d, want main and memory", len(completer.calls))
	}
	want := []CompletionMessage{
		{Role: "system", Content: BaseSystemMessage},
		{Role: "system", Content: "Активные invariants:\nstorage.database = SQLite; rule: Use SQLite.; source: user"},
		{Role: "system", Content: "Активный профиль:\nname: Ada\nlanguage: English\nresponse_style: concise\nresponse_format: bullets\nconstraints: no emojis"},
		{Role: "system", Content: "Долгосрочная память:\nprofile decision.format: bullets\ntask knowledge.schema-version: 2"},
		{Role: "system", Content: ActiveTaskSystemBlock(&task)},
		{Role: "user", Content: "next"},
	}
	if !reflect.DeepEqual(completer.calls[0].Messages, want) {
		t.Fatalf("main prompt = %#v, want %#v", completer.calls[0].Messages, want)
	}
}

func TestCheckContextOverflowCountsFinalLayeredPrompt(t *testing.T) {
	taskID := int64(2)
	prompt, err := BuildMainPrompt(PromptState{
		Branch:           Branch{Strategy: strategyFull, WindowSize: 1},
		Invariants:       []Invariant{{Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user", Active: true}},
		Profile:          &Profile{Name: "Ada"},
		LongTermMemories: []LongTermMemory{{Kind: MemoryKindDecision, Key: "storage", Value: "SQLite", TaskID: &taskID}},
		Task:             &Task{Goal: "Ship parser"},
	}, "next")
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if err := CheckContextOverflow(fixedTokenCounter{tokens: 1}, ProviderProfile{ContextWindow: 8, MainMax: 3}, prompt); err == nil {
		t.Fatal("CheckContextOverflow accepted final layered prompt exceeding context window")
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
