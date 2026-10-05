package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type evaluationPair struct {
	name  string
	lanes [2]LaneSettings
}

// RunEvaluate executes the approved control set, one explicit smoke question,
// or displays an immutable previously saved comparison without model calls.
func RunEvaluate(ctx context.Context, args []string) (runErr error) {
	for _, arg := range args {
		if arg == "-scenario" || arg == "--scenario" || strings.HasPrefix(arg, "-scenario=") || strings.HasPrefix(arg, "--scenario=") {
			return RunScenario(ctx, args)
		}
	}
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	questionsPath := flags.String("questions", "week-05/questions.json", "approved control question set")
	snapshotPath := flags.String("snapshot", "week-05/corpus/gtnh-20261004.json", "normalized source snapshot")
	dbPath := flags.String("db", "week-05/rag.db", "SQLite index database")
	ollamaEndpoint := flags.String("ollama", defaultOllamaEndpoint, "Ollama base URL")
	embeddingModel := flags.String("embedding-model", defaultOllamaModel, "installed Ollama embedding model")
	dimensions := flags.Int("dimensions", nomicDimensions, "embedding vector dimensions")
	provider := flags.String("provider", providerGroq, "chat provider: groq or deepseek")
	endpoint := flags.String("endpoint", "", "provider chat completions endpoint")
	mainModel := flags.String("main-model", "", "main answer model")
	auxModel := flags.String("aux-model", "", "rewrite model")
	strategy := flags.String("strategy", strategyStructural, "selected chunk strategy: fixed or structural")
	mode := flags.String("mode", "rag", "mode for the fixed-vs-structural comparison")
	candidateK := flags.Int("candidate-k", 10, "retrieval candidate limit")
	contextK := flags.Int("context-k", 3, "maximum chunks included in answer context")
	threshold := flags.Float64("threshold", 0.25, "cosine similarity threshold for filtered modes")
	calibration := flags.String("calibration", "uncalibrated", "threshold calibration note")
	temperature := flags.Float64("temperature", 0, "answer sampling temperature")
	maxTokens := flags.Int("max-tokens", 2048, "maximum answer tokens")
	interval := flags.Duration("interval", 0, "wait between sequential lane requests and comparisons")
	questionText := flags.String("question", "", "run one explicit smoke/experiment question instead of the control set")
	runID := flags.String("run", "", "inspect one saved comparison ID without model calls")
	outputDir := flags.String("out", "week-05/evaluations", "directory for JSON and Markdown reports")
	reviewPath := flags.String("review", "", "optional manual review JSON keyed by pair/question ID")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: evaluate [flags]")
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
		return fmt.Errorf("evaluate: unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *runID != "" {
		if *questionText != "" {
			return errors.New("evaluate: -run cannot be combined with -question")
		}
		store, err := openReadOnlyStore(*dbPath)
		if err != nil {
			return fmt.Errorf("open comparison store read-only: %w", err)
		}
		defer store.Close()
		run, err := store.LoadRun(ctx, *runID)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(run, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if *dimensions <= 0 || *candidateK <= 0 || *contextK <= 0 || *maxTokens <= 0 {
		return errors.New("evaluate: dimensions, candidate-k, context-k, and max-tokens must be positive")
	}
	if *threshold < -1 || *threshold > 1 {
		return errors.New("evaluate: threshold must be between -1 and 1")
	}
	if *interval < 0 {
		return errors.New("evaluate: interval must not be negative")
	}
	if *strategy != strategyFixed && *strategy != strategyStructural {
		return fmt.Errorf("evaluate: unknown strategy %q (want fixed or structural)", *strategy)
	}
	switch *mode {
	case "rag", "filtered", "rewritten-filtered", "grounded":
	default:
		return fmt.Errorf("evaluate: unknown mode %q", *mode)
	}

	client, generation, err := newProviderClient(*provider, *endpoint, *mainModel, *auxModel, *temperature, *maxTokens)
	if err != nil {
		return fmt.Errorf("evaluate: %w", err)
	}
	report := evaluationReport{Status: "incomplete", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Experiment: *questionText != "", QuestionSet: *questionsPath}
	defer func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if runErr != nil {
			report.Status = "incomplete"
			report.Error = runErr.Error()
		}
		if err := applyManualReview(&report, *reviewPath); err != nil && runErr == nil {
			runErr = err
			report.Status = "incomplete"
			report.Error = err.Error()
		}
		if err := writeEvaluationReport(*outputDir, report); err != nil && runErr == nil {
			runErr = err
		}
	}()

	snapshot, err := LoadSnapshot(*snapshotPath)
	if err != nil {
		return fmt.Errorf("load evaluation snapshot: %w", err)
	}
	store, err := OpenStore(*dbPath)
	if err != nil {
		return fmt.Errorf("open evaluation database: %w", err)
	}
	defer store.Close()
	build, err := store.ActiveBuild(ctx)
	if err != nil {
		return err
	}
	questions := QuestionSet{}
	if *questionText == "" {
		questions, err = loadQuestionSet(*questionsPath)
		if err != nil {
			return err
		}
		if err := validateQuestionSnapshot(questions, snapshot, build); err != nil {
			return err
		}
	} else {
		if build.SnapshotID != snapshot.ID {
			return fmt.Errorf("active index build snapshot %s does not match loaded snapshot %s", build.SnapshotID, snapshot.ID)
		}
		questions = QuestionSet{SnapshotID: snapshot.ID, Questions: []ControlQuestion{{ID: "smoke", Question: *questionText}}}
	}
	if build.Fingerprint.Dimensions != *dimensions {
		return fmt.Errorf("embedding dimensions mismatch: active build has %d; requested %d", build.Fingerprint.Dimensions, *dimensions)
	}
	report.SnapshotID, report.BuildID = snapshot.ID, build.ID
	ollama := OllamaClient{Endpoint: *ollamaEndpoint, Model: *embeddingModel, Dimensions: *dimensions}
	fingerprint, err := ollama.Fingerprint(ctx)
	if err != nil {
		return fmt.Errorf("read evaluation embedding fingerprint: %w", err)
	}
	if err := CompatibleFingerprint(build.Fingerprint, fingerprint); err != nil {
		return err
	}
	retrieval := RetrievalSettings{CandidateK: *candidateK, ContextK: *contextK, Threshold: *threshold, Calibrated: *calibration != "" && *calibration != "uncalibrated", Calibration: *calibration}
	engine := &Engine{Store: store, Ollama: ollama, Groq: client, Retrieval: retrieval}
	report.Settings = evaluationSettings{OllamaEndpoint: *ollamaEndpoint, EmbeddingModel: *embeddingModel, Dimensions: *dimensions, Provider: generation.Provider, Endpoint: generation.Endpoint, Generation: generation, Retrieval: retrieval, Strategy: *strategy, Mode: *mode, Interval: interval.String(), EmbeddingFingerprint: fingerprint}
	pairs := []evaluationPair{
		{name: "baseline-rag", lanes: [2]LaneSettings{{Mode: "no-rag", Strategy: *strategy}, {Mode: "rag", Strategy: *strategy}}},
		{name: "fixed-structural", lanes: [2]LaneSettings{{Mode: *mode, Strategy: strategyFixed}, {Mode: *mode, Strategy: strategyStructural}}},
		{name: "rag-filtered", lanes: [2]LaneSettings{{Mode: "rag", Strategy: *strategy}, {Mode: "filtered", Strategy: *strategy}}},
		{name: "filtered-rewritten", lanes: [2]LaneSettings{{Mode: "filtered", Strategy: *strategy}, {Mode: "rewritten-filtered", Strategy: *strategy}}},
		{name: "rag-grounded", lanes: [2]LaneSettings{{Mode: "rag", Strategy: *strategy}, {Mode: "grounded", Strategy: *strategy}}},
	}
	comparisonNumber := 0
	totalComparisons := len(questions.Questions) * len(pairs)
	for _, question := range questions.Questions {
		for _, pair := range pairs {
			comparison := evaluationComparison{QuestionID: question.ID, Pair: pair.name, Control: question}
			var lanes [2]LaneResult
			if *interval == 0 {
				type outcome struct {
					lane   int
					result LaneResult
					err    error
				}
				pairCtx, cancel := context.WithCancel(ctx)
				outcomes := make(chan outcome, 2)
				for lane := range lanes {
					go func(lane int) {
						started := time.Now()
						result, err := engine.RunLane(pairCtx, question.Question, pair.lanes[lane], nil, TaskState{})
						if err != nil {
							result.DurationMS = time.Since(started).Milliseconds()
						}
						outcomes <- outcome{lane: lane, result: result, err: err}
					}(lane)
				}
				for range lanes {
					out := <-outcomes
					lanes[out.lane] = out.result
					comparison.Observed[out.lane] = &lanes[out.lane]
					if out.err != nil && comparison.Error == "" {
						comparison.Error = fmt.Sprintf("lane %d failed: %v", out.lane+1, out.err)
						cancel()
					}
				}
				cancel()
			} else {
				for lane := range lanes {
					if lane > 0 {
						if err := waitEvaluationInterval(ctx, *interval, pair.name, question.ID, "between-lanes"); err != nil {
							comparison.Error = err.Error()
							break
						}
					}
					started := time.Now()
					result, laneErr := engine.RunLane(ctx, question.Question, pair.lanes[lane], nil, TaskState{})
					if laneErr != nil {
						result.DurationMS = time.Since(started).Milliseconds()
					}
					lanes[lane] = result
					comparison.Observed[lane] = &lanes[lane]
					if laneErr != nil {
						comparison.Error = fmt.Sprintf("lane %d failed: %v", lane+1, laneErr)
						break
					}
				}
			}
			if comparison.Error == "" {
				id, idErr := newRunID()
				if idErr != nil {
					comparison.Error = idErr.Error()
				} else {
					run := ComparisonRun{ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Question: question.Question, Lanes: lanes}
					if err := store.SaveRun(ctx, run); err != nil {
						comparison.Error = fmt.Sprintf("save complete comparison: %v", err)
					} else {
						comparison.Run = &run
						comparison.Observed = [2]*LaneResult{}
					}
				}
			}
			if comparison.Run != nil {
				for lane := range comparison.Metrics {
					metrics := evaluateLane(question, comparison.Run.Lanes[lane], snapshot)
					comparison.Metrics[lane] = &metrics
				}
			} else {
				for lane := range comparison.Observed {
					if comparison.Observed[lane] != nil {
						metrics := evaluateLane(question, *comparison.Observed[lane], snapshot)
						comparison.Metrics[lane] = &metrics
					}
				}
			}
			report.Comparisons = append(report.Comparisons, comparison)
			if comparison.Error != "" {
				return fmt.Errorf("%s / %s: %s", pair.name, question.ID, comparison.Error)
			}
			comparisonNumber++
			if *interval > 0 && comparisonNumber < totalComparisons {
				if err := waitEvaluationInterval(ctx, *interval, pair.name, question.ID, "between-comparisons"); err != nil {
					return err
				}
			}
		}
	}
	report.Status = "complete"
	return nil
}

func waitEvaluationInterval(ctx context.Context, interval time.Duration, pair, question, boundary string) error {
	started := time.Now().UTC()
	fmt.Printf("EVALUATION_PACING_START boundary=%s pair=%s question=%s at=%s duration=%s\n", boundary, pair, question, started.Format(time.RFC3339Nano), interval)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		fmt.Printf("EVALUATION_PACING_END boundary=%s pair=%s question=%s at=%s status=canceled\n", boundary, pair, question, time.Now().UTC().Format(time.RFC3339Nano))
		return ctx.Err()
	case <-timer.C:
		fmt.Printf("EVALUATION_PACING_END boundary=%s pair=%s question=%s at=%s status=complete\n", boundary, pair, question, time.Now().UTC().Format(time.RFC3339Nano))
		return nil
	}
}
