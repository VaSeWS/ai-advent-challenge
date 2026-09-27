package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeNote(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFindDueQuestionsFiltersHashtagsAndDates(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "late.md", "---\nsr-due: 2026-09-28\n---\n#Go\n")
	writeNote(t, vault, "today.md", "---\nsr-due: 2026-09-27\n---\n#go\nprivate note body")
	writeNote(t, vault, "old.md", "---\nsr-due: 2026-09-01\n---\n#Go\n")
	writeNote(t, vault, "substring.md", "---\nsr-due: 2026-09-01\n---\n#Golang\n")
	writeNote(t, vault, "missing.md", "---\ntitle: no due date\n---\n#Go\n")

	questions, missing, err := findDueQuestions(context.Background(), vault, "gO", "2026-09-27")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(questions))
	for i, item := range questions {
		got[i] = item.Title
	}
	if want := []string{"old", "today"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
	if missing != 1 {
		t.Fatalf("missing due count = %d, want 1", missing)
	}
	if questions[1].Topic != "gO" || questions[1].Path != "today.md" || questions[1].Due != "2026-09-27" {
		t.Fatalf("unexpected question result: %+v", questions[1])
	}
	plan, err := buildReviewPlan(buildPlanInput{Questions: questions, Topic: "gO", Date: "2026-09-27"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Count != len(questions) || !strings.Contains(plan.Plan, "old") || !strings.Contains(plan.Plan, "today") || strings.Contains(plan.Plan, "private note body") {
		t.Fatalf("plan did not preserve the filtered metadata: %+v", plan)
	}
}

func TestParseDateRejectsNonCanonicalDates(t *testing.T) {
	for _, value := range []string{"2026-9-7", "2026-02-30", "2026-09-27T00:00:00Z"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseDate(value); err == nil {
				t.Fatalf("parseDate(%q) succeeded", value)
			}
		})
	}
}

func TestBuildReviewPlanUsesQuestionMetadataOnly(t *testing.T) {
	result, err := buildReviewPlan(buildPlanInput{
		Questions: []question{{Title: "Concurrency", Topic: "Go", Due: "2026-09-27", Path: "concurrency.md"}},
		Topic:     "Go",
		Date:      "2026-09-27",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d, want 1", result.Count)
	}
	for _, expected := range []string{"Concurrency", "#Go", "2026-09-27"} {
		if !strings.Contains(result.Plan, expected) {
			t.Errorf("plan %q does not include %q", result.Plan, expected)
		}
	}
	if strings.Contains(result.Plan, "concurrency.md") || strings.Contains(result.Plan, "private note body") {
		t.Fatalf("plan exposed non-metadata: %q", result.Plan)
	}
}

func TestNormalizeTopicAllowsAllAndOptionalHash(t *testing.T) {
	for input, want := range map[string]string{"all": "", "#gO": "gO", "": ""} {
		if got := normalizeTopic(input); got != want {
			t.Errorf("normalizeTopic(%q) = %q, want %q", input, got, want)
		}
	}
}
