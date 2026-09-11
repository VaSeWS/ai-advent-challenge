package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

// Store owns the durable chat graph and all accounting recorded for it.
// A Store is safe for sequential TUI use; each mutating operation is committed
// before it returns.
type Store struct {
	db *sql.DB
}

// SummaryUpdate replaces a branch summary. ThroughMessageID must be a message
// on that branch's current lineage.
type SummaryUpdate struct {
	ThroughMessageID int64
	Content          string
}

// Fact is one durable fact entry. Facts are returned sorted by Key.
type Fact struct {
	Key   string
	Value string
}

// Checkpoint captures a branch head and its branch-local derived state without
// copying the immutable message lineage.
type Checkpoint struct {
	ID                      int64
	ChatID                  int64
	SourceBranchID          int64
	Name                    string
	HeadMessageID           *int64
	SummaryThroughMessageID *int64
	Summary                 string
	Facts                   map[string]string
	CreatedAt               time.Time
}

// SaveTurnInput is the complete result of a successful user turn. Facts is nil
// when facts must remain unchanged; a non-nil empty map deliberately clears it.
// Summary is nil when the branch summary must remain unchanged.
type SaveTurnInput struct {
	ChatID           int64
	BranchID         int64
	UserContent      string
	AssistantContent string
	Summary          *SummaryUpdate
	Facts            *map[string]string
	APICalls         []APICall
}

// OpenStore opens path, enables SQLite foreign keys and WAL, migrates schema
// version 1, and creates the initial Chat 1/main branch when the database is new.
func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is empty")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// PRAGMAs are connection-local. One connection keeps foreign-key enforcement
	// invariant for all Store operations while WAL still permits external readers.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.ensureInitialChat(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS chats (
			id INTEGER PRIMARY KEY,
			title TEXT NOT NULL,
			active_branch_id INTEGER REFERENCES branches(id),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS branches (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			head_message_id INTEGER REFERENCES messages(id),
			strategy TEXT NOT NULL CHECK(strategy IN ('full', 'summary', 'sliding', 'facts', 'branching')),
			window_size INTEGER NOT NULL CHECK(window_size > 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(chat_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			previous_message_id INTEGER REFERENCES messages(id),
			role TEXT NOT NULL CHECK(role IN ('user', 'assistant')),
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS summaries (
			branch_id INTEGER PRIMARY KEY REFERENCES branches(id) ON DELETE CASCADE,
			through_message_id INTEGER NOT NULL REFERENCES messages(id),
			content TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS facts (
			branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(branch_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			source_branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			head_message_id INTEGER REFERENCES messages(id),
			summary_through_message_id INTEGER REFERENCES messages(id),
			summary TEXT NOT NULL,
			facts_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			UNIQUE(source_branch_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS api_calls (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('main', 'summary', 'facts')),
			strategy TEXT NOT NULL,
			counter_label TEXT NOT NULL,
			current_tokens INTEGER,
			full_history_tokens INTEGER,
			sent_tokens_local INTEGER NOT NULL,
			response_tokens_local INTEGER NOT NULL,
			prompt_tokens_api INTEGER NOT NULL,
			cached_prompt_tokens_api INTEGER NOT NULL,
			uncached_prompt_tokens_api INTEGER NOT NULL,
			completion_tokens_api INTEGER NOT NULL,
			total_tokens_api INTEGER NOT NULL,
			pricing_tier TEXT NOT NULL,
			input_cost_usd REAL NOT NULL,
			output_cost_usd REAL NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS branches_chat_id_idx ON branches(chat_id)`,
		`CREATE INDEX IF NOT EXISTS messages_chat_id_idx ON messages(chat_id)`,
		`CREATE INDEX IF NOT EXISTS messages_previous_message_id_idx ON messages(previous_message_id)`,
		`CREATE INDEX IF NOT EXISTS checkpoints_source_branch_id_idx ON checkpoints(source_branch_id)`,
		`CREATE INDEX IF NOT EXISTS api_calls_grouping_idx ON api_calls(branch_id, provider, model, strategy, kind)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("apply schema version %d: %w", schemaVersion, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

func (s *Store) ensureInitialChat() error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&count); err != nil {
		return fmt.Errorf("count chats: %w", err)
	}
	if count != 0 {
		return nil
	}
	_, _, err := s.CreateChat("Chat 1")
	return err
}

// ActiveChat returns the most recently selected chat and its active branch.
func (s *Store) ActiveChat() (Chat, Branch, error) {
	row := s.db.QueryRow(`SELECT c.id, c.title, c.active_branch_id, c.created_at, c.updated_at,
		b.id, b.chat_id, b.name, b.head_message_id, b.strategy, b.window_size, b.created_at, b.updated_at
		FROM chats c JOIN branches b ON b.id = c.active_branch_id
		ORDER BY c.updated_at DESC, c.id DESC LIMIT 1`)
	chat, branch, err := scanChatBranch(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Chat{}, Branch{}, errors.New("no active chat")
		}
		return Chat{}, Branch{}, fmt.Errorf("load active chat: %w", err)
	}
	return chat, branch, nil
}

// Chat returns one chat, including its selected branch ID.
func (s *Store) Chat(chatID int64) (Chat, error) {
	chat, err := scanChat(s.db.QueryRow(`SELECT id, title, active_branch_id, created_at, updated_at FROM chats WHERE id = ?`, chatID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Chat{}, fmt.Errorf("chat %d not found", chatID)
		}
		return Chat{}, fmt.Errorf("load chat: %w", err)
	}
	return chat, nil
}

// Branch returns one branch, rejecting a branch from another chat.
func (s *Store) Branch(chatID, branchID int64) (Branch, error) {
	branch, err := scanBranch(s.db.QueryRow(`SELECT id, chat_id, name, head_message_id, strategy, window_size, created_at, updated_at FROM branches WHERE id = ? AND chat_id = ?`, branchID, chatID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Branch{}, fmt.Errorf("branch %d not found in chat %d", branchID, chatID)
		}
		return Branch{}, fmt.Errorf("load branch: %w", err)
	}
	return branch, nil
}

// Chats lists chats newest selected first.
func (s *Store) Chats() ([]Chat, error) {
	rows, err := s.db.Query(`SELECT id, title, active_branch_id, created_at, updated_at FROM chats ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()
	chats := make([]Chat, 0)
	for rows.Next() {
		chat, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		chats = append(chats, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	return chats, nil
}

// Branches lists all branches in a chat by creation order.
func (s *Store) Branches(chatID int64) ([]Branch, error) {
	rows, err := s.db.Query(`SELECT id, chat_id, name, head_message_id, strategy, window_size, created_at, updated_at FROM branches WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	defer rows.Close()
	branches := make([]Branch, 0)
	for rows.Next() {
		branch, err := scanBranch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan branch: %w", err)
		}
		branches = append(branches, branch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	return branches, nil
}

// Lineage loads a branch's immutable linked history in chronological order with
// one recursive query; forked branches therefore reuse their shared prefix.
func (s *Store) Lineage(branchID int64) ([]Message, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`WITH RECURSIVE lineage(id, chat_id, previous_message_id, role, content, created_at, depth) AS (
		SELECT id, chat_id, previous_message_id, role, content, created_at, 0
		FROM messages WHERE id = (SELECT head_message_id FROM branches WHERE id = ?)
		UNION ALL
		SELECT m.id, m.chat_id, m.previous_message_id, m.role, m.content, m.created_at, lineage.depth + 1
		FROM messages m JOIN lineage ON m.id = lineage.previous_message_id
	)
	SELECT id, chat_id, previous_message_id, role, content, created_at FROM lineage ORDER BY depth DESC`, branchID)
	if err != nil {
		return nil, fmt.Errorf("load lineage: %w", err)
	}
	defer rows.Close()
	messages := make([]Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan lineage message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load lineage: %w", err)
	}
	return messages, nil
}

// CreateChat creates an independent chat with its own full/main branch and
// selects it immediately.
func (s *Store) CreateChat(title string) (Chat, Branch, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Chat{}, Branch{}, errors.New("chat title is empty")
	}
	now := databaseTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("begin create chat: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO chats(title, active_branch_id, created_at, updated_at) VALUES (?, NULL, ?, ?)`, title, now, now)
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("create chat: %w", err)
	}
	chatID, err := result.LastInsertId()
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("read chat id: %w", err)
	}
	result, err = tx.Exec(`INSERT INTO branches(chat_id, name, head_message_id, strategy, window_size, created_at, updated_at) VALUES (?, 'main', NULL, ?, 10, ?, ?)`, chatID, strategyFull, now, now)
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("create main branch: %w", err)
	}
	branchID, err := result.LastInsertId()
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("read branch id: %w", err)
	}
	if _, err := tx.Exec(`UPDATE chats SET active_branch_id = ? WHERE id = ?`, branchID, chatID); err != nil {
		return Chat{}, Branch{}, fmt.Errorf("select main branch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Chat{}, Branch{}, fmt.Errorf("commit create chat: %w", err)
	}
	chat, branch, err := s.ActiveChat()
	if err != nil {
		return Chat{}, Branch{}, err
	}
	return chat, branch, nil
}

// UseChat selects an existing chat and returns its persisted active branch.
func (s *Store) UseChat(chatID int64) (Chat, Branch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Chat{}, Branch{}, fmt.Errorf("begin select chat: %w", err)
	}
	defer tx.Rollback()
	var active sql.NullInt64
	if err := tx.QueryRow(`SELECT active_branch_id FROM chats WHERE id = ?`, chatID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Chat{}, Branch{}, fmt.Errorf("chat %d not found", chatID)
		}
		return Chat{}, Branch{}, fmt.Errorf("load chat %d: %w", chatID, err)
	}
	if !active.Valid {
		return Chat{}, Branch{}, fmt.Errorf("chat %d has no active branch", chatID)
	}
	if _, err := tx.Exec(`UPDATE chats SET updated_at = ? WHERE id = ?`, databaseTime(time.Now()), chatID); err != nil {
		return Chat{}, Branch{}, fmt.Errorf("select chat: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Chat{}, Branch{}, fmt.Errorf("commit select chat: %w", err)
	}
	chat, err := s.Chat(chatID)
	if err != nil {
		return Chat{}, Branch{}, err
	}
	branch, err := s.Branch(chatID, active.Int64)
	if err != nil {
		return Chat{}, Branch{}, err
	}
	return chat, branch, nil
}

// SwitchBranch selects name as the active branch for chatID.
func (s *Store) SwitchBranch(chatID int64, name string) (Branch, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Branch{}, errors.New("branch name is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Branch{}, fmt.Errorf("begin switch branch: %w", err)
	}
	defer tx.Rollback()
	var branchID int64
	if err := tx.QueryRow(`SELECT id FROM branches WHERE chat_id = ? AND name = ?`, chatID, name).Scan(&branchID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Branch{}, fmt.Errorf("branch %q not found in chat %d", name, chatID)
		}
		return Branch{}, fmt.Errorf("load branch: %w", err)
	}
	now := databaseTime(time.Now())
	if _, err := tx.Exec(`UPDATE chats SET active_branch_id = ?, updated_at = ? WHERE id = ?`, branchID, now, chatID); err != nil {
		return Branch{}, fmt.Errorf("select branch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Branch{}, fmt.Errorf("commit switch branch: %w", err)
	}
	return s.Branch(chatID, branchID)
}

// SetBranchMode persists a valid context strategy for a branch.
func (s *Store) SetBranchMode(branchID int64, strategy string) (Branch, error) {
	if !validStrategy(strategy) {
		return Branch{}, fmt.Errorf("unknown context mode %q", strategy)
	}
	branch, err := s.branchByID(branchID)
	if err != nil {
		return Branch{}, err
	}
	if _, err := s.db.Exec(`UPDATE branches SET strategy = ?, updated_at = ? WHERE id = ?`, strategy, databaseTime(time.Now()), branchID); err != nil {
		return Branch{}, fmt.Errorf("set branch mode: %w", err)
	}
	return s.Branch(branch.ChatID, branchID)
}

// SetWindow persists a positive raw-message window for a branch.
func (s *Store) SetWindow(branchID int64, windowSize int) (Branch, error) {
	if windowSize <= 0 {
		return Branch{}, fmt.Errorf("window size must be positive (got %d)", windowSize)
	}
	branch, err := s.branchByID(branchID)
	if err != nil {
		return Branch{}, err
	}
	if _, err := s.db.Exec(`UPDATE branches SET window_size = ?, updated_at = ? WHERE id = ?`, windowSize, databaseTime(time.Now()), branchID); err != nil {
		return Branch{}, fmt.Errorf("set window size: %w", err)
	}
	return s.Branch(branch.ChatID, branchID)
}

// Summary returns the branch's optional durable summary.
func (s *Store) Summary(branchID int64) (*Summary, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	var summary Summary
	var updated string
	err := s.db.QueryRow(`SELECT branch_id, through_message_id, content, updated_at FROM summaries WHERE branch_id = ?`, branchID).Scan(&summary.BranchID, &summary.ThroughMessageID, &summary.Content, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load summary: %w", err)
	}
	summary.UpdatedAt, err = parseDatabaseTime(updated)
	if err != nil {
		return nil, fmt.Errorf("load summary: %w", err)
	}
	return &summary, nil
}

// SaveSummary replaces a summary after verifying its watermark is on the
// branch lineage, so a summary cannot accidentally describe another branch.
func (s *Store) SaveSummary(branchID int64, update SummaryUpdate) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin save summary: %w", err)
	}
	defer tx.Rollback()
	if err := saveSummaryTx(tx, branchID, update, databaseTime(time.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save summary: %w", err)
	}
	return nil
}

// Facts returns a branch's durable facts sorted lexicographically by key.
func (s *Store) Facts(branchID int64) ([]Fact, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT key, value FROM facts WHERE branch_id = ? ORDER BY key`, branchID)
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer rows.Close()
	facts := make([]Fact, 0)
	for rows.Next() {
		var fact Fact
		if err := rows.Scan(&fact.Key, &fact.Value); err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	return facts, nil
}

// ReplaceFacts atomically replaces the complete fact map for one branch.
func (s *Store) ReplaceFacts(branchID int64, facts map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace facts: %w", err)
	}
	defer tx.Rollback()
	if err := replaceFactsTx(tx, branchID, facts, databaseTime(time.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace facts: %w", err)
	}
	return nil
}

// SaveFactsRebuild atomically replaces facts and records all successful facts
// completions from one rebuild. It is used only after every rebuild batch has
// completed, so a failed rebuild cannot leave a partial fact map or its costs.
func (s *Store) SaveFactsRebuild(chatID, branchID int64, facts map[string]string, calls []APICall) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin save facts rebuild: %w", err)
	}
	defer tx.Rollback()
	branch, err := branchByIDTx(tx, branchID)
	if err != nil {
		return err
	}
	if branch.ChatID != chatID {
		return fmt.Errorf("branch %d does not belong to chat %d", branchID, chatID)
	}
	now := databaseTime(time.Now())
	if err := replaceFactsTx(tx, branchID, facts, now); err != nil {
		return err
	}
	for _, call := range calls {
		if call.Kind != "facts" {
			return fmt.Errorf("facts rebuild cannot record %q API call", call.Kind)
		}
		if err := insertAPICallTx(tx, chatID, branchID, call, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE branches SET updated_at = ? WHERE id = ?`, now, branchID); err != nil {
		return fmt.Errorf("update facts branch: %w", err)
	}
	if _, err := tx.Exec(`UPDATE chats SET updated_at = ? WHERE id = ?`, now, chatID); err != nil {
		return fmt.Errorf("update facts chat: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save facts rebuild: %w", err)
	}
	return nil
}

// CreateCheckpoint snapshots a branching-mode branch at its current head.
func (s *Store) CreateCheckpoint(branchID int64, name string) (Checkpoint, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Checkpoint{}, errors.New("checkpoint name is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Checkpoint{}, fmt.Errorf("begin create checkpoint: %w", err)
	}
	defer tx.Rollback()
	branch, err := branchByIDTx(tx, branchID)
	if err != nil {
		return Checkpoint{}, err
	}
	if branch.Strategy != strategyBranching {
		return Checkpoint{}, errors.New("checkpoints require branching mode")
	}
	summaryThrough, summary, err := summarySnapshotTx(tx, branchID)
	if err != nil {
		return Checkpoint{}, err
	}
	facts, err := factsMapTx(tx, branchID)
	if err != nil {
		return Checkpoint{}, err
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("encode checkpoint facts: %w", err)
	}
	now := databaseTime(time.Now())
	if _, err := tx.Exec(`INSERT INTO checkpoints(chat_id, source_branch_id, name, head_message_id, summary_through_message_id, summary, facts_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, branch.ChatID, branchID, name, nullableInt64(branch.HeadMessageID), nullableInt64(summaryThrough), summary, string(factsJSON), now); err != nil {
		return Checkpoint{}, fmt.Errorf("create checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, fmt.Errorf("commit create checkpoint: %w", err)
	}
	return s.Checkpoint(branchID, name)
}

// Checkpoint returns a named checkpoint and decodes its immutable facts snapshot.
func (s *Store) Checkpoint(branchID int64, name string) (Checkpoint, error) {
	checkpoint, err := scanCheckpoint(s.db.QueryRow(`SELECT id, chat_id, source_branch_id, name, head_message_id, summary_through_message_id, summary, facts_json, created_at FROM checkpoints WHERE source_branch_id = ? AND name = ?`, branchID, name))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Checkpoint{}, fmt.Errorf("checkpoint %q not found in branch %d", name, branchID)
		}
		return Checkpoint{}, fmt.Errorf("load checkpoint: %w", err)
	}
	return checkpoint, nil
}

// Fork creates and selects a new branching-mode branch at a checkpoint head.
// It restores checkpoint-derived state while reusing all prefix messages.
func (s *Store) Fork(sourceBranchID int64, checkpointName, newBranchName string) (Branch, error) {
	checkpointName, newBranchName = strings.TrimSpace(checkpointName), strings.TrimSpace(newBranchName)
	if checkpointName == "" {
		return Branch{}, errors.New("checkpoint name is empty")
	}
	if newBranchName == "" {
		return Branch{}, errors.New("branch name is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Branch{}, fmt.Errorf("begin fork: %w", err)
	}
	defer tx.Rollback()
	source, err := branchByIDTx(tx, sourceBranchID)
	if err != nil {
		return Branch{}, err
	}
	if source.Strategy != strategyBranching {
		return Branch{}, errors.New("forks require branching mode")
	}
	checkpoint, err := scanCheckpoint(tx.QueryRow(`SELECT id, chat_id, source_branch_id, name, head_message_id, summary_through_message_id, summary, facts_json, created_at FROM checkpoints WHERE source_branch_id = ? AND name = ?`, sourceBranchID, checkpointName))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Branch{}, fmt.Errorf("checkpoint %q not found in branch %d", checkpointName, sourceBranchID)
		}
		return Branch{}, fmt.Errorf("load checkpoint: %w", err)
	}
	now := databaseTime(time.Now())
	result, err := tx.Exec(`INSERT INTO branches(chat_id, name, head_message_id, strategy, window_size, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, source.ChatID, newBranchName, nullableInt64(checkpoint.HeadMessageID), strategyBranching, source.WindowSize, now, now)
	if err != nil {
		return Branch{}, fmt.Errorf("create fork: %w", err)
	}
	branchID, err := result.LastInsertId()
	if err != nil {
		return Branch{}, fmt.Errorf("read fork id: %w", err)
	}
	if checkpoint.SummaryThroughMessageID != nil {
		if _, err := tx.Exec(`INSERT INTO summaries(branch_id, through_message_id, content, updated_at) VALUES (?, ?, ?, ?)`, branchID, *checkpoint.SummaryThroughMessageID, checkpoint.Summary, now); err != nil {
			return Branch{}, fmt.Errorf("restore checkpoint summary: %w", err)
		}
	}
	if err := replaceFactsTx(tx, branchID, checkpoint.Facts, now); err != nil {
		return Branch{}, err
	}
	if _, err := tx.Exec(`UPDATE chats SET active_branch_id = ?, updated_at = ? WHERE id = ?`, branchID, now, source.ChatID); err != nil {
		return Branch{}, fmt.Errorf("select fork: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Branch{}, fmt.Errorf("commit fork: %w", err)
	}
	return s.Branch(source.ChatID, branchID)
}

// SaveTurn atomically appends a user/assistant pair, advances the branch head,
// applies optional derived-state replacements, and records every API call. A
// failed write leaves no partial turn, derived state, or accounting rows.
func (s *Store) SaveTurn(input SaveTurnInput) (Message, error) {
	if input.ChatID <= 0 || input.BranchID <= 0 {
		return Message{}, errors.New("chat and branch IDs must be positive")
	}
	if strings.TrimSpace(input.UserContent) == "" {
		return Message{}, errors.New("user message is empty")
	}
	if strings.TrimSpace(input.AssistantContent) == "" {
		return Message{}, errors.New("assistant message is empty")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, fmt.Errorf("begin save turn: %w", err)
	}
	defer tx.Rollback()
	branch, err := branchByIDTx(tx, input.BranchID)
	if err != nil {
		return Message{}, err
	}
	if branch.ChatID != input.ChatID {
		return Message{}, fmt.Errorf("branch %d does not belong to chat %d", input.BranchID, input.ChatID)
	}
	nowTime := time.Now()
	now := databaseTime(nowTime)
	userID, err := insertMessageTx(tx, input.ChatID, branch.HeadMessageID, "user", input.UserContent, now)
	if err != nil {
		return Message{}, err
	}
	assistantID, err := insertMessageTx(tx, input.ChatID, &userID, "assistant", input.AssistantContent, now)
	if err != nil {
		return Message{}, err
	}
	if input.Summary != nil {
		if err := saveSummaryTx(tx, input.BranchID, *input.Summary, now); err != nil {
			return Message{}, err
		}
	}
	if input.Facts != nil {
		if err := replaceFactsTx(tx, input.BranchID, *input.Facts, now); err != nil {
			return Message{}, err
		}
	}
	for _, call := range input.APICalls {
		if err := insertAPICallTx(tx, input.ChatID, input.BranchID, call, now); err != nil {
			return Message{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE branches SET head_message_id = ?, updated_at = ? WHERE id = ?`, assistantID, now, input.BranchID); err != nil {
		return Message{}, fmt.Errorf("advance branch head: %w", err)
	}
	if _, err := tx.Exec(`UPDATE chats SET active_branch_id = ?, updated_at = ? WHERE id = ?`, input.BranchID, now, input.ChatID); err != nil {
		return Message{}, fmt.Errorf("update active chat: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit save turn: %w", err)
	}
	return Message{ID: assistantID, ChatID: input.ChatID, Previous: int64Ptr(userID), Role: "assistant", Content: input.AssistantContent, CreatedAt: nowTime.UTC()}, nil
}

// Stats returns grouped cost and API-token accounting for one branch.
func (s *Store) Stats(branchID int64) ([]StatsRow, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	return s.stats(`WHERE branch_id = ?`, branchID)
}

// AllStats returns grouped accounting across every chat and branch.
func (s *Store) AllStats() ([]StatsRow, error) { return s.stats("", nil) }

func (s *Store) stats(where string, argument any) ([]StatsRow, error) {
	query := `SELECT provider, model, strategy, kind, pricing_tier, COUNT(*), COALESCE(SUM(prompt_tokens_api), 0), COALESCE(SUM(completion_tokens_api), 0), COALESCE(SUM(input_cost_usd), 0), COALESCE(SUM(output_cost_usd), 0) FROM api_calls ` + where + ` GROUP BY provider, model, strategy, kind, pricing_tier ORDER BY provider, model, strategy, kind, pricing_tier`
	var rows *sql.Rows
	var err error
	if argument == nil {
		rows, err = s.db.Query(query)
	} else {
		rows, err = s.db.Query(query, argument)
	}
	if err != nil {
		return nil, fmt.Errorf("load stats: %w", err)
	}
	defer rows.Close()
	stats := make([]StatsRow, 0)
	for rows.Next() {
		var row StatsRow
		if err := rows.Scan(&row.Provider, &row.Model, &row.Strategy, &row.Kind, &row.PricingTier, &row.Calls, &row.PromptTokens, &row.CompletionTokens, &row.InputCostUSD, &row.OutputCostUSD); err != nil {
			return nil, fmt.Errorf("scan stats: %w", err)
		}
		stats = append(stats, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load stats: %w", err)
	}
	return stats, nil
}

func (s *Store) branchByID(branchID int64) (Branch, error) {
	branch, err := scanBranch(s.db.QueryRow(`SELECT id, chat_id, name, head_message_id, strategy, window_size, created_at, updated_at FROM branches WHERE id = ?`, branchID))
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, fmt.Errorf("branch %d not found", branchID)
	}
	if err != nil {
		return Branch{}, fmt.Errorf("load branch: %w", err)
	}
	return branch, nil
}

func branchByIDTx(tx *sql.Tx, branchID int64) (Branch, error) {
	branch, err := scanBranch(tx.QueryRow(`SELECT id, chat_id, name, head_message_id, strategy, window_size, created_at, updated_at FROM branches WHERE id = ?`, branchID))
	if errors.Is(err, sql.ErrNoRows) {
		return Branch{}, fmt.Errorf("branch %d not found", branchID)
	}
	if err != nil {
		return Branch{}, fmt.Errorf("load branch: %w", err)
	}
	return branch, nil
}

func saveSummaryTx(tx *sql.Tx, branchID int64, update SummaryUpdate, now string) error {
	if update.ThroughMessageID <= 0 {
		return errors.New("summary watermark must be a message ID")
	}
	if !messageOnBranchTx(tx, branchID, update.ThroughMessageID) {
		return fmt.Errorf("summary watermark message %d is not on branch %d", update.ThroughMessageID, branchID)
	}
	if _, err := tx.Exec(`INSERT INTO summaries(branch_id, through_message_id, content, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(branch_id) DO UPDATE SET through_message_id = excluded.through_message_id, content = excluded.content, updated_at = excluded.updated_at`, branchID, update.ThroughMessageID, update.Content, now); err != nil {
		return fmt.Errorf("save summary: %w", err)
	}
	return nil
}

func replaceFactsTx(tx *sql.Tx, branchID int64, facts map[string]string, now string) error {
	if _, err := branchByIDTx(tx, branchID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM facts WHERE branch_id = ?`, branchID); err != nil {
		return fmt.Errorf("clear facts: %w", err)
	}
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return errors.New("fact key is empty")
		}
		if _, err := tx.Exec(`INSERT INTO facts(branch_id, key, value, updated_at) VALUES (?, ?, ?, ?)`, branchID, key, facts[key], now); err != nil {
			return fmt.Errorf("save fact %q: %w", key, err)
		}
	}
	return nil
}

func summarySnapshotTx(tx *sql.Tx, branchID int64) (*int64, string, error) {
	var through sql.NullInt64
	var content string
	err := tx.QueryRow(`SELECT through_message_id, content FROM summaries WHERE branch_id = ?`, branchID).Scan(&through, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load summary snapshot: %w", err)
	}
	return nullablePointer(through), content, nil
}

func factsMapTx(tx *sql.Tx, branchID int64) (map[string]string, error) {
	rows, err := tx.Query(`SELECT key, value FROM facts WHERE branch_id = ?`, branchID)
	if err != nil {
		return nil, fmt.Errorf("load facts snapshot: %w", err)
	}
	defer rows.Close()
	facts := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan fact snapshot: %w", err)
		}
		facts[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load facts snapshot: %w", err)
	}
	return facts, nil
}

func messageOnBranchTx(tx *sql.Tx, branchID, messageID int64) bool {
	var found int
	err := tx.QueryRow(`WITH RECURSIVE lineage(id, previous_message_id) AS (
		SELECT id, previous_message_id FROM messages WHERE id = (SELECT head_message_id FROM branches WHERE id = ?)
		UNION ALL
		SELECT m.id, m.previous_message_id FROM messages m JOIN lineage ON m.id = lineage.previous_message_id
	) SELECT EXISTS(SELECT 1 FROM lineage WHERE id = ?)`, branchID, messageID).Scan(&found)
	return err == nil && found != 0
}

func insertMessageTx(tx *sql.Tx, chatID int64, previous *int64, role, content, now string) (int64, error) {
	result, err := tx.Exec(`INSERT INTO messages(chat_id, previous_message_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`, chatID, nullableInt64(previous), role, content, now)
	if err != nil {
		return 0, fmt.Errorf("save %s message: %w", role, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read %s message id: %w", role, err)
	}
	return id, nil
}

func insertAPICallTx(tx *sql.Tx, chatID, branchID int64, call APICall, now string) error {
	if call.Provider == "" || call.Model == "" || call.CounterLabel == "" || call.PricingTier == "" {
		return errors.New("API call provider, model, counter label, and pricing tier are required")
	}
	if call.Kind != "main" && call.Kind != "summary" && call.Kind != "facts" {
		return fmt.Errorf("unknown API call kind %q", call.Kind)
	}
	if !validStrategy(call.Strategy) {
		return fmt.Errorf("unknown API call strategy %q", call.Strategy)
	}
	if _, err := tx.Exec(`INSERT INTO api_calls(chat_id, branch_id, provider, model, kind, strategy, counter_label, current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local, prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api, completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd, output_cost_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, chatID, branchID, call.Provider, call.Model, call.Kind, call.Strategy, call.CounterLabel, nullableCallTokens(call.Kind, call.CurrentTokens), nullableCallTokens(call.Kind, call.FullHistoryTokens), call.SentTokensLocal, call.ResponseTokensLocal, call.Usage.PromptTokens, call.Usage.CachedPromptTokens, call.Usage.UncachedPromptTokens, call.Usage.CompletionTokens, call.Usage.TotalTokens, call.PricingTier, call.InputCostUSD, call.OutputCostUSD, now); err != nil {
		return fmt.Errorf("save API call: %w", err)
	}
	return nil
}

func nullableCallTokens(kind string, value int) any {
	if kind != "main" {
		return nil
	}
	return value
}

func scanChat(scanner interface{ Scan(...any) error }) (Chat, error) {
	var chat Chat
	var created, updated string
	err := scanner.Scan(&chat.ID, &chat.Title, &chat.ActiveBranchID, &created, &updated)
	if err != nil {
		return Chat{}, err
	}
	var parseErr error
	chat.CreatedAt, parseErr = parseDatabaseTime(created)
	if parseErr != nil {
		return Chat{}, parseErr
	}
	chat.UpdatedAt, parseErr = parseDatabaseTime(updated)
	if parseErr != nil {
		return Chat{}, parseErr
	}
	return chat, nil
}

func scanBranch(scanner interface{ Scan(...any) error }) (Branch, error) {
	var branch Branch
	var head sql.NullInt64
	var created, updated string
	err := scanner.Scan(&branch.ID, &branch.ChatID, &branch.Name, &head, &branch.Strategy, &branch.WindowSize, &created, &updated)
	if err != nil {
		return Branch{}, err
	}
	branch.HeadMessageID = nullablePointer(head)
	var parseErr error
	branch.CreatedAt, parseErr = parseDatabaseTime(created)
	if parseErr != nil {
		return Branch{}, parseErr
	}
	branch.UpdatedAt, parseErr = parseDatabaseTime(updated)
	if parseErr != nil {
		return Branch{}, parseErr
	}
	return branch, nil
}

func scanChatBranch(scanner interface{ Scan(...any) error }) (Chat, Branch, error) {
	var chat Chat
	var branch Branch
	var chatCreated, chatUpdated, branchCreated, branchUpdated string
	var head sql.NullInt64
	err := scanner.Scan(&chat.ID, &chat.Title, &chat.ActiveBranchID, &chatCreated, &chatUpdated, &branch.ID, &branch.ChatID, &branch.Name, &head, &branch.Strategy, &branch.WindowSize, &branchCreated, &branchUpdated)
	if err != nil {
		return Chat{}, Branch{}, err
	}
	var parseErr error
	if chat.CreatedAt, parseErr = parseDatabaseTime(chatCreated); parseErr != nil {
		return Chat{}, Branch{}, parseErr
	}
	if chat.UpdatedAt, parseErr = parseDatabaseTime(chatUpdated); parseErr != nil {
		return Chat{}, Branch{}, parseErr
	}
	if branch.CreatedAt, parseErr = parseDatabaseTime(branchCreated); parseErr != nil {
		return Chat{}, Branch{}, parseErr
	}
	if branch.UpdatedAt, parseErr = parseDatabaseTime(branchUpdated); parseErr != nil {
		return Chat{}, Branch{}, parseErr
	}
	branch.HeadMessageID = nullablePointer(head)
	return chat, branch, nil
}

func scanMessage(scanner interface{ Scan(...any) error }) (Message, error) {
	var message Message
	var previous sql.NullInt64
	var created string
	err := scanner.Scan(&message.ID, &message.ChatID, &previous, &message.Role, &message.Content, &created)
	if err != nil {
		return Message{}, err
	}
	message.Previous = nullablePointer(previous)
	message.CreatedAt, err = parseDatabaseTime(created)
	if err != nil {
		return Message{}, err
	}
	return message, nil
}

func scanCheckpoint(scanner interface{ Scan(...any) error }) (Checkpoint, error) {
	var checkpoint Checkpoint
	var head, through sql.NullInt64
	var factsJSON, created string
	err := scanner.Scan(&checkpoint.ID, &checkpoint.ChatID, &checkpoint.SourceBranchID, &checkpoint.Name, &head, &through, &checkpoint.Summary, &factsJSON, &created)
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint.HeadMessageID = nullablePointer(head)
	checkpoint.SummaryThroughMessageID = nullablePointer(through)
	if err := json.Unmarshal([]byte(factsJSON), &checkpoint.Facts); err != nil {
		return Checkpoint{}, fmt.Errorf("decode checkpoint facts: %w", err)
	}
	if checkpoint.Facts == nil {
		checkpoint.Facts = make(map[string]string)
	}
	checkpoint.CreatedAt, err = parseDatabaseTime(created)
	if err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

func databaseTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseDatabaseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse database timestamp %q: %w", value, err)
	}
	return parsed, nil
}

func nullablePointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return int64Ptr(value.Int64)
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Ptr(value int64) *int64 { return &value }
