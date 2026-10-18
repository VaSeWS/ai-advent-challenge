package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type memoryAgentCompleter struct {
	completions []Completion
	errors      []error
	calls       []CompletionRequest
}

func (c *memoryAgentCompleter) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	c.calls = append(c.calls, request)
	index := len(c.calls) - 1
	if index < len(c.errors) && c.errors[index] != nil {
		return Completion{}, c.errors[index]
	}
	return c.completions[index], nil
}

type memoryAgentCounter struct{}

func (memoryAgentCounter) Count(string) int { return 1 }
func (memoryAgentCounter) Label() string    { return "synthetic" }
func (memoryAgentCounter) Exact() bool      { return true }

func TestAgentSendAtomicallyPersistsMemoryExtraction(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory-agent.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "old goal", Plan: "old plan", CurrentStep: "old step", ExpectedAction: "old action"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user"}); err != nil {
		t.Fatalf("add invariant: %v", err)
	}

	completer := &memoryAgentCompleter{completions: []Completion{
		{Content: "main response"},
		{Content: `{"profile":{"response_format":"bullets"},"task":{"goal":"new goal"},"records":[{"scope":"profile","kind":"decision","key":"format","value":"bullets"},{"scope":"task","kind":"knowledge","key":"database","value":"SQLite"}]}`},
	}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "remember this")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Assistant.Content != "main response" {
		t.Fatalf("assistant response = %q, want main response", result.Assistant.Content)
	}
	if len(completer.calls) != 2 || completer.calls[0].Kind != "main" || completer.calls[1].Kind != "memory" {
		t.Fatalf("completion call kinds = %#v, want main then memory", completionKinds(completer.calls))
	}

	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].Content != "remember this" || lineage[1].Content != "main response" {
		t.Fatalf("persisted raw turn = %#v, want user and main response", lineage)
	}
	updatedProfile, err := store.Profile(profile.ID)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if updatedProfile.ResponseFormat != "bullets" {
		t.Fatalf("profile response format = %q, want bullets", updatedProfile.ResponseFormat)
	}
	updatedTask, err := store.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updatedTask == nil || updatedTask.Goal != "new goal" {
		t.Fatalf("active task = %#v, want updated goal", updatedTask)
	}
	profileMemories, err := store.ProfileMemories(profile.ID)
	if err != nil {
		t.Fatalf("load profile memories: %v", err)
	}
	taskMemories, err := store.TaskMemories(task.ID)
	if err != nil {
		t.Fatalf("load task memories: %v", err)
	}
	if len(profileMemories) != 1 || profileMemories[0].Key != "format" || len(taskMemories) != 1 || taskMemories[0].Key != "database" {
		t.Fatalf("persisted memories = profile %#v task %#v", profileMemories, taskMemories)
	}
	stats, err := store.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load stats: %v", err)
	}
	if len(stats) != 2 || statCalls(stats, "main") != 1 || statCalls(stats, "memory") != 1 {
		t.Fatalf("accounting rows = %#v, want one main and one memory", stats)
	}
}

func TestAgentSendRoutesAutomaticMemoryToActiveOwnersAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory-routing.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, mainBranch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	mainTask, err := store.CreateTask(mainBranch.ID, TaskSnapshot{Goal: "main old goal", Plan: "main old plan", CurrentStep: "main old step", ExpectedAction: "main old action", Paused: true})
	if err != nil {
		t.Fatalf("create main task: %v", err)
	}
	if _, err := store.AddInvariant(Invariant{TaskID: mainTask.ID, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user"}); err != nil {
		t.Fatalf("add main invariant: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET phase = ? WHERE id = ?`, TaskPhaseExecution, mainTask.ID); err != nil {
		t.Fatalf("set main task phase: %v", err)
	}
	if _, err := store.SetBranchMode(mainBranch.ID, strategyBranching); err != nil {
		t.Fatalf("set branching mode: %v", err)
	}
	if _, err := store.CreateCheckpoint(mainBranch.ID, "start"); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	forkBranch, err := store.Fork(mainBranch.ID, "start", "fork")
	if err != nil {
		t.Fatalf("fork branch: %v", err)
	}
	forkTask, err := store.CreateTask(forkBranch.ID, TaskSnapshot{Goal: "fork old goal", Plan: "fork old plan", CurrentStep: "fork old step", ExpectedAction: "fork old action"})
	if err != nil {
		t.Fatalf("create fork task: %v", err)
	}

	alpha, err := store.CreateProfile(ProfileInput{Name: "alpha", Language: "English", ResponseStyle: "concise", ResponseFormat: "text", Constraints: "none"})
	if err != nil {
		t.Fatalf("create alpha profile: %v", err)
	}
	beta, err := store.CreateProfile(ProfileInput{Name: "beta", Language: "Finnish", ResponseStyle: "brief", ResponseFormat: "plain", Constraints: "no markdown"})
	if err != nil {
		t.Fatalf("create beta profile: %v", err)
	}
	if _, err := store.SelectProfile(alpha.ID); err != nil {
		t.Fatalf("select alpha profile: %v", err)
	}

	completer := &memoryAgentCompleter{completions: []Completion{
		{Content: "main response"},
		{Content: `{"profile":{"language":"Russian","response_style":"detailed","response_format":"bullets","constraints":"cite sources"},"task":{"goal":"main goal","plan":"main plan","current_step":"main step","expected_action":"main action"},"records":[{"scope":"profile","kind":"decision","key":"shared","value":"alpha profile decision"},{"scope":"profile","kind":"knowledge","key":"shared","value":"alpha profile knowledge"},{"scope":"task","kind":"decision","key":"shared","value":"main task decision"},{"scope":"task","kind":"knowledge","key":"shared","value":"main task knowledge"}]}`},
		{Content: "fork response"},
		{Content: `{"profile":{"response_format":"table"},"task":{"plan":"fork plan"},"records":[{"scope":"profile","kind":"decision","key":"shared","value":"beta profile decision"},{"scope":"profile","kind":"knowledge","key":"shared","value":"beta profile knowledge"},{"scope":"task","kind":"decision","key":"shared","value":"fork task decision"},{"scope":"task","kind":"knowledge","key":"shared","value":"fork task knowledge"}]}`},
	}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	if _, err := agent.Send(context.Background(), chat.ID, mainBranch.ID, "update alpha"); err != nil {
		t.Fatalf("send alpha turn: %v", err)
	}
	if _, err := store.SelectProfile(beta.ID); err != nil {
		t.Fatalf("select beta profile: %v", err)
	}
	if _, err := agent.Send(context.Background(), chat.ID, forkBranch.ID, "update beta"); err != nil {
		t.Fatalf("send beta turn: %v", err)
	}
	if len(completer.calls) != 4 || completionKinds(completer.calls)[0] != "main" || completionKinds(completer.calls)[1] != "memory" || completionKinds(completer.calls)[2] != "main" || completionKinds(completer.calls)[3] != "memory" {
		t.Fatalf("completion call kinds = %#v, want main then memory for each turn", completionKinds(completer.calls))
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	updatedAlpha, err := store.Profile(alpha.ID)
	if err != nil {
		t.Fatalf("load alpha profile: %v", err)
	}
	if updatedAlpha.Language != "Russian" || updatedAlpha.ResponseStyle != "detailed" || updatedAlpha.ResponseFormat != "bullets" || updatedAlpha.Constraints != "cite sources" {
		t.Fatalf("alpha profile = %#v, want all routed fields updated", updatedAlpha)
	}
	updatedBeta, err := store.Profile(beta.ID)
	if err != nil {
		t.Fatalf("load beta profile: %v", err)
	}
	if updatedBeta.Language != "Finnish" || updatedBeta.ResponseStyle != "brief" || updatedBeta.ResponseFormat != "table" || updatedBeta.Constraints != "no markdown" {
		t.Fatalf("beta profile = %#v, want only response format updated", updatedBeta)
	}
	updatedMainTask, err := store.ActiveTask(mainBranch.ID)
	if err != nil {
		t.Fatalf("load main task: %v", err)
	}
	if updatedMainTask == nil || updatedMainTask.Goal != "main goal" || updatedMainTask.Plan != "main plan" || updatedMainTask.CurrentStep != "main step" || updatedMainTask.ExpectedAction != "main action" || updatedMainTask.Phase != TaskPhaseExecution || !updatedMainTask.Paused {
		t.Fatalf("main task = %#v, want routed fields only", updatedMainTask)
	}
	updatedForkTask, err := store.ActiveTask(forkBranch.ID)
	if err != nil {
		t.Fatalf("load fork task: %v", err)
	}
	if updatedForkTask == nil || updatedForkTask.Goal != "fork old goal" || updatedForkTask.Plan != "fork plan" || updatedForkTask.CurrentStep != "fork old step" || updatedForkTask.ExpectedAction != "fork old action" {
		t.Fatalf("fork task = %#v, want only plan updated", updatedForkTask)
	}
	mainInvariants, err := store.ActiveInvariants(mainTask.ID)
	if err != nil {
		t.Fatalf("load main invariants: %v", err)
	}
	if len(mainInvariants) != 1 || mainInvariants[0].Category != "storage" || mainInvariants[0].Key != "database" || mainInvariants[0].RequiredValue != "SQLite" {
		t.Fatalf("main invariants = %#v, want unchanged invariant", mainInvariants)
	}

	expectMemories := func(name string, memories []LongTermMemory, ownerID int64, profileScoped bool, want map[MemoryKind]string) {
		t.Helper()
		if len(memories) != len(want) {
			t.Fatalf("%s memories = %#v, want %#v", name, memories, want)
		}
		for _, memory := range memories {
			if memory.Key != "shared" || memory.Value != want[memory.Kind] {
				t.Fatalf("%s memory = %#v, want shared values %#v", name, memory, want)
			}
			if profileScoped {
				if memory.ProfileID == nil || *memory.ProfileID != ownerID || memory.TaskID != nil {
					t.Fatalf("%s memory owner = %#v, want profile %d only", name, memory, ownerID)
				}
			} else if memory.TaskID == nil || *memory.TaskID != ownerID || memory.ProfileID != nil {
				t.Fatalf("%s memory owner = %#v, want task %d only", name, memory, ownerID)
			}
		}
	}
	alphaMemories, err := store.ProfileMemories(alpha.ID)
	if err != nil {
		t.Fatalf("load alpha memories: %v", err)
	}
	betaMemories, err := store.ProfileMemories(beta.ID)
	if err != nil {
		t.Fatalf("load beta memories: %v", err)
	}
	mainMemories, err := store.TaskMemories(mainTask.ID)
	if err != nil {
		t.Fatalf("load main task memories: %v", err)
	}
	forkMemories, err := store.TaskMemories(forkTask.ID)
	if err != nil {
		t.Fatalf("load fork task memories: %v", err)
	}
	expectMemories("alpha profile", alphaMemories, alpha.ID, true, map[MemoryKind]string{MemoryKindDecision: "alpha profile decision", MemoryKindKnowledge: "alpha profile knowledge"})
	expectMemories("beta profile", betaMemories, beta.ID, true, map[MemoryKind]string{MemoryKindDecision: "beta profile decision", MemoryKindKnowledge: "beta profile knowledge"})
	expectMemories("main task", mainMemories, mainTask.ID, false, map[MemoryKind]string{MemoryKindDecision: "main task decision", MemoryKindKnowledge: "main task knowledge"})
	expectMemories("fork task", forkMemories, forkTask.ID, false, map[MemoryKind]string{MemoryKindDecision: "fork task decision", MemoryKindKnowledge: "fork task knowledge"})

	mainLineage, err := store.Lineage(mainBranch.ID)
	if err != nil {
		t.Fatalf("load main lineage: %v", err)
	}
	forkLineage, err := store.Lineage(forkBranch.ID)
	if err != nil {
		t.Fatalf("load fork lineage: %v", err)
	}
	if len(mainLineage) != 2 || len(forkLineage) != 2 {
		t.Fatalf("reopened lineages = main %#v fork %#v, want one raw turn per branch", mainLineage, forkLineage)
	}
}

func TestAgentSendPersistsTurnAndValidMemoryAfterToolSideEffectWithoutTask(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory-routing-no-task.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text", Constraints: "none"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}

	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "save_plan")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1", "topic": "go", "due": "2026-09-28", "path": "Q1.md"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "# Review\nQ1", "count": 1}
	manager.results["save_plan"] = map[string]any{"path": "week-04/plan.md"}
	completer := &memoryAgentCompleter{completions: []Completion{
		toolCall("call-1", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
		toolCall("call-2", "build_review_plan", `{"questions":[],"topic":"wrong","date":"wrong"}`),
		toolCall("call-3", "save_plan", `{"plan":"model-invented"}`),
		{Content: "Review plan saved."},
		{Content: `{"profile":{"response_format":"bullets"},"task":{"goal":"must be discarded"},"records":[{"scope":"profile","kind":"decision","key":"format","value":"bullets"},{"scope":"task","kind":"knowledge","key":"must-not-persist","value":"no active task"}]}`},
	}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	agent.mcp = manager

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Составь план повторения темы, сохрани его")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Assistant.Content != "Review plan saved." {
		t.Fatalf("assistant response = %q, want persisted main response", result.Assistant.Content)
	}
	if len(manager.calls) != 3 || len(result.ToolEvents) != 3 || result.ToolEvents[2].Name != "save_plan" || result.ToolEvents[2].Status != "completed" {
		t.Fatalf("tool side effects = calls %#v events %#v, want completed save_plan", manager.calls, result.ToolEvents)
	}
	if len(completer.calls) != 5 || completer.calls[4].Kind != "memory" {
		t.Fatalf("completion call kinds = %#v, want tool continuation then memory extraction", completionKinds(completer.calls))
	}

	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].Content != "Составь план повторения темы, сохрани его" || lineage[1].Content != "Review plan saved." {
		t.Fatalf("persisted turn = %#v, want user and assistant reply", lineage)
	}
	activeTask, err := store.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load active task: %v", err)
	}
	if activeTask != nil {
		t.Fatalf("active task = %#v, want none", activeTask)
	}
	updatedProfile, err := store.Profile(profile.ID)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if updatedProfile.ResponseFormat != "bullets" {
		t.Fatalf("profile response format = %q, want bullets", updatedProfile.ResponseFormat)
	}
	profileMemories, err := store.ProfileMemories(profile.ID)
	if err != nil {
		t.Fatalf("load profile memories: %v", err)
	}
	if len(profileMemories) != 1 || profileMemories[0].Key != "format" || profileMemories[0].Value != "bullets" {
		t.Fatalf("profile memories = %#v, want valid profile record only", profileMemories)
	}
	stats, err := store.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load stats: %v", err)
	}
	if statCalls(stats, "main") != 4 || statCalls(stats, "memory") != 1 {
		t.Fatalf("accounting rows = %#v, want main and memory calls persisted with turn", stats)
	}
}

func TestAgentPersistsConfirmedSendAfterFinalCompletionFailure(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "partial-final.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}

	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "send_message")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1", "topic": "go", "due": "2026-09-28", "path": "Q1.md"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "# Review\nQ1", "count": 1}
	manager.results["send_message"] = map[string]any{"sent": true}
	completer := &memoryAgentCompleter{
		completions: []Completion{
			toolCall("call-1", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
			toolCall("call-2", "build_review_plan", `{"questions":[],"topic":"wrong","date":"wrong"}`),
			toolCall("call-3", "send_message", `{"text":"model-invented"}`),
		},
		errors: []error{nil, nil, nil, errors.New("completion request failed: 429 Too Many Requests")},
	}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	agent.mcp = manager

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Составь план и отправь его")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.Partial || result.Assistant.Content == "" {
		t.Fatalf("result = %#v, want persisted partial assistant status", result)
	}
	if !strings.Contains(result.Assistant.Content, "The plan was sent.") ||
		!strings.Contains(result.Assistant.Content, "Assistant completion failed") ||
		strings.Contains(result.Assistant.Content, "week-04/") ||
		strings.Contains(result.Assistant.Content, "model-invented") {
		t.Fatalf("partial assistant status = %q, want only confirmed send and completion failure", result.Assistant.Content)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].Content != "Составь план и отправь его" || lineage[1].Content != result.Assistant.Content {
		t.Fatalf("persisted lineage = %#v, want the user request and partial status", lineage)
	}
	stats, err := store.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load API stats: %v", err)
	}
	if statCalls(stats, "main") != 3 || statCalls(stats, "memory") != 0 {
		t.Fatalf("API calls = %#v, want three observed main calls and no auxiliary extraction", stats)
	}
}

func TestAgentPersistsConfirmedSendAfterAuxiliaryFailure(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "partial-auxiliary.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}

	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "send_message")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1", "topic": "go", "due": "2026-09-28", "path": "Q1.md"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "# Review\nQ1", "count": 1}
	manager.results["send_message"] = map[string]any{"sent": true}
	completer := &memoryAgentCompleter{
		completions: []Completion{
			toolCall("call-1", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
			toolCall("call-2", "build_review_plan", `{"questions":[],"topic":"wrong","date":"wrong"}`),
			toolCall("call-3", "send_message", `{"text":"model-invented"}`),
			{Content: "A response claiming the plan was sent."},
		},
		errors: []error{nil, nil, nil, nil, errors.New("auxiliary provider unavailable")},
	}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	agent.mcp = manager

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Составь план и отправь его")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !result.Partial || !strings.Contains(result.Assistant.Content, "The plan was sent.") ||
		!strings.Contains(result.Assistant.Content, "turn completion failed during auxiliary processing") ||
		strings.Contains(result.Assistant.Content, "Assistant completion failed") ||
		strings.Contains(result.Assistant.Content, "model-invented") {
		t.Fatalf("partial result = %#v, want persisted host status based on confirmed send", result)
	}
	if len(completer.calls) != 5 || completer.calls[4].Kind != "memory" {
		t.Fatalf("completion call kinds = %#v, want main requests then failed memory extraction", completionKinds(completer.calls))
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[1].Content != result.Assistant.Content {
		t.Fatalf("persisted lineage = %#v, want the partial assistant status", lineage)
	}
	stats, err := store.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load API stats: %v", err)
	}
	if statCalls(stats, "main") != 4 || statCalls(stats, "memory") != 0 {
		t.Fatalf("API calls = %#v, want four observed main calls and no failed auxiliary call", stats)
	}
}

func TestAgentKeepsNonSideEffectFailureFailFast(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "no-effect-failure.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}

	completer := &memoryAgentCompleter{errors: []error{errors.New("provider unavailable")}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "ordinary request")
	if err == nil || result.Partial {
		t.Fatalf("send result = %#v, error %v; want fail-fast error without partial turn", result, err)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 0 {
		t.Fatalf("lineage = %#v, want no persisted user turn", lineage)
	}
}

func TestAgentReturnsPartialOnConfirmedSendPersistenceFailure(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "partial-persist-failure.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_user_turn BEFORE INSERT ON messages WHEN NEW.role = 'user' BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END`); err != nil {
		t.Fatalf("create persistence failure trigger: %v", err)
	}

	manager := newToolCallTestManager("find_due_questions", "build_review_plan", "send_message")
	manager.results["find_due_questions"] = map[string]any{"questions": []any{map[string]any{"title": "Q1", "topic": "go", "due": "2026-09-28", "path": "Q1.md"}}}
	manager.results["build_review_plan"] = map[string]any{"plan": "# Review\nQ1", "count": 1}
	manager.results["send_message"] = map[string]any{"sent": true}
	completer := &memoryAgentCompleter{
		completions: []Completion{
			toolCall("call-1", "find_due_questions", `{"topic":"go","date":"2026-09-28"}`),
			toolCall("call-2", "build_review_plan", `{"questions":[],"topic":"wrong","date":"wrong"}`),
			toolCall("call-3", "send_message", `{"text":"model-invented"}`),
		},
		errors: []error{nil, nil, nil, errors.New("final completion unavailable")},
	}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	agent.mcp = manager

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Составь план и отправь его")
	if err == nil || !result.Partial {
		t.Fatalf("send result = %#v, error %v; want error and partial result after confirmed send", result, err)
	}
	if len(result.ToolEvents) != 3 || result.ToolEvents[2].Name != "send_message" || result.ToolEvents[2].Status != "completed" {
		t.Fatalf("tool events = %#v, want confirmed send retained", result.ToolEvents)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 0 {
		t.Fatalf("lineage = %#v, want atomic rollback after persistence failure", lineage)
	}
}

func TestAgentSendDiscardsMemoryForMissingActiveProfile(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory-routing-missing-profile.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "task goal", Plan: "task plan", CurrentStep: "task step", ExpectedAction: "task action"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeTask, MemoryKindKnowledge, "seed", "keep task"); err != nil {
		t.Fatalf("seed task memory: %v", err)
	}
	inactiveProfile, err := store.CreateProfile(ProfileInput{Name: "inactive", Language: "English", ResponseStyle: "concise", ResponseFormat: "text", Constraints: "none"})
	if err != nil {
		t.Fatalf("create inactive profile: %v", err)
	}

	completer := &memoryAgentCompleter{completions: []Completion{
		{Content: "main response"},
		{Content: `{"profile":{"response_format":"bullets"},"task":{"goal":"changed goal"},"records":[{"scope":"profile","kind":"knowledge","key":"seed","value":"replace profile"},{"scope":"task","kind":"knowledge","key":"seed","value":"replace task"}]}`},
	}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "missing profile scope"); err != nil {
		t.Fatalf("send with absent profile candidate: %v", err)
	}

	activeProfile, err := store.ActiveProfile()
	if err != nil {
		t.Fatalf("load active profile: %v", err)
	}
	if activeProfile != nil {
		t.Fatalf("active profile = %#v, want none", activeProfile)
	}
	unchangedProfile, err := store.Profile(inactiveProfile.ID)
	if err != nil {
		t.Fatalf("load inactive profile: %v", err)
	}
	if unchangedProfile.ResponseFormat != "text" {
		t.Fatalf("inactive profile response format = %q, want text", unchangedProfile.ResponseFormat)
	}
	profileMemories, err := store.ProfileMemories(inactiveProfile.ID)
	if err != nil {
		t.Fatalf("load inactive profile memories: %v", err)
	}
	if len(profileMemories) != 0 {
		t.Fatalf("inactive profile memories = %#v, want none", profileMemories)
	}
	updatedTask, err := store.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updatedTask == nil || updatedTask.Goal != "changed goal" || updatedTask.Plan != "task plan" {
		t.Fatalf("task = %#v, want valid task update without changing other fields", updatedTask)
	}
	taskMemories, err := store.TaskMemories(task.ID)
	if err != nil {
		t.Fatalf("load task memories: %v", err)
	}
	if len(taskMemories) != 1 || taskMemories[0].Value != "replace task" {
		t.Fatalf("task memories = %#v, want valid task record", taskMemories)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].Content != "missing profile scope" || lineage[1].Content != "main response" {
		t.Fatalf("lineage = %#v, want persisted user and assistant turn", lineage)
	}
}

func TestAgentSendRoutesTaskMemoryWithoutSelectingProfile(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory-routing-task-only.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "task goal", Plan: "task plan", CurrentStep: "task step", ExpectedAction: "task action"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	inactiveProfile, err := store.CreateProfile(ProfileInput{Name: "inactive", Language: "English", ResponseStyle: "concise", ResponseFormat: "text", Constraints: "none"})
	if err != nil {
		t.Fatalf("create inactive profile: %v", err)
	}
	completer := &memoryAgentCompleter{completions: []Completion{
		{Content: "main response"},
		{Content: `{"task":{"plan":"updated task plan"},"records":[{"scope":"task","kind":"decision","key":"task-only","value":"keep it local"}]}`},
	}}
	agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "task-only update"); err != nil {
		t.Fatalf("send task-only update: %v", err)
	}

	activeProfile, err := store.ActiveProfile()
	if err != nil {
		t.Fatalf("load active profile: %v", err)
	}
	if activeProfile != nil {
		t.Fatalf("active profile = %#v, want none", activeProfile)
	}
	unchangedProfile, err := store.Profile(inactiveProfile.ID)
	if err != nil {
		t.Fatalf("load inactive profile: %v", err)
	}
	if unchangedProfile.ResponseFormat != "text" {
		t.Fatalf("inactive profile response format = %q, want text", unchangedProfile.ResponseFormat)
	}
	updatedTask, err := store.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if updatedTask == nil || updatedTask.Goal != "task goal" || updatedTask.Plan != "updated task plan" || updatedTask.CurrentStep != "task step" || updatedTask.ExpectedAction != "task action" {
		t.Fatalf("task = %#v, want task-only update", updatedTask)
	}
	taskMemories, err := store.TaskMemories(task.ID)
	if err != nil {
		t.Fatalf("load task memories: %v", err)
	}
	if len(taskMemories) != 1 || taskMemories[0].Kind != MemoryKindDecision || taskMemories[0].Key != "task-only" || taskMemories[0].Value != "keep it local" || taskMemories[0].TaskID == nil || *taskMemories[0].TaskID != task.ID || taskMemories[0].ProfileID != nil {
		t.Fatalf("task memories = %#v, want task-only record", taskMemories)
	}
}

func TestAgentSendReservesAuxiliaryBudgetForMemoryExtraction(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory-budget.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	completer := &memoryAgentCompleter{completions: []Completion{{Content: "main response"}, {Content: "{}"}}}
	profile := ProviderProfile{
		Name:          "synthetic",
		Model:         "model",
		ContextWindow: 12,
		MainMax:       8,
		AuxiliaryMax:  2,
	}
	memoryPrompt := BuildMemoryExtractionPrompt("remember this", "main response", MemoryExtractionState{})
	if err := CheckContextOverflow(memoryAgentCounter{}, profile, memoryPrompt); err == nil {
		t.Fatal("memory prompt accepted with main completion reserve")
	}
	auxiliaryProfile := profile
	auxiliaryProfile.MainMax = profile.AuxiliaryMax
	if err := CheckContextOverflow(memoryAgentCounter{}, auxiliaryProfile, memoryPrompt); err != nil {
		t.Fatalf("memory prompt rejected with auxiliary completion reserve: %v", err)
	}
	agent := NewAgent(store, completer, memoryAgentCounter{}, profile)

	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "remember this"); err != nil {
		t.Fatalf("send with auxiliary memory budget: %v", err)
	}
	if len(completer.calls) != 2 || completer.calls[1].Kind != "memory" {
		t.Fatalf("completion call kinds = %#v, want main then memory", completionKinds(completer.calls))
	}
}

func TestAgentSendDoesNotPersistOnMemoryExtractionFailure(t *testing.T) {
	tests := []struct {
		name        string
		completions []Completion
		errors      []error
	}{
		{name: "transport", completions: []Completion{{Content: "main response"}, {}}, errors: []error{nil, errors.New("extractor unavailable")}},
		{name: "parse", completions: []Completion{{Content: "main response"}, {Content: "not JSON"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "memory-agent.sqlite"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			chat, branch, err := store.ActiveChat()
			if err != nil {
				t.Fatalf("load active chat: %v", err)
			}
			completer := &memoryAgentCompleter{completions: test.completions, errors: test.errors}
			profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text"})
			if err != nil {
				t.Fatalf("create profile: %v", err)
			}
			if _, err := store.SelectProfile(profile.ID); err != nil {
				t.Fatalf("select profile: %v", err)
			}
			task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "old goal", Plan: "old plan", CurrentStep: "old step", ExpectedAction: "old action"})
			if err != nil {
				t.Fatalf("create task: %v", err)
			}
			agent := NewAgent(store, completer, memoryAgentCounter{}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})

			if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "remember this"); err == nil {
				t.Fatal("send succeeded after memory extraction failure")
			}
			if len(completer.calls) != 2 || completer.calls[0].Kind != "main" || completer.calls[1].Kind != "memory" {
				t.Fatalf("completion call kinds = %#v, want main then memory", completionKinds(completer.calls))
			}
			lineage, err := store.Lineage(branch.ID)
			if err != nil {
				t.Fatalf("load lineage: %v", err)
			}
			if len(lineage) != 0 {
				t.Fatalf("lineage after failed extraction = %#v, want none", lineage)
			}
			stats, err := store.Stats(branch.ID)
			if err != nil {
				t.Fatalf("load stats: %v", err)
			}
			if len(stats) != 0 {
				t.Fatalf("accounting after failed extraction = %#v, want none", stats)
			}
			profileMemories, err := store.ProfileMemories(profile.ID)
			if err != nil {
				t.Fatalf("load profile memories: %v", err)
			}
			taskMemories, err := store.TaskMemories(task.ID)
			if err != nil {
				t.Fatalf("load task memories: %v", err)
			}
			if len(profileMemories) != 0 || len(taskMemories) != 0 {
				t.Fatalf("memory after failed extraction = profile %#v task %#v, want none", profileMemories, taskMemories)
			}
		})
	}
}

func completionKinds(calls []CompletionRequest) []string {
	kinds := make([]string, len(calls))
	for index, call := range calls {
		kinds[index] = call.Kind
	}
	return kinds
}

func statCalls(stats []StatsRow, kind string) int {
	for _, stat := range stats {
		if stat.Kind == kind {
			return stat.Calls
		}
	}
	return 0
}
