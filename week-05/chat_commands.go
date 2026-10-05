package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// RunChat validates chat configuration, restores or creates a session, and runs
// the terminal interface. Saved sessions retain their own immutable settings.
func RunChat(ctx context.Context, args []string) (runErr error) {
	flags := flag.NewFlagSet("chat", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dbPath := flags.String("db", "week-05/rag.db", "SQLite database path")
	ollamaEndpoint := flags.String("ollama", defaultOllamaEndpoint, "Ollama base URL")
	embeddingModel := flags.String("embedding-model", defaultOllamaModel, "installed Ollama embedding model")
	dimensions := flags.Int("dimensions", nomicDimensions, "embedding vector dimensions")
	provider := flags.String("provider", providerGroq, "chat provider: groq or deepseek")
	endpoint := flags.String("endpoint", "", "provider chat completions endpoint")
	mainModel := flags.String("main-model", "", "main answer model")
	auxModel := flags.String("aux-model", "", "rewrite and task-state model")
	dbRawTurns := flags.Int("raw-turns", 0, "maximum recent conversation turns passed to each lane (default: 6; resumes use the saved value unless explicitly set)")
	sessionID := flags.String("session", "", "resume this saved session (its stored settings are used)")
	pair := flags.String("pair", "grounded-memory", "lane pair: grounded-memory, grounded-no-memory, baseline-rag, fixed-structural, rag-filtered, filtered-rewritten, or rag-grounded")
	experiment := flags.Bool("experiment", false, "run an isolated no-history retrieval comparison instead of persistent chat")
	strategy := flags.String("strategy", strategyStructural, "persistent lane strategy, or shared strategy for fixed-structural: fixed or structural")
	candidateK := flags.Int("candidate-k", 10, "retrieval candidate limit")
	contextK := flags.Int("context-k", 3, "maximum chunks included in answer context")
	threshold := flags.Float64("threshold", 0.60, "cosine similarity threshold for filtered modes")
	calibration := flags.String("calibration", "week-05/calibration.json", "threshold calibration note; 'uncalibrated' marks threshold uncalibrated")
	temperature := flags.Float64("temperature", 0, "answer sampling temperature")
	maxTokens := flags.Int("max-tokens", 2048, "maximum answer tokens")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: chat [flags]")
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
		return fmt.Errorf("chat: unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *dimensions <= 0 || *dbRawTurns < 0 || *candidateK <= 0 || *contextK <= 0 || *maxTokens <= 0 {
		return errors.New("chat: dimensions, candidate-k, context-k, and max-tokens must be positive; raw-turns must not be negative")
	}
	if *threshold < -1 || *threshold > 1 {
		return errors.New("chat: threshold must be between -1 and 1")
	}
	if *strategy != strategyFixed && *strategy != strategyStructural {
		return fmt.Errorf("chat: unknown strategy %q (want fixed or structural)", *strategy)
	}
	if *experiment && *sessionID != "" {
		return errors.New("chat: -experiment cannot be combined with -session")
	}

	store, err := OpenStore(*dbPath)
	if err != nil {
		return fmt.Errorf("open chat database: %w", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && runErr == nil {
			runErr = fmt.Errorf("close chat database: %w", closeErr)
		}
	}()

	client, generation, err := newProviderClient(*provider, *endpoint, *mainModel, *auxModel, *temperature, *maxTokens)
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}
	ollama := OllamaClient{Endpoint: *ollamaEndpoint, Model: *embeddingModel, Dimensions: *dimensions}
	engine := &Engine{Store: store, Ollama: ollama, Groq: client, Retrieval: RetrievalSettings{
		CandidateK: *candidateK, ContextK: *contextK, Threshold: *threshold,
		Calibrated: *calibration != "" && *calibration != "uncalibrated", Calibration: *calibration,
	}}
	lanes, err := chatPair(*pair, *strategy, *experiment)
	if err != nil {
		return err
	}

	var session ChatSession
	if !*experiment {
		explicit := make(map[string]bool)
		flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
		changed := false
		for _, name := range []string{"provider", "endpoint", "main-model", "aux-model", "temperature", "max-tokens", "candidate-k", "context-k", "threshold", "calibration", "pair", "strategy", "raw-turns"} {
			changed = changed || explicit[name]
		}
		if *sessionID != "" {
			session, err = store.LoadSession(ctx, *sessionID)
			if err != nil {
				return fmt.Errorf("resume chat session: %w", err)
			}
			if changed {
				resolved, resolveErr := resolveChatResumeConfig(
					session,
					explicit,
					GenerationSettings{
						Provider: *provider, Endpoint: *endpoint, MainModel: *mainModel, AuxModel: *auxModel,
						Temperature: *temperature, MaxTokens: *maxTokens,
					},
					engine.Retrieval, *dbRawTurns, *pair, *strategy,
				)
				if resolveErr != nil {
					return resolveErr
				}
				session = resolved
				client, generation, err = newProviderClient(
					session.Generation.Provider, session.Generation.Endpoint,
					session.Generation.MainModel, session.Generation.AuxModel,
					session.Generation.Temperature, session.Generation.MaxTokens,
				)
				if err != nil {
					return fmt.Errorf("chat: configure resumed provider: %w", err)
				}
				engine.Groq = client
				engine.Retrieval = session.Retrieval
				created, createErr := store.NewSession(ctx, session.Lanes, generation, session.Retrieval, session.RawTurns)
				if createErr != nil {
					return fmt.Errorf("create clean chat session: %w", createErr)
				}
				session = created
			}
		} else {
			session, err = store.NewSession(ctx, lanes, generation, engine.Retrieval, *dbRawTurns)
			if err != nil {
				return fmt.Errorf("create clean chat session: %w", err)
			}
		}
	}

	uiCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if session.ID != "" {
		lanes = session.Lanes
		engine.Retrieval = session.Retrieval
		if session.Generation.Provider == "" || session.Generation.Endpoint == "" {
			return errors.New("chat: saved session lacks provider/endpoint provenance; start a new session instead of resuming it")
		}
		savedClient, savedGeneration, err := newProviderClient(
			session.Generation.Provider, session.Generation.Endpoint,
			session.Generation.MainModel, session.Generation.AuxModel,
			session.Generation.Temperature, session.Generation.MaxTokens,
		)
		if err != nil {
			return fmt.Errorf("chat: restore saved provider settings: %w", err)
		}
		if savedGeneration != session.Generation {
			return errors.New("chat: saved generation settings are incomplete")
		}
		engine.Groq = savedClient
		generation = savedGeneration
	}
	app, err := newChatApp(uiCtx, cancel, store, engine, *dbRawTurns, session, lanes, *experiment, generation)
	if err != nil {
		return err
	}
	program := tea.NewProgram(app, tea.WithContext(uiCtx))
	finalModel, err := program.Run()
	if err != nil {
		cancel()
		if app.fatalErr != nil {
			return app.fatalErr
		}
		return fmt.Errorf("run chat terminal UI: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if finalApp, ok := finalModel.(*chatApp); ok && finalApp.fatalErr != nil {
		return finalApp.fatalErr
	}
	return nil
}

func resolveChatResumeConfig(saved ChatSession, explicit map[string]bool, requestedGeneration GenerationSettings, requestedRetrieval RetrievalSettings, requestedRawTurns int, requestedPair, requestedStrategy string) (ChatSession, error) {
	resolved := saved
	generation := saved.Generation
	if explicit["provider"] {
		providerChanged := requestedGeneration.Provider != generation.Provider
		generation.Provider = requestedGeneration.Provider
		if providerChanged {
			generation.Endpoint = defaultProviderEndpoint(generation.Provider)
			generation.MainModel, generation.AuxModel = providerModels(generation.Provider)
		}
	}
	if explicit["endpoint"] {
		generation.Endpoint = requestedGeneration.Endpoint
		if generation.Endpoint == "" {
			generation.Endpoint = defaultProviderEndpoint(generation.Provider)
		}
	}
	if explicit["main-model"] {
		generation.MainModel = requestedGeneration.MainModel
		if generation.MainModel == "" {
			generation.MainModel, _ = providerModels(generation.Provider)
		}
	}
	if explicit["aux-model"] {
		generation.AuxModel = requestedGeneration.AuxModel
		if generation.AuxModel == "" {
			_, generation.AuxModel = providerModels(generation.Provider)
		}
	}
	if explicit["temperature"] {
		generation.Temperature = requestedGeneration.Temperature
	}
	if explicit["max-tokens"] {
		generation.MaxTokens = requestedGeneration.MaxTokens
	}
	resolved.Generation = generation

	retrieval := saved.Retrieval
	if explicit["candidate-k"] {
		retrieval.CandidateK = requestedRetrieval.CandidateK
	}
	if explicit["context-k"] {
		retrieval.ContextK = requestedRetrieval.ContextK
	}
	if explicit["threshold"] {
		retrieval.Threshold = requestedRetrieval.Threshold
	}
	if explicit["calibration"] {
		retrieval.Calibration = requestedRetrieval.Calibration
		retrieval.Calibrated = requestedRetrieval.Calibrated
	}
	resolved.Retrieval = retrieval
	if explicit["raw-turns"] {
		resolved.RawTurns = requestedRawTurns
	}

	strategy := saved.Lanes[0].Strategy
	if explicit["strategy"] {
		strategy = requestedStrategy
	}
	if explicit["pair"] {
		lanes, err := chatPair(requestedPair, strategy, false)
		if err != nil {
			return ChatSession{}, err
		}
		resolved.Lanes = lanes
	} else if explicit["strategy"] {
		for i := range resolved.Lanes {
			resolved.Lanes[i].Strategy = strategy
		}
	}
	return resolved, nil
}

func chatPair(name, strategy string, experiment bool) ([2]LaneSettings, error) {
	structural := LaneSettings{Mode: "grounded", Strategy: strategy}
	var lanes [2]LaneSettings
	switch name {
	case "grounded-memory":
		lanes = [2]LaneSettings{{Mode: "grounded", Strategy: strategy}, {Mode: "grounded", Strategy: strategy, TaskMemory: true}}
	case "grounded-no-memory":
		lanes = [2]LaneSettings{structural, structural}
	case "baseline-rag":
		lanes = [2]LaneSettings{{Mode: "no-rag", Strategy: strategy}, {Mode: "rag", Strategy: strategy}}
	case "fixed-structural":
		lanes = [2]LaneSettings{{Mode: "grounded", Strategy: strategyFixed}, {Mode: "grounded", Strategy: strategyStructural}}
	case "rag-filtered":
		lanes = [2]LaneSettings{{Mode: "rag", Strategy: strategy}, {Mode: "filtered", Strategy: strategy}}
	case "filtered-rewritten":
		lanes = [2]LaneSettings{{Mode: "filtered", Strategy: strategy}, {Mode: "rewritten-filtered", Strategy: strategy}}
	case "rag-grounded":
		lanes = [2]LaneSettings{{Mode: "rag", Strategy: strategy}, {Mode: "grounded", Strategy: strategy}}
	default:
		return lanes, fmt.Errorf("chat: unknown pair %q", name)
	}
	if !experiment {
		for i := range lanes {
			if lanes[i].Mode != "grounded" {
				return lanes, fmt.Errorf("chat: pair %q is experimental; pass -experiment", name)
			}
		}
	}
	return lanes, nil
}
