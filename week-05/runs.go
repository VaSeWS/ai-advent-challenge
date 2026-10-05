package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const comparisonRunsSchema = `CREATE TABLE IF NOT EXISTS comparison_runs (
 id TEXT PRIMARY KEY,
 created_at TEXT NOT NULL,
 question TEXT NOT NULL,
 result_json TEXT NOT NULL
)`

func (s *Store) ensureRunsSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("run store is not initialized")
	}
	if _, err := s.DB.ExecContext(ctx, comparisonRunsSchema); err != nil {
		return fmt.Errorf("initialize comparison run storage: %w", err)
	}
	return nil
}

// SaveRun stores only a complete pair; serialized results contain reproducible
// settings and evidence but no provider clients or credentials.
func (s *Store) SaveRun(ctx context.Context, run ComparisonRun) error {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.CreatedAt) == "" || strings.TrimSpace(run.Question) == "" {
		return errors.New("comparison run requires ID, creation time, and question")
	}
	for i, lane := range run.Lanes {
		if lane.OriginalQuery != run.Question {
			return fmt.Errorf("lane %d original query does not match comparison question", i+1)
		}
		if lane.DurationMS < 0 {
			return fmt.Errorf("lane %d has negative duration", i+1)
		}
		if lane.Answer.Status != "answer" && lane.Answer.Status != "insufficient_context" {
			return fmt.Errorf("lane %d is incomplete", i+1)
		}
		if err := ValidateAnswer(lane.Answer, lane.Settings.Mode, lane.Context); err != nil {
			return fmt.Errorf("lane %d answer is invalid: %w", i+1, err)
		}
		if lane.Settings.Mode != "no-rag" && lane.Build.ID == "" {
			return fmt.Errorf("lane %d has no index build", i+1)
		}
	}
	payload, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode comparison run: %w", err)
	}
	if err := s.ensureRunsSchema(ctx); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin comparison run transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO comparison_runs(id, created_at, question, result_json) VALUES (?, ?, ?, ?)`, run.ID, run.CreatedAt, run.Question, string(payload)); err != nil {
		return fmt.Errorf("save comparison run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit comparison run: %w", err)
	}
	return nil
}

// LoadRun returns the exact immutable result captured for one comparison ID.
func (s *Store) LoadRun(ctx context.Context, id string) (ComparisonRun, error) {
	var run ComparisonRun
	if strings.TrimSpace(id) == "" {
		return run, errors.New("comparison run ID must not be empty")
	}
	if err := s.ensureRunsSchema(ctx); err != nil {
		return run, err
	}
	var payload string
	if err := s.DB.QueryRowContext(ctx, `SELECT result_json FROM comparison_runs WHERE id = ?`, id).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return run, fmt.Errorf("comparison run %q not found", id)
		}
		return run, fmt.Errorf("load comparison run: %w", err)
	}
	if err := json.Unmarshal([]byte(payload), &run); err != nil {
		return run, fmt.Errorf("decode comparison run: %w", err)
	}
	return run, nil
}

// ListRuns returns stored comparisons in creation order, with complete
// historical lane settings and evidence retained in each result.
func (s *Store) ListRuns(ctx context.Context) ([]ComparisonRun, error) {
	if err := s.ensureRunsSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT result_json FROM comparison_runs ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list comparison runs: %w", err)
	}
	defer rows.Close()
	runs := make([]ComparisonRun, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan comparison run: %w", err)
		}
		var run ComparisonRun
		if err := json.Unmarshal([]byte(payload), &run); err != nil {
			return nil, fmt.Errorf("decode comparison run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comparison runs: %w", err)
	}
	return runs, nil
}

func newRunID() (string, error) { return newBuildID() }
