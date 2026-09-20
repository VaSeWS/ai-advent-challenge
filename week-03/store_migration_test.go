package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

const (
	migrationTestTime    = "2026-01-02T03:04:05.000000006Z"
	v1MigrationAPICallID = 300
)

func TestFreshStoreCreatesSchemaVersion3(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	requireSchemaVersion(t, store.db, 3)
	for table, columns := range map[string][]string{
		"profiles":           {"id", "name", "language", "response_style", "response_format", "constraints", "created_at", "updated_at"},
		"active_profile":     {"id", "profile_id", "updated_at"},
		"tasks":              {"id", "branch_id", "goal", "plan", "phase", "current_step", "expected_action", "paused", "active", "created_at", "updated_at"},
		"long_term_memories": {"id", "kind", "key", "value", "profile_id", "task_id", "created_at", "updated_at"},
		"invariants":         {"id", "task_id", "category", "key", "required_value", "rule", "source", "active", "created_at", "updated_at", "forbidden"},
		"task_events":        {"id", "task_id", "from_phase", "event", "to_phase", "created_at"},
	} {
		requireTableColumns(t, store.db, table, columns)
	}
	requireForeignKey(t, store.db, "active_profile", "profiles")
	requireForeignKey(t, store.db, "tasks", "branches")
	requireForeignKey(t, store.db, "long_term_memories", "profiles")
	requireForeignKey(t, store.db, "long_term_memories", "tasks")
	requireForeignKey(t, store.db, "invariants", "tasks")
	requireForeignKey(t, store.db, "task_events", "tasks")
	requireNoForeignKeyViolations(t, store.db)
	requireObjectAbsent(t, store.db, "api_calls_v2")
	requireIndexTable(t, store.db, "api_calls_grouping_idx", "api_calls")

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load initial chat: %v", err)
	}
	profileID := insertMigrationProfile(t, store.db, "default")
	if _, err := store.db.Exec(`INSERT INTO active_profile(id, profile_id, updated_at) VALUES (1, ?, ?)`, profileID, migrationTestTime); err != nil {
		t.Fatalf("set active profile: %v", err)
	}
	requireExecFails(t, store.db, `INSERT INTO active_profile(id, profile_id, updated_at) VALUES (2, ?, ?)`, profileID, migrationTestTime)
	requireExecFails(t, store.db, `INSERT INTO profiles(name, language, response_style, response_format, constraints, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "default", "en", "brief", "text", "", migrationTestTime, migrationTestTime)

	taskID := insertMigrationTask(t, store.db, branch.ID)
	requireExecFails(t, store.db, `INSERT INTO tasks(branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at) VALUES (?, ?, ?, 'blocked', ?, ?, 0, 0, ?, ?)`, branch.ID, "goal", "plan", "step", "action", migrationTestTime, migrationTestTime)
	requireExecFails(t, store.db, `INSERT INTO tasks(branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at) VALUES (?, ?, ?, 'planning', ?, ?, 2, 0, ?, ?)`, branch.ID, "goal", "plan", "step", "action", migrationTestTime, migrationTestTime)
	requireExecFails(t, store.db, `INSERT INTO tasks(branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at) VALUES (?, ?, ?, 'planning', ?, ?, 0, 1, ?, ?)`, branch.ID, "other", "plan", "step", "action", migrationTestTime, migrationTestTime)

	if _, err := store.db.Exec(`INSERT INTO long_term_memories(kind, key, value, profile_id, created_at, updated_at) VALUES ('knowledge', 'language', 'English', ?, ?, ?)`, profileID, migrationTestTime, migrationTestTime); err != nil {
		t.Fatalf("insert profile memory: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO long_term_memories(kind, key, value, task_id, created_at, updated_at) VALUES ('decision', 'storage', 'SQLite', ?, ?, ?)`, taskID, migrationTestTime, migrationTestTime); err != nil {
		t.Fatalf("insert task memory: %v", err)
	}
	requireExecFails(t, store.db, `INSERT INTO long_term_memories(kind, key, value, profile_id, created_at, updated_at) VALUES ('preference', 'format', 'text', ?, ?, ?)`, profileID, migrationTestTime, migrationTestTime)
	requireExecFails(t, store.db, `INSERT INTO long_term_memories(kind, key, value, profile_id, task_id, created_at, updated_at) VALUES ('decision', 'scope', 'invalid', ?, ?, ?, ?)`, profileID, taskID, migrationTestTime, migrationTestTime)

	if _, err := store.db.Exec(`INSERT INTO invariants(task_id, category, key, required_value, rule, source, active, created_at, updated_at) VALUES (?, 'storage', 'database', 'SQLite', 'Use SQLite.', 'user', 1, ?, ?)`, taskID, migrationTestTime, migrationTestTime); err != nil {
		t.Fatalf("insert invariant: %v", err)
	}
	requireExecFails(t, store.db, `INSERT INTO invariants(task_id, category, key, required_value, rule, source, active, created_at, updated_at) VALUES (?, 'storage', 'database', 'PostgreSQL', 'Use PostgreSQL.', 'user', 1, ?, ?)`, taskID, migrationTestTime, migrationTestTime)
	requireExecFails(t, store.db, `INSERT INTO invariants(task_id, category, key, required_value, rule, source, active, created_at, updated_at) VALUES (?, 'storage', 'engine', 'SQLite', 'Use SQLite.', 'user', 2, ?, ?)`, taskID, migrationTestTime, migrationTestTime)

	result, err := store.db.Exec(`INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, 'planning', 'approve_plan', 'execution', ?)`, taskID, migrationTestTime)
	if err != nil {
		t.Fatalf("insert task event: %v", err)
	}
	eventID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read task event id: %v", err)
	}
	requireExecFails(t, store.db, `INSERT INTO task_events(task_id, from_phase, event, to_phase, created_at) VALUES (?, 'planning', 'submit_result', 'validation', ?)`, taskID, migrationTestTime)
	requireExecFails(t, store.db, `UPDATE task_events SET to_phase = 'done' WHERE id = ?`, eventID)
	requireExecFails(t, store.db, `DELETE FROM task_events WHERE id = ?`, eventID)

	insertMigrationAPICall(t, store.db, chat.ID, branch.ID, "memory")
	requireExecFails(t, store.db, migrationAPICallInsert, chat.ID, branch.ID, "test", "model", "unknown", "full", "counter", nil, nil, 1, 1, 1, 0, 1, 1, 2, "standard", 0.1, 0.2, migrationTestTime)
}

func TestStoreMigratesVersion1DataToVersion3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.sqlite")
	db := createV1Database(t, path)
	insertV1Fixture(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close v1 fixture: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("migrate v1 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	requireSchemaVersion(t, store.db, 3)
	requireNoForeignKeyViolations(t, store.db)
	requireObjectAbsent(t, store.db, "api_calls_v2")
	requireIndexTable(t, store.db, "api_calls_grouping_idx", "api_calls")
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load migrated active chat: %v", err)
	}
	if chat.ID != 7 || chat.Title != "v1 chat" || branch.ID != 11 || branch.Name != "main" {
		t.Fatalf("migrated active state = %#v / %#v, want v1 chat and branch", chat, branch)
	}
	lineage, err := store.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load migrated lineage: %v", err)
	}
	requireMessageSequence(t, lineage, []CompletionMessage{{Role: "user", Content: "v1 question"}, {Role: "assistant", Content: "v1 answer"}})
	summary, err := store.Summary(branch.ID)
	if err != nil {
		t.Fatalf("load migrated summary: %v", err)
	}
	if summary == nil || summary.ThroughMessageID != 102 || summary.Content != "v1 summary" {
		t.Fatalf("migrated summary = %#v, want v1 summary", summary)
	}
	facts, err := store.Facts(branch.ID)
	if err != nil {
		t.Fatalf("load migrated facts: %v", err)
	}
	if len(facts) != 1 || facts[0] != (Fact{Key: "topic", Value: "migration"}) {
		t.Fatalf("migrated facts = %#v, want v1 fact", facts)
	}

	requireV1MigrationAPICall(t, store.db)
	insertMigrationAPICall(t, store.db, chat.ID, branch.ID, "memory")
}

func TestStoreMigratesVersion2DataToVersion3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.sqlite")
	db := createV1Database(t, path)
	if err := executeSchemaStatementsForTest(db, schemaV2Statements()); err != nil {
		t.Fatalf("create v2 fixture: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatalf("set v2 schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v2 fixture: %v", err)
	}

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("migrate v2 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	requireSchemaVersion(t, store.db, 3)
	requireTableColumns(t, store.db, "invariants", []string{"id", "task_id", "category", "key", "required_value", "rule", "source", "active", "created_at", "updated_at", "forbidden"})
}

func TestStoreReopensVersion3Unchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open v3 store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close v3 store: %v", err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen v3 store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	requireSchemaVersion(t, reopened.db, 3)
}

func executeSchemaStatementsForTest(db *sql.DB, statements []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := executeSchemaStatements(tx, statements); err != nil {
		return err
	}
	return tx.Commit()
}

func TestStoreMigrationRollsBackOnV2Failure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed.sqlite")
	db := createV1Database(t, path)
	insertV1Fixture(t, db)
	if _, err := db.Exec(`CREATE VIEW api_calls_v2 AS SELECT 1 AS id`); err != nil {
		t.Fatalf("create conflicting replacement view: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close failed fixture: %v", err)
	}

	if _, err := OpenStore(path); err == nil {
		t.Fatal("migrate conflicting v1 database succeeded, want error")
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen failed fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	requireSchemaVersion(t, db, 1)
	requireObjectType(t, db, "api_calls_v2", "view")
	requireObjectAbsent(t, db, "profiles")
	requireObjectAbsent(t, db, "active_profile")
	requireObjectAbsent(t, db, "tasks")
	requireObjectAbsent(t, db, "long_term_memories")
	requireObjectAbsent(t, db, "invariants")
	requireObjectAbsent(t, db, "task_events")
	requireV1MigrationAPICall(t, db)
}

func TestStoreRejectsFutureSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("create future database: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 4`); err != nil {
		db.Close()
		t.Fatalf("set future version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future database: %v", err)
	}

	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("open future database error = %v, want newer-version rejection", err)
	}
}

func createV1Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v1 fixture: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		t.Fatalf("enable v1 fixture foreign keys: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		db.Close()
		t.Fatalf("begin v1 fixture schema: %v", err)
	}
	if err := executeSchemaStatements(tx, schemaV1Statements()); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("create v1 fixture schema: %v", err)
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatalf("commit v1 fixture schema: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		db.Close()
		t.Fatalf("set v1 fixture version: %v", err)
	}
	return db
}

func insertV1Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execMigrationSQL(t, db, `INSERT INTO chats(id, title, active_branch_id, created_at, updated_at) VALUES (7, 'v1 chat', NULL, ?, ?)`, migrationTestTime, migrationTestTime)
	execMigrationSQL(t, db, `INSERT INTO branches(id, chat_id, name, head_message_id, strategy, window_size, created_at, updated_at) VALUES (11, 7, 'main', NULL, 'full', 8, ?, ?)`, migrationTestTime, migrationTestTime)
	execMigrationSQL(t, db, `INSERT INTO messages(id, chat_id, previous_message_id, role, content, created_at) VALUES (101, 7, NULL, 'user', 'v1 question', ?)`, migrationTestTime)
	execMigrationSQL(t, db, `INSERT INTO messages(id, chat_id, previous_message_id, role, content, created_at) VALUES (102, 7, 101, 'assistant', 'v1 answer', ?)`, migrationTestTime)
	execMigrationSQL(t, db, `UPDATE chats SET active_branch_id = 11 WHERE id = 7`)
	execMigrationSQL(t, db, `UPDATE branches SET head_message_id = 102 WHERE id = 11`)
	execMigrationSQL(t, db, `INSERT INTO summaries(branch_id, through_message_id, content, updated_at) VALUES (11, 102, 'v1 summary', ?)`, migrationTestTime)
	execMigrationSQL(t, db, `INSERT INTO facts(branch_id, key, value, updated_at) VALUES (11, 'topic', 'migration', ?)`, migrationTestTime)
	execMigrationSQL(t, db, v1MigrationAPICallInsert, v1MigrationAPICallID, 7, 11, "groq", "model-v1", "main", "full", "counter", 8, 8, 8, 5, 13, 0, 13, 8, 21, "standard", 0.01, 0.02, migrationTestTime)
}

func insertMigrationProfile(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO profiles(name, language, response_style, response_format, constraints, created_at, updated_at) VALUES (?, 'en', 'brief', 'text', '', ?, ?)`, name, migrationTestTime, migrationTestTime)
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read profile id: %v", err)
	}
	return id
}

func insertMigrationTask(t *testing.T, db *sql.DB, branchID int64) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO tasks(branch_id, goal, plan, phase, current_step, expected_action, paused, active, created_at, updated_at) VALUES (?, 'finish migration', 'write schema', 'planning', 'migration', 'review', 0, 1, ?, ?)`, branchID, migrationTestTime, migrationTestTime)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read task id: %v", err)
	}
	return id
}

const migrationAPICallInsert = `INSERT INTO api_calls(chat_id, branch_id, provider, model, kind, strategy, counter_label, current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local, prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api, completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd, output_cost_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const v1MigrationAPICallInsert = `INSERT INTO api_calls(id, chat_id, branch_id, provider, model, kind, strategy, counter_label, current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local, prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api, completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd, output_cost_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func requireV1MigrationAPICall(t *testing.T, db *sql.DB) {
	t.Helper()
	var id, chatID, branchID int64
	var provider, model, kind, strategy, counterLabel, pricingTier, createdAt string
	var currentTokens, fullHistoryTokens, sentTokens, responseTokens, promptTokens, cachedPromptTokens, uncachedPromptTokens, completionTokens, totalTokens int
	var inputCost, outputCost float64
	if err := db.QueryRow(`SELECT id, chat_id, branch_id, provider, model, kind, strategy, counter_label, current_tokens, full_history_tokens, sent_tokens_local, response_tokens_local, prompt_tokens_api, cached_prompt_tokens_api, uncached_prompt_tokens_api, completion_tokens_api, total_tokens_api, pricing_tier, input_cost_usd, output_cost_usd, created_at FROM api_calls WHERE id = ?`, v1MigrationAPICallID).Scan(&id, &chatID, &branchID, &provider, &model, &kind, &strategy, &counterLabel, &currentTokens, &fullHistoryTokens, &sentTokens, &responseTokens, &promptTokens, &cachedPromptTokens, &uncachedPromptTokens, &completionTokens, &totalTokens, &pricingTier, &inputCost, &outputCost, &createdAt); err != nil {
		t.Fatalf("load v1 api call: %v", err)
	}
	if id != v1MigrationAPICallID || chatID != 7 || branchID != 11 || provider != "groq" || model != "model-v1" || kind != "main" || strategy != "full" || counterLabel != "counter" || currentTokens != 8 || fullHistoryTokens != 8 || sentTokens != 8 || responseTokens != 5 || promptTokens != 13 || cachedPromptTokens != 0 || uncachedPromptTokens != 13 || completionTokens != 8 || totalTokens != 21 || pricingTier != "standard" || inputCost != 0.01 || outputCost != 0.02 || createdAt != migrationTestTime {
		t.Fatal("v1 api call does not faithfully retain all fields")
	}
}

func insertMigrationAPICall(t *testing.T, db *sql.DB, chatID, branchID int64, kind string) {
	t.Helper()
	execMigrationSQL(t, db, migrationAPICallInsert, chatID, branchID, "test", "model", kind, "full", "counter", nil, nil, 1, 1, 1, 0, 1, 1, 2, "standard", 0.1, 0.2, migrationTestTime)
}

func execMigrationSQL(t *testing.T, db *sql.DB, statement string, arguments ...any) {
	t.Helper()
	if _, err := db.Exec(statement, arguments...); err != nil {
		t.Fatalf("execute migration fixture SQL: %v", err)
	}
}

func requireSchemaVersion(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}

func requireTableColumns(t *testing.T, db *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	got := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan %s column: %v", table, err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	for _, column := range want {
		if !got[column] {
			t.Fatalf("%s is missing column %q", table, column)
		}
	}
}

func requireForeignKey(t *testing.T, db *sql.DB, table, referencedTable string) {
	t.Helper()
	rows, err := db.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		t.Fatalf("read %s foreign keys: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			t.Fatalf("scan %s foreign key: %v", table, err)
		}
		if got == referencedTable {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s foreign keys: %v", table, err)
	}
	t.Fatalf("%s is missing foreign key to %s", table, referencedTable)
}

func requireExecFails(t *testing.T, db *sql.DB, statement string, arguments ...any) {
	t.Helper()
	if _, err := db.Exec(statement, arguments...); err == nil {
		t.Fatalf("SQL succeeded, want constraint failure: %s", statement)
	}
}

func requireObjectType(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT type FROM sqlite_master WHERE name = ?`, name).Scan(&got); err != nil {
		t.Fatalf("read %s object: %v", name, err)
	}
	if got != want {
		t.Fatalf("%s type = %q, want %q", name, got, want)
	}
}

func requireObjectAbsent(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("check %s absence: %v", name, err)
	}
	if count != 0 {
		t.Fatalf("%s remains after rollback", name)
	}
}

func requireNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign key check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key check reported a violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign key check: %v", err)
	}
}

func requireIndexTable(t *testing.T, db *sql.DB, index, table string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&got); err != nil {
		t.Fatalf("read %s index: %v", index, err)
	}
	if got != table {
		t.Fatalf("%s belongs to %s, want %s", index, got, table)
	}
}
