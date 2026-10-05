package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const (
	strategyFixed      = "fixed"
	strategyStructural = "structural"
)

// Store persists corpus snapshots and isolated sqlite-vec index builds.
type Store struct {
	DB *sql.DB
}

// IndexBuild identifies one complete, reproducible index build.
type IndexBuild struct {
	ID          string
	SnapshotID  string
	Fingerprint EmbeddingFingerprint
	Config      ChunkConfig
}

// Candidate pairs a retrieved chunk with its cosine distance.
type Candidate struct {
	Chunk    Chunk
	Distance float64
}

// OpenStore opens and initializes the local SQLite database.
func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path must not be empty")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// A single connection keeps in-memory databases, vec virtual tables, and
	// transaction behavior consistent across the database/sql pool.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect sqlite database: %w", err)
	}
	if err := initStoreSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Close releases the underlying database connection pool.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func initStoreSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL,
    content_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS documents (
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
    id TEXT NOT NULL,
    title TEXT NOT NULL,
    url TEXT NOT NULL,
    permalink TEXT NOT NULL,
    revision_timestamp TEXT NOT NULL,
    retrieved_at TEXT NOT NULL,
    hash TEXT NOT NULL,
    license TEXT NOT NULL,
    attribution TEXT NOT NULL,
    page_id INTEGER NOT NULL,
    revision_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    warnings_json TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, id)
);
CREATE TABLE IF NOT EXISTS sections (
    snapshot_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    id TEXT NOT NULL,
    path TEXT NOT NULL,
    start_word INTEGER NOT NULL,
    end_word INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, document_id, id),
    FOREIGN KEY (snapshot_id, document_id) REFERENCES documents(snapshot_id, id)
);
CREATE TABLE IF NOT EXISTS index_builds (
    id TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
    fingerprint_model TEXT NOT NULL,
    fingerprint_digest TEXT NOT NULL,
    fingerprint_preparation TEXT NOT NULL,
    fingerprint_dimensions INTEGER NOT NULL,
    config_size INTEGER NOT NULL,
    config_overlap INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS active_index (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    build_id TEXT NOT NULL REFERENCES index_builds(id)
);
CREATE TABLE IF NOT EXISTS chunks (
    row_id INTEGER PRIMARY KEY,
    build_id TEXT NOT NULL REFERENCES index_builds(id),
    strategy TEXT NOT NULL,
    chunk_id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    title TEXT NOT NULL,
    permalink TEXT NOT NULL,
    revision_timestamp TEXT NOT NULL,
    revision_id INTEGER NOT NULL,
    section_paths_json TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    start_word INTEGER NOT NULL,
    end_word INTEGER NOT NULL,
    text TEXT NOT NULL,
    UNIQUE (build_id, strategy, chunk_id)
);
CREATE INDEX IF NOT EXISTS chunks_by_build_strategy ON chunks(build_id, strategy, ordinal);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("initialize store schema: %w", err)
	}
	return nil
}

// BuildIndex embeds both strategies and atomically activates the complete build.
// A failed embedding or database write leaves the previous active build intact.
func (s *Store) BuildIndex(ctx context.Context, snapshot Snapshot, config ChunkConfig, fingerprint EmbeddingFingerprint, embed func(context.Context, []string) ([][]float32, error)) (string, error) {
	if s == nil || s.DB == nil {
		return "", errors.New("store is not open")
	}
	if strings.TrimSpace(snapshot.ID) == "" {
		return "", errors.New("snapshot ID must not be empty")
	}
	if fingerprint.Dimensions <= 0 {
		return "", errors.New("embedding dimensions must be positive")
	}
	if embed == nil {
		return "", errors.New("embedding function is required")
	}
	if config.Size <= 0 || config.Overlap < 0 || config.Overlap >= config.Size {
		return "", errors.New("invalid chunk configuration")
	}

	allChunks := make(map[string][]Chunk, 2)
	allTexts := make([]string, 0)
	for _, strategy := range []string{strategyFixed, strategyStructural} {
		chunks, err := MakeChunks(snapshot, strategy, config)
		if err != nil {
			return "", fmt.Errorf("make %s chunks: %w", strategy, err)
		}
		allChunks[strategy] = chunks
		for _, chunk := range chunks {
			allTexts = append(allTexts, chunk.Text)
		}
	}
	vectors, err := embed(ctx, allTexts)
	if err != nil {
		return "", fmt.Errorf("embed index chunks: %w", err)
	}
	if len(vectors) != len(allTexts) {
		return "", fmt.Errorf("embedding count mismatch: got %d vectors for %d chunks", len(vectors), len(allTexts))
	}
	for i, vector := range vectors {
		if err := validateVector(vector, fingerprint.Dimensions); err != nil {
			return "", fmt.Errorf("embedding %d: %w", i, err)
		}
	}

	buildID, err := newBuildID()
	if err != nil {
		return "", fmt.Errorf("create index build ID: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin index build: %w", err)
	}
	defer tx.Rollback()
	if err := persistSnapshot(ctx, tx, snapshot); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO index_builds
(id, snapshot_id, fingerprint_model, fingerprint_digest, fingerprint_preparation, fingerprint_dimensions, config_size, config_overlap, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, buildID, snapshot.ID, fingerprint.Model, fingerprint.Digest, fingerprint.Preparation, fingerprint.Dimensions, config.Size, config.Overlap); err != nil {
		return "", fmt.Errorf("insert index build: %w", err)
	}

	vectorIndex := 0
	for _, strategy := range []string{strategyFixed, strategyStructural} {
		table := vectorTableName(buildID, strategy)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE VIRTUAL TABLE %s USING vec0(embedding float[%d] distance_metric=cosine)", table, fingerprint.Dimensions)); err != nil {
			return "", fmt.Errorf("create %s vector table: %w", strategy, err)
		}
		for _, chunk := range allChunks[strategy] {
			paths, err := json.Marshal(chunk.SectionPaths)
			if err != nil {
				return "", fmt.Errorf("encode chunk section paths: %w", err)
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO chunks
(build_id, strategy, chunk_id, snapshot_id, document_id, title, permalink, revision_timestamp, revision_id, section_paths_json, ordinal, start_word, end_word, text)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, buildID, strategy, chunk.ID, chunk.SnapshotID, chunk.DocumentID, chunk.Title, chunk.Permalink, chunk.RevisionTimestamp, chunk.RevisionID, string(paths), chunk.Ordinal, chunk.Start, chunk.End, chunk.Text)
			if err != nil {
				return "", fmt.Errorf("insert %s chunk %q: %w", strategy, chunk.ID, err)
			}
			rowID, err := result.LastInsertId()
			if err != nil {
				return "", fmt.Errorf("read chunk row ID: %w", err)
			}
			blob := encodeFloat32(vectors[vectorIndex])
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(rowid, embedding) VALUES (?, ?)", table), rowID, blob); err != nil {
				return "", fmt.Errorf("insert %s vector: %w", strategy, err)
			}
			vectorIndex++
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_index(singleton, build_id) VALUES (1, ?) ON CONFLICT(singleton) DO UPDATE SET build_id = excluded.build_id`, buildID); err != nil {
		return "", fmt.Errorf("activate index build: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit index build: %w", err)
	}
	return buildID, nil
}

func persistSnapshot(ctx context.Context, tx *sql.Tx, snapshot Snapshot) error {
	contentHash, err := canonicalSnapshotHash(snapshot)
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM snapshots WHERE id = ?)`, snapshot.ID).Scan(&exists); err != nil {
		return fmt.Errorf("check stored snapshot: %w", err)
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(id, created_at, content_hash) VALUES (?, ?, ?)`, snapshot.ID, snapshot.CreatedAt, contentHash); err != nil {
			return fmt.Errorf("persist snapshot: %w", err)
		}
	} else {
		persistedHash, err := persistedSnapshotHash(ctx, tx, snapshot.ID)
		if err != nil {
			return err
		}
		if persistedHash != contentHash {
			return fmt.Errorf("snapshot %q already exists with different content", snapshot.ID)
		}
		// Older records hashed the complete JSON payload, including acquisition
		// timestamps and document order. Once the persisted corpus itself has
		// been validated, migrate only its hash; retain the original capture time.
		if _, err := tx.ExecContext(ctx, `UPDATE snapshots SET content_hash = ? WHERE id = ?`, contentHash, snapshot.ID); err != nil {
			return fmt.Errorf("migrate snapshot content hash: %w", err)
		}
		return nil
	}

	for _, document := range snapshot.Documents {
		warnings, err := json.Marshal(document.Warnings)
		if err != nil {
			return fmt.Errorf("encode warnings for document %q: %w", document.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO documents
(snapshot_id, id, title, url, permalink, revision_timestamp, retrieved_at, hash, license, attribution, page_id, revision_id, text, warnings_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID, document.ID, document.Title, document.URL, document.Permalink, document.RevisionTimestamp, document.RetrievedAt, document.Hash, document.License, document.Attribution, document.PageID, document.RevisionID, document.Text, string(warnings)); err != nil {
			return fmt.Errorf("persist document %q: %w", document.ID, err)
		}
		for _, section := range document.Sections {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sections(snapshot_id, document_id, id, path, start_word, end_word) VALUES (?, ?, ?, ?, ?, ?)`, snapshot.ID, document.ID, section.ID, section.Path, section.Start, section.End); err != nil {
				return fmt.Errorf("persist section %q: %w", section.ID, err)
			}
		}
	}
	return nil
}

func canonicalSnapshotHash(snapshot Snapshot) (string, error) {
	canonical := Snapshot{ID: snapshot.ID, Documents: append([]Document(nil), snapshot.Documents...)}
	sort.Slice(canonical.Documents, func(i, j int) bool {
		return canonical.Documents[i].ID < canonical.Documents[j].ID
	})
	for i := range canonical.Documents {
		document := &canonical.Documents[i]
		document.RetrievedAt = ""
		document.Sections = append([]Section(nil), document.Sections...)
		if len(document.Sections) == 0 {
			document.Sections = nil
		}
		sort.Slice(document.Sections, func(i, j int) bool {
			return document.Sections[i].ID < document.Sections[j].ID
		})
	}
	serialized, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(serialized)
	return hex.EncodeToString(digest[:]), nil
}

func persistedSnapshotHash(ctx context.Context, tx *sql.Tx, snapshotID string) (string, error) {
	persisted := Snapshot{ID: snapshotID}
	rows, err := tx.QueryContext(ctx, `SELECT id, title, url, permalink, revision_timestamp, retrieved_at, hash, license, attribution, page_id, revision_id, text, warnings_json FROM documents WHERE snapshot_id = ? ORDER BY id`, snapshotID)
	if err != nil {
		return "", fmt.Errorf("read stored snapshot documents: %w", err)
	}
	for rows.Next() {
		var document Document
		var warnings string
		if err := rows.Scan(&document.ID, &document.Title, &document.URL, &document.Permalink, &document.RevisionTimestamp, &document.RetrievedAt, &document.Hash, &document.License, &document.Attribution, &document.PageID, &document.RevisionID, &document.Text, &warnings); err != nil {
			rows.Close()
			return "", fmt.Errorf("read stored snapshot document: %w", err)
		}
		if err := json.Unmarshal([]byte(warnings), &document.Warnings); err != nil {
			rows.Close()
			return "", fmt.Errorf("decode stored warnings for document %q: %w", document.ID, err)
		}
		persisted.Documents = append(persisted.Documents, document)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", fmt.Errorf("read stored snapshot documents: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("close stored snapshot documents: %w", err)
	}
	for i := range persisted.Documents {
		document := &persisted.Documents[i]
		sectionRows, err := tx.QueryContext(ctx, `SELECT id, path, start_word, end_word FROM sections WHERE snapshot_id = ? AND document_id = ? ORDER BY id`, snapshotID, document.ID)
		if err != nil {
			return "", fmt.Errorf("read stored sections for document %q: %w", document.ID, err)
		}
		for sectionRows.Next() {
			var section Section
			if err := sectionRows.Scan(&section.ID, &section.Path, &section.Start, &section.End); err != nil {
				sectionRows.Close()
				return "", fmt.Errorf("read stored section for document %q: %w", document.ID, err)
			}
			document.Sections = append(document.Sections, section)
		}
		if err := sectionRows.Err(); err != nil {
			sectionRows.Close()
			return "", fmt.Errorf("read stored sections for document %q: %w", document.ID, err)
		}
		if err := sectionRows.Close(); err != nil {
			return "", fmt.Errorf("close stored sections for document %q: %w", document.ID, err)
		}
	}
	if len(persisted.Documents) == 0 {
		return "", fmt.Errorf("snapshot %q exists without persisted documents", snapshotID)
	}
	contentHash, err := canonicalSnapshotHash(persisted)
	if err != nil {
		return "", fmt.Errorf("encode stored snapshot: %w", err)
	}
	return contentHash, nil
}

// ActiveBuild returns the currently activated complete build.
func (s *Store) ActiveBuild(ctx context.Context) (IndexBuild, error) {
	var build IndexBuild
	err := s.DB.QueryRowContext(ctx, `SELECT b.id, b.snapshot_id, b.fingerprint_model, b.fingerprint_digest, b.fingerprint_preparation, b.fingerprint_dimensions, b.config_size, b.config_overlap
FROM active_index a JOIN index_builds b ON b.id = a.build_id WHERE a.singleton = 1`).Scan(&build.ID, &build.SnapshotID, &build.Fingerprint.Model, &build.Fingerprint.Digest, &build.Fingerprint.Preparation, &build.Fingerprint.Dimensions, &build.Config.Size, &build.Config.Overlap)
	if err != nil {
		return IndexBuild{}, fmt.Errorf("read active index build: %w", err)
	}
	return build, nil
}

// Search returns the nearest chunks from one build and strategy, ordered by cosine distance.
func (s *Store) Search(ctx context.Context, buildID, strategy string, query []float32, k int) ([]Candidate, error) {
	if k <= 0 {
		return nil, errors.New("search limit must be positive")
	}
	if !validStrategy(strategy) {
		return nil, fmt.Errorf("unknown chunk strategy %q", strategy)
	}
	build, err := s.loadBuild(ctx, buildID)
	if err != nil {
		return nil, err
	}
	if err := validateVector(query, build.Fingerprint.Dimensions); err != nil {
		return nil, fmt.Errorf("query embedding: %w", err)
	}
	table := vectorTableName(buildID, strategy)
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(`SELECT c.chunk_id, c.snapshot_id, c.document_id, c.title, c.permalink, c.revision_timestamp, c.revision_id,
 c.section_paths_json, c.ordinal, c.start_word, c.end_word, c.text, v.distance
FROM %s v JOIN chunks c ON c.row_id = v.rowid
WHERE v.embedding MATCH ? AND v.k = ? AND c.build_id = ? AND c.strategy = ?
ORDER BY v.distance`, table), encodeFloat32(query), k, buildID, strategy)
	if err != nil {
		return nil, fmt.Errorf("search %s index: %w", strategy, err)
	}
	defer rows.Close()
	candidates := make([]Candidate, 0, k)
	for rows.Next() {
		var candidate Candidate
		var paths string
		chunk := &candidate.Chunk
		if err := rows.Scan(&chunk.ID, &chunk.SnapshotID, &chunk.DocumentID, &chunk.Title, &chunk.Permalink, &chunk.RevisionTimestamp, &chunk.RevisionID, &paths, &chunk.Ordinal, &chunk.Start, &chunk.End, &chunk.Text, &candidate.Distance); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		chunk.Strategy = strategy
		if err := json.Unmarshal([]byte(paths), &chunk.SectionPaths); err != nil {
			return nil, fmt.Errorf("decode result section paths: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate search results: %w", err)
	}
	return candidates, nil
}

// InspectChunks returns every persisted chunk for one build and strategy.
func (s *Store) InspectChunks(ctx context.Context, buildID, strategy string) ([]Chunk, error) {
	if !validStrategy(strategy) {
		return nil, fmt.Errorf("unknown chunk strategy %q", strategy)
	}
	if _, err := s.loadBuild(ctx, buildID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT chunk_id, snapshot_id, document_id, title, permalink, revision_timestamp, revision_id, section_paths_json, ordinal, start_word, end_word, text
FROM chunks WHERE build_id = ? AND strategy = ? ORDER BY ordinal`, buildID, strategy)
	if err != nil {
		return nil, fmt.Errorf("inspect %s chunks: %w", strategy, err)
	}
	defer rows.Close()
	chunks := make([]Chunk, 0)
	for rows.Next() {
		var chunk Chunk
		var paths string
		if err := rows.Scan(&chunk.ID, &chunk.SnapshotID, &chunk.DocumentID, &chunk.Title, &chunk.Permalink, &chunk.RevisionTimestamp, &chunk.RevisionID, &paths, &chunk.Ordinal, &chunk.Start, &chunk.End, &chunk.Text); err != nil {
			return nil, fmt.Errorf("scan inspected chunk: %w", err)
		}
		chunk.Strategy = strategy
		if err := json.Unmarshal([]byte(paths), &chunk.SectionPaths); err != nil {
			return nil, fmt.Errorf("decode inspected chunk section paths: %w", err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inspected chunks: %w", err)
	}
	return chunks, nil
}

func (s *Store) loadBuild(ctx context.Context, id string) (IndexBuild, error) {
	var build IndexBuild
	err := s.DB.QueryRowContext(ctx, `SELECT id, snapshot_id, fingerprint_model, fingerprint_digest, fingerprint_preparation, fingerprint_dimensions, config_size, config_overlap FROM index_builds WHERE id = ?`, id).Scan(&build.ID, &build.SnapshotID, &build.Fingerprint.Model, &build.Fingerprint.Digest, &build.Fingerprint.Preparation, &build.Fingerprint.Dimensions, &build.Config.Size, &build.Config.Overlap)
	if err != nil {
		return IndexBuild{}, fmt.Errorf("load index build %q: %w", id, err)
	}
	return build, nil
}

func validStrategy(strategy string) bool {
	return strategy == strategyFixed || strategy == strategyStructural
}

func vectorTableName(buildID, strategy string) string {
	digest := sha256.Sum256([]byte(buildID + "\x00" + strategy))
	return "vec_index_" + hex.EncodeToString(digest[:16])
}

func encodeFloat32(vector []float32) []byte {
	encoded := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(encoded[i*4:], math.Float32bits(value))
	}
	return encoded
}

func validateVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf("got %d dimensions, want %d", len(vector), dimensions)
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("vector contains a non-finite value")
		}
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return errors.New("vector must not be zero")
	}
	return nil
}

func newBuildID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}
