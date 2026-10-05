package main

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreVecVersionAndPersistentStrategySearch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var vecVersion string
	if err := store.DB.QueryRowContext(ctx, `SELECT vec_version()`).Scan(&vecVersion); err != nil {
		t.Fatalf("read vec_version: %v", err)
	}
	if vecVersion == "" {
		t.Fatal("vec_version() returned an empty value")
	}

	snapshot := testStoreSnapshot()
	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	buildID, err := store.BuildIndex(ctx, snapshot, ChunkConfig{Size: 2}, fingerprint, testEmbeddings)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := store.Search(ctx, buildID, strategyFixed, []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("search fixed chunks: %v", err)
	}
	structural, err := store.Search(ctx, buildID, strategyStructural, []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("search structural chunks: %v", err)
	}
	if len(fixed) == 0 || len(structural) == 0 {
		t.Fatalf("expected results for both strategies, fixed=%d structural=%d", len(fixed), len(structural))
	}
	for _, candidate := range fixed {
		if candidate.Chunk.Strategy != strategyFixed {
			t.Fatalf("fixed search leaked strategy %q", candidate.Chunk.Strategy)
		}
	}
	for _, candidate := range structural {
		if candidate.Chunk.Strategy != strategyStructural {
			t.Fatalf("structural search leaked strategy %q", candidate.Chunk.Strategy)
		}
	}
	if fixed[0].Distance > 0.001 || structural[0].Distance > 0.001 {
		t.Fatalf("expected cosine nearest vectors first, distances fixed=%f structural=%f", fixed[0].Distance, structural[0].Distance)
	}
	if len(fixed) > 1 && fixed[0].Distance > fixed[1].Distance {
		t.Fatalf("fixed results not ordered by distance: %#v", fixed)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer store.Close()
	persisted, err := store.Search(ctx, buildID, strategyFixed, []float32{1, 0}, 2)
	if err != nil {
		t.Fatalf("search after reopen: %v", err)
	}
	if len(persisted) != len(fixed) || persisted[0].Chunk.ID != fixed[0].Chunk.ID {
		t.Fatalf("reopened results differ: before=%#v after=%#v", fixed, persisted)
	}
	active, err := store.ActiveBuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != buildID || active.Fingerprint != fingerprint || active.Config.Size != 2 {
		t.Fatalf("reopened active metadata mismatch: %#v", active)
	}
}

func TestStoreBuildsPreservePreviousSearch(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "rebuild.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	oldSnapshot := testStoreSnapshot()
	oldID, err := store.BuildIndex(ctx, oldSnapshot, ChunkConfig{Size: 2}, fingerprint, testEmbeddings)
	if err != nil {
		t.Fatal(err)
	}

	newSnapshot := testStoreSnapshot()
	newSnapshot.ID = "snapshot-next"
	newSnapshot.Documents[0].Text = "green green red red"
	newSnapshot.Documents[0].Sections = []Section{
		{ID: "green", Path: "Green", Start: 0, End: 2},
		{ID: "red", Path: "Red", Start: 2, End: 4},
	}
	newID, err := store.BuildIndex(ctx, newSnapshot, ChunkConfig{Size: 2}, fingerprint, testEmbeddings)
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatalf("second build reused build ID %q", oldID)
	}
	active, err := store.ActiveBuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != newID {
		t.Fatalf("active build = %q, want second build %q", active.ID, newID)
	}
	oldResults, err := store.Search(ctx, oldID, strategyFixed, []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("search previous build after rebuild: %v", err)
	}
	if len(oldResults) != 1 || oldResults[0].Chunk.SnapshotID != oldSnapshot.ID {
		t.Fatalf("previous build search was not preserved: %#v", oldResults)
	}
}

func TestStoreStrategyTopKIsSelectedIndependently(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "strategy-isolation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot := testStoreSnapshot()
	snapshot.Documents[0].Text = "red blue green gold"
	snapshot.Documents[0].Sections = []Section{
		{ID: "first", Path: "First", Start: 0, End: 2},
		{ID: "second", Path: "Second", Start: 2, End: 4},
	}
	embed := func(_ context.Context, texts []string) ([][]float32, error) {
		vectors := make([][]float32, len(texts))
		for i, text := range texts {
			switch text {
			case "red blue green gold":
				vectors[i] = []float32{0, 1}
			case "red blue":
				vectors[i] = []float32{1, 0}
			case "green gold":
				vectors[i] = []float32{-1, -1}
			default:
				vectors[i] = []float32{-1, -1}
			}
		}
		return vectors, nil
	}
	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	buildID, err := store.BuildIndex(ctx, snapshot, ChunkConfig{Size: 4}, fingerprint, embed)
	if err != nil {
		t.Fatal(err)
	}

	fixed, err := store.Search(ctx, buildID, strategyFixed, []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("search fixed strategy: %v", err)
	}
	structural, err := store.Search(ctx, buildID, strategyStructural, []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("search structural strategy: %v", err)
	}
	if len(fixed) != 1 || fixed[0].Chunk.Strategy != strategyFixed || fixed[0].Distance < 0.99 {
		t.Fatalf("fixed top-K should contain its own less-similar chunk, got %#v", fixed)
	}
	if len(structural) != 1 || structural[0].Chunk.Strategy != strategyStructural ||
		structural[0].Chunk.Text != "red blue" || structural[0].Distance > 0.001 {
		t.Fatalf("structural top-K should contain its closer chunk, got %#v", structural)
	}
	fixedCloser, err := store.Search(ctx, buildID, strategyFixed, []float32{0, 1}, 1)
	if err != nil {
		t.Fatalf("search fixed strategy for its closer vector: %v", err)
	}
	structuralDespiteCloserFixed, err := store.Search(ctx, buildID, strategyStructural, []float32{0, 1}, 1)
	if err != nil {
		t.Fatalf("search structural strategy despite closer fixed vector: %v", err)
	}
	if len(fixedCloser) != 1 || fixedCloser[0].Distance > 0.001 {
		t.Fatalf("fixed search did not select its own closest vector: %#v", fixedCloser)
	}
	if len(structuralDespiteCloserFixed) != 1 ||
		structuralDespiteCloserFixed[0].Chunk.Strategy != strategyStructural ||
		structuralDespiteCloserFixed[0].Chunk.Text != "red blue" ||
		structuralDespiteCloserFixed[0].Distance < 0.99 ||
		structuralDespiteCloserFixed[0].Distance > 1.001 {
		t.Fatalf("structural search should select its uniquely closest less-similar chunk despite fixed's closer result: %#v", structuralDespiteCloserFixed)
	}
}

func TestStoreDatabaseWriteFailureRollsBackBuild(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "write-failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	oldSnapshot := testStoreSnapshot()
	oldID, err := store.BuildIndex(ctx, oldSnapshot, ChunkConfig{Size: 2}, fingerprint, testEmbeddings)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.DB.ExecContext(ctx, `CREATE TRIGGER reject_structural_chunks
BEFORE INSERT ON chunks WHEN NEW.strategy = 'structural'
BEGIN SELECT RAISE(ABORT, 'injected structural chunk write failure'); END`); err != nil {
		t.Fatalf("install write-failure trigger: %v", err)
	}
	newSnapshot := testStoreSnapshot()
	newSnapshot.ID = "snapshot-write-failure"
	newSnapshot.Documents[0].Text = "green green red red"
	newSnapshot.Documents[0].Sections = []Section{
		{ID: "green", Path: "Green", Start: 0, End: 2},
		{ID: "red", Path: "Red", Start: 2, End: 4},
	}
	if _, err := store.BuildIndex(ctx, newSnapshot, ChunkConfig{Size: 2}, fingerprint, testEmbeddings); err == nil {
		t.Fatal("expected database write failure")
	}

	active, err := store.ActiveBuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != oldID {
		t.Fatalf("failed database write replaced active build: got %q, want %q", active.ID, oldID)
	}
	for _, check := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "snapshot", query: `SELECT COUNT(*) FROM snapshots WHERE id = ?`, args: []any{newSnapshot.ID}},
		{name: "build", query: `SELECT COUNT(*) FROM index_builds WHERE snapshot_id = ?`, args: []any{newSnapshot.ID}},
		{name: "chunks", query: `SELECT COUNT(*) FROM chunks WHERE snapshot_id = ?`, args: []any{newSnapshot.ID}},
	} {
		var count int
		if err := store.DB.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			t.Fatalf("count rolled-back %s rows: %v", check.name, err)
		}
		if count != 0 {
			t.Errorf("failed build left %d %s rows", count, check.name)
		}
	}
	oldResults, err := store.Search(ctx, oldID, strategyFixed, []float32{1, 0}, 1)
	if err != nil {
		t.Fatalf("search previous build after failed write: %v", err)
	}
	if len(oldResults) != 1 || oldResults[0].Chunk.SnapshotID != oldSnapshot.ID {
		t.Fatalf("previous build search was not preserved after failed write: %#v", oldResults)
	}
}

func TestStoreBuildFailurePreservesActiveBuild(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	activeID, err := store.BuildIndex(ctx, testStoreSnapshot(), ChunkConfig{Size: 2}, fingerprint, testEmbeddings)
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot := testStoreSnapshot()
	newSnapshot.ID = "snapshot-next"
	_, err = store.BuildIndex(ctx, newSnapshot, ChunkConfig{Size: 2}, fingerprint, func(context.Context, []string) ([][]float32, error) {
		return nil, errors.New("injected embedding failure")
	})
	if err == nil || !strings.Contains(err.Error(), "injected embedding failure") {
		t.Fatalf("expected embedding failure, got %v", err)
	}
	active, err := store.ActiveBuild(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != activeID {
		t.Fatalf("failed build replaced active build: got %q, want %q", active.ID, activeID)
	}
	if _, err := store.loadBuild(ctx, ""); err == nil {
		t.Fatal("unexpected empty build lookup success")
	}
}

func TestStoreRejectsInvalidVectorDimensionsAndValues(t *testing.T) {
	for _, test := range []struct {
		name string
		vec  []float32
	}{
		{name: "wrong dimensions", vec: []float32{1}},
		{name: "non-finite", vec: []float32{float32(math.Inf(1)), 0}},
		{name: "zero", vec: []float32{0, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateVector(test.vec, 2); err == nil {
				t.Fatalf("validateVector(%v) unexpectedly succeeded", test.vec)
			}
		})
	}
}

func TestStoreReusesSnapshotAcrossAcquisitionsAndRejectsChangedContent(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "snapshot-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := testStoreSnapshot()
	first.Documents[0].ID = "doc-z"
	first.Documents[0].Title = "Zulu"
	first.Documents[0].Hash = "hash-z"
	first.Documents[0].RetrievedAt = "2026-10-04T00:00:00Z"
	first.Documents[0].Sections = []Section{{ID: "section-z", Path: "Zulu", Start: 0, End: 6}}
	second := first.Documents[0]
	second.ID = "doc-a"
	second.Title = "Alpha"
	second.URL = "https://example.test/alpha"
	second.Permalink = "https://example.test/alpha?oldid=43"
	second.RevisionTimestamp = "2026-10-02T00:00:00Z"
	second.Hash = "hash-a"
	second.PageID = 2
	second.RevisionID = 43
	second.Text = "alpha alpha beta beta gamma gamma"
	second.Sections = []Section{{ID: "section-a", Path: "Alpha", Start: 0, End: 6}}
	first.Documents = append(first.Documents, second)
	first.ID = snapshotID(first.Documents)
	first.CreatedAt = "2026-10-04T00:00:00Z"

	fingerprint := EmbeddingFingerprint{Model: "test", Digest: "digest", Preparation: "raw", Dimensions: 2}
	if _, err := store.BuildIndex(ctx, first, ChunkConfig{Size: 2}, fingerprint, testEmbeddings); err != nil {
		t.Fatalf("build initial snapshot: %v", err)
	}

	// Simulate a database written by the previous implementation, which stored
	// the full acquired snapshot payload hash.
	if _, err := store.DB.ExecContext(ctx, `UPDATE snapshots SET content_hash = 'legacy-full-payload-hash' WHERE id = ?`, first.ID); err != nil {
		t.Fatalf("set legacy content hash: %v", err)
	}

	refetched := first
	refetched.Documents = append([]Document(nil), first.Documents...)
	refetched.Documents[0].RetrievedAt = "2026-10-05T12:30:00Z"
	refetched.Documents[1].RetrievedAt = "2026-10-05T12:30:00Z"
	refetched.Documents[0], refetched.Documents[1] = refetched.Documents[1], refetched.Documents[0]
	refetched.CreatedAt = "2026-10-05T12:30:00Z"
	if _, err := store.BuildIndex(ctx, refetched, ChunkConfig{Size: 2}, fingerprint, testEmbeddings); err != nil {
		t.Fatalf("rebuild unchanged snapshot from a new acquisition: %v", err)
	}

	var createdAt, contentHash string
	if err := store.DB.QueryRowContext(ctx, `SELECT created_at, content_hash FROM snapshots WHERE id = ?`, first.ID).Scan(&createdAt, &contentHash); err != nil {
		t.Fatalf("read persisted snapshot: %v", err)
	}
	if createdAt != first.CreatedAt {
		t.Errorf("snapshot created_at = %q, want original %q", createdAt, first.CreatedAt)
	}
	wantHash, err := canonicalSnapshotHash(first)
	if err != nil {
		t.Fatalf("hash canonical snapshot: %v", err)
	}
	if contentHash != wantHash {
		t.Errorf("migrated content_hash = %q, want %q", contentHash, wantHash)
	}
	for _, document := range first.Documents {
		var retrievedAt string
		if err := store.DB.QueryRowContext(ctx, `SELECT retrieved_at FROM documents WHERE snapshot_id = ? AND id = ?`, first.ID, document.ID).Scan(&retrievedAt); err != nil {
			t.Fatalf("read acquisition metadata for %q: %v", document.ID, err)
		}
		if retrievedAt != document.RetrievedAt {
			t.Errorf("document %q retrieved_at = %q, want original %q", document.ID, retrievedAt, document.RetrievedAt)
		}
	}

	changed := refetched
	changed.Documents = append([]Document(nil), refetched.Documents...)
	changed.Documents[0].Text = strings.Replace(changed.Documents[0].Text, "alpha", "delta", 1)
	if changed.Documents[0].Text == refetched.Documents[0].Text {
		t.Fatal("changed snapshot fixture did not change document text")
	}
	if len(strings.Fields(changed.Documents[0].Text)) != len(strings.Fields(refetched.Documents[0].Text)) {
		t.Fatal("changed snapshot fixture altered the document word count")
	}
	if changed.Documents[0].Hash != refetched.Documents[0].Hash {
		t.Fatalf("changed snapshot fixture hash = %q, want unchanged old hash %q", changed.Documents[0].Hash, refetched.Documents[0].Hash)
	}
	if _, err := store.BuildIndex(ctx, changed, ChunkConfig{Size: 2}, fingerprint, testEmbeddings); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("reused snapshot ID with changed canonical content error = %v, want canonical content mismatch", err)
	}

	var persistedCreatedAt, persistedHash, persistedText string
	if err := store.DB.QueryRowContext(ctx, `SELECT created_at, content_hash FROM snapshots WHERE id = ?`, first.ID).Scan(&persistedCreatedAt, &persistedHash); err != nil {
		t.Fatalf("read snapshot after rejected content: %v", err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT text FROM documents WHERE snapshot_id = ? AND id = ?`, first.ID, changed.Documents[0].ID).Scan(&persistedText); err != nil {
		t.Fatalf("read document %q after rejected content: %v", changed.Documents[0].ID, err)
	}
	if persistedCreatedAt != first.CreatedAt || persistedHash != wantHash || persistedText != refetched.Documents[0].Text {
		t.Errorf("rejected content changed persisted snapshot: created_at=%q content_hash=%q text=%q", persistedCreatedAt, persistedHash, persistedText)
	}

}

func testStoreSnapshot() Snapshot {
	return Snapshot{
		ID:        "snapshot-test",
		CreatedAt: "2026-10-04T00:00:00Z",
		Documents: []Document{{
			ID:                "doc-test",
			Title:             "Test source",
			URL:               "https://example.test/source",
			Permalink:         "https://example.test/source?oldid=42",
			RevisionTimestamp: "2026-10-03T00:00:00Z",
			RetrievedAt:       "2026-10-04T00:00:00Z",
			Hash:              "hash",
			License:           "CC BY-SA 4.0",
			Attribution:       "Test attribution",
			PageID:            1,
			RevisionID:        42,
			Text:              "red red blue blue green green",
			Sections:          []Section{{ID: "red", Path: "Red", Start: 0, End: 2}, {ID: "blue", Path: "Blue", Start: 2, End: 4}, {ID: "green", Path: "Green", Start: 4, End: 6}},
			Warnings:          []string{"test warning"},
		}},
	}
}

func testEmbeddings(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		if strings.Contains(text, "red") {
			vectors[i] = []float32{1, 0}
		} else {
			vectors[i] = []float32{0, 1}
		}
	}
	return vectors, nil
}
