package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDueQuestionsFiltersTopicsAndSkipsInvalidDates(t *testing.T) {
	vault := t.TempDir()
	files := map[string]string{
		"go.md":     "---\nsr-due: 2026-09-20\n---\n# Go slices\n\n#Go #Architectural\n",
		"python.md": "---\nsr-due: 2026-09-27\n---\n# Python typing\n\n#Python\n",
		"python/Дженерики в Python. Как ограничить значения типов в дженерике.md": "---\nsr-due: 2026-09-27\n---\n# Normal\n\n#Python\n",
		"future.md":      "---\nsr-due: 2026-10-01\n---\n# Future\n\n#Go\n",
		"invalid.md":     "---\nsr-due: 2026-13-01\n---\n# Invalid\n\n#Go\n",
		"undated.md":     "---\ntags: #Go\n---\n# Undated\n",
		"nested/arch.md": "---\nsr-due: 2026-09-27\n---\n# Architecture\n\n#Architectural\n",
	}
	for name, contents := range files {
		path := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scheduler, err := newScheduler(vault, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	questions, err := scheduler.dueQuestions("#Go", time.Date(2026, 9, 27, 14, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 || questions[0].Title != "go" || questions[0].Due != "2026-09-20" || questions[0].Path != "go.md" {
		t.Fatalf("Go filter returned unexpected questions: %#v", questions)
	}
	all, err := scheduler.dueQuestions("", time.Date(2026, 9, 27, 14, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]question{
		"go.md":          {Title: "go", Topic: "#Architectural, #Go", Due: "2026-09-20", Path: "go.md"},
		"nested/arch.md": {Title: "arch", Topic: "#Architectural", Due: "2026-09-27", Path: "nested/arch.md"},
		"python.md":      {Title: "python", Topic: "#Python", Due: "2026-09-27", Path: "python.md"},
		"python/Дженерики в Python. Как ограничить значения типов в дженерике.md": {Title: "Дженерики в Python. Как ограничить значения типов в дженерике", Topic: "#Python", Due: "2026-09-27", Path: "python/Дженерики в Python. Как ограничить значения типов в дженерике.md"},
	}
	seen := make(map[string]bool, len(expected))
	if len(all) != len(expected) {
		t.Fatalf("empty topic should return exactly %d due valid entries, got %#v", len(expected), all)
	}
	for _, got := range all {
		want, ok := expected[got.Path]
		if !ok {
			t.Errorf("empty topic returned unexpected entry %q (future, invalid, and undated entries must be excluded)", got.Path)
			continue
		}
		if seen[got.Path] {
			t.Errorf("empty topic returned duplicate entry %q", got.Path)
		}
		seen[got.Path] = true
		if got.Title != want.Title || got.Topic != want.Topic || got.Due != want.Due || got.Path != want.Path {
			t.Errorf("empty topic returned unexpected question for %q: got %#v, want %#v", got.Path, got, want)
		}
	}
	for path := range expected {
		if !seen[path] {
			t.Errorf("empty topic omitted due question %q", path)
		}
	}
}

func TestScheduleAndExecutionPersistAcrossRestart(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "question.md"), []byte("---\nsr-due: 2026-09-27\n---\n# Persistent result\n#Go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "nested", "state.json")
	scheduler, err := newScheduler(vault, statePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.Local)
	if _, err := scheduler.add("Go", time.Second, now.Add(-2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.runDue(now); err != nil {
		t.Fatal(err)
	}

	restarted, err := newScheduler(vault, statePath)
	if err != nil {
		t.Fatal(err)
	}
	state := restarted.snapshot()
	if len(state.Schedules) != 1 || state.Schedules[0].Topic != "go" {
		t.Fatalf("schedule did not survive restart: %#v", state.Schedules)
	}
	if len(state.History) != 1 || state.History[0].Count != 1 || state.History[0].Questions[0].Title != "question" {
		t.Fatalf("execution result did not survive restart: %#v", state.History)
	}
}
