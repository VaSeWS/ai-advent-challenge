package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateScenarioPrefixTurn(t *testing.T) {
	approved := scenario{Turns: []scenarioTurn{{Question: "first approved input"}, {Question: "second approved input"}}}
	tests := []struct {
		name  string
		turn  ChatTurn
		index int
		want  bool
	}{
		{name: "matching checkpoint prefix", turn: ChatTurn{Ordinal: 1, Question: "first approved input"}, index: 0, want: true},
		{name: "input prefix mismatch", turn: ChatTurn{Ordinal: 1, Question: "different input"}, index: 0},
		{name: "ordinal mismatch", turn: ChatTurn{Ordinal: 2, Question: "first approved input"}, index: 0},
		{name: "beyond dataset", turn: ChatTurn{Ordinal: 3, Question: "extra input"}, index: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateScenarioPrefixTurn(test.turn, test.index, approved)
			if (err == nil) != test.want {
				t.Fatalf("validateScenarioPrefixTurn() error = %v, want success %t", err, test.want)
			}
		})
	}
}

func TestLoadScenarioSetRejectsModifiedApprovedTurn(t *testing.T) {
	set, err := loadScenarioSet("scenarios.json")
	if err != nil {
		t.Fatalf("load approved scenarios: %v", err)
	}
	if len(set.Scenarios) != 2 || set.Scenarios[0].ID != "progression" || set.Scenarios[1].ID != "ore-processing" {
		t.Fatalf("approved scenario identities changed: %+v", set.Scenarios)
	}
	for _, approved := range set.Scenarios {
		if len(approved.Turns) != 12 {
			t.Fatalf("scenario %q has %d turns, want 12", approved.ID, len(approved.Turns))
		}
	}
	set.Scenarios[0].Turns[0].Question = "Modified question with the same scenario IDs and turn counts."
	set.Scenarios[0].Turns[0].ExpectedConstraints[0] = "Modified constraint."
	modified, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("encode modified scenarios: %v", err)
	}
	path := filepath.Join(t.TempDir(), "scenarios.json")
	if err := os.WriteFile(path, modified, 0o600); err != nil {
		t.Fatalf("write modified scenarios: %v", err)
	}
	if _, err := loadScenarioSet(path); err == nil {
		t.Fatal("loadScenarioSet() accepted modified approved question and constraint")
	}
}

func TestScenarioResumeRetainsCommittedPrefixesWithoutReplay(t *testing.T) {
	ctx := context.Background()
	set, err := loadScenarioSet("scenarios.json")
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := findScenario(set, "progression")
	if !ok {
		t.Fatal("approved progression scenario missing")
	}

	var callsMu sync.Mutex
	callsByQuestion := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "test-embed:latest", "digest": "test-digest"}}})
		case "/api/embed":
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Ollama embed request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			vectors := make([][]float32, len(request.Input))
			for i := range vectors {
				vectors[i] = []float32{1, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		case "/chat/completions":
			var request struct {
				Messages []LLMMessage `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode chat completion request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			var question string
			for i := len(selected.Turns) - 1; i >= 0 && question == ""; i-- {
				for _, message := range request.Messages {
					if strings.Contains(message.Content, selected.Turns[i].Question) {
						question = selected.Turns[i].Question
						break
					}
				}
			}
			if question == "" {
				t.Errorf("chat completion request did not contain a scenario question")
			} else {
				callsMu.Lock()
				callsByQuestion[question]++
				callsMu.Unlock()
			}
			schema := "rag_answer"
			for _, message := range request.Messages {
				if strings.Contains(message.Content, `"lane_query_resolution"`) {
					schema = "lane_query_resolution"
					break
				}
			}
			content := `{"status":"insufficient_context","answer":"The available context does not establish a complete response.","citations":[]}`
			if schema == "lane_query_resolution" {
				content = `{"search_query":"GregTech New Horizons progression","goal":"","goal_quote":"","clarifications":[],"constraints":[],"terms":[]}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("DEEPSEEK_API_KEY", "test-only")

	snapshot, err := LoadSnapshot("corpus/gtnh-20261004.json")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "scenario.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	fingerprint := EmbeddingFingerprint{Model: "test-embed:latest", Digest: "test-digest", Preparation: embeddingPreparation("test-embed:latest"), Dimensions: 2}
	buildID, err := store.BuildIndex(ctx, snapshot, ChunkConfig{Size: 512}, fingerprint, func(_ context.Context, texts []string) ([][]float32, error) {
		vectors := make([][]float32, len(texts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		return vectors, nil
	})
	if err != nil {
		t.Fatalf("build pinned snapshot index: %v", err)
	}
	build, err := store.ActiveBuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if build.ID != buildID {
		t.Fatalf("active build ID=%q, want %q", build.ID, buildID)
	}

	lanes := [2]LaneSettings{{Mode: "grounded", Strategy: strategyFixed}, {Mode: "grounded", Strategy: strategyFixed, TaskMemory: true}}
	generation := GenerationSettings{Provider: providerDeepSeek, Endpoint: server.URL + "/chat/completions", MainModel: "test-main", AuxModel: "test-aux", MaxTokens: 64}
	retrieval := RetrievalSettings{CandidateK: 6, ContextK: 2, Threshold: 0.25, Calibration: "uncalibrated"}
	for _, prefix := range []int{10, 11, 12} {
		t.Run(fmt.Sprintf("prefix-%d", prefix), func(t *testing.T) {
			session, err := store.NewSession(ctx, lanes, generation, retrieval, 4)
			if err != nil {
				t.Fatal(err)
			}
			for i := range prefix {
				prior := [2]LaneResult{
					{OriginalQuery: selected.Turns[i].Question, SearchQuery: selected.Turns[i].Question, Settings: lanes[0], Build: build, Generation: generation, Retrieval: retrieval, Answer: Answer{Status: "insufficient_context", Answer: "The available context does not establish a complete response.", Citations: []Citation{}}},
					{OriginalQuery: selected.Turns[i].Question, SearchQuery: selected.Turns[i].Question, Settings: lanes[1], Build: build, Generation: generation, Retrieval: retrieval, Answer: Answer{Status: "insufficient_context", Answer: "The available context does not establish a complete response.", Citations: []Citation{}}},
				}
				encoded, err := json.Marshal(prior)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.DB.ExecContext(ctx, `INSERT INTO chat_turns(id,session_id,ordinal,created_at,question,lanes_json) VALUES(?,?,?,?,?,?)`, fmt.Sprintf("%s-turn-%02d", session.ID, i+1), session.ID, i+1, "saved-time", selected.Turns[i].Question, encoded); err != nil {
					t.Fatalf("persist committed turn %d: %v", i+1, err)
				}
				for lane := range lanes {
					for messageOrdinal, message := range []LLMMessage{{Role: "user", Content: selected.Turns[i].Question}, {Role: "assistant", Content: prior[lane].Answer.Answer}} {
						if _, err := store.DB.ExecContext(ctx, `INSERT INTO chat_messages(session_id,lane,ordinal,role,content) VALUES(?,?,?,?,?)`, session.ID, lane, 2*i+messageOrdinal+1, message.Role, message.Content); err != nil {
							t.Fatalf("persist lane %d history for turn %d: %v", lane+1, i+1, err)
						}
					}
				}
			}
			callsMu.Lock()
			callsByQuestion = make(map[string]int)
			callsMu.Unlock()

			outDir := filepath.Join(t.TempDir(), "reports")
			args := []string{
				"-scenario", "progression", "-session", session.ID, "-until", "12",
				"-scenarios", "scenarios.json", "-snapshot", "corpus/gtnh-20261004.json",
				"-db", dbPath, "-ollama", server.URL, "-embedding-model", "test-embed:latest",
				"-dimensions", "2", "-out", outDir,
			}
			if err := RunScenario(ctx, args); err != nil {
				t.Fatalf("RunScenario() with %d-turn prefix: %v", prefix, err)
			}

			persisted, err := store.LoadTurns(ctx, session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(persisted) != 12 {
				t.Fatalf("persisted %d turns after resume, want 12", len(persisted))
			}
			for i, turn := range persisted {
				if turn.Ordinal != i+1 || turn.Question != selected.Turns[i].Question {
					t.Fatalf("persisted turn %d = ordinal %d question %q, want approved ordinal %d question %q", i+1, turn.Ordinal, turn.Question, i+1, selected.Turns[i].Question)
				}
			}

			entries, err := os.ReadDir(outDir)
			if err != nil {
				t.Fatal(err)
			}
			var report scenarioReport
			var reportFound bool
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(outDir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &report); err != nil {
					t.Fatalf("decode RunScenario report: %v", err)
				}
				reportFound = true
			}
			if !reportFound {
				t.Fatal("RunScenario did not write its JSON report")
			}
			if report.Status != "complete" || len(report.Turns) != 12 {
				t.Fatalf("RunScenario report status=%q turns=%d, want complete with 12 turns", report.Status, len(report.Turns))
			}
			for i, turn := range report.Turns {
				if turn.Ordinal != i+1 || turn.Question != selected.Turns[i].Question {
					t.Fatalf("report turn %d = ordinal %d question %q, want approved ordinal %d question %q", i+1, turn.Ordinal, turn.Question, i+1, selected.Turns[i].Question)
				}
			}
			if report.Settings.RawTurns != 4 || report.Settings.Strategy != strategyFixed ||
				report.Settings.Retrieval.CandidateK != retrieval.CandidateK ||
				report.Settings.Retrieval.ContextK != retrieval.ContextK ||
				report.Settings.Retrieval.Threshold != retrieval.Threshold ||
				report.Settings.Retrieval.Calibration != retrieval.Calibration {
				t.Fatalf("report did not restore saved session settings: raw=%d strategy=%q retrieval=%+v", report.Settings.RawTurns, report.Settings.Strategy, report.Settings.Retrieval)
			}
			callsMu.Lock()
			for i, turn := range selected.Turns {
				want := 0
				if i >= prefix {
					want = 4
				}
				if got := callsByQuestion[turn.Question]; got != want {
					callsMu.Unlock()
					t.Fatalf("provider calls for scenario turn %d with prefix %d = %d, want %d", i+1, prefix, got, want)
				}
			}
			callsMu.Unlock()
		})
	}
	for _, mismatch := range []struct {
		name           string
		flag           string
		errorSubstring string
	}{
		{name: "strategy", flag: "-strategy=structural", errorSubstring: "strategy"},
		{name: "retrieval", flag: "-candidate-k=10", errorSubstring: "retrieval settings"},
	} {
		t.Run("explicit-"+mismatch.name+"-mismatch", func(t *testing.T) {
			session, err := store.NewSession(ctx, lanes, generation, retrieval, 4)
			if err != nil {
				t.Fatal(err)
			}
			callsMu.Lock()
			callsByQuestion = make(map[string]int)
			callsMu.Unlock()
			args := []string{
				"-scenario", "progression", "-session", session.ID, "-until", "12",
				"-scenarios", "scenarios.json", "-snapshot", "corpus/gtnh-20261004.json",
				"-db", dbPath, "-ollama", server.URL, "-embedding-model", "test-embed:latest",
				"-dimensions", "2", "-out", filepath.Join(t.TempDir(), "reports"), mismatch.flag,
			}
			err = RunScenario(ctx, args)
			if err == nil || !strings.Contains(err.Error(), mismatch.errorSubstring) {
				t.Fatalf("RunScenario mismatch error = %v, want %q", err, mismatch.errorSubstring)
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			if len(callsByQuestion) != 0 {
				t.Fatalf("provider calls occurred before explicit %s mismatch rejection: %v", mismatch.name, callsByQuestion)
			}
		})
	}
}

func TestScenarioResumeRejectsPrefixBeyondTarget(t *testing.T) {
	selected := scenario{RestartAfter: 10, Turns: make([]scenarioTurn, 12)}
	if err := validateScenarioResumePrefix(make([]ChatTurn, 13), 12, selected); err == nil || !strings.Contains(err.Error(), "beyond requested limit") {
		t.Fatalf("13-turn prefix validation error = %v, want beyond-target rejection", err)
	}
	if err := validateScenarioResumePrefix(make([]ChatTurn, 10), 13, selected); err == nil {
		t.Fatal("resume target beyond approved scenario was accepted")
	}
}

func TestBuildScenarioTurnReportRetainsCommittedStateAndEvidence(t *testing.T) {
	const sourceText = "Exact source quotation."
	chunk := Chunk{ID: "chunk-a", DocumentID: "doc-a", SectionPaths: []string{"Guide/Power"}, Start: 0, End: 3, Text: sourceText}
	turn := ChatTurn{
		Ordinal:  1,
		Question: "actual committed question",
		Lanes: [2]LaneResult{{
			Settings: LaneSettings{Mode: "grounded", TaskMemory: true},
			Context:  []Candidate{{Chunk: chunk}},
			Answer:   Answer{Status: "answer", Answer: "Actual committed answer.", Citations: []Citation{{ChunkID: chunk.ID, Quote: sourceText}}},
			State:    TaskState{Goal: "Persisted goal", Constraints: []StateFact{{Key: "limit", Value: "persisted", Quote: "user said so"}}},
		}},
	}
	expected := scenarioTurn{Question: "expected rubric input", ExpectedGoal: "goal rubric", ExpectedConstraints: []string{"constraint rubric"}, ExpectedSources: []ExpectedSource{{DocumentID: "doc-a", SectionID: "section-a"}}}
	snapshot := Snapshot{ID: "snapshot-a", Documents: []Document{{ID: "doc-a", Text: sourceText, Sections: []Section{{ID: "section-a", Path: "Guide/Power", Start: 0, End: 3}}}}}
	report := buildScenarioTurnReport(turn, expected, snapshot)
	if report.Question != turn.Question || report.Ordinal != turn.Ordinal {
		t.Fatalf("report did not retain actual committed turn: %+v", report)
	}
	lane := report.Lanes[0]
	if lane.Result.State.Goal != "Persisted goal" || len(lane.Result.State.Constraints) != 1 {
		t.Fatalf("report did not retain actual task state: %+v", lane.Result.State)
	}
	if len(lane.ExpectedSources) != 1 {
		t.Fatalf("report did not retain source/quote evidence: %+v", lane.ExpectedSources)
	}
	source := lane.ExpectedSources[0]
	if !source.CitedHit || !source.SourcePresent || !source.QuotePresent || !source.QuotesExact {
		t.Fatalf("report did not retain cited source/quote evidence: %+v", source)
	}
	var citationEvidenceFound bool
	for _, evidence := range source.Chunks {
		if evidence.Role == "citation" && evidence.ChunkID == chunk.ID && evidence.Quote == sourceText && evidence.QuoteExact {
			citationEvidenceFound = true
			break
		}
	}
	if !citationEvidenceFound {
		t.Fatalf("report did not associate the exact citation quote with the expected chunk: %+v", source.Chunks)
	}
	if lane.Manual.SemanticSupport != "unreviewed" || lane.Manual.GoalCorrectness != "unreviewed" || lane.Manual.ConstraintCompliance != "unreviewed" {
		t.Fatalf("mechanical evidence changed manual review status: %+v", lane.Manual)
	}
}

func TestScenarioResumeRestoresOmittedSettingsAndRejectsOverrides(t *testing.T) {
	savedRetrieval := RetrievalSettings{CandidateK: 7, ContextK: 2, Threshold: 0.3, Calibration: "uncalibrated"}
	saved := ChatSession{
		RawTurns: 4,
		Lanes:    [2]LaneSettings{{Mode: "grounded", Strategy: strategyFixed}, {Mode: "grounded", Strategy: strategyFixed, TaskMemory: true}},
		Generation: GenerationSettings{
			Provider: providerDeepSeek, Endpoint: "https://api.deepseek.com/v1/chat/completions",
			MainModel: "deep-main", AuxModel: "deep-aux", Temperature: 0.2, MaxTokens: 900,
		},
		Retrieval: savedRetrieval,
	}
	requested := GenerationSettings{Provider: providerGroq, MainModel: "default-main", AuxModel: "default-aux", MaxTokens: 2048}
	requestedRetrieval := RetrievalSettings{CandidateK: 10, ContextK: 3, Threshold: 0.6, Calibrated: true, Calibration: "calibration.json"}
	restored, retrieval, strategy, rawTurns, err := resolveScenarioResumeSettings(saved, map[string]bool{}, requested, requestedRetrieval, strategyStructural, 6)
	if err != nil {
		t.Fatal(err)
	}
	if restored != saved.Generation || retrieval != savedRetrieval || strategy != strategyFixed || rawTurns != saved.RawTurns {
		t.Fatalf("omitted settings restored generation=%+v retrieval=%+v strategy=%q raw-turns=%d", restored, retrieval, strategy, rawTurns)
	}
	if _, _, _, _, err := resolveScenarioResumeSettings(saved, map[string]bool{"raw-turns": true}, requested, requestedRetrieval, strategyStructural, 6); err == nil || !strings.Contains(err.Error(), "raw-turns") {
		t.Fatalf("explicit raw-turn mismatch error = %v", err)
	}
	if _, _, _, _, err := resolveScenarioResumeSettings(saved, map[string]bool{"provider": true}, requested, requestedRetrieval, strategyStructural, 4); err == nil || !strings.Contains(err.Error(), "generation settings") {
		t.Fatalf("explicit provider mismatch error = %v", err)
	}
	if _, _, _, _, err := resolveScenarioResumeSettings(saved, map[string]bool{"strategy": true}, requested, requestedRetrieval, strategyStructural, 4); err == nil || !strings.Contains(err.Error(), "strategy") {
		t.Fatalf("explicit strategy mismatch error = %v", err)
	}
	if _, _, _, _, err := resolveScenarioResumeSettings(saved, map[string]bool{"candidate-k": true}, requested, requestedRetrieval, strategyFixed, 4); err == nil || !strings.Contains(err.Error(), "retrieval settings") {
		t.Fatalf("explicit retrieval mismatch error = %v", err)
	}
}
