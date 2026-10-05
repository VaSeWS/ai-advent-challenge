package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ChatSession stores immutable settings for a persistent pair of chat lanes.
type ChatSession struct {
	ID         string             `json:"id"`
	CreatedAt  string             `json:"created_at"`
	Lanes      [2]LaneSettings    `json:"lanes"`
	Generation GenerationSettings `json:"generation"`
	Retrieval  RetrievalSettings  `json:"retrieval"`
	RawTurns   int                `json:"raw_turns"`
}

// ChatTurn records one committed question and both lane results.
type ChatTurn struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	CreatedAt string        `json:"created_at"`
	Question  string        `json:"question"`
	Ordinal   int           `json:"ordinal"`
	Lanes     [2]LaneResult `json:"lanes"`
}

// TurnPreparation holds isolated, bounded request inputs for one uncommitted turn.
type TurnPreparation struct {
	Session   ChatSession     `json:"session"`
	Question  string          `json:"question"`
	Ordinal   int             `json:"ordinal"`
	Histories [2][]LLMMessage `json:"histories"`
	States    [2]TaskState    `json:"states"`
}

// ChatService runs lane-isolated turns and atomically persists complete pairs.
type ChatService struct {
	Engine   *Engine
	Interval time.Duration
}

// PrepareTurn loads persistent session state without storing a pending question.
func (c *ChatService) PrepareTurn(ctx context.Context, sessionID, question string) (TurnPreparation, error) {
	var prep TurnPreparation
	if c == nil || c.Engine == nil || c.Engine.Store == nil {
		return prep, errors.New("chat engine and store are required")
	}
	if question == "" {
		return prep, errors.New("question must not be empty")
	}
	store := c.Engine.Store
	session, err := store.LoadSession(ctx, sessionID)
	if err != nil {
		return prep, err
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_turns WHERE session_id=?`, sessionID).Scan(&count); err != nil {
		return prep, fmt.Errorf("count committed turns: %w", err)
	}
	prep = TurnPreparation{Session: session, Question: question, Ordinal: count + 1}
	limit := session.RawTurns
	if limit <= 0 {
		return TurnPreparation{}, errors.New("chat session raw turn limit is unknown; session migration is required")
	}
	for lane := range prep.Histories {
		prep.States[lane], err = store.LoadState(ctx, sessionID, lane)
		if err != nil {
			return TurnPreparation{}, err
		}
		prep.Histories[lane], err = store.loadRecentHistory(ctx, sessionID, lane, 2*limit)
		if err != nil {
			return TurnPreparation{}, err
		}
	}
	return prep, nil
}

// RunLane runs one stored lane using a private engine configuration.
func (c *ChatService) RunLane(ctx context.Context, prep TurnPreparation, lane int) (LaneResult, error) {
	if c == nil || c.Engine == nil {
		return LaneResult{}, errors.New("chat engine is required")
	}
	if lane < 0 || lane >= len(prep.Session.Lanes) {
		return LaneResult{}, errors.New("lane index must be 0 or 1")
	}
	settings := prep.Session.Lanes[lane]
	if settings.Mode != "no-rag" && settings.Mode != "grounded" {
		return LaneResult{}, fmt.Errorf("persistent chat does not allow experimental mode %q", settings.Mode)
	}
	if settings.Mode == "no-rag" && settings.TaskMemory {
		return LaneResult{}, errors.New("persistent no-rag baseline cannot use task memory")
	}
	engine := *c.Engine
	engine.Retrieval = prep.Session.Retrieval
	if prep.Session.Generation.Provider != "" {
		engine.Groq.Provider = prep.Session.Generation.Provider
		engine.Groq.Endpoint = prep.Session.Generation.Endpoint
		apiKey, err := providerAPIKey(prep.Session.Generation.Provider)
		if err != nil {
			return LaneResult{}, err
		}
		engine.Groq.APIKey = apiKey
	}
	engine.Groq.MainModel = prep.Session.Generation.MainModel
	engine.Groq.AuxModel = prep.Session.Generation.AuxModel
	engine.Groq.Temperature = prep.Session.Generation.Temperature
	engine.Groq.MaxTokens = prep.Session.Generation.MaxTokens
	result, err := engine.RunLane(ctx, prep.Question, settings, prep.Histories[lane], prep.States[lane])
	if err != nil {
		return LaneResult{}, err
	}
	return result, nil
}

// CommitTurn stores both lane outputs, histories, and task states atomically.
func (c *ChatService) CommitTurn(ctx context.Context, prep TurnPreparation, results [2]LaneResult) (ChatTurn, error) {
	if c == nil || c.Engine == nil || c.Engine.Store == nil {
		return ChatTurn{}, errors.New("chat engine and store are required")
	}
	return c.Engine.Store.commitTurn(ctx, prep, results)
}

// RunTurn executes and commits a complete pair; interval pacing is opt-in.
func (c *ChatService) RunTurn(ctx context.Context, sessionID, question string) (ChatTurn, error) {
	prep, err := c.PrepareTurn(ctx, sessionID, question)
	if err != nil {
		return ChatTurn{}, err
	}
	var results [2]LaneResult
	if c.Interval > 0 {
		results[0], err = c.RunLane(ctx, prep, 0)
		if err != nil {
			return ChatTurn{}, fmt.Errorf("lane 1 failed: %w", err)
		}
		if err := waitChatInterval(ctx, c.Interval); err != nil {
			return ChatTurn{}, err
		}
		results[1], err = c.RunLane(ctx, prep, 1)
		if err != nil {
			return ChatTurn{}, fmt.Errorf("lane 2 failed: %w", err)
		}
	} else {
		type outcome struct {
			lane   int
			result LaneResult
			err    error
		}
		pairCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		outcomes := make(chan outcome, 2)
		for lane := range results {
			go func(lane int) {
				result, runErr := c.RunLane(pairCtx, prep, lane)
				outcomes <- outcome{lane, result, runErr}
			}(lane)
		}
		for range results {
			out := <-outcomes
			if out.err != nil {
				cancel()
				return ChatTurn{}, fmt.Errorf("lane %d failed: %w", out.lane+1, out.err)
			}
			results[out.lane] = out.result
		}
	}
	return c.CommitTurn(ctx, prep, results)
}

func waitChatInterval(ctx context.Context, interval time.Duration) error {
	fmt.Printf("CHAT_INTERVAL_START %s duration=%s\n", time.Now().UTC().Format(time.RFC3339Nano), interval)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		fmt.Printf("CHAT_INTERVAL_END %s status=canceled\n", time.Now().UTC().Format(time.RFC3339Nano))
		return ctx.Err()
	case <-timer.C:
		fmt.Printf("CHAT_INTERVAL_END %s status=complete\n", time.Now().UTC().Format(time.RFC3339Nano))
		return nil
	}
}
