package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func chatTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func chatTestSession(t *testing.T, store *Store, memory bool) ChatSession {
	t.Helper()

	lanes := [2]LaneSettings{{Mode: "grounded", Strategy: strategyFixed, TaskMemory: memory}, {Mode: "grounded", Strategy: strategyStructural, TaskMemory: memory}}
	session, err := store.NewSession(context.Background(), lanes, GenerationSettings{MainModel: "main", AuxModel: "aux", Temperature: 0, MaxTokens: 64}, RetrievalSettings{CandidateK: 10, ContextK: 3, Threshold: 0.2}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func chatTestResults(prep TurnPreparation, states [2]TaskState) [2]LaneResult {
	var results [2]LaneResult
	for lane := range results {
		contextChunk := Candidate{Chunk: Chunk{ID: "chunk", Text: "Synthetic source quotation."}}
		results[lane] = LaneResult{
			OriginalQuery: prep.Question,
			SearchQuery:   prep.Question,
			Settings:      prep.Session.Lanes[lane],
			Generation:    prep.Session.Generation,
			Retrieval:     prep.Session.Retrieval,
			Context:       []Candidate{contextChunk},
			Answer:        Answer{Status: "answer", Answer: "Stored reply.", Citations: []Citation{{ChunkID: "chunk", Quote: "Synthetic source quotation."}}},
			State:         states[lane],
		}
	}
	return results
}

func TestChatHistoryIsLaneIsolatedBoundedAndRestored(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := chatTestSession(t, store, true)
	prep, err := (&ChatService{Engine: &Engine{Store: store}}).PrepareTurn(ctx, session.ID, "first question")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prep.States, [2]TaskState{}) || len(prep.Histories[0]) != 0 || len(prep.Histories[1]) != 0 {
		t.Fatalf("new session is not empty: %#v", prep)
	}
	for i := range 8 {
		prep, err = (&ChatService{Engine: &Engine{Store: store}}).PrepareTurn(ctx, session.ID, "question")
		results := chatTestResults(prep, [2]TaskState{{Goal: "objective", GoalQuote: "question"}, {Goal: "objective", GoalQuote: "question"}})
		results[0].Answer.Answer = "lane zero"
		results[1].Answer.Answer = "lane one"
		if _, err = store.commitTurn(ctx, prep, results); err != nil {
			t.Fatalf("commit turn %d: %v", i+1, err)
		}
	}
	service := &ChatService{Engine: &Engine{Store: store}}
	prep, err = service.PrepareTurn(ctx, session.ID, "ninth")
	if err != nil {
		t.Fatal(err)
	}
	if prep.Ordinal != 9 || len(prep.Histories[0]) != 12 || len(prep.Histories[1]) != 12 {
		t.Fatalf("bounded inputs ordinal=%d histories=%d,%d", prep.Ordinal, len(prep.Histories[0]), len(prep.Histories[1]))
	}
	if prep.Histories[0][1].Content != "lane zero" || prep.Histories[1][1].Content != "lane one" {
		t.Fatalf("lane history leaked: %#v", prep.Histories)
	}
	full, err := store.LoadHistory(ctx, session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 16 {
		t.Fatalf("stored full history has %d messages, want 16", len(full))
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.LoadSession(ctx, session.ID)
	if err != nil || reloaded.ID != session.ID || reloaded.RawTurns != session.RawTurns {
		t.Fatalf("reopen session=%#v err=%v", reloaded, err)
	}
	reopenedHistory, err := reopened.LoadHistory(ctx, session.ID, 0)
	if err != nil || len(reopenedHistory) != 16 {
		t.Fatalf("reopened full history count=%d err=%v", len(reopenedHistory), err)
	}
	reopenedTurns, err := reopened.LoadTurns(ctx, session.ID)
	reopenedState, err := reopened.LoadState(ctx, session.ID, 0)
	if err != nil || reopenedState.Goal != "objective" {
		t.Fatalf("reopened task state=%#v err=%v", reopenedState, err)
	}
	if err != nil || len(reopenedTurns) != 8 {
		t.Fatalf("reopened turns count=%d err=%v", len(reopenedTurns), err)
	}
}

func TestChatTurnAtomicValidationRefusalAndStaleOrdinal(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	session := chatTestSession(t, store, true)
	prep := TurnPreparation{Session: session, Question: "I am at LV", Ordinal: 1}
	states := [2]TaskState{{Goal: "Reach Moon", GoalQuote: "I am at LV"}, {Goal: "Reach Moon", GoalQuote: "I am at LV"}}
	results := chatTestResults(prep, states)
	results[1].Answer = Answer{Status: "insufficient_context", Answer: insufficientAnswer, Citations: []Citation{}}
	if _, err := store.commitTurn(ctx, prep, results); err != nil {
		t.Fatalf("valid refusal should commit: %v", err)
	}
	before, err := store.LoadTurns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("turn count=%d", len(before))
	}
	if before[0].Lanes[1].Answer.Status != "insufficient_context" {
		t.Fatal("successful refusal result was not persisted")
	}
	prep2 := TurnPreparation{Session: session, Question: "Correction: now at MV", Ordinal: 2, States: states}
	invalid := chatTestResults(prep2, states)
	invalid[0].State = TaskState{Goal: "invented", GoalQuote: "assistant said this"}
	if _, err := store.commitTurn(ctx, prep2, invalid); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("invalid state error=%v", err)
	}
	after, err := store.LoadTurns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatal("invalid next state changed prior successful turn")
	}
	latest := [2]TaskState{{Goal: "Reach Moon from MV", GoalQuote: "Correction: now at MV"}, {Goal: "Reach Moon from MV", GoalQuote: "Correction: now at MV"}}
	valid := chatTestResults(prep2, latest)
	if _, err := store.commitTurn(ctx, prep2, valid); err != nil {
		t.Fatalf("latest user correction should be accepted: %v", err)
	}
	state, err := store.LoadState(ctx, session.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.GoalQuote != "Correction: now at MV" {
		t.Fatalf("stored outdated goal quote: %#v", state)
	}
	if _, err := store.commitTurn(ctx, prep2, valid); err == nil || !strings.Contains(err.Error(), "stale turn ordinal") {
		t.Fatalf("stale ordinal error=%v", err)
	}
}

func TestChatTurnSQLFailureRollsBackPairMessagesAndState(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	session := chatTestSession(t, store, true)
	prep := TurnPreparation{Session: session, Question: "My goal is Moon", Ordinal: 1}
	state := TaskState{Goal: "Moon", GoalQuote: "My goal is Moon"}
	results := chatTestResults(prep, [2]TaskState{state, state})
	if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_second_state BEFORE UPDATE ON chat_states WHEN OLD.lane=1 BEGIN SELECT RAISE(ABORT,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.commitTurn(ctx, prep, results); err == nil {
		t.Fatal("expected injected transaction failure")
	}
	turns, err := store.LoadTurns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	histories := [2][]LLMMessage{}
	states := [2]TaskState{}
	for lane := range histories {
		histories[lane], err = store.LoadHistory(ctx, session.ID, lane)
		if err != nil {
			t.Fatal(err)
		}
		states[lane], err = store.LoadState(ctx, session.ID, lane)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(turns) != 0 || len(histories[0]) != 0 || len(histories[1]) != 0 || !reflect.DeepEqual(states, [2]TaskState{}) {
		t.Fatalf("partial transaction persisted: turns=%d histories=%d,%d states=%#v", len(turns), len(histories[0]), len(histories[1]), states)
	}
}

type chatStoreSnapshot struct {
	turns     []ChatTurn
	histories [2][]LLMMessage
	states    [2]TaskState
}

func loadChatStoreSnapshot(t *testing.T, store *Store, sessionID string) chatStoreSnapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := chatStoreSnapshot{}
	var err error
	snapshot.turns, err = store.LoadTurns(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	for lane := range snapshot.histories {
		snapshot.histories[lane], err = store.LoadHistory(ctx, sessionID, lane)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.states[lane], err = store.LoadState(ctx, sessionID, lane)
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func prepareChatCommitRegression(t *testing.T) (string, *Store, ChatSession, TurnPreparation, [2]TaskState) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "chat.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := chatTestSession(t, store, true)
	service := &ChatService{Engine: &Engine{Store: store}}
	prior, err := service.PrepareTurn(ctx, session.ID, "First committed question")
	if err != nil {
		t.Fatal(err)
	}
	priorStates := [2]TaskState{
		{Goal: "Keep the prior lane zero state", GoalQuote: prior.Question},
		{Goal: "Keep the prior lane one state", GoalQuote: prior.Question},
	}
	if _, err := service.CommitTurn(ctx, prior, chatTestResults(prior, priorStates)); err != nil {
		t.Fatalf("commit prior paired turn: %v", err)
	}
	prep, err := service.PrepareTurn(ctx, session.ID, "Second prepared question")
	if err != nil {
		t.Fatal(err)
	}
	return path, store, session, prep, priorStates
}

func TestChatCommitRejectsResultSettingsMismatchWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*LaneResult)
	}{
		{
			name: "lane settings",
			change: func(result *LaneResult) {
				result.Settings.Strategy = strategyStructural
			},
		},
		{
			name: "generation settings",
			change: func(result *LaneResult) {
				result.Generation.MainModel = "different-main"
			},
		},
		{
			name: "retrieval settings",
			change: func(result *LaneResult) {
				result.Retrieval.CandidateK++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, store, session, prep, priorStates := prepareChatCommitRegression(t)
			before := loadChatStoreSnapshot(t, store, session.ID)
			results := chatTestResults(prep, priorStates)
			test.change(&results[0])
			if _, err := store.commitTurn(context.Background(), prep, results); err == nil {
				t.Fatal("commit accepted lane result settings that differ from the prepared session")
			}
			assertChatSnapshotUnchanged(t, store, session.ID, before)
			assertChatSnapshotUnchangedAfterReopen(t, store, path, session.ID, before)
		})
	}
}

func TestChatCommitRejectsPersistedSessionMismatchWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Store, ChatSession)
	}{
		{
			name: "lane settings",
			change: func(t *testing.T, store *Store, session ChatSession) {
				lanes := session.Lanes
				lanes[0].Strategy = strategyStructural
				updateChatSessionJSON(t, store, session.ID, "lanes_json", lanes)
			},
		},
		{
			name: "generation settings",
			change: func(t *testing.T, store *Store, session ChatSession) {
				generation := session.Generation
				generation.MainModel = "different-main"
				updateChatSessionJSON(t, store, session.ID, "generation_json", generation)
			},
		},
		{
			name: "retrieval settings",
			change: func(t *testing.T, store *Store, session ChatSession) {
				retrieval := session.Retrieval
				retrieval.CandidateK++
				updateChatSessionJSON(t, store, session.ID, "retrieval_json", retrieval)
			},
		},
		{
			name: "raw turn window",
			change: func(t *testing.T, store *Store, session ChatSession) {
				if _, err := store.DB.ExecContext(context.Background(), `UPDATE chat_sessions SET raw_turns=? WHERE id=?`, session.RawTurns+1, session.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, store, session, prep, priorStates := prepareChatCommitRegression(t)
			before := loadChatStoreSnapshot(t, store, session.ID)
			results := chatTestResults(prep, priorStates)
			test.change(t, store, session)
			if _, err := store.commitTurn(context.Background(), prep, results); err == nil {
				t.Fatal("commit accepted persisted session settings that differ from its preparation")
			}
			assertChatSnapshotUnchanged(t, store, session.ID, before)
			assertChatSnapshotUnchangedAfterReopen(t, store, path, session.ID, before)
		})
	}
}

func updateChatSessionJSON(t *testing.T, store *Store, sessionID, column string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	query := "UPDATE chat_sessions SET " + column + "=? WHERE id=?"
	if _, err := store.DB.ExecContext(context.Background(), query, encoded, sessionID); err != nil {
		t.Fatal(err)
	}
}

func assertChatSnapshotUnchanged(t *testing.T, store *Store, sessionID string, before chatStoreSnapshot) {
	t.Helper()
	after := loadChatStoreSnapshot(t, store, sessionID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected commit changed persisted turns, histories, or states:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func assertChatSnapshotUnchangedAfterReopen(t *testing.T, store *Store, path, sessionID string, before chatStoreSnapshot) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertChatSnapshotUnchanged(t, reopened, sessionID, before)
}

func TestNewChatSessionStartsWithoutPriorTaskMemory(t *testing.T) {
	store := chatTestStore(t)
	session := chatTestSession(t, store, true)
	for lane := 0; lane < 2; lane++ {
		state, err := store.LoadState(context.Background(), session.ID, lane)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(state, TaskState{}) {
			t.Fatalf("lane %d inherited task state: %#v", lane, state)
		}
	}
}

func TestChatSessionRawTurnsPersistAndLegacyUnknownIsRejected(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	if _, err := store.DB.ExecContext(ctx, `CREATE TABLE chat_sessions (id TEXT PRIMARY KEY, created_at TEXT NOT NULL, lanes_json TEXT NOT NULL, generation_json TEXT NOT NULL, retrieval_json TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO chat_sessions VALUES('legacy','old-time','[{},{}]','{}','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSession(ctx, "legacy"); err == nil {
		t.Fatal("legacy session with unknown raw-turn window loaded successfully")
	}
	listed, err := store.ListSessions(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != "legacy" || listed[0].RawTurns != 0 {
		t.Fatalf("legacy session listing=%+v err=%v, want visible unknown window", listed, err)
	}
	var retained int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_sessions WHERE id='legacy'`).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("legacy row retained=%d err=%v", retained, err)
	}
	session := chatTestSession(t, store, false)
	if session.RawTurns != 6 {
		t.Fatalf("zero raw-turn setting normalized to %d, want 6", session.RawTurns)
	}
	loaded, err := store.LoadSession(ctx, session.ID)
	if err != nil || loaded.RawTurns != 6 {
		t.Fatalf("new session raw turns=%d err=%v", loaded.RawTurns, err)
	}
	listed, err = store.ListSessions(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listing legacy and new sessions=%+v err=%v", listed, err)
	}
	var foundLegacy, foundNew bool
	for _, item := range listed {
		switch item.ID {
		case "legacy":
			foundLegacy = item.RawTurns == 0
		case session.ID:
			foundNew = item.RawTurns == 6
		}
	}
	if !foundLegacy || !foundNew {
		t.Fatalf("listing lost legacy or usable session: %+v", listed)
	}
	if _, err := store.NewSession(ctx, session.Lanes, session.Generation, session.Retrieval, -1); err == nil {
		t.Fatal("negative raw-turn limit was accepted")
	}
}
