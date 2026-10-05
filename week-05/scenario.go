package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultScenarioPath = "week-05/scenarios.json"

// SHA-256 of the canonical two-scenario, twelve-turn dataset approved on 2026-10-04.
var approvedScenarioSetDigest = [sha256.Size]byte{
	0xd7, 0x51, 0xea, 0xa5, 0xee, 0xcc, 0x41, 0x11,
	0x43, 0x55, 0x73, 0x62, 0x8d, 0x63, 0x35, 0xc1,
	0xde, 0x2a, 0x53, 0x15, 0x21, 0x05, 0x32, 0x24,
	0xe8, 0xc9, 0xeb, 0xfe, 0x2d, 0xb0, 0x8b, 0x80,
}

type scenarioSet struct {
	SnapshotID string     `json:"snapshot_id"`
	Scenarios  []scenario `json:"scenarios"`
}

type scenario struct {
	ID           string         `json:"id"`
	Theme        string         `json:"theme"`
	RestartAfter int            `json:"restart_after"`
	Turns        []scenarioTurn `json:"turns"`
}

type scenarioTurn struct {
	Question            string           `json:"question"`
	ExpectedGoal        string           `json:"expected_goal"`
	ExpectedConstraints []string         `json:"expected_constraints"`
	ExpectedSources     []ExpectedSource `json:"expected_sources"`
	CorpusSufficient    bool             `json:"corpus_sufficient"`
}

type scenarioTurnReport struct {
	Ordinal             int                   `json:"ordinal"`
	Question            string                `json:"question"`
	ExpectedGoal        string                `json:"expected_goal"`
	ExpectedConstraints []string              `json:"expected_constraints"`
	CorpusSufficient    bool                  `json:"corpus_sufficient"`
	Lanes               [2]scenarioLaneReport `json:"lanes"`
}

type scenarioLaneReport struct {
	Result          LaneResult           `json:"result"`
	ExpectedSources []SourceEvaluation   `json:"expected_sources"`
	Manual          scenarioManualReview `json:"manual_review"`
}

type scenarioManualReview struct {
	GoalCorrectness      string `json:"goal_correctness"`
	ConstraintCompliance string `json:"constraint_compliance"`
	SemanticSupport      string `json:"semantic_support"`
	RefusalCorrectness   string `json:"refusal_correctness"`
}

type scenarioReport struct {
	Status       string               `json:"status"`
	StartedAt    string               `json:"started_at"`
	FinishedAt   string               `json:"finished_at,omitempty"`
	ScenarioID   string               `json:"scenario_id"`
	Theme        string               `json:"theme,omitempty"`
	SnapshotID   string               `json:"snapshot_id,omitempty"`
	BuildID      string               `json:"build_id,omitempty"`
	Embedding    EmbeddingFingerprint `json:"embedding_fingerprint,omitempty"`
	SessionID    string               `json:"session_id,omitempty"`
	RestartAfter int                  `json:"restart_after"`
	Settings     scenarioSettings     `json:"settings"`
	Turns        []scenarioTurnReport `json:"turns"`
	Error        string               `json:"error,omitempty"`
}

type scenarioSettings struct {
	OllamaEndpoint string             `json:"ollama_endpoint"`
	EmbeddingModel string             `json:"embedding_model"`
	Dimensions     int                `json:"embedding_dimensions"`
	Provider       string             `json:"provider"`
	Endpoint       string             `json:"endpoint"`
	Generation     GenerationSettings `json:"generation"`
	Retrieval      RetrievalSettings  `json:"retrieval"`
	Strategy       string             `json:"strategy"`
	Interval       string             `json:"interval"`
	RawTurns       int                `json:"raw_turns"`
}

// RunScenario executes or resumes one approved persistent two-lane scenario.
func RunScenario(ctx context.Context, args []string) (runErr error) {
	flags := flag.NewFlagSet("evaluate -scenario", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	scenarioID := flags.String("scenario", "", "approved scenario ID: progression or ore-processing")
	scenariosPath := flags.String("scenarios", defaultScenarioPath, "approved scenario dataset")
	sessionID := flags.String("session", "", "resume this existing chat session")
	until := flags.Int("until", 10, "stop after scenario turn 10 checkpoint or turn 12")
	dbPath := flags.String("db", "week-05/rag.db", "SQLite index and chat database")
	snapshotPath := flags.String("snapshot", "week-05/corpus/gtnh-20261004.json", "normalized source snapshot")
	rawTurns := flags.Int("raw-turns", 6, "recent raw turns supplied to each lane")
	ollamaEndpoint := flags.String("ollama", defaultOllamaEndpoint, "Ollama base URL")
	embeddingModel := flags.String("embedding-model", defaultOllamaModel, "installed Ollama embedding model")
	dimensions := flags.Int("dimensions", nomicDimensions, "embedding vector dimensions")
	provider := flags.String("provider", providerGroq, "chat provider: groq or deepseek")
	endpoint := flags.String("endpoint", "", "provider chat completions endpoint")
	mainModel := flags.String("main-model", "", "main answer model")
	auxModel := flags.String("aux-model", "", "task-memory model")
	strategy := flags.String("strategy", strategyStructural, "shared grounded chunk strategy: fixed or structural")
	candidateK := flags.Int("candidate-k", 10, "retrieval candidate limit")
	contextK := flags.Int("context-k", 3, "maximum chunks included in answer context")
	threshold := flags.Float64("threshold", 0.6, "cosine similarity threshold")
	calibration := flags.String("calibration", "week-05/calibration.json", "threshold calibration note")
	temperature := flags.Float64("temperature", 0, "answer sampling temperature")
	maxTokens := flags.Int("max-tokens", 2048, "maximum answer tokens")
	interval := flags.Duration("interval", 0, "explicit wait between sequential lanes and turns")
	outDir := flags.String("out", "week-05/scenario-reports", "scenario JSON and Markdown report directory")
	reviewPath := flags.String("review", "", "optional manual review JSON keyed by turn number")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: evaluate -scenario <id> [flags]")
		flags.SetOutput(os.Stderr)
		flags.PrintDefaults()
		flags.SetOutput(io.Discard)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("scenario: unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *scenarioID == "" {
		return errors.New("scenario: -scenario is required")
	}
	if *until != 10 && *until != 12 {
		return errors.New("scenario: -until must be 10 or 12")
	}
	if *sessionID != "" && *until == 10 {
		return errors.New("scenario: resumed sessions must target -until 12")
	}
	visitedFlags := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { visitedFlags[f.Name] = true })
	if *rawTurns < 0 || *dimensions <= 0 || *candidateK <= 0 || *contextK <= 0 || *maxTokens <= 0 {
		return errors.New("scenario: raw-turns must be nonnegative; dimensions, K values, and max-tokens must be positive")
	}
	if *threshold < -1 || *threshold > 1 || *interval < 0 {
		return errors.New("scenario: threshold must be between -1 and 1 and interval must not be negative")
	}
	if *strategy != strategyFixed && *strategy != strategyStructural {
		return fmt.Errorf("scenario: unknown strategy %q (want fixed or structural)", *strategy)
	}
	if *sessionID == "" && *until != 10 {
		return errors.New("scenario: a new session must stop at checkpoint -until 10")
	}
	var client GroqClient
	var generation GenerationSettings
	effectiveRawTurns := *rawTurns
	retrieval := RetrievalSettings{CandidateK: *candidateK, ContextK: *contextK, Threshold: *threshold, Calibrated: *calibration != "" && *calibration != "uncalibrated", Calibration: *calibration}
	report := scenarioReport{Status: "incomplete", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ScenarioID: *scenarioID, RestartAfter: 10, Settings: scenarioSettings{OllamaEndpoint: *ollamaEndpoint, EmbeddingModel: *embeddingModel, Dimensions: *dimensions, Retrieval: retrieval, Strategy: *strategy, Interval: interval.String(), RawTurns: *rawTurns}}
	defer func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if runErr != nil {
			report.Status, report.Error = "incomplete", runErr.Error()
		}
		if err := applyScenarioReview(&report, *reviewPath); err != nil && runErr == nil {
			runErr = err
			report.Status, report.Error = "incomplete", err.Error()
		}
		if err := writeScenarioReport(*outDir, report); err != nil && runErr == nil {
			runErr = err
		}
	}()

	set, err := loadScenarioSet(*scenariosPath)
	if err != nil {
		return err
	}
	selected, ok := findScenario(set, *scenarioID)
	if !ok {
		return fmt.Errorf("scenario: unknown scenario %q", *scenarioID)
	}
	report.Theme, report.RestartAfter = selected.Theme, selected.RestartAfter
	if len(set.Scenarios) != 2 {
		return errors.New("scenario: dataset must contain exactly the approved two scenarios")
	}
	approvedIDs := map[string]bool{"progression": false, "ore-processing": false}
	for _, item := range set.Scenarios {
		if _, ok := approvedIDs[item.ID]; !ok || approvedIDs[item.ID] || item.RestartAfter != 10 || len(item.Turns) != 12 {
			return errors.New("scenario: dataset must contain progression and ore-processing with 12 turns and restart_after 10")
		}
		approvedIDs[item.ID] = true
	}
	snapshot, err := LoadSnapshot(*snapshotPath)
	if err != nil {
		return fmt.Errorf("scenario: load snapshot: %w", err)
	}
	report.SnapshotID = snapshot.ID
	store, err := OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	var session ChatSession
	var existing []ChatTurn
	if *sessionID != "" {
		session, err = store.LoadSession(ctx, *sessionID)
		if err != nil {
			return fmt.Errorf("scenario: resume session: %w", err)
		}
		requestedGeneration := GenerationSettings{Provider: *provider, Endpoint: *endpoint, MainModel: *mainModel, AuxModel: *auxModel, Temperature: *temperature, MaxTokens: *maxTokens}
		var resolvedStrategy string
		generation, retrieval, resolvedStrategy, effectiveRawTurns, err = resolveScenarioResumeSettings(session, visitedFlags, requestedGeneration, retrieval, *strategy, *rawTurns)
		if err != nil {
			return err
		}
		*strategy = resolvedStrategy
		report.SessionID = session.ID
		existing, err = store.LoadTurns(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("scenario: load committed turns: %w", err)
		}
		evidenceSnapshot := snapshot
		if snapshot.ID != set.SnapshotID {
			evidenceSnapshot = Snapshot{}
		}
		appendCommittedScenarioTurns(&report, existing, selected, evidenceSnapshot)
	}
	if snapshot.ID != set.SnapshotID {
		return fmt.Errorf("scenario: dataset snapshot %s does not match source snapshot %s", set.SnapshotID, snapshot.ID)
	}
	build, err := store.ActiveBuild(ctx)
	if err != nil {
		return err
	}
	if build.SnapshotID != snapshot.ID {
		return fmt.Errorf("scenario: active index snapshot %s does not match scenario snapshot %s", build.SnapshotID, snapshot.ID)
	}
	if build.Fingerprint.Dimensions != *dimensions {
		return fmt.Errorf("scenario: embedding dimensions mismatch: active build has %d; requested %d", build.Fingerprint.Dimensions, *dimensions)
	}
	ollama := OllamaClient{Endpoint: *ollamaEndpoint, Model: *embeddingModel, Dimensions: *dimensions}
	fingerprint, err := ollama.Fingerprint(ctx)
	if err != nil {
		return fmt.Errorf("scenario: read embedding fingerprint: %w", err)
	}
	if err := CompatibleFingerprint(build.Fingerprint, fingerprint); err != nil {
		return fmt.Errorf("scenario: %w", err)
	}
	for ordinal, turn := range selected.Turns {
		question := ControlQuestion{ID: fmt.Sprintf("scenario-%s-%02d", selected.ID, ordinal+1), ExpectedSources: turn.ExpectedSources}
		if err := validateQuestionSnapshot(QuestionSet{SnapshotID: snapshot.ID, Questions: []ControlQuestion{question}}, snapshot, build); err != nil {
			return fmt.Errorf("scenario turn %d: %w", ordinal+1, err)
		}
	}

	lanes := [2]LaneSettings{{Mode: "grounded", Strategy: *strategy}, {Mode: "grounded", Strategy: *strategy, TaskMemory: true}}
	report.SnapshotID, report.BuildID, report.Embedding = snapshot.ID, build.ID, fingerprint

	if *sessionID == "" {
		client, generation, err = newProviderClient(*provider, *endpoint, *mainModel, *auxModel, *temperature, *maxTokens)
		if err != nil {
			return fmt.Errorf("scenario: %w", err)
		}
		session, err = store.NewSession(ctx, lanes, generation, retrieval, *rawTurns)
		if err != nil {
			return fmt.Errorf("scenario: create chat session: %w", err)
		}
	} else {
		if err := validateScenarioResumePrefix(existing, *until, selected); err != nil {
			return err
		}
		for i, saved := range existing {
			for lane := range saved.Lanes {
				if saved.Lanes[lane].Build.ID != build.ID || saved.Lanes[lane].Build.SnapshotID != snapshot.ID || saved.Lanes[lane].Build.Fingerprint != fingerprint {
					return fmt.Errorf("scenario: stored turn %d lane %d used an incompatible build", i+1, lane+1)
				}
			}
		}
		if session.Lanes != lanes || session.Retrieval != retrieval {
			return errors.New("scenario: saved session settings do not match requested grounded lanes and retrieval settings")
		}
		client, generation, err = newProviderClient(generation.Provider, generation.Endpoint, generation.MainModel, generation.AuxModel, generation.Temperature, generation.MaxTokens)
		if err != nil {
			return fmt.Errorf("scenario: restore saved provider: %w", err)
		}
	}
	report.SessionID = session.ID
	effectiveRawTurns = session.RawTurns
	report.Settings.RawTurns = effectiveRawTurns
	report.Settings.Provider, report.Settings.Endpoint, report.Settings.Generation = generation.Provider, generation.Endpoint, generation
	report.Settings.Retrieval = session.Retrieval
	report.Settings.Strategy = session.Lanes[0].Strategy
	service := ChatService{Engine: &Engine{Store: store, Ollama: ollama, Groq: client, Retrieval: session.Retrieval}, Interval: *interval}
	pending := scenarioTurnsAfterPrefix(selected, len(existing), *until)
	for offset, turnToRun := range pending {
		i := len(existing) + offset
		if i > len(existing) && *interval > 0 {
			if err := waitChatInterval(ctx, *interval); err != nil {
				return fmt.Errorf("scenario: pacing before turn %d: %w", i+1, err)
			}
		}
		turn, runErr := service.RunTurn(ctx, session.ID, turnToRun.Question)
		if runErr != nil {
			return fmt.Errorf("scenario turn %d failed (retained %d committed turns): %w", i+1, len(report.Turns), runErr)
		}
		report.Turns = append(report.Turns, buildScenarioTurnReport(turn, turnToRun, snapshot))
		fmt.Printf("SCENARIO_TURN_COMMITTED scenario=%s session=%s turn=%d/%d\n", selected.ID, session.ID, turn.Ordinal, *until)
	}
	if *until == selected.RestartAfter {
		report.Status = "checkpoint"
		fmt.Printf("SCENARIO_CHECKPOINT scenario=%s session=%s committed=%d next=%d\n", selected.ID, session.ID, len(report.Turns), selected.RestartAfter+1)
	} else {
		report.Status = "complete"
	}
	return nil
}

func validateScenarioResumePrefix(existing []ChatTurn, target int, selected scenario) error {
	if target < selected.RestartAfter || target > len(selected.Turns) {
		return fmt.Errorf("scenario: target %d is outside the resumable range %d..%d", target, selected.RestartAfter, len(selected.Turns))
	}
	if len(existing) < selected.RestartAfter {
		return fmt.Errorf("scenario: resume requires at least %d committed turns, found %d", selected.RestartAfter, len(existing))
	}
	if len(existing) > target {
		return fmt.Errorf("scenario: session already has %d turns, beyond requested limit %d", len(existing), target)
	}
	for i, saved := range existing {
		if err := validateScenarioPrefixTurn(saved, i, selected); err != nil {
			return err
		}
	}
	return nil
}

func loadScenarioSet(path string) (scenarioSet, error) {
	var set scenarioSet
	data, err := os.ReadFile(path)
	if err != nil {
		return set, fmt.Errorf("scenario: read dataset: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return set, fmt.Errorf("scenario: decode dataset: %w", err)
	}
	if err := requireJSONFields(fields, "snapshot_id", "scenarios"); err != nil {
		return set, fmt.Errorf("scenario: dataset: %w", err)
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(fields["scenarios"], &records); err != nil {
		return set, fmt.Errorf("scenario: decode scenario records: %w", err)
	}
	for i, record := range records {
		if err := requireJSONFields(record, "id", "theme", "restart_after", "turns"); err != nil {
			return set, fmt.Errorf("scenario %d: %w", i+1, err)
		}
		var turns []map[string]json.RawMessage
		if err := json.Unmarshal(record["turns"], &turns); err != nil {
			return set, fmt.Errorf("scenario %d turns: %w", i+1, err)
		}
		for j, turn := range turns {
			if err := requireJSONFields(turn, "question", "expected_goal", "expected_constraints", "expected_sources", "corpus_sufficient"); err != nil {
				return set, fmt.Errorf("scenario %d turn %d: %w", i+1, j+1, err)
			}
			var sources []map[string]json.RawMessage
			if err := json.Unmarshal(turn["expected_sources"], &sources); err != nil {
				return set, fmt.Errorf("scenario %d turn %d expected_sources: %w", i+1, j+1, err)
			}
			for k, source := range sources {
				if err := requireJSONFields(source, "document_id", "section_id"); err != nil {
					return set, fmt.Errorf("scenario %d turn %d source %d: %w", i+1, j+1, k+1, err)
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return set, fmt.Errorf("scenario: decode dataset: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return set, fmt.Errorf("scenario: decode dataset: %w", err)
	}
	for _, item := range set.Scenarios {
		if item.ID == "" || item.Theme == "" || item.RestartAfter <= 0 || len(item.Turns) == 0 {
			return set, errors.New("scenario: dataset contains an incomplete scenario")
		}
		for i, turn := range item.Turns {
			if strings.TrimSpace(turn.Question) == "" || turn.ExpectedGoal == "" || turn.ExpectedConstraints == nil || turn.ExpectedSources == nil {
				return set, fmt.Errorf("scenario %s turn %d is incomplete", item.ID, i+1)
			}
			if turn.CorpusSufficient != (len(turn.ExpectedSources) > 0) {
				return set, fmt.Errorf("scenario %s turn %d corpus_sufficient disagrees with expected_sources", item.ID, i+1)
			}
		}
	}
	canonical, err := json.Marshal(set)
	if err != nil {
		return set, fmt.Errorf("scenario: encode dataset: %w", err)
	}
	if sha256.Sum256(canonical) != approvedScenarioSetDigest {
		return set, errors.New("scenario: dataset differs from the approved two-scenario, twelve-turn inputs and expectations")
	}
	return set, nil
}

func validateScenarioPrefixTurn(saved ChatTurn, index int, selected scenario) error {
	if index < 0 || index >= len(selected.Turns) || saved.Ordinal != index+1 || saved.Question != selected.Turns[index].Question {
		return fmt.Errorf("scenario: stored turn %d does not match the approved scenario prefix", index+1)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func findScenario(set scenarioSet, id string) (scenario, bool) {
	for _, item := range set.Scenarios {
		if item.ID == id {
			return item, true
		}
	}
	return scenario{}, false
}

func buildScenarioTurnReport(turn ChatTurn, expected scenarioTurn, snapshot Snapshot) scenarioTurnReport {
	out := scenarioTurnReport{Ordinal: turn.Ordinal, Question: turn.Question, ExpectedGoal: expected.ExpectedGoal, ExpectedConstraints: append([]string(nil), expected.ExpectedConstraints...), CorpusSufficient: expected.CorpusSufficient}
	control := ControlQuestion{ExpectedSources: expected.ExpectedSources}
	for lane := range turn.Lanes {
		var sources []SourceEvaluation
		if snapshot.ID != "" {
			sources = evaluateLane(control, turn.Lanes[lane], snapshot).Sources
		}
		out.Lanes[lane] = scenarioLaneReport{Result: turn.Lanes[lane], ExpectedSources: sources, Manual: scenarioManualReview{GoalCorrectness: "unreviewed", ConstraintCompliance: "unreviewed", SemanticSupport: "unreviewed", RefusalCorrectness: "unreviewed"}}
	}
	return out
}

func applyScenarioReview(report *scenarioReport, path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("scenario: read manual review: %w", err)
	}
	var reviews map[string][2]scenarioManualReview
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reviews); err != nil {
		return fmt.Errorf("scenario: decode manual review: %w", err)
	}
	for i := range report.Turns {
		key := fmt.Sprintf("turn-%02d", report.Turns[i].Ordinal)
		if review, ok := reviews[key]; ok {
			for lane := range review {
				if review[lane].GoalCorrectness != "" {
					report.Turns[i].Lanes[lane].Manual.GoalCorrectness = review[lane].GoalCorrectness
				}
				if review[lane].ConstraintCompliance != "" {
					report.Turns[i].Lanes[lane].Manual.ConstraintCompliance = review[lane].ConstraintCompliance
				}
				if review[lane].SemanticSupport != "" {
					report.Turns[i].Lanes[lane].Manual.SemanticSupport = review[lane].SemanticSupport
				}
				if review[lane].RefusalCorrectness != "" {
					report.Turns[i].Lanes[lane].Manual.RefusalCorrectness = review[lane].RefusalCorrectness
				}
			}
		}
	}
	return nil
}

func writeScenarioReport(dir string, report scenarioReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("scenario: create report directory: %w", err)
	}
	base := "scenario-" + report.ScenarioID + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("scenario: encode report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".json"), data, 0o600); err != nil {
		return fmt.Errorf("scenario: write JSON report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, base+".md"), []byte(renderScenarioMarkdown(report)), 0o600); err != nil {
		return fmt.Errorf("scenario: write Markdown report: %w", err)
	}
	return nil
}

func renderScenarioMarkdown(report scenarioReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Scenario %s — %s\n\nStatus: %s\nTheme: %s\nSnapshot: %s\nBuild: %s\nSession: %s\nRestart after: %d\nSettings: `%+v`\n", report.ScenarioID, report.Theme, report.Status, report.Theme, report.SnapshotID, report.BuildID, report.SessionID, report.RestartAfter, report.Settings)
	if report.Error != "" {
		fmt.Fprintf(&b, "\nIncomplete: %s\n", report.Error)
	}
	for _, turn := range report.Turns {
		fmt.Fprintf(&b, "\n## Turn %d — %s\n\nExpected goal rubric: %s\nExpected constraint rubrics: %v\nCorpus sufficient: %t\n", turn.Ordinal, turn.Question, turn.ExpectedGoal, turn.ExpectedConstraints, turn.CorpusSufficient)
		for lane, result := range turn.Lanes {
			fmt.Fprintf(&b, "\n### Lane %d — memory=%t\n\nSettings: `%+v`\nGeneration: `%+v`\nRetrieval: `%+v`\nAnswer status: %s\nRefusal: %t\nAnswer: %s\nGoal state: %q (quote %q)\nClarifications: `%+v`\nConstraints: `%+v`\nTerms: `%+v`\nManual review: goal correctness=%s; constraint compliance=%s; semantic support=%s; refusal correctness=%s\n", lane+1, result.Result.Settings.TaskMemory, result.Result.Settings, result.Result.Generation, result.Result.Retrieval, result.Result.Answer.Status, result.Result.Answer.Status == "insufficient_context", result.Result.Answer.Answer, result.Result.State.Goal, result.Result.State.GoalQuote, result.Result.State.Clarifications, result.Result.State.Constraints, result.Result.State.Terms, result.Manual.GoalCorrectness, result.Manual.ConstraintCompliance, result.Manual.SemanticSupport, result.Manual.RefusalCorrectness)
			fmt.Fprintf(&b, "Candidates: %d; context: %d\n", len(result.Result.Candidates), len(result.Result.Context))
			for _, candidate := range result.Result.Candidates {
				fmt.Fprintf(&b, "- candidate %s doc=%s sections=%v text=%q\n", candidate.Chunk.ID, candidate.Chunk.DocumentID, candidate.Chunk.SectionPaths, candidate.Chunk.Text)
			}
			for _, candidate := range result.Result.Context {
				fmt.Fprintf(&b, "- evidence %s doc=%s sections=%v text=%q\n", candidate.Chunk.ID, candidate.Chunk.DocumentID, candidate.Chunk.SectionPaths, candidate.Chunk.Text)
			}
			for _, citation := range result.Result.Answer.Citations {
				fmt.Fprintf(&b, "- citation %s exact-quote=%q\n", citation.ChunkID, citation.Quote)
			}
			for _, source := range result.ExpectedSources {
				fmt.Fprintf(&b, "- expected source %s/%s: candidate=%t context/source_present=%t cited=%t quote_present/exact=%t section=%q\n", source.DocumentID, source.SectionID, source.CandidateHit, source.SourcePresent, source.CitedHit, source.QuotesExact, source.SectionPath)
			}
		}
	}
	return b.String()
}

func appendCommittedScenarioTurns(report *scenarioReport, turns []ChatTurn, selected scenario, snapshot Snapshot) {
	for i, saved := range turns {
		expected := scenarioTurn{}
		if i < len(selected.Turns) {
			expected = selected.Turns[i]
		}
		report.Turns = append(report.Turns, buildScenarioTurnReport(saved, expected, snapshot))
	}
}

func resolveScenarioResumeSettings(session ChatSession, explicit map[string]bool, requested GenerationSettings, requestedRetrieval RetrievalSettings, requestedStrategy string, rawTurns int) (GenerationSettings, RetrievalSettings, string, int, error) {
	if explicit["raw-turns"] && rawTurns != session.RawTurns {
		return GenerationSettings{}, RetrievalSettings{}, "", 0, fmt.Errorf("scenario: requested raw-turns %d does not match saved value %d", rawTurns, session.RawTurns)
	}
	if explicit["provider"] && requested.Provider != session.Generation.Provider ||
		explicit["endpoint"] && requested.Endpoint != session.Generation.Endpoint ||
		explicit["main-model"] && requested.MainModel != session.Generation.MainModel ||
		explicit["aux-model"] && requested.AuxModel != session.Generation.AuxModel ||
		explicit["temperature"] && requested.Temperature != session.Generation.Temperature ||
		explicit["max-tokens"] && requested.MaxTokens != session.Generation.MaxTokens {
		return GenerationSettings{}, RetrievalSettings{}, "", 0, errors.New("scenario: explicit generation settings do not match saved session")
	}
	if explicit["candidate-k"] && requestedRetrieval.CandidateK != session.Retrieval.CandidateK ||
		explicit["context-k"] && requestedRetrieval.ContextK != session.Retrieval.ContextK ||
		explicit["threshold"] && requestedRetrieval.Threshold != session.Retrieval.Threshold ||
		explicit["calibration"] && (requestedRetrieval.Calibrated != session.Retrieval.Calibrated || requestedRetrieval.Calibration != session.Retrieval.Calibration) {
		return GenerationSettings{}, RetrievalSettings{}, "", 0, errors.New("scenario: explicit retrieval settings do not match saved session")
	}
	savedStrategy := session.Lanes[0].Strategy
	expectedLanes := [2]LaneSettings{{Mode: "grounded", Strategy: savedStrategy}, {Mode: "grounded", Strategy: savedStrategy, TaskMemory: true}}
	if session.Lanes != expectedLanes || !validStrategy(savedStrategy) {
		return GenerationSettings{}, RetrievalSettings{}, "", 0, errors.New("scenario: saved session lanes are not a supported grounded scenario pair")
	}
	if explicit["strategy"] && requestedStrategy != session.Lanes[0].Strategy {
		return GenerationSettings{}, RetrievalSettings{}, "", 0, errors.New("scenario: explicit strategy does not match saved session")
	}
	return session.Generation, session.Retrieval, savedStrategy, session.RawTurns, nil
}

func scenarioTurnsAfterPrefix(selected scenario, committed, target int) []scenarioTurn {
	return selected.Turns[committed:target]
}
