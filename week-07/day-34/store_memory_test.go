package main

import (
	"path/filepath"
	"testing"
)

func TestStoreMemoryDomainsPersistAndStayIsolated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	first, err := store.CreateProfile(ProfileInput{
		Name:           "Ada",
		Language:       "English",
		ResponseStyle:  "concise",
		ResponseFormat: "markdown",
		Constraints:    "cite primary sources",
	})
	if err != nil {
		t.Fatalf("create first profile: %v", err)
	}
	second, err := store.CreateProfile(ProfileInput{
		Name:           "Bela",
		Language:       "Russian",
		ResponseStyle:  "detailed",
		ResponseFormat: "plain text",
	})
	if err != nil {
		t.Fatalf("create second profile: %v", err)
	}
	updatedProfile, err := store.UpdateProfile(first.ID, ProfileInput{
		Name:           "Ada",
		Language:       "English",
		ResponseStyle:  "direct",
		ResponseFormat: "markdown",
		Constraints:    "cite primary sources",
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updatedProfile.CreatedAt.IsZero() || updatedProfile.UpdatedAt.Before(updatedProfile.CreatedAt) || updatedProfile.ResponseStyle != "direct" {
		t.Fatalf("updated profile = %#v, want preserved creation and updated style", updatedProfile)
	}
	loadedProfile, err := store.Profile(first.ID)
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if loadedProfile.ID != first.ID || loadedProfile.ResponseStyle != "direct" || loadedProfile.Constraints != "cite primary sources" {
		t.Fatalf("loaded profile = %#v, want updated profile", loadedProfile)
	}
	if _, err := store.SelectProfile(first.ID); err != nil {
		t.Fatalf("select first profile: %v", err)
	}
	if _, err := store.SelectProfile(second.ID); err != nil {
		t.Fatalf("select second profile: %v", err)
	}

	profiles, err := store.Profiles()
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 2 || profiles[0].ID != first.ID || profiles[1].ID != second.ID {
		t.Fatalf("profiles = %#v, want creation order", profiles)
	}

	_, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active branch: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{
		Goal:           "persist memory",
		Plan:           "write storage accessors",
		CurrentStep:    "implement tests",
		ExpectedAction: "review round trip",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	updatedTask, err := store.UpdateTask(task.ID, TaskSnapshot{
		Goal:           "persist memory",
		Plan:           "write storage accessors",
		CurrentStep:    "reopen database",
		ExpectedAction: "inspect restored state",
		Paused:         true,
	})
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	if updatedTask.Phase != TaskPhasePlanning || !updatedTask.Paused || updatedTask.CreatedAt.IsZero() || updatedTask.UpdatedAt.Before(updatedTask.CreatedAt) {
		t.Fatalf("updated task = %#v, want paused planning snapshot", updatedTask)
	}

	profileMemory, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKindDecision, "storage", "use short answers")
	if err != nil {
		t.Fatalf("save profile memory: %v", err)
	}
	updatedProfileMemory, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKindDecision, "storage", "use concise answers")
	if err != nil {
		t.Fatalf("update profile memory: %v", err)
	}
	if updatedProfileMemory.ID != profileMemory.ID || updatedProfileMemory.CreatedAt != profileMemory.CreatedAt {
		t.Fatalf("updated profile memory = %#v, want in-scope upsert", updatedProfileMemory)
	}
	profileMemory = updatedProfileMemory
	taskMemory, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeTask, MemoryKindDecision, "storage", "SQLite")
	if err != nil {
		t.Fatalf("save task memory: %v", err)
	}
	if profileMemory.ID == taskMemory.ID {
		t.Fatalf("profile and task memories share ID %d", profileMemory.ID)
	}

	activeInvariant, err := store.AddInvariant(Invariant{
		TaskID:        task.ID,
		Category:      "storage",
		Key:           "database",
		RequiredValue: "SQLite",
		Rule:          "Use SQLite.",
		Source:        "user",
	})
	if err != nil {
		t.Fatalf("add active invariant: %v", err)
	}
	inactiveInvariant, err := store.AddInvariant(Invariant{
		TaskID:        task.ID,
		Category:      "delivery",
		Key:           "format",
		RequiredValue: "text",
		Rule:          "Use text output.",
		Source:        "user",
	})
	if err != nil {
		t.Fatalf("add inactive invariant: %v", err)
	}
	if _, err := store.DeactivateInvariant(inactiveInvariant.ID); err != nil {
		t.Fatalf("deactivate invariant: %v", err)
	}

	createdAt := "2026-01-02T03:04:05Z"
	firstEvent, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, task.ID, TaskPhasePlanning, TaskEventApprovePlan, TaskPhaseExecution, createdAt)
	if err != nil {
		t.Fatalf("insert first task event: %v", err)
	}
	firstEventID, err := firstEvent.LastInsertId()
	if err != nil {
		t.Fatalf("read first task event ID: %v", err)
	}
	laterCreatedAt := "2026-01-02T03:04:05.1Z"
	secondEvent, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, task.ID, TaskPhaseExecution, TaskEventSubmitResult, TaskPhaseValidation, laterCreatedAt)
	if err != nil {
		t.Fatalf("insert second task event: %v", err)
	}
	secondEventID, err := secondEvent.LastInsertId()
	if err != nil {
		t.Fatalf("read second task event ID: %v", err)
	}
	thirdEvent, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, task.ID, TaskPhaseValidation, TaskEventValidationPassed, TaskPhaseDone, laterCreatedAt)
	if err != nil {
		t.Fatalf("insert third task event: %v", err)
	}
	thirdEventID, err := thirdEvent.LastInsertId()
	if err != nil {
		t.Fatalf("read third task event ID: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	activeProfile, err := reopened.ActiveProfile()
	if err != nil {
		t.Fatalf("load active profile: %v", err)
	}
	if activeProfile == nil || activeProfile.ID != second.ID || activeProfile.Language != "Russian" {
		t.Fatalf("active profile = %#v, want second profile", activeProfile)
	}
	restoredFirstProfile, err := reopened.Profile(first.ID)
	if err != nil {
		t.Fatalf("load restored profile: %v", err)
	}
	if restoredFirstProfile.ResponseStyle != "direct" || restoredFirstProfile.Constraints != "cite primary sources" {
		t.Fatalf("restored first profile = %#v, want updated profile fields", restoredFirstProfile)
	}
	restoredTask, err := reopened.ActiveTask(branch.ID)
	if err != nil {
		t.Fatalf("load active task: %v", err)
	}
	if restoredTask == nil || restoredTask.ID != task.ID || restoredTask.Phase != TaskPhasePlanning || !restoredTask.Paused || restoredTask.CurrentStep != "reopen database" || restoredTask.ExpectedAction != "inspect restored state" {
		t.Fatalf("active task = %#v, want persisted snapshot", restoredTask)
	}

	profileMemories, err := reopened.ProfileMemories(second.ID)
	if err != nil {
		t.Fatalf("list profile memories: %v", err)
	}
	if len(profileMemories) != 1 || profileMemories[0].ID != profileMemory.ID || profileMemories[0].TaskID != nil || profileMemories[0].Value != "use concise answers" {
		t.Fatalf("profile memories = %#v, want isolated profile memory", profileMemories)
	}
	taskMemories, err := reopened.TaskMemories(task.ID)
	if err != nil {
		t.Fatalf("list task memories: %v", err)
	}
	if len(taskMemories) != 1 || taskMemories[0].ID != taskMemory.ID || taskMemories[0].ProfileID != nil || taskMemories[0].Value != "SQLite" {
		t.Fatalf("task memories = %#v, want isolated task memory", taskMemories)
	}

	allInvariants, err := reopened.Invariants(task.ID)
	if err != nil {
		t.Fatalf("list invariants: %v", err)
	}
	if len(allInvariants) != 2 {
		t.Fatalf("all invariants = %#v, want active and inactive records", allInvariants)
	}
	activeInvariants, err := reopened.ActiveInvariants(task.ID)
	if err != nil {
		t.Fatalf("list active invariants: %v", err)
	}
	if len(activeInvariants) != 1 || activeInvariants[0].ID != activeInvariant.ID || !activeInvariants[0].Active {
		t.Fatalf("active invariants = %#v, want only active record", activeInvariants)
	}

	events, err := reopened.TaskEvents(task.ID)
	if err != nil {
		t.Fatalf("list task events: %v", err)
	}
	if len(events) != 3 || events[0].ID != firstEventID || events[1].ID != secondEventID || events[2].ID != thirdEventID || events[0].Event != TaskEventApprovePlan || events[1].Event != TaskEventSubmitResult || events[2].Event != TaskEventValidationPassed {
		t.Fatalf("task events = %#v, want chronological event history", events)
	}
}

func TestStoreMemoryAccessorsRejectInvalidWrites(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "memory.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.CreateProfile(ProfileInput{Language: "English", ResponseStyle: "concise", ResponseFormat: "text"}); err == nil {
		t.Fatal("create blank profile succeeded")
	}
	profile, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "English", ResponseStyle: "concise", ResponseFormat: "text"})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := store.CreateProfile(ProfileInput{Name: "Ada", Language: "Russian", ResponseStyle: "detailed", ResponseFormat: "text"}); err == nil {
		t.Fatal("duplicate profile name succeeded")
	}
	if _, err := store.SelectProfile(profile.ID); err != nil {
		t.Fatalf("select profile: %v", err)
	}
	if _, err := store.SelectProfile(profile.ID + 100); err == nil {
		t.Fatal("select unknown profile succeeded")
	}
	active, err := store.ActiveProfile()
	if err != nil {
		t.Fatalf("load selected profile: %v", err)
	}
	if active == nil || active.ID != profile.ID {
		t.Fatalf("active profile = %#v, want original selection", active)
	}

	_, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active branch: %v", err)
	}
	task, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "goal", Plan: "plan", CurrentStep: "step", ExpectedAction: "action"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := store.CreateTask(branch.ID, TaskSnapshot{Goal: "other", Plan: "plan", CurrentStep: "step", ExpectedAction: "action"}); err == nil {
		t.Fatal("create second active task succeeded")
	}
	if _, err := store.UpdateTask(task.ID, TaskSnapshot{Goal: " ", Plan: "plan", CurrentStep: "step", ExpectedAction: "action"}); err == nil {
		t.Fatal("update task with blank goal succeeded")
	}

	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScope("other"), MemoryKindDecision, "storage", "SQLite"); err == nil {
		t.Fatal("unknown memory scope succeeded")
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKind("other"), "storage", "SQLite"); err == nil {
		t.Fatal("unknown memory kind succeeded")
	}
	if _, err := store.PutActiveLongTermMemory(branch.ID, MemoryScopeProfile, MemoryKindDecision, "Storage", "SQLite"); err == nil {
		t.Fatal("unnormalized memory key succeeded")
	}
	if _, err := store.AddInvariant(Invariant{TaskID: task.ID + 100, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user"}); err == nil {
		t.Fatal("foreign invariant task succeeded")
	}
	invariant := Invariant{TaskID: task.ID, Category: "storage", Key: "database", RequiredValue: "SQLite", Rule: "Use SQLite.", Source: "user"}
	if _, err := store.AddInvariant(invariant); err != nil {
		t.Fatalf("add invariant: %v", err)
	}
	if _, err := store.AddInvariant(invariant); err == nil {
		t.Fatal("duplicate active invariant succeeded")
	}
}
