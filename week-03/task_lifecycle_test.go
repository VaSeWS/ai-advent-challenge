package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskCommandsResumePausedSnapshotAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task-lifecycle.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	commands := NewCommandService(store, nil)
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task create Ship lifecycle | Guard every phase | Approve plan | Record approval"); err != nil {
		t.Fatalf("create task command: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task update plan Guard every transition"); err != nil {
		t.Fatalf("update task plan command: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task update step Resume safely"); err != nil {
		t.Fatalf("update task step command: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task update action Confirm preserved state"); err != nil {
		t.Fatalf("update task action command: %v", err)
	}
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task pause"); err != nil {
		t.Fatalf("pause task command: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	chat, branch, err = reopened.ActiveChat()
	if err != nil {
		t.Fatalf("load reopened active chat: %v", err)
	}
	task, err := reopened.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load paused task: %v", err)
	}
	if task == nil || task.Phase != TaskPhasePlanning || !task.Paused || task.Plan != "Guard every transition" || task.CurrentStep != "Resume safely" || task.ExpectedAction != "Confirm preserved state" {
		t.Fatalf("paused task after restart = %#v, want preserved planning snapshot", task)
	}

	commands = NewCommandService(reopened, nil)
	if _, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task resume"); err != nil {
		t.Fatalf("resume task command: %v", err)
	}
	result, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task show")
	if err != nil {
		t.Fatalf("show task command: %v", err)
	}
	for _, fragment := range []string{"phase: planning", "paused: false", "plan: Guard every transition", "current step: Resume safely", "expected action: Confirm preserved state"} {
		if !strings.Contains(result.Status, fragment) {
			t.Fatalf("task status = %q, want %q", result.Status, fragment)
		}
	}
}

func TestTransitionTaskAllowsEveryLifecycleEventAndOrdersHistory(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "transitions.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active branch: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "Ship lifecycle", Plan: "Use guards", CurrentStep: "Approve", ExpectedAction: "Approve plan"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sequence := []struct {
		event TaskEventKind
		phase TaskPhase
	}{
		{TaskEventApprovePlan, TaskPhaseExecution},
		{TaskEventSubmitResult, TaskPhaseValidation},
		{TaskEventValidationFailed, TaskPhaseExecution},
		{TaskEventSubmitResult, TaskPhaseValidation},
		{TaskEventValidationPassed, TaskPhaseDone},
	}
	for _, transition := range sequence {
		updated, err := store.TransitionTask(task.ID, transition.event)
		if err != nil {
			t.Fatalf("transition %q: %v", transition.event, err)
		}
		if updated.Phase != transition.phase {
			t.Fatalf("phase after %q = %q, want %q", transition.event, updated.Phase, transition.phase)
		}
	}

	events, err := store.TaskEvents(task.ID)
	if err != nil {
		t.Fatalf("load task history: %v", err)
	}
	if len(events) != len(sequence) {
		t.Fatalf("history length = %d, want %d", len(events), len(sequence))
	}
	for index, event := range events {
		if event.Event != sequence[index].event || event.ToPhase != sequence[index].phase {
			t.Fatalf("history[%d] = %#v, want event %q to phase %q", index, event, sequence[index].event, sequence[index].phase)
		}
		if index > 0 && event.ID <= events[index-1].ID {
			t.Fatalf("history IDs are not chronological: %#v", events)
		}
	}
	commands := NewCommandService(store, nil)
	history, err := commands.Execute(context.Background(), chat.ID, branch.ID, "/task history")
	if err != nil {
		t.Fatalf("show task history command: %v", err)
	}
	if !strings.Contains(history.Status, "task history:\n1. planning --approve_plan--> execution") || !strings.Contains(history.Status, "5. validation --validation_passed--> done") {
		t.Fatalf("task history status = %q, want chronological transition output", history.Status)
	}
}

func TestTransitionTaskRejectsInvalidStateWithoutHistory(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "rejections.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active branch: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "Ship lifecycle", Plan: "Use guards", CurrentStep: "Approve", ExpectedAction: "Approve plan"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if _, err := store.TransitionTask(task.ID, TaskEventSubmitResult); err == nil {
		t.Fatal("planning task accepted submit_result without plan approval")
	}
	assertTaskStateAndHistory(t, store, task.ID, TaskPhasePlanning, 0)
	pausedSnapshot := snapshotForTask(&task)
	pausedSnapshot.Paused = true
	if _, err := store.UpdateTask(task.ID, pausedSnapshot); err != nil {
		t.Fatalf("pause task: %v", err)
	}
	if _, err := store.TransitionTask(task.ID, TaskEventApprovePlan); err == nil {
		t.Fatal("paused task accepted a transition")
	}
	assertTaskStateAndHistory(t, store, task.ID, TaskPhasePlanning, 0)
	pausedSnapshot.Paused = false
	if _, err := store.UpdateTask(task.ID, pausedSnapshot); err != nil {
		t.Fatalf("resume task: %v", err)
	}

	if _, err := store.TransitionTask(task.ID, TaskEventApprovePlan); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if _, err := store.TransitionTask(task.ID, TaskEventValidationPassed); err == nil {
		t.Fatal("execution task accepted completion before validation")
	}
	assertTaskStateAndHistory(t, store, task.ID, TaskPhaseExecution, 1)
}

func TestTaskEventsOrderSameSecondFractionalTimestamps(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "same-second-history.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active branch: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "Order history", Plan: "Parse timestamps", CurrentStep: "Record events", ExpectedAction: "List chronologically"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	first, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, task.ID, TaskPhasePlanning, TaskEventApprovePlan, TaskPhaseExecution, "2026-01-02T03:04:05Z")
	if err != nil {
		t.Fatalf("insert whole-second event: %v", err)
	}
	second, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, task.ID, TaskPhaseExecution, TaskEventSubmitResult, TaskPhaseValidation, "2026-01-02T03:04:05.1Z")
	if err != nil {
		t.Fatalf("insert fractional-second event: %v", err)
	}
	firstID, err := first.LastInsertId()
	if err != nil {
		t.Fatalf("read first event ID: %v", err)
	}
	secondID, err := second.LastInsertId()
	if err != nil {
		t.Fatalf("read second event ID: %v", err)
	}
	events, err := store.TaskEvents(task.ID)
	if err != nil {
		t.Fatalf("load ordered history: %v", err)
	}
	if len(events) != 2 || events[0].ID != firstID || events[1].ID != secondID {
		t.Fatalf("same-second event order = %#v, want IDs %d then %d", events, firstID, secondID)
	}
}

func assertTaskStateAndHistory(t *testing.T, store *Store, taskID int64, wantPhase TaskPhase, wantEvents int) {
	t.Helper()
	task, err := store.taskByID(taskID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if task.Phase != wantPhase {
		t.Fatalf("task phase = %q, want %q", task.Phase, wantPhase)
	}
	events, err := store.TaskEvents(taskID)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(events) != wantEvents {
		t.Fatalf("history length = %d, want %d", len(events), wantEvents)
	}
}
