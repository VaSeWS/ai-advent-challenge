package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 3

// Store owns the durable chat graph and all accounting recorded for it.
// A Store is safe for sequential TUI use; each mutating operation is committed
// before it returns.
type Store struct {
	db *sql.DB
}

// ProposedChange is one normalized, task-scoped structured mutation subject to
// active invariant enforcement.
type ProposedChange struct {
	TaskID   int64
	Category string
	Key      string
	Value    string
}

// InvariantConflictError reports the exact violated rule and the safe value
// that leaves the active requirement satisfied.
type InvariantConflictError struct {
	Invariant Invariant
	Change    ProposedChange
}

func (e *InvariantConflictError) Error() string {
	return fmt.Sprintf("invariant conflict: %s; safe next action: keep %s.%s=%s",
		e.Invariant.Rule, e.Invariant.Category, e.Invariant.Key, e.Invariant.RequiredValue)
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
	Memory           MemoryUpdate
	APICalls         []APICall
}

// OpenStore opens path, enables SQLite foreign keys and WAL, migrates schema
// through version 3, and creates the initial Chat 1/main branch when the database is new.
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

	if version == 0 {
		if err := executeSchemaStatements(tx, schemaV1Statements()); err != nil {
			return fmt.Errorf("apply schema version 1: %w", err)
		}
	}
	if version <= 1 {
		if err := executeSchemaStatements(tx, schemaV2Statements()); err != nil {
			return fmt.Errorf("apply schema version 2: %w", err)
		}
	}
	if version <= 2 {
		if err := executeSchemaStatements(tx, schemaV3Statements()); err != nil {
			return fmt.Errorf("apply schema version 3: %w", err)
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

func executeSchemaStatements(tx *sql.Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func schemaV1Statements() []string {
	return []string{
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
}

func schemaV2Statements() []string {
	return []string{
		`CREATE TABLE profiles (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			language TEXT NOT NULL,
			response_style TEXT NOT NULL,
			response_format TEXT NOT NULL,
			constraints TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE active_profile (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE RESTRICT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE tasks (
			id INTEGER PRIMARY KEY,
			branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
			goal TEXT NOT NULL,
			plan TEXT NOT NULL,
			phase TEXT NOT NULL CHECK(phase IN ('planning', 'execution', 'validation', 'done')),
			current_step TEXT NOT NULL,
			expected_action TEXT NOT NULL,
			paused INTEGER NOT NULL DEFAULT 0 CHECK(paused IN (0, 1)),
			active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX tasks_one_active_per_branch_idx ON tasks(branch_id) WHERE active = 1`,
		`CREATE TABLE long_term_memories (
			id INTEGER PRIMARY KEY,
			kind TEXT NOT NULL CHECK(kind IN ('decision', 'knowledge')),
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			profile_id INTEGER REFERENCES profiles(id) ON DELETE CASCADE,
			task_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK((profile_id IS NOT NULL AND task_id IS NULL) OR (profile_id IS NULL AND task_id IS NOT NULL))
		)`,
		`CREATE UNIQUE INDEX long_term_memories_profile_key_idx ON long_term_memories(profile_id, kind, key) WHERE profile_id IS NOT NULL`,
		`CREATE UNIQUE INDEX long_term_memories_task_key_idx ON long_term_memories(task_id, kind, key) WHERE task_id IS NOT NULL`,
		`CREATE TABLE invariants (
			id INTEGER PRIMARY KEY,
			task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			category TEXT NOT NULL,
			key TEXT NOT NULL,
			required_value TEXT NOT NULL,
			rule TEXT NOT NULL,
			source TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1 CHECK(active IN (0, 1)),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX invariants_one_active_key_idx ON invariants(task_id, category, key) WHERE active = 1`,
		`CREATE TABLE task_events (
			id INTEGER PRIMARY KEY,
			task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			from_phase TEXT NOT NULL CHECK(from_phase IN ('planning', 'execution', 'validation', 'done')),
			event TEXT NOT NULL CHECK(event IN ('approve_plan', 'submit_result', 'validation_passed', 'validation_failed')),
			to_phase TEXT NOT NULL CHECK(to_phase IN ('planning', 'execution', 'validation', 'done')),
			created_at TEXT NOT NULL,
			CHECK(
				(event = 'approve_plan' AND from_phase = 'planning' AND to_phase = 'execution') OR
				(event = 'submit_result' AND from_phase = 'execution' AND to_phase = 'validation') OR
				(event = 'validation_passed' AND from_phase = 'validation' AND to_phase = 'done') OR
				(event = 'validation_failed' AND from_phase = 'validation' AND to_phase = 'execution')
			)
		)`,
		`CREATE INDEX task_events_task_created_at_idx ON task_events(task_id, created_at, id)`,
		`CREATE TRIGGER task_events_immutable_update
			BEFORE UPDATE ON task_events
			BEGIN
				SELECT RAISE(ABORT, 'task events are immutable');
			END`,
		`CREATE TRIGGER task_events_immutable_delete
			BEFORE DELETE ON task_events
			BEGIN
				SELECT RAISE(ABORT, 'task events are immutable');
			END`,
		`CREATE TABLE api_calls_v2 (
			id INTEGER PRIMARY KEY,
			chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			branch_id INTEGER NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('main', 'summary', 'facts', 'memory')),
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
		`INSERT INTO api_calls_v2 (
			id, chat_id, branch_id, provider, model, kind, strategy, counter_label,
			current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local,
			prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api,
			completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd,
			output_cost_usd, created_at
		)
		SELECT
			id, chat_id, branch_id, provider, model, kind, strategy, counter_label,
			current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local,
			prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api,
			completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd,
			output_cost_usd, created_at
		FROM api_calls`,
		`DROP TABLE api_calls`,
		`ALTER TABLE api_calls_v2 RENAME TO api_calls`,
		`CREATE INDEX api_calls_grouping_idx ON api_calls(branch_id, provider, model, strategy, kind)`,
	}
}

func schemaV3Statements() []string {
	return []string{
		`ALTER TABLE invariants ADD COLUMN forbidden TEXT NOT NULL DEFAULT ''`,
	}
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

// CreateProfile persists a named presentation profile without selecting it.
func (s *Store) CreateProfile(input ProfileInput) (Profile, error) {
	var err error
	if input, err = validatedProfileInput(input); err != nil {
		return Profile{}, err
	}
	now := databaseTime(time.Now())
	result, err := s.db.Exec(`INSERT INTO profiles(name, language, response_style, response_format, constraints, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, input.Name, input.Language, input.ResponseStyle, input.ResponseFormat, input.Constraints, now, now)
	if err != nil {
		return Profile{}, fmt.Errorf("create profile: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Profile{}, fmt.Errorf("read profile ID: %w", err)
	}
	return s.Profile(id)
}

// Profiles lists saved profiles in creation order.
func (s *Store) Profiles() ([]Profile, error) {
	rows, err := s.db.Query(`SELECT id, name, language, response_style, response_format, constraints, created_at, updated_at FROM profiles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]Profile, 0)
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	return profiles, nil
}

// Profile returns one saved profile.
func (s *Store) Profile(profileID int64) (Profile, error) {
	if profileID <= 0 {
		return Profile{}, errors.New("profile ID must be positive")
	}
	profile, err := scanProfile(s.db.QueryRow(`SELECT id, name, language, response_style, response_format, constraints, created_at, updated_at FROM profiles WHERE id = ?`, profileID))
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, fmt.Errorf("profile %d not found", profileID)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load profile: %w", err)
	}
	return profile, nil
}

// UpdateProfile replaces a profile's mutable fields while retaining its identity.
func (s *Store) UpdateProfile(profileID int64, input ProfileInput) (Profile, error) {
	if profileID <= 0 {
		return Profile{}, errors.New("profile ID must be positive")
	}
	var err error
	if input, err = validatedProfileInput(input); err != nil {
		return Profile{}, err
	}
	result, err := s.db.Exec(`UPDATE profiles SET name = ?, language = ?, response_style = ?, response_format = ?, constraints = ?, updated_at = ? WHERE id = ?`, input.Name, input.Language, input.ResponseStyle, input.ResponseFormat, input.Constraints, databaseTime(time.Now()), profileID)
	if err != nil {
		return Profile{}, fmt.Errorf("update profile: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Profile{}, fmt.Errorf("read profile update: %w", err)
	}
	if affected != 1 {
		return Profile{}, fmt.Errorf("profile %d not found", profileID)
	}
	return s.Profile(profileID)
}

// ActiveProfile returns the persistently selected profile, if any.
func (s *Store) ActiveProfile() (*Profile, error) {
	profile, err := scanProfile(s.db.QueryRow(`SELECT p.id, p.name, p.language, p.response_style, p.response_format, p.constraints, p.created_at, p.updated_at FROM active_profile a JOIN profiles p ON p.id = a.profile_id WHERE a.id = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active profile: %w", err)
	}
	return &profile, nil
}

// SelectProfile makes an existing profile the sole persistent active selection.
func (s *Store) SelectProfile(profileID int64) (Profile, error) {
	if profileID <= 0 {
		return Profile{}, errors.New("profile ID must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Profile{}, fmt.Errorf("begin select profile: %w", err)
	}
	defer tx.Rollback()
	if _, err := profileByIDTx(tx, profileID); err != nil {
		return Profile{}, err
	}
	now := databaseTime(time.Now())
	if _, err := tx.Exec(`INSERT INTO active_profile(id, profile_id, updated_at) VALUES (1, ?, ?) ON CONFLICT(id) DO UPDATE SET profile_id = excluded.profile_id, updated_at = excluded.updated_at`, profileID, now); err != nil {
		return Profile{}, fmt.Errorf("select profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Profile{}, fmt.Errorf("commit select profile: %w", err)
	}
	return s.Profile(profileID)
}

// CreateTask creates the single active planning task for a branch.
func (s *Store) CreateTask(branchID int64, snapshot TaskSnapshot) (Task, error) {
	if branchID <= 0 {
		return Task{}, errors.New("branch ID must be positive")
	}
	var err error
	if snapshot, err = validatedTaskSnapshot(snapshot); err != nil {
		return Task{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, fmt.Errorf("begin create task: %w", err)
	}
	defer tx.Rollback()
	if _, err := branchByIDTx(tx, branchID); err != nil {
		return Task{}, err
	}
	var activeTaskID int64
	err = tx.QueryRow(`SELECT id FROM tasks WHERE branch_id = ? AND active = 1`, branchID).Scan(&activeTaskID)
	if err == nil {
		return Task{}, fmt.Errorf("branch %d already has active task %d", branchID, activeTaskID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("load active task: %w", err)
	}
	now := databaseTime(time.Now())
	result, err := tx.Exec(`INSERT INTO tasks(branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, branchID, snapshot.Goal, snapshot.Plan, TaskPhasePlanning, snapshot.CurrentStep, snapshot.ExpectedAction, boolInteger(snapshot.Paused), now, now)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	taskID, err := result.LastInsertId()
	if err != nil {
		return Task{}, fmt.Errorf("read task ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit create task: %w", err)
	}
	return s.taskByID(taskID)
}

// ActiveTask returns the active working-memory snapshot for branchID, if any.
func (s *Store) ActiveTask(branchID int64) (*Task, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	task, err := scanTask(s.db.QueryRow(`SELECT id, branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at FROM tasks WHERE branch_id = ? AND active = 1`, branchID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active task: %w", err)
	}
	return &task, nil
}

// UpdateTask replaces mutable working-memory fields without changing lifecycle
// phase or active status.
func (s *Store) UpdateTask(taskID int64, snapshot TaskSnapshot) (Task, error) {
	if taskID <= 0 {
		return Task{}, errors.New("task ID must be positive")
	}
	var err error
	if snapshot, err = validatedTaskSnapshot(snapshot); err != nil {
		return Task{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, fmt.Errorf("begin update task: %w", err)
	}
	defer tx.Rollback()
	task, err := taskByIDTx(tx, taskID)
	if err != nil {
		return Task{}, err
	}
	if !task.Active {
		return Task{}, fmt.Errorf("task %d is inactive", taskID)
	}
	if task.Phase == TaskPhaseDone && snapshot.Paused {
		return Task{}, errors.New("completed task cannot be paused")
	}
	if err := checkTaskSnapshotInvariantsTx(tx, taskID, snapshot); err != nil {
		return Task{}, err
	}
	if _, err := tx.Exec(`UPDATE tasks SET goal = ?, plan = ?, current_step = ?, expected_action = ?, paused = ?, updated_at = ? WHERE id = ?`, snapshot.Goal, snapshot.Plan, snapshot.CurrentStep, snapshot.ExpectedAction, boolInteger(snapshot.Paused), databaseTime(time.Now()), taskID); err != nil {
		return Task{}, fmt.Errorf("update task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit update task: %w", err)
	}
	return s.taskByID(taskID)
}

// TransitionTask applies one allowed lifecycle event to an active, unpaused
// task and records the matching immutable history entry in the same
// transaction. Phase has no independent setter.
func (s *Store) TransitionTask(taskID int64, event TaskEventKind) (Task, error) {
	if taskID <= 0 {
		return Task{}, errors.New("task ID must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, fmt.Errorf("begin task transition: %w", err)
	}
	defer tx.Rollback()

	task, err := taskByIDTx(tx, taskID)
	if err != nil {
		return Task{}, err
	}
	if !task.Active {
		return Task{}, fmt.Errorf("task %d is inactive", taskID)
	}
	if task.Paused {
		return Task{}, fmt.Errorf("task %d is paused; resume it before transitioning", taskID)
	}
	target, ok := transitionTarget(task.Phase, event)
	if !ok {
		return Task{}, fmt.Errorf("task %d cannot apply %q while phase is %q", taskID, event, task.Phase)
	}
	if err := checkInvariantsTx(tx, ProposedChange{TaskID: taskID, Category: "task", Key: "phase", Value: string(target)}); err != nil {
		return Task{}, err
	}

	now := databaseTime(time.Now())
	if _, err := tx.Exec(`UPDATE tasks SET phase = ?, updated_at = ? WHERE id = ?`, target, now, taskID); err != nil {
		return Task{}, fmt.Errorf("update task phase: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, ?, ?, ?, ?)`, taskID, task.Phase, event, target, now); err != nil {
		return Task{}, fmt.Errorf("record task transition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit task transition: %w", err)
	}
	return s.taskByID(taskID)
}

// PutActiveLongTermMemory upserts one record in an active branch scope. The
// owner is resolved inside the transaction and never supplied by callers.
// Any active task on the branch guards both profile- and task-scoped records.
func (s *Store) PutActiveLongTermMemory(branchID int64, scope MemoryScope, kind MemoryKind, key, value string) (LongTermMemory, error) {
	if branchID <= 0 {
		return LongTermMemory{}, errors.New("branch ID must be positive")
	}
	if scope != MemoryScopeProfile && scope != MemoryScopeTask {
		return LongTermMemory{}, fmt.Errorf("unknown memory scope %q", scope)
	}
	memory := LongTermMemory{Kind: kind, Key: key, Value: value}
	var err error
	if memory, err = validatedLongTermMemoryFields(memory); err != nil {
		return LongTermMemory{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return LongTermMemory{}, fmt.Errorf("begin save long-term memory: %w", err)
	}
	defer tx.Rollback()
	if _, err := branchByIDTx(tx, branchID); err != nil {
		return LongTermMemory{}, err
	}
	profile, err := activeProfileTx(tx)
	if err != nil {
		return LongTermMemory{}, err
	}
	task, err := activeTaskTx(tx, branchID)
	if err != nil {
		return LongTermMemory{}, err
	}
	switch scope {
	case MemoryScopeProfile:
		if profile == nil {
			return LongTermMemory{}, errors.New("profile memory requires an active profile")
		}
		memory.ProfileID = int64Ptr(profile.ID)
	case MemoryScopeTask:
		if task == nil {
			return LongTermMemory{}, fmt.Errorf("task memory requires an active task for branch %d", branchID)
		}
		memory.TaskID = int64Ptr(task.ID)
	}
	if task != nil {
		if err := checkInvariantsTx(tx, ProposedChange{TaskID: task.ID, Category: string(memory.Kind), Key: memory.Key, Value: memory.Value}); err != nil {
			return LongTermMemory{}, err
		}
	}
	now := databaseTime(time.Now())
	if memory.ProfileID != nil {
		if _, err := tx.Exec(`INSERT INTO long_term_memories(kind, key, value, profile_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(profile_id, kind, key) WHERE profile_id IS NOT NULL DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, memory.Kind, memory.Key, memory.Value, *memory.ProfileID, now, now); err != nil {
			return LongTermMemory{}, fmt.Errorf("save profile memory: %w", err)
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO long_term_memories(kind, key, value, task_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(task_id, kind, key) WHERE task_id IS NOT NULL DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, memory.Kind, memory.Key, memory.Value, *memory.TaskID, now, now); err != nil {
			return LongTermMemory{}, fmt.Errorf("save task memory: %w", err)
		}
	}
	memory, err = longTermMemoryByScopeTx(tx, memory.Kind, memory.Key, memory.ProfileID, memory.TaskID)
	if err != nil {
		return LongTermMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return LongTermMemory{}, fmt.Errorf("commit save long-term memory: %w", err)
	}
	return memory, nil
}

// ActiveLongTermMemories returns records from the selected profile and this
// branch's active task, in deterministic scope/kind/key order.
func (s *Store) ActiveLongTermMemories(branchID int64) ([]LongTermMemory, error) {
	if _, err := s.branchByID(branchID); err != nil {
		return nil, err
	}
	profile, err := s.ActiveProfile()
	if err != nil {
		return nil, err
	}
	task, err := s.ActiveTask(branchID)
	if err != nil {
		return nil, err
	}
	memories := make([]LongTermMemory, 0)
	if profile != nil {
		profileMemories, err := s.longTermMemories(`profile_id = ?`, profile.ID)
		if err != nil {
			return nil, err
		}
		memories = append(memories, profileMemories...)
	}
	if task != nil {
		taskMemories, err := s.longTermMemories(`task_id = ?`, task.ID)
		if err != nil {
			return nil, err
		}
		memories = append(memories, taskMemories...)
	}
	sort.Slice(memories, func(i, j int) bool {
		leftScope, rightScope := longTermMemoryScope(memories[i]), longTermMemoryScope(memories[j])
		if leftScope != rightScope {
			return leftScope < rightScope
		}
		if memories[i].Kind != memories[j].Kind {
			return memories[i].Kind < memories[j].Kind
		}
		if memories[i].Key != memories[j].Key {
			return memories[i].Key < memories[j].Key
		}
		return memories[i].ID < memories[j].ID
	})
	return memories, nil
}

// ProfileMemories lists one profile's records in stable kind/key order.
func (s *Store) ProfileMemories(profileID int64) ([]LongTermMemory, error) {
	if _, err := s.Profile(profileID); err != nil {
		return nil, err
	}
	return s.longTermMemories(`profile_id = ?`, profileID)
}

// TaskMemories lists one task's records in stable kind/key order.
func (s *Store) TaskMemories(taskID int64) ([]LongTermMemory, error) {
	if _, err := s.taskByID(taskID); err != nil {
		return nil, err
	}
	return s.longTermMemories(`task_id = ?`, taskID)
}

// AddInvariant persists one active normalized, task-scoped requirement.
func (s *Store) AddInvariant(invariant Invariant) (Invariant, error) {
	var err error
	if invariant, err = validatedInvariant(invariant); err != nil {
		return Invariant{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Invariant{}, fmt.Errorf("begin add invariant: %w", err)
	}
	defer tx.Rollback()
	if _, err := taskByIDTx(tx, invariant.TaskID); err != nil {
		return Invariant{}, err
	}
	if err := checkInvariantsTx(tx, ProposedChange{
		TaskID: invariant.TaskID, Category: invariant.Category, Key: invariant.Key, Value: invariant.RequiredValue,
	}); err != nil {
		return Invariant{}, err
	}
	now := databaseTime(time.Now())
	result, err := tx.Exec(`INSERT INTO invariants(task_id, category, key, required_value, rule, source, forbidden, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`, invariant.TaskID, invariant.Category, invariant.Key, invariant.RequiredValue, invariant.Rule, invariant.Source, invariant.Forbidden, now, now)
	if err != nil {
		return Invariant{}, fmt.Errorf("add invariant: %w", err)
	}
	invariantID, err := result.LastInsertId()
	if err != nil {
		return Invariant{}, fmt.Errorf("read invariant ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Invariant{}, fmt.Errorf("commit add invariant: %w", err)
	}
	return s.invariantByID(invariantID)
}

// Invariants lists all task invariants, including deactivated history.
func (s *Store) Invariants(taskID int64) ([]Invariant, error) {
	if _, err := s.taskByID(taskID); err != nil {
		return nil, err
	}
	return s.listInvariants(`task_id = ?`, taskID)
}

// ActiveInvariants lists only active task invariants.
func (s *Store) ActiveInvariants(taskID int64) ([]Invariant, error) {
	if _, err := s.taskByID(taskID); err != nil {
		return nil, err
	}
	return s.listInvariants(`task_id = ? AND active = 1`, taskID)
}

// CheckInvariants refuses a proposed task-scoped mutation that conflicts with
// an active structured requirement. It never persists state.
func (s *Store) CheckInvariants(change ProposedChange) error {
	var err error
	if change, err = validatedProposedChange(change); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin invariant check: %w", err)
	}
	defer tx.Rollback()
	if _, err := taskByIDTx(tx, change.TaskID); err != nil {
		return err
	}
	return checkInvariantsTx(tx, change)
}

func checkInvariantsTx(tx *sql.Tx, change ProposedChange) error {
	invariant, err := scanInvariant(tx.QueryRow(`SELECT id, task_id, category, key, required_value, rule, source, forbidden, active, created_at, updated_at FROM invariants WHERE task_id = ? AND category = ? AND key = ? AND active = 1`, change.TaskID, change.Category, change.Key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load active invariant: %w", err)
	}
	if invariant.RequiredValue == change.Value {
		return nil
	}
	return &InvariantConflictError{Invariant: invariant, Change: change}
}

func checkTaskSnapshotInvariantsTx(tx *sql.Tx, taskID int64, snapshot TaskSnapshot) error {
	for _, change := range []ProposedChange{
		{TaskID: taskID, Category: "task", Key: "goal", Value: snapshot.Goal},
		{TaskID: taskID, Category: "task", Key: "plan", Value: snapshot.Plan},
		{TaskID: taskID, Category: "task", Key: "current_step", Value: snapshot.CurrentStep},
		{TaskID: taskID, Category: "task", Key: "expected_action", Value: snapshot.ExpectedAction},
		{TaskID: taskID, Category: "task", Key: "paused", Value: strconv.FormatBool(snapshot.Paused)},
	} {
		if err := checkInvariantsTx(tx, change); err != nil {
			return err
		}
	}
	return nil
}
func checkProfileInvariantsTx(tx *sql.Tx, taskID int64, input ProfileInput) error {
	for _, change := range []ProposedChange{
		{TaskID: taskID, Category: "profile", Key: "language", Value: input.Language},
		{TaskID: taskID, Category: "profile", Key: "response_style", Value: input.ResponseStyle},
		{TaskID: taskID, Category: "profile", Key: "response_format", Value: input.ResponseFormat},
		{TaskID: taskID, Category: "profile", Key: "constraints", Value: input.Constraints},
	} {
		if err := checkInvariantsTx(tx, change); err != nil {
			return err
		}
	}
	return nil
}

// DeactivateInvariant preserves the invariant record while removing it from
// active enforcement.
func (s *Store) DeactivateInvariant(invariantID int64) (Invariant, error) {
	if invariantID <= 0 {
		return Invariant{}, errors.New("invariant ID must be positive")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Invariant{}, fmt.Errorf("begin deactivate invariant: %w", err)
	}
	defer tx.Rollback()
	if _, err := invariantByIDTx(tx, invariantID); err != nil {
		return Invariant{}, err
	}
	if _, err := tx.Exec(`UPDATE invariants SET active = 0, updated_at = ? WHERE id = ?`, databaseTime(time.Now()), invariantID); err != nil {
		return Invariant{}, fmt.Errorf("deactivate invariant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Invariant{}, fmt.Errorf("commit deactivate invariant: %w", err)
	}
	return s.invariantByID(invariantID)
}

// TaskEvents lists immutable transition history chronologically.
func (s *Store) TaskEvents(taskID int64) ([]TaskEvent, error) {
	if _, err := s.taskByID(taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, task_id, from_phase, event, to_phase, created_at FROM task_events WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	defer rows.Close()
	events := make([]TaskEvent, 0)
	for rows.Next() {
		event, err := scanTaskEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task events: %w", err)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].ID < events[j].ID
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	return events, nil
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
// applies validated memory and optional derived-state replacements, and records
// every API call. A failed write leaves no partial turn, derived state, memory,
// or accounting rows.
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
	if err := applyMemoryUpdateTx(tx, input.BranchID, input.Memory, now); err != nil {
		return Message{}, err
	}
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

func (s *Store) taskByID(taskID int64) (Task, error) {
	if taskID <= 0 {
		return Task{}, errors.New("task ID must be positive")
	}
	task, err := scanTask(s.db.QueryRow(`SELECT id, branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d not found", taskID)
	}
	if err != nil {
		return Task{}, fmt.Errorf("load task: %w", err)
	}
	return task, nil
}

func taskByIDTx(tx *sql.Tx, taskID int64) (Task, error) {
	task, err := scanTask(tx.QueryRow(`SELECT id, branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at FROM tasks WHERE id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d not found", taskID)
	}
	if err != nil {
		return Task{}, fmt.Errorf("load task: %w", err)
	}
	return task, nil
}

func profileByIDTx(tx *sql.Tx, profileID int64) (Profile, error) {
	profile, err := scanProfile(tx.QueryRow(`SELECT id, name, language, response_style, response_format, constraints, created_at, updated_at FROM profiles WHERE id = ?`, profileID))
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, fmt.Errorf("profile %d not found", profileID)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load profile: %w", err)
	}
	return profile, nil
}

func (s *Store) longTermMemories(where string, argument int64) ([]LongTermMemory, error) {
	rows, err := s.db.Query(`SELECT id, kind, key, value, profile_id, task_id, created_at, updated_at FROM long_term_memories WHERE `+where+` ORDER BY kind, key, id`, argument)
	if err != nil {
		return nil, fmt.Errorf("list long-term memories: %w", err)
	}
	defer rows.Close()
	memories := make([]LongTermMemory, 0)
	for rows.Next() {
		memory, err := scanLongTermMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan long-term memory: %w", err)
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list long-term memories: %w", err)
	}
	return memories, nil
}

func longTermMemoryByScopeTx(tx *sql.Tx, kind MemoryKind, key string, profileID, taskID *int64) (LongTermMemory, error) {
	var row *sql.Row
	if profileID != nil {
		row = tx.QueryRow(`SELECT id, kind, key, value, profile_id, task_id, created_at, updated_at FROM long_term_memories WHERE profile_id = ? AND kind = ? AND key = ?`, *profileID, kind, key)
	} else {
		row = tx.QueryRow(`SELECT id, kind, key, value, profile_id, task_id, created_at, updated_at FROM long_term_memories WHERE task_id = ? AND kind = ? AND key = ?`, *taskID, kind, key)
	}
	memory, err := scanLongTermMemory(row)
	if err != nil {
		return LongTermMemory{}, fmt.Errorf("load long-term memory: %w", err)
	}
	return memory, nil
}

func (s *Store) invariantByID(invariantID int64) (Invariant, error) {
	invariant, err := scanInvariant(s.db.QueryRow(`SELECT id, task_id, category, key, required_value, rule, source, forbidden, active, created_at, updated_at FROM invariants WHERE id = ?`, invariantID))
	if errors.Is(err, sql.ErrNoRows) {
		return Invariant{}, fmt.Errorf("invariant %d not found", invariantID)
	}
	if err != nil {
		return Invariant{}, fmt.Errorf("load invariant: %w", err)
	}
	return invariant, nil
}

func invariantByIDTx(tx *sql.Tx, invariantID int64) (Invariant, error) {
	invariant, err := scanInvariant(tx.QueryRow(`SELECT id, task_id, category, key, required_value, rule, source, forbidden, active, created_at, updated_at FROM invariants WHERE id = ?`, invariantID))
	if errors.Is(err, sql.ErrNoRows) {
		return Invariant{}, fmt.Errorf("invariant %d not found", invariantID)
	}
	if err != nil {
		return Invariant{}, fmt.Errorf("load invariant: %w", err)
	}
	return invariant, nil
}

func (s *Store) listInvariants(where string, argument int64) ([]Invariant, error) {
	rows, err := s.db.Query(`SELECT id, task_id, category, key, required_value, rule, source, forbidden, active, created_at, updated_at FROM invariants WHERE `+where+` ORDER BY category, key, id`, argument)
	if err != nil {
		return nil, fmt.Errorf("list invariants: %w", err)
	}
	defer rows.Close()
	invariants := make([]Invariant, 0)
	for rows.Next() {
		invariant, err := scanInvariant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan invariant: %w", err)
		}
		invariants = append(invariants, invariant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list invariants: %w", err)
	}
	return invariants, nil
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

func applyMemoryUpdateTx(tx *sql.Tx, branchID int64, update MemoryUpdate, now string) error {
	needsProfile := update.Profile != nil
	needsTask := update.Task != nil
	for _, record := range update.Records {
		switch record.Scope {
		case MemoryScopeProfile:
			needsProfile = true
		case MemoryScopeTask:
			needsTask = true
		default:
			return fmt.Errorf("unknown memory scope %q", record.Scope)
		}
	}

	var profile *Profile
	if needsProfile {
		var err error
		profile, err = activeProfileTx(tx)
		if err != nil {
			return err
		}
		if profile == nil {
			return errors.New("memory update requires an active profile")
		}
	}
	var task *Task
	if needsTask {
		var err error
		task, err = activeTaskTx(tx, branchID)
		if err != nil {
			return err
		}
		if task == nil {
			return fmt.Errorf("memory update requires an active task for branch %d", branchID)
		}
	}

	var profileInput *ProfileInput
	if update.Profile != nil {
		input := ProfileInput{
			Name:           profile.Name,
			Language:       profile.Language,
			ResponseStyle:  profile.ResponseStyle,
			ResponseFormat: profile.ResponseFormat,
			Constraints:    profile.Constraints,
		}
		if update.Profile.Language != nil {
			input.Language = *update.Profile.Language
		}
		if update.Profile.ResponseStyle != nil {
			input.ResponseStyle = *update.Profile.ResponseStyle
		}
		if update.Profile.ResponseFormat != nil {
			input.ResponseFormat = *update.Profile.ResponseFormat
		}
		if update.Profile.Constraints != nil {
			input.Constraints = *update.Profile.Constraints
		}
		validated, err := validatedProfileInput(input)
		if err != nil {
			return err
		}
		profileInput = &validated
	}

	var taskSnapshot *TaskSnapshot
	if update.Task != nil {
		snapshot := TaskSnapshot{
			Goal:           task.Goal,
			Plan:           task.Plan,
			CurrentStep:    task.CurrentStep,
			ExpectedAction: task.ExpectedAction,
			Paused:         task.Paused,
		}
		if update.Task.Goal != "" {
			snapshot.Goal = update.Task.Goal
		}
		if update.Task.Plan != "" {
			snapshot.Plan = update.Task.Plan
		}
		if update.Task.CurrentStep != "" {
			snapshot.CurrentStep = update.Task.CurrentStep
		}
		if update.Task.ExpectedAction != "" {
			snapshot.ExpectedAction = update.Task.ExpectedAction
		}
		validated, err := validatedTaskSnapshot(snapshot)
		if err != nil {
			return err
		}
		taskSnapshot = &validated
	}

	memories := make([]LongTermMemory, 0, len(update.Records))
	seen := make(map[string]struct{}, len(update.Records))
	for _, record := range update.Records {
		memory := LongTermMemory{Kind: record.Kind, Key: record.Key, Value: record.Value}
		switch record.Scope {
		case MemoryScopeProfile:
			memory.ProfileID = int64Ptr(profile.ID)
		case MemoryScopeTask:
			memory.TaskID = int64Ptr(task.ID)
		}
		var err error
		if memory, err = validatedLongTermMemory(memory); err != nil {
			return err
		}
		identity := string(record.Scope) + "\x00" + string(memory.Kind) + "\x00" + memory.Key
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("duplicate memory candidate %q", identity)
		}
		seen[identity] = struct{}{}
		memories = append(memories, memory)
	}
	guardTask := task
	if guardTask == nil && (profileInput != nil || len(memories) != 0) {
		var err error
		guardTask, err = activeTaskTx(tx, branchID)
		if err != nil {
			return err
		}
	}
	if taskSnapshot != nil {
		if err := checkTaskSnapshotInvariantsTx(tx, task.ID, *taskSnapshot); err != nil {
			return err
		}
	}
	if guardTask != nil {
		if profileInput != nil {
			if err := checkProfileInvariantsTx(tx, guardTask.ID, *profileInput); err != nil {
				return err
			}
		}
		for _, memory := range memories {
			if err := checkInvariantsTx(tx, ProposedChange{TaskID: guardTask.ID, Category: string(memory.Kind), Key: memory.Key, Value: memory.Value}); err != nil {
				return err
			}
		}
	}

	if profileInput != nil {
		if _, err := tx.Exec(`UPDATE profiles SET language = ?, response_style = ?, response_format = ?, constraints = ?, updated_at = ? WHERE id = ?`, profileInput.Language, profileInput.ResponseStyle, profileInput.ResponseFormat, profileInput.Constraints, now, profile.ID); err != nil {
			return fmt.Errorf("update active profile memory: %w", err)
		}
	}
	if taskSnapshot != nil {
		if _, err := tx.Exec(`UPDATE tasks SET goal = ?, plan = ?, current_step = ?, expected_action = ?, updated_at = ? WHERE id = ?`, taskSnapshot.Goal, taskSnapshot.Plan, taskSnapshot.CurrentStep, taskSnapshot.ExpectedAction, now, task.ID); err != nil {
			return fmt.Errorf("update active task memory: %w", err)
		}
	}
	for _, memory := range memories {
		if memory.ProfileID != nil {
			if _, err := tx.Exec(`INSERT INTO long_term_memories(kind, key, value, profile_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(profile_id, kind, key) WHERE profile_id IS NOT NULL DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, memory.Kind, memory.Key, memory.Value, *memory.ProfileID, now, now); err != nil {
				return fmt.Errorf("save profile memory: %w", err)
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO long_term_memories(kind, key, value, task_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(task_id, kind, key) WHERE task_id IS NOT NULL DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, memory.Kind, memory.Key, memory.Value, *memory.TaskID, now, now); err != nil {
			return fmt.Errorf("save task memory: %w", err)
		}
	}
	return nil
}

func activeProfileTx(tx *sql.Tx) (*Profile, error) {
	profile, err := scanProfile(tx.QueryRow(`SELECT p.id, p.name, p.language, p.response_style, p.response_format, p.constraints, p.created_at, p.updated_at FROM active_profile a JOIN profiles p ON p.id = a.profile_id WHERE a.id = 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active profile: %w", err)
	}
	return &profile, nil
}

func activeTaskTx(tx *sql.Tx, branchID int64) (*Task, error) {
	task, err := scanTask(tx.QueryRow(`SELECT id, branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at FROM tasks WHERE branch_id = ? AND active = 1`, branchID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active task: %w", err)
	}
	return &task, nil
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
	if call.Kind != "main" && call.Kind != "summary" && call.Kind != "facts" && call.Kind != "memory" {
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

func scanProfile(scanner interface{ Scan(...any) error }) (Profile, error) {
	var profile Profile
	var created, updated string
	err := scanner.Scan(&profile.ID, &profile.Name, &profile.Language, &profile.ResponseStyle, &profile.ResponseFormat, &profile.Constraints, &created, &updated)
	if err != nil {
		return Profile{}, err
	}
	if profile.CreatedAt, err = parseDatabaseTime(created); err != nil {
		return Profile{}, err
	}
	if profile.UpdatedAt, err = parseDatabaseTime(updated); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func scanTask(scanner interface{ Scan(...any) error }) (Task, error) {
	var task Task
	var phase, created, updated string
	var paused, active int
	err := scanner.Scan(&task.ID, &task.BranchID, &task.Goal, &task.Plan, &phase, &task.CurrentStep, &task.ExpectedAction, &paused, &active, &created, &updated)
	if err != nil {
		return Task{}, err
	}
	task.Phase = TaskPhase(phase)
	if !validTaskPhase(task.Phase) {
		return Task{}, fmt.Errorf("invalid task phase %q", phase)
	}
	var boolErr error
	if task.Paused, boolErr = scanBoolean(paused, "task paused"); boolErr != nil {
		return Task{}, boolErr
	}
	if task.Active, boolErr = scanBoolean(active, "task active"); boolErr != nil {
		return Task{}, boolErr
	}
	if task.CreatedAt, err = parseDatabaseTime(created); err != nil {
		return Task{}, err
	}
	if task.UpdatedAt, err = parseDatabaseTime(updated); err != nil {
		return Task{}, err
	}
	return task, nil
}

func scanLongTermMemory(scanner interface{ Scan(...any) error }) (LongTermMemory, error) {
	var memory LongTermMemory
	var kind, created, updated string
	var profileID, taskID sql.NullInt64
	err := scanner.Scan(&memory.ID, &kind, &memory.Key, &memory.Value, &profileID, &taskID, &created, &updated)
	if err != nil {
		return LongTermMemory{}, err
	}
	memory.Kind = MemoryKind(kind)
	if !validMemoryKind(memory.Kind) {
		return LongTermMemory{}, fmt.Errorf("invalid long-term memory kind %q", kind)
	}
	memory.ProfileID = nullablePointer(profileID)
	memory.TaskID = nullablePointer(taskID)
	if (memory.ProfileID == nil) == (memory.TaskID == nil) {
		return LongTermMemory{}, errors.New("long-term memory must have exactly one owner")
	}
	if memory.CreatedAt, err = parseDatabaseTime(created); err != nil {
		return LongTermMemory{}, err
	}
	if memory.UpdatedAt, err = parseDatabaseTime(updated); err != nil {
		return LongTermMemory{}, err
	}
	return memory, nil
}

func scanInvariant(scanner interface{ Scan(...any) error }) (Invariant, error) {
	var invariant Invariant
	var active int
	var created, updated string
	err := scanner.Scan(&invariant.ID, &invariant.TaskID, &invariant.Category, &invariant.Key, &invariant.RequiredValue, &invariant.Rule, &invariant.Source, &invariant.Forbidden, &active, &created, &updated)
	if err != nil {
		return Invariant{}, err
	}
	var boolErr error
	if invariant.Active, boolErr = scanBoolean(active, "invariant active"); boolErr != nil {
		return Invariant{}, boolErr
	}
	if invariant.CreatedAt, err = parseDatabaseTime(created); err != nil {
		return Invariant{}, err
	}
	if invariant.UpdatedAt, err = parseDatabaseTime(updated); err != nil {
		return Invariant{}, err
	}
	return invariant, nil
}

func scanTaskEvent(scanner interface{ Scan(...any) error }) (TaskEvent, error) {
	var event TaskEvent
	var from, kind, to, created string
	err := scanner.Scan(&event.ID, &event.TaskID, &from, &kind, &to, &created)
	if err != nil {
		return TaskEvent{}, err
	}
	event.FromPhase = TaskPhase(from)
	event.Event = TaskEventKind(kind)
	event.ToPhase = TaskPhase(to)
	if !validTaskEvent(event.FromPhase, event.Event, event.ToPhase) {
		return TaskEvent{}, fmt.Errorf("invalid task event %q: %q -> %q", kind, from, to)
	}
	if event.CreatedAt, err = parseDatabaseTime(created); err != nil {
		return TaskEvent{}, err
	}
	return event, nil
}

func validatedProfileInput(input ProfileInput) (ProfileInput, error) {
	var err error
	if input.Name, err = requiredString("profile name", input.Name); err != nil {
		return ProfileInput{}, err
	}
	if input.Language, err = requiredString("profile language", input.Language); err != nil {
		return ProfileInput{}, err
	}
	if input.ResponseStyle, err = requiredString("profile response style", input.ResponseStyle); err != nil {
		return ProfileInput{}, err
	}
	if input.ResponseFormat, err = requiredString("profile response format", input.ResponseFormat); err != nil {
		return ProfileInput{}, err
	}
	input.Constraints = strings.TrimSpace(input.Constraints)
	return input, nil
}

func validatedTaskSnapshot(snapshot TaskSnapshot) (TaskSnapshot, error) {
	var err error
	if snapshot.Goal, err = requiredString("task goal", snapshot.Goal); err != nil {
		return TaskSnapshot{}, err
	}
	if snapshot.Plan, err = requiredString("task plan", snapshot.Plan); err != nil {
		return TaskSnapshot{}, err
	}
	if snapshot.CurrentStep, err = requiredString("task current step", snapshot.CurrentStep); err != nil {
		return TaskSnapshot{}, err
	}
	if snapshot.ExpectedAction, err = requiredString("task expected action", snapshot.ExpectedAction); err != nil {
		return TaskSnapshot{}, err
	}
	return snapshot, nil
}

func validatedLongTermMemory(memory LongTermMemory) (LongTermMemory, error) {
	var err error
	if memory, err = validatedLongTermMemoryFields(memory); err != nil {
		return LongTermMemory{}, err
	}
	if (memory.ProfileID == nil) == (memory.TaskID == nil) {
		return LongTermMemory{}, errors.New("long-term memory must have exactly one profile or task owner")
	}
	if memory.ProfileID != nil && *memory.ProfileID <= 0 {
		return LongTermMemory{}, errors.New("profile owner ID must be positive")
	}
	if memory.TaskID != nil && *memory.TaskID <= 0 {
		return LongTermMemory{}, errors.New("task owner ID must be positive")
	}
	return memory, nil
}

func validatedLongTermMemoryFields(memory LongTermMemory) (LongTermMemory, error) {
	if !validMemoryKind(memory.Kind) {
		return LongTermMemory{}, fmt.Errorf("unknown long-term memory kind %q", memory.Kind)
	}
	var err error
	if memory.Key, err = normalizedIdentifier("long-term memory key", memory.Key); err != nil {
		return LongTermMemory{}, err
	}
	if memory.Value, err = requiredString("long-term memory value", memory.Value); err != nil {
		return LongTermMemory{}, err
	}
	return memory, nil
}

func validatedInvariant(invariant Invariant) (Invariant, error) {
	if invariant.TaskID <= 0 {
		return Invariant{}, errors.New("task ID must be positive")
	}
	var err error
	if invariant.Category, err = normalizedIdentifier("invariant category", invariant.Category); err != nil {
		return Invariant{}, err
	}
	if invariant.Key, err = normalizedIdentifier("invariant key", invariant.Key); err != nil {
		return Invariant{}, err
	}
	if invariant.RequiredValue, err = requiredString("invariant required value", invariant.RequiredValue); err != nil {
		return Invariant{}, err
	}
	if invariant.Rule, err = requiredString("invariant rule", invariant.Rule); err != nil {
		return Invariant{}, err
	}
	if invariant.Source, err = requiredString("invariant source", invariant.Source); err != nil {
		return Invariant{}, err
	}
	if invariant.Forbidden, err = canonicalForbiddenTerms(invariant.Forbidden); err != nil {
		return Invariant{}, err
	}
	return invariant, nil
}

func canonicalForbiddenTerms(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	terms := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(terms))
	for index := range terms {
		term := strings.ToLower(strings.TrimSpace(terms[index]))
		if term == "" {
			return "", errors.New("forbidden terms must not contain empty values")
		}
		if _, exists := seen[term]; exists {
			return "", fmt.Errorf("duplicate forbidden term %q", term)
		}
		seen[term] = struct{}{}
		terms[index] = term
	}
	return strings.Join(terms, ","), nil
}
func validatedProposedChange(change ProposedChange) (ProposedChange, error) {
	if change.TaskID <= 0 {
		return ProposedChange{}, errors.New("task ID must be positive")
	}
	var err error
	if change.Category, err = normalizedIdentifier("proposed change category", change.Category); err != nil {
		return ProposedChange{}, err
	}
	if change.Key, err = normalizedIdentifier("proposed change key", change.Key); err != nil {
		return ProposedChange{}, err
	}
	if change.Value, err = requiredString("proposed change value", change.Value); err != nil {
		return ProposedChange{}, err
	}
	return change, nil
}

func requiredString(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	return value, nil
}

func normalizedIdentifier(name, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	if value != strings.TrimSpace(value) || value != strings.ToLower(value) {
		return "", fmt.Errorf("%s must be normalized", name)
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return "", fmt.Errorf("%s must be a normalized identifier", name)
		}
	}
	return value, nil
}

func validTaskPhase(phase TaskPhase) bool {
	switch phase {
	case TaskPhasePlanning, TaskPhaseExecution, TaskPhaseValidation, TaskPhaseDone:
		return true
	default:
		return false
	}
}

func transitionTarget(from TaskPhase, event TaskEventKind) (TaskPhase, bool) {
	switch event {
	case TaskEventApprovePlan:
		if from == TaskPhasePlanning {
			return TaskPhaseExecution, true
		}
	case TaskEventSubmitResult:
		if from == TaskPhaseExecution {
			return TaskPhaseValidation, true
		}
	case TaskEventValidationPassed:
		if from == TaskPhaseValidation {
			return TaskPhaseDone, true
		}
	case TaskEventValidationFailed:
		if from == TaskPhaseValidation {
			return TaskPhaseExecution, true
		}
	}
	return "", false
}

func validTaskEvent(from TaskPhase, event TaskEventKind, to TaskPhase) bool {
	target, ok := transitionTarget(from, event)
	return ok && target == to
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func scanBoolean(value int, name string) (bool, error) {
	switch value {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("%s must be 0 or 1", name)
	}
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
