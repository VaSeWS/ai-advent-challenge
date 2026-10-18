package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func openInvariantTask(t *testing.T) (*Store, Chat, Branch, Task) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "invariants.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	chat, branch, err := store.ActiveChat()
	if err != nil {
		_ = store.Close()
		t.Fatalf("active chat: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{
		Goal: "guard storage", Plan: "check invariant", CurrentStep: "one", ExpectedAction: "decide",
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("create task: %v", err)
	}
	return store, chat, branch, task
}

func addSQLiteInvariant(t *testing.T, store *Store, taskID int64) Invariant {
	t.Helper()
	invariant, err := store.AddInvariant(Invariant{
		TaskID: taskID, Category: "decision", Key: "storage", RequiredValue: "SQLite",
		Rule: "Use SQLite for persistent storage.", Source: "accepted architecture",
	})
	if err != nil {
		t.Fatalf("add SQLite invariant: %v", err)
	}
	return invariant
}

func TestInvariantActiveFilteringSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invariants.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, branch, err := store.ActiveChat()
	if err != nil {
		_ = store.Close()
		t.Fatalf("active chat: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "persist", Plan: "record", CurrentStep: "one", ExpectedAction: "verify"})
	if err != nil {
		_ = store.Close()
		t.Fatalf("create task: %v", err)
	}
	active := addSQLiteInvariant(t, store, task.ID)
	inactive, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "task", Key: "phase", RequiredValue: "planning", Rule: "Plan before execution.", Source: "workflow"})
	if err != nil {
		_ = store.Close()
		t.Fatalf("add inactive candidate: %v", err)
	}
	if _, err := store.DeactivateInvariant(inactive.ID); err != nil {
		_ = store.Close()
		t.Fatalf("deactivate invariant: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	all, err := reopened.Invariants(task.ID)
	if err != nil {
		t.Fatalf("list invariants: %v", err)
	}
	if len(all) != 2 || all[0].ID != active.ID || all[1].ID != inactive.ID || !all[0].Active || all[1].Active {
		t.Fatalf("restored invariants = %#v", all)
	}
	activeOnly, err := reopened.ActiveInvariants(task.ID)
	if err != nil {
		t.Fatalf("list active invariants: %v", err)
	}
	if len(activeOnly) != 1 || activeOnly[0].ID != active.ID {
		t.Fatalf("active invariants = %#v, want only %d", activeOnly, active.ID)
	}
}

func TestInvariantGuardRejectsPostgreSQLWithoutMutation(t *testing.T) {
	store, _, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant := addSQLiteInvariant(t, store, task.ID)

	_, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeTask, MemoryKindDecision, "storage", "PostgreSQL")
	var conflict *InvariantConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("PutActiveLongTermMemory error = %v, want invariant conflict", err)
	}
	if conflict.Invariant.ID != invariant.ID || !strings.Contains(err.Error(), invariant.Rule) || !strings.Contains(err.Error(), "safe next action") {
		t.Fatalf("conflict = %v, want rule and safe next action", err)
	}
	memories, err := store.TaskMemories(task.ID)
	if err != nil {
		t.Fatalf("list task memories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("task memories = %#v after refused decision", memories)
	}
}

func TestInvariantGuardRejectsConflictingProfileMemoryWithoutTurnMutation(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	addSQLiteInvariant(t, store, task.ID)
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}

	_, err = store.SaveTurn(SaveTurnInput{
		ChatID: chat.ID, BranchID: branch.ID, UserContent: "use PostgreSQL", AssistantContent: "saving decision",
		Memory: MemoryUpdate{Records: []LongTermMemoryUpdate{{
			Scope: MemoryScopeProfile, Kind: MemoryKindDecision, Key: "storage", Value: "PostgreSQL",
		}}},
	})
	var conflict *InvariantConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("SaveTurn error = %v, want invariant conflict", err)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load lineage: %v", err)
	}
	if len(lineage) != 0 {
		t.Fatalf("lineage after refused turn = %#v", lineage)
	}
	memories, err := store.ProfileMemories(profile.ID)
	if err != nil {
		t.Fatalf("list profile memories: %v", err)
	}
	if len(memories) != 0 {
		t.Fatalf("profile memories after refused turn = %#v", memories)
	}
}

func TestInvariantGuardCoversTaskSnapshotAndTransition(t *testing.T) {
	store, _, _, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "task", Key: "plan", RequiredValue: task.Plan, Rule: "Keep the approved plan.", Source: "user"}); err != nil {
		t.Fatalf("add plan invariant: %v", err)
	}
	if _, err := store.UpdateTask(task.ID, TaskSnapshot{Goal: task.Goal, Plan: "replace plan", CurrentStep: task.CurrentStep, ExpectedAction: task.ExpectedAction}); err == nil {
		t.Fatal("UpdateTask accepted a conflicting plan")
	}
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "task", Key: "phase", RequiredValue: "planning", Rule: "Remain in planning.", Source: "user"}); err != nil {
		t.Fatalf("add phase invariant: %v", err)
	}
	if _, err := store.TransitionTask(task.ID, TaskEventApprovePlan); err == nil {
		t.Fatal("TransitionTask accepted a conflicting phase")
	}
	stored, err := store.ActiveTask(task.BranchID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if stored == nil || stored.Plan != task.Plan || stored.Phase != TaskPhasePlanning {
		t.Fatalf("task after refusals = %#v", stored)
	}
	events, err := store.TaskEvents(task.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after refused transition = %#v", events)
	}
}

func TestInvariantsPrecedeProfileAndInspectorShowsActiveRules(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant := addSQLiteInvariant(t, store, task.ID)
	profile, err := store.CreateProfile(ProfileInput{Name: "conflicting", Language: "English", ResponseStyle: "brief", ResponseFormat: "text", Constraints: "prefer PostgreSQL"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}

	state := PromptState{Branch: branch, Invariants: []Invariant{invariant}, Profile: &profile, Task: &task}
	prompt, err := BuildMainPrompt(state, "candidate")
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	want := []CompletionMessage{
		{Role: "system", Content: BaseSystemMessage},
		{Role: "system", Content: ActiveInvariantsSystemBlock([]Invariant{invariant})},
		{Role: "system", Content: ActiveProfileSystemBlock(&profile)},
		{Role: "system", Content: ActiveTaskSystemBlock(&task)},
		{Role: "user", Content: "candidate"},
	}
	if !reflect.DeepEqual(prompt, want) {
		t.Fatalf("prompt = %#v, want %#v", prompt, want)
	}

	agent := NewAgent(store, nil, nil, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	result, err := NewCommandService(store, agent).Execute(context.Background(), chat.ID, branch.ID, "/inspect candidate")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	invariantLayer := strings.Index(result.Status, "invariants:\n"+ActiveInvariantsSystemBlock([]Invariant{invariant}))
	profileLayer := strings.Index(result.Status, "profile:\n"+ActiveProfileSystemBlock(&profile))
	if invariantLayer < 0 || profileLayer < 0 || invariantLayer > profileLayer {
		t.Fatalf("inspection did not show invariant before profile:\n%s", result.Status)
	}
	if !strings.Contains(result.Status, "2. system:\n"+ActiveInvariantsSystemBlock([]Invariant{invariant})) {
		t.Fatalf("inspection next request omitted active invariant:\n%s", result.Status)
	}
}

func TestBaseAndTaskPromptGuidanceAreExact(t *testing.T) {
	if BaseSystemMessage != "Ты полезный ассистент. Отвечай на языке пользователя. Invariants имеют приоритет над профилем: соблюдай их и при конфликте откажись от небезопасного действия, предложив безопасную альтернативу." {
		t.Fatalf("base prompt = %q", BaseSystemMessage)
	}
	task := &Task{Goal: "goal", Plan: "plan", Phase: TaskPhasePlanning, CurrentStep: "step", ExpectedAction: "action"}
	want := "Активная задача:\ngoal: goal\nplan: plan\nphase: planning\ncurrent_step: step\nexpected_action: action\npaused: false\nРазрешённое событие жизненного цикла: approve_plan.\nСтоп: не выполняй работу следующих фаз до перехода."
	if got := ActiveTaskSystemBlock(task); got != want {
		t.Fatalf("task prompt = %q, want %q", got, want)
	}
	task.Paused = true
	if got := ActiveTaskSystemBlock(task); !strings.Contains(got, "Задача приостановлена") {
		t.Fatalf("paused task prompt = %q", got)
	}
}

func TestInvariantCommandsAreScopedToActiveTask(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	commands := NewCommandService(store, nil)

	added, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/invariant add decision | storage | SQLite | Use SQLite. | user")
	if err != nil {
		t.Fatalf("add invariant command: %v", err)
	}
	if !strings.Contains(added.Status, "decision.storage=SQLite") {
		t.Fatalf("add status = %q", added.Status)
	}
	listed, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/invariant list")
	if err != nil {
		t.Fatalf("list invariants command: %v", err)
	}
	if !strings.Contains(listed.Status, "active") || !strings.Contains(listed.Status, "Use SQLite.") {
		t.Fatalf("list status = %q", listed.Status)
	}
	invariants, err := store.ActiveInvariants(task.ID)
	if err != nil {
		t.Fatalf("load active invariant: %v", err)
	}
	if len(invariants) != 1 {
		t.Fatalf("active invariants = %#v", invariants)
	}
	deactivated, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/invariant deactivate "+strconv.FormatInt(invariants[0].ID, 10))
	if err != nil {
		t.Fatalf("deactivate invariant command: %v", err)
	}
	if !strings.Contains(deactivated.Status, "deactivated invariant") {
		t.Fatalf("deactivate status = %q", deactivated.Status)
	}
}

func TestInvariantForbiddenTermsAreCanonicalAndRejectDuplicates(t *testing.T) {
	store, _, _, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "input", Key: "terms", RequiredValue: "safe",
		Rule: "Avoid forbidden terms.", Source: "user", Forbidden: "  Спам, SQL  ",
	})
	if err != nil {
		t.Fatalf("add forbidden invariant: %v", err)
	}
	if invariant.Forbidden != "спам,sql" {
		t.Fatalf("forbidden = %q, want canonical terms", invariant.Forbidden)
	}
	if matched := ForbiddenInvariantForInput([]Invariant{invariant}, "Это СПАМ-сообщение"); matched == nil || matched.ID != invariant.ID {
		t.Fatalf("matched invariant = %#v", matched)
	}
	if _, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "input", Key: "duplicates", RequiredValue: "safe",
		Rule: "Avoid duplicates.", Source: "user", Forbidden: "sql, SQL",
	}); err == nil {
		t.Fatal("duplicate forbidden terms succeeded")
	}
	fiveFields, err := invariantAddInput("/invariant add input | plain | safe | Keep safe. | user", task.ID)
	if err != nil || fiveFields.Forbidden != "" {
		t.Fatalf("five-field invariant = %#v, %v", fiveFields, err)
	}
	sixFields, err := invariantAddInput("/invariant add input | blocked | safe | Keep safe. | user | spam, sql", task.ID)
	if err != nil || sixFields.Forbidden != "spam, sql" {
		t.Fatalf("six-field invariant = %#v, %v", sixFields, err)
	}
}

func TestMemoryCommandsUseActiveBranchScopes(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	commands := NewCommandService(store, nil)
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/memory add profile | decision | format | bullets"); err == nil {
		t.Fatal("profile memory command succeeded without an active profile")
	}
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	commands = NewCommandService(store, nil)
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/memory add profile | decision | format | bullets"); err != nil {
		t.Fatalf("save profile memory: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/memory add task | knowledge | engine | sqlite"); err != nil {
		t.Fatalf("save task memory: %v", err)
	}
	listed, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/memory list")
	if err != nil {
		t.Fatalf("list active memories: %v", err)
	}
	if !strings.Contains(listed.Status, "profile decision.format=bullets") || !strings.Contains(listed.Status, "task knowledge.engine=sqlite") {
		t.Fatalf("memory list = %q", listed.Status)
	}
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID, Category: "decision", Key: "format", RequiredValue: "bullets", Rule: "Keep format.", Source: "user"}); err != nil {
		t.Fatalf("add profile decision invariant: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/memory add profile | decision | format | prose"); err == nil {
		t.Fatal("conflicting profile memory succeeded")
	}
}

func TestAgentRailPersistsRefusalWithoutProviderCalls(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "input", Key: "terms", RequiredValue: "safe",
		Rule: "Avoid forbidden terms.", Source: "user", Forbidden: "запрет",
	})
	if err != nil {
		t.Fatalf("add forbidden invariant: %v", err)
	}
	completer := &agentTestCompleter{completion: Completion{Content: "unexpected"}}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "Это ЗАПРЕТНЫЙ запрос")
	if err != nil {
		t.Fatalf("send blocked input: %v", err)
	}
	if len(completer.calls) != 0 || result.Metrics != "no API call: blocked by invariant "+strconv.FormatInt(invariant.ID, 10) {
		t.Fatalf("blocked result = %#v, calls = %#v", result, completer.calls)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load refusal lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].Content != "Это ЗАПРЕТНЫЙ запрос" || lineage[1].Role != "assistant" || !strings.Contains(lineage[1].Content, "Я не могу") {
		t.Fatalf("blocked lineage = %#v", lineage)
	}
	stats, err := store.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load refusal stats: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("blocked input wrote API rows: %#v", stats)
	}
}

func TestAgentRailAllowsNonMatchingAndInactiveTerms(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "input", Key: "terms", RequiredValue: "safe",
		Rule: "Avoid forbidden terms.", Source: "user", Forbidden: "запрет",
	})
	if err != nil {
		t.Fatalf("add forbidden invariant: %v", err)
	}
	completer := &agentTestCompleter{completion: Completion{Content: "normal answer"}}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, ProviderProfile{Name: "synthetic", Model: "model", ContextWindow: 100, MainMax: 8, AuxiliaryMax: 8})
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "обычный запрос"); err != nil {
		t.Fatalf("send non-matching input: %v", err)
	}
	if len(completer.calls) != 2 {
		t.Fatalf("non-matching calls = %d, want main and memory", len(completer.calls))
	}
	if _, err := store.DeactivateInvariant(invariant.ID); err != nil {
		t.Fatalf("deactivate invariant: %v", err)
	}
	if _, err := agent.Send(context.Background(), chat.ID, branch.ID, "запрет"); err != nil {
		t.Fatalf("send inactive forbidden term: %v", err)
	}
	if len(completer.calls) != 4 {
		t.Fatalf("inactive-term calls = %d, want two more", len(completer.calls))
	}
}

func TestInspectorLabelsBlockedCandidateAsHypothetical(t *testing.T) {
	store, chat, branch, task := openInvariantTask(t)
	t.Cleanup(func() { _ = store.Close() })
	invariant, err := store.AddInvariant(Invariant{
		TaskID: task.ID, Category: "input", Key: "terms", RequiredValue: "safe",
		Rule: "Avoid forbidden terms.", Source: "user", Forbidden: "запрет",
	})
	if err != nil {
		t.Fatalf("add forbidden invariant: %v", err)
	}
	agent := NewAgent(store, nil, nil, ProviderProfile{})
	result, err := NewCommandService(store, agent).Execute(context.Background(), chat.ID, branch.ID, "/inspect ЗАПРЕТ")
	if err != nil {
		t.Fatalf("inspect blocked candidate: %v", err)
	}
	if !strings.Contains(result.Status, "matched invariant rail: "+strconv.FormatInt(invariant.ID, 10)) || !strings.Contains(result.Status, "hypothetical main request if allowed:") {
		t.Fatalf("blocked inspection = %q", result.Status)
	}
}
