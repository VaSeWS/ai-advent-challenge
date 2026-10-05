package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) ensureChatSchema(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("store is required")
	}
	const schema = `
CREATE TABLE IF NOT EXISTS chat_sessions (
 id TEXT PRIMARY KEY, created_at TEXT NOT NULL, lanes_json TEXT NOT NULL,
 generation_json TEXT NOT NULL, retrieval_json TEXT NOT NULL,
 raw_turns INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chat_turns (
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES chat_sessions(id),
 ordinal INTEGER NOT NULL, created_at TEXT NOT NULL, question TEXT NOT NULL,
 lanes_json TEXT NOT NULL, UNIQUE(session_id, ordinal)
);
CREATE TABLE IF NOT EXISTS chat_messages (
 session_id TEXT NOT NULL REFERENCES chat_sessions(id), lane INTEGER NOT NULL,
 ordinal INTEGER NOT NULL, role TEXT NOT NULL, content TEXT NOT NULL,
 PRIMARY KEY(session_id, lane, ordinal)
);
CREATE TABLE IF NOT EXISTS chat_states (
 session_id TEXT NOT NULL REFERENCES chat_sessions(id), lane INTEGER NOT NULL,
 state_json TEXT NOT NULL, PRIMARY KEY(session_id, lane)
);`
	if _, err := s.DB.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize chat schema: %w", err)
	}
	rows, err := s.DB.QueryContext(ctx, `PRAGMA table_info(chat_sessions)`)
	if err != nil {
		return fmt.Errorf("inspect chat session schema: %w", err)
	}
	hasRawTurns := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("inspect chat session schema: %w", err)
		}
		if name == "raw_turns" {
			hasRawTurns = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect chat session schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect chat session schema: %w", err)
	}
	if !hasRawTurns {
		if _, err := s.DB.ExecContext(ctx, `ALTER TABLE chat_sessions ADD COLUMN raw_turns INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate chat session raw-turn setting: %w", err)
		}
	}
	return nil
}

// NewSession creates a persistent chat with empty lane-local task state.
func (s *Store) NewSession(ctx context.Context, lanes [2]LaneSettings, generation GenerationSettings, retrieval RetrievalSettings, rawTurns int) (ChatSession, error) {
	var session ChatSession
	if s == nil || s.DB == nil {
		return session, errors.New("store is required")
	}
	if rawTurns < 0 {
		return session, errors.New("raw turn limit must not be negative")
	}
	if rawTurns == 0 {
		rawTurns = 6
	}
	for i, lane := range lanes {
		if lane.Mode == "no-rag" {
			if lane.TaskMemory {
				return session, fmt.Errorf("lane %d no-rag baseline cannot use task memory", i+1)
			}
		} else if lane.Mode != "grounded" {
			return session, fmt.Errorf("lane %d persistent chat mode must be no-rag or grounded", i+1)
		}
		if !validStrategy(lane.Strategy) {
			return session, fmt.Errorf("lane %d has invalid strategy %q", i+1, lane.Strategy)
		}
	}
	generation.MainModel = strings.TrimSpace(generation.MainModel)
	generation.AuxModel = strings.TrimSpace(generation.AuxModel)
	if generation.MainModel == "" || generation.AuxModel == "" || generation.MaxTokens <= 0 {
		return session, errors.New("chat generation models and positive token budget are required")
	}
	if retrieval.CandidateK <= 0 || retrieval.ContextK <= 0 || retrieval.Threshold < -1 || retrieval.Threshold > 1 {
		return session, errors.New("invalid chat retrieval settings")
	}
	if err := s.ensureChatSchema(ctx); err != nil {
		return session, err
	}
	id, err := newBuildID()
	if err != nil {
		return session, fmt.Errorf("create chat session ID: %w", err)
	}
	session = ChatSession{ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Lanes: lanes, Generation: generation, Retrieval: retrieval, RawTurns: rawTurns}
	lanesJSON, err := json.Marshal(lanes)
	if err != nil {
		return ChatSession{}, err
	}
	generationJSON, err := json.Marshal(generation)
	if err != nil {
		return ChatSession{}, err
	}
	retrievalJSON, err := json.Marshal(retrieval)
	if err != nil {
		return ChatSession{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ChatSession{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO chat_sessions(id,created_at,lanes_json,generation_json,retrieval_json,raw_turns) VALUES(?,?,?,?,?,?)`, session.ID, session.CreatedAt, lanesJSON, generationJSON, retrievalJSON, session.RawTurns); err != nil {
		return ChatSession{}, fmt.Errorf("insert chat session: %w", err)
	}
	for lane := range lanes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO chat_states(session_id,lane,state_json) VALUES(?,?,?)`, id, lane, `{}`); err != nil {
			return ChatSession{}, fmt.Errorf("initialize chat lane state: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return ChatSession{}, fmt.Errorf("commit chat session: %w", err)
	}
	return session, nil
}

// LoadSession returns immutable settings for one chat.
func (s *Store) LoadSession(ctx context.Context, id string) (ChatSession, error) {
	var session ChatSession
	if err := s.ensureChatSchema(ctx); err != nil {
		return session, err
	}
	var lanes, generation, retrieval string
	err := s.DB.QueryRowContext(ctx, `SELECT id,created_at,lanes_json,generation_json,retrieval_json,raw_turns FROM chat_sessions WHERE id=?`, id).Scan(&session.ID, &session.CreatedAt, &lanes, &generation, &retrieval, &session.RawTurns)
	if err != nil {
		return ChatSession{}, fmt.Errorf("load chat session: %w", err)
	}
	if session.RawTurns <= 0 {
		return ChatSession{}, errors.New("load chat session: raw turn limit is unknown; session migration is required")
	}
	if err = json.Unmarshal([]byte(lanes), &session.Lanes); err != nil {
		return ChatSession{}, fmt.Errorf("decode chat lanes: %w", err)
	}
	if err = json.Unmarshal([]byte(generation), &session.Generation); err != nil {
		return ChatSession{}, fmt.Errorf("decode chat generation: %w", err)
	}
	if err = json.Unmarshal([]byte(retrieval), &session.Retrieval); err != nil {
		return ChatSession{}, fmt.Errorf("decode chat retrieval: %w", err)
	}
	return session, nil
}

// ListSessions returns saved sessions newest first.
func (s *Store) ListSessions(ctx context.Context) ([]ChatSession, error) {
	if err := s.ensureChatSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,created_at,lanes_json,generation_json,retrieval_json,raw_turns FROM chat_sessions ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, fmt.Errorf("list chat sessions: %w", err)
	}
	defer rows.Close()
	var sessions []ChatSession
	for rows.Next() {
		var session ChatSession
		var lanes, generation, retrieval string
		if err := rows.Scan(&session.ID, &session.CreatedAt, &lanes, &generation, &retrieval, &session.RawTurns); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(lanes), &session.Lanes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(generation), &session.Generation); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(retrieval), &session.Retrieval); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

// LoadHistory returns every persisted raw message for one lane.
func (s *Store) LoadHistory(ctx context.Context, id string, lane int) ([]LLMMessage, error) {
	if lane < 0 || lane > 1 {
		return nil, errors.New("lane index must be 0 or 1")
	}
	if err := s.ensureChatSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT role,content FROM chat_messages WHERE session_id=? AND lane=? ORDER BY ordinal`, id, lane)
	if err != nil {
		return nil, fmt.Errorf("load chat history: %w", err)
	}
	defer rows.Close()
	var messages []LLMMessage
	for rows.Next() {
		var message LLMMessage
		if err := rows.Scan(&message.Role, &message.Content); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) loadRecentHistory(ctx context.Context, id string, lane, limit int) ([]LLMMessage, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT role,content FROM (
SELECT ordinal,role,content FROM chat_messages WHERE session_id=? AND lane=? ORDER BY ordinal DESC LIMIT ?
) ORDER BY ordinal`, id, lane, limit)
	if err != nil {
		return nil, fmt.Errorf("load recent chat history: %w", err)
	}
	defer rows.Close()
	messages := make([]LLMMessage, 0, limit)
	for rows.Next() {
		var message LLMMessage
		if err := rows.Scan(&message.Role, &message.Content); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// LoadState restores the full lane-local task memory.
func (s *Store) LoadState(ctx context.Context, id string, lane int) (TaskState, error) {
	var state TaskState
	if lane < 0 || lane > 1 {
		return state, errors.New("lane index must be 0 or 1")
	}
	if err := s.ensureChatSchema(ctx); err != nil {
		return state, err
	}
	var encoded string
	if err := s.DB.QueryRowContext(ctx, `SELECT state_json FROM chat_states WHERE session_id=? AND lane=?`, id, lane).Scan(&encoded); err != nil {
		return state, fmt.Errorf("load chat state: %w", err)
	}
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return TaskState{}, fmt.Errorf("decode chat state: %w", err)
	}
	return state, nil
}

// LoadTurns returns every complete committed paired turn in ordinal order.
func (s *Store) LoadTurns(ctx context.Context, id string) ([]ChatTurn, error) {
	if err := s.ensureChatSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,session_id,created_at,question,ordinal,lanes_json FROM chat_turns WHERE session_id=? ORDER BY ordinal`, id)
	if err != nil {
		return nil, fmt.Errorf("load chat turns: %w", err)
	}
	defer rows.Close()
	var turns []ChatTurn
	for rows.Next() {
		var turn ChatTurn
		var lanes string
		if err := rows.Scan(&turn.ID, &turn.SessionID, &turn.CreatedAt, &turn.Question, &turn.Ordinal, &lanes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(lanes), &turn.Lanes); err != nil {
			return nil, err
		}
		turns = append(turns, turn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return turns, nil
}

func (s *Store) commitTurn(ctx context.Context, prep TurnPreparation, results [2]LaneResult) (ChatTurn, error) {
	if strings.TrimSpace(prep.Question) == "" || prep.Ordinal <= 0 || prep.Session.ID == "" {
		return ChatTurn{}, errors.New("invalid turn preparation")
	}
	if err := s.ensureChatSchema(ctx); err != nil {
		return ChatTurn{}, err
	}
	turn := ChatTurn{SessionID: prep.Session.ID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Question: prep.Question, Ordinal: prep.Ordinal, Lanes: results}
	id, err := newBuildID()
	if err != nil {
		return ChatTurn{}, err
	}
	turn.ID = id
	for lane, result := range results {
		if result.Settings.Mode == "no-rag" && result.Settings.TaskMemory {
			return ChatTurn{}, fmt.Errorf("lane %d no-rag baseline cannot use task memory", lane+1)
		}
		if result.Settings.Mode != "no-rag" && result.Settings.Mode != "grounded" {
			return ChatTurn{}, fmt.Errorf("lane %d persistent chat mode must be no-rag or grounded", lane+1)
		}
		if result.Settings.Mode == "no-rag" && (result.State.Goal != "" || result.State.GoalQuote != "" || len(result.State.Clarifications) > 0 || len(result.State.Constraints) > 0 || len(result.State.Terms) > 0) {
			return ChatTurn{}, fmt.Errorf("lane %d no-rag baseline returned task memory", lane+1)
		}
		if result.OriginalQuery != prep.Question || result.Settings != prep.Session.Lanes[lane] {
			return ChatTurn{}, fmt.Errorf("lane %d result does not match prepared question/settings", lane+1)
		}
		if result.Generation != prep.Session.Generation || result.Retrieval != prep.Session.Retrieval {
			return ChatTurn{}, fmt.Errorf("lane %d result settings do not match session", lane+1)
		}
		if err := ValidateAnswer(result.Answer, result.Settings.Mode, result.Context); err != nil {
			return ChatTurn{}, fmt.Errorf("validate lane %d answer: %w", lane+1, err)
		}
		if err := validateTaskState(result.State); err != nil {
			return ChatTurn{}, fmt.Errorf("validate lane %d state: %w", lane+1, err)
		}
		if err := validateStateQuotes(result.State, prep.States[lane], prep.Question, prep.Histories[lane]); err != nil {
			return ChatTurn{}, fmt.Errorf("validate lane %d state provenance: %w", lane+1, err)
		}
	}
	lanesJSON, err := json.Marshal(results)
	if err != nil {
		return ChatTurn{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ChatTurn{}, err
	}
	defer tx.Rollback()
	var storedID, storedLanes, storedGeneration, storedRetrieval string
	var storedRawTurns int
	if err = tx.QueryRowContext(ctx, `SELECT id,lanes_json,generation_json,retrieval_json,raw_turns FROM chat_sessions WHERE id=?`, prep.Session.ID).Scan(&storedID, &storedLanes, &storedGeneration, &storedRetrieval, &storedRawTurns); err != nil {
		return ChatTurn{}, fmt.Errorf("load chat session for commit: %w", err)
	}
	if storedRawTurns <= 0 {
		return ChatTurn{}, errors.New("chat session raw turn limit is unknown; session migration is required")
	}
	preparedLanes, _ := json.Marshal(prep.Session.Lanes)
	preparedGeneration, _ := json.Marshal(prep.Session.Generation)
	preparedRetrieval, _ := json.Marshal(prep.Session.Retrieval)
	if storedID != prep.Session.ID || storedLanes != string(preparedLanes) || storedGeneration != string(preparedGeneration) || storedRetrieval != string(preparedRetrieval) || storedRawTurns != prep.Session.RawTurns {
		return ChatTurn{}, errors.New("prepared session settings do not match persisted session")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_turns WHERE session_id=?`, prep.Session.ID).Scan(&count); err != nil {
		return ChatTurn{}, err
	}
	if count+1 != prep.Ordinal {
		return ChatTurn{}, fmt.Errorf("stale turn ordinal %d; next is %d", prep.Ordinal, count+1)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO chat_turns(id,session_id,ordinal,created_at,question,lanes_json) VALUES(?,?,?,?,?,?)`, turn.ID, turn.SessionID, turn.Ordinal, turn.CreatedAt, turn.Question, lanesJSON); err != nil {
		return ChatTurn{}, fmt.Errorf("insert paired chat turn: %w", err)
	}
	for lane, result := range results {
		var last int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0) FROM chat_messages WHERE session_id=? AND lane=?`, turn.SessionID, lane).Scan(&last); err != nil {
			return ChatTurn{}, err
		}
		for i, message := range []LLMMessage{{Role: "user", Content: prep.Question}, {Role: "assistant", Content: result.Answer.Answer}} {
			if _, err = tx.ExecContext(ctx, `INSERT INTO chat_messages(session_id,lane,ordinal,role,content) VALUES(?,?,?,?,?)`, turn.SessionID, lane, last+i+1, message.Role, message.Content); err != nil {
				return ChatTurn{}, fmt.Errorf("insert lane %d chat message: %w", lane+1, err)
			}
		}
		stateJSON, marshalErr := json.Marshal(result.State)
		if marshalErr != nil {
			return ChatTurn{}, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE chat_states SET state_json=? WHERE session_id=? AND lane=?`, stateJSON, turn.SessionID, lane); err != nil {
			return ChatTurn{}, fmt.Errorf("update lane %d chat state: %w", lane+1, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return ChatTurn{}, fmt.Errorf("commit paired chat turn: %w", err)
	}
	return turn, nil
}
