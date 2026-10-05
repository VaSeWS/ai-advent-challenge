package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RunIndex builds both chunking indexes, or inspects/searches an existing build.
func RunIndex(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("index", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	snapshotPath := flags.String("snapshot", "week-05/corpus/gtnh-20261004.json", "normalized source snapshot")
	dbPath := flags.String("db", "week-05/rag.db", "SQLite index database")
	ollamaEndpoint := flags.String("ollama", defaultOllamaEndpoint, "Ollama base URL")
	model := flags.String("embedding-model", defaultOllamaModel, "installed Ollama embedding model")
	dimensions := flags.Int("dimensions", nomicDimensions, "embedding vector dimensions")
	size := flags.Int("size", 400, "maximum chunk size in words")
	overlap := flags.Int("overlap", 80, "chunk overlap in words")
	inspect := flags.Bool("inspect", false, "inspect an existing index without building")
	strategy := flags.String("strategy", strategyFixed, "inspection/search strategy: fixed or structural")
	chunkID := flags.String("chunk", "", "show full source and text for this chunk ID")
	buildID := flags.String("build", "", "inspect/search this build ID (default: active build)")
	searchQuery := flags.String("search", "", "embed this query and show nearest chunks")
	topK := flags.Int("k", 5, "number of nearest chunks to search (must be positive)")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: index [flags]")
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
		return fmt.Errorf("index: unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *strategy != strategyFixed && *strategy != strategyStructural {
		return fmt.Errorf("index: unknown strategy %q (want fixed or structural)", *strategy)
	}
	if *chunkID != "" && !*inspect {
		return errors.New("index: -chunk requires -inspect")
	}
	if *buildID != "" && !*inspect {
		return errors.New("index: -build requires -inspect")
	}
	if *searchQuery != "" && *topK <= 0 {
		return errors.New("index: -k must be positive")
	}
	if *dimensions <= 0 || *size <= 0 || *overlap < 0 || *overlap >= *size {
		return errors.New("index: dimensions and chunk size must be positive; overlap must be between zero and size")
	}
	var store *Store
	var err error
	if *inspect {
		if strings.HasPrefix(strings.ToLower(*dbPath), "file:") {
			return errors.New("inspect existing index database: file: URI paths are not supported")
		}
		if _, err := os.Stat(*dbPath); err != nil {
			return fmt.Errorf("inspect existing index database: %w", err)
		}
		store, err = openReadOnlyStore(*dbPath)
	} else {
		if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
			return fmt.Errorf("create database directory: %w", err)
		}
		store, err = OpenStore(*dbPath)
	}
	if err != nil {
		return err
	}
	defer store.Close()

	var build IndexBuild
	if *inspect {
		if *buildID == "" {
			build, err = store.ActiveBuild(ctx)
		} else {
			build, err = store.loadBuild(ctx, *buildID)
		}
		if err != nil {
			return err
		}
		fmt.Println("Index inspection (no index or embedding generation)")
	} else {
		snapshot, err := LoadSnapshot(*snapshotPath)
		if err != nil {
			return fmt.Errorf("load index snapshot: %w", err)
		}
		client := OllamaClient{Endpoint: *ollamaEndpoint, Model: *model, Dimensions: *dimensions}
		fingerprint, err := client.Fingerprint(ctx)
		if err != nil {
			return err
		}
		id, err := store.BuildIndex(ctx, snapshot, ChunkConfig{Size: *size, Overlap: *overlap}, fingerprint, client.EmbedDocuments)
		if err != nil {
			return err
		}
		build, err = store.ActiveBuild(ctx)
		if err != nil {
			return err
		}
		if build.ID != id {
			return fmt.Errorf("index build activation mismatch: built %q but active build is %q", id, build.ID)
		}
		fmt.Printf("Built and activated index from %s\n", *snapshotPath)
	}
	printBuild(build)

	if *inspect || *searchQuery != "" {
		for _, selectedStrategy := range []string{strategyFixed, strategyStructural} {
			chunks, err := store.InspectChunks(ctx, build.ID, selectedStrategy)
			if err != nil {
				return err
			}
			if err := reportChunks(ctx, store, build, selectedStrategy, chunks); err != nil {
				return err
			}
		}
	}
	if *chunkID != "" {
		if err := printChunkSource(ctx, store, build, *strategy, *chunkID); err != nil {
			return err
		}
	}
	if *searchQuery != "" {
		client := OllamaClient{Endpoint: *ollamaEndpoint, Model: *model, Dimensions: *dimensions}
		current, err := client.Fingerprint(ctx)
		if err != nil {
			return err
		}
		if err := CompatibleFingerprint(build.Fingerprint, current); err != nil {
			return fmt.Errorf("search refused: %w", err)
		}
		query, err := client.EmbedQuery(ctx, *searchQuery)
		if err != nil {
			return fmt.Errorf("embed search query: %w", err)
		}
		results, err := store.Search(ctx, build.ID, *strategy, query, *topK)
		if err != nil {
			return err
		}
		fmt.Printf("Search: %q | strategy: %s | top-K: %d\n", *searchQuery, *strategy, *topK)
		for rank, result := range results {
			fmt.Printf("%d. distance: %.6f | title: %s | chunk: %s | revision: %d (%s)\n   source: %s\n   text: %s\n", rank+1, result.Distance, result.Chunk.Title, result.Chunk.ID, result.Chunk.RevisionID, result.Chunk.RevisionTimestamp, result.Chunk.Permalink, result.Chunk.Text)
		}
	}
	return nil
}

func openReadOnlyStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path must not be empty")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve inspection database path: %w", err)
	}
	databaseURI := (&url.URL{
		Scheme:   "file",
		Path:     absolutePath,
		RawQuery: "mode=ro",
	}).String()
	db, err := sql.Open("sqlite3", databaseURI)
	if err != nil {
		return nil, fmt.Errorf("open read-only sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect read-only sqlite database: %w", err)
	}
	return &Store{DB: db}, nil
}

func printBuild(build IndexBuild) {
	fmt.Printf("Active/index build: %s\nSnapshot: %s\nEmbedding model: %s\nEmbedding digest: %s\nPreparation: %s\nDimensions: %d\nChunk size/overlap: %d/%d words\n", build.ID, build.SnapshotID, build.Fingerprint.Model, build.Fingerprint.Digest, build.Fingerprint.Preparation, build.Fingerprint.Dimensions, build.Config.Size, build.Config.Overlap)
}

type chunkRange struct{ start, end int }

func reportChunks(ctx context.Context, store *Store, build IndexBuild, strategy string, chunks []Chunk) error {
	type source struct {
		words  int
		text   string
		offset []wordOffset
		ranges []chunkRange
	}
	sources := make(map[string]*source)
	rows, err := store.DB.QueryContext(ctx, `SELECT id, text FROM documents WHERE snapshot_id = ? ORDER BY title`, build.SnapshotID)
	if err != nil {
		return fmt.Errorf("read source text for chunk report: %w", err)
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return fmt.Errorf("scan source text: %w", err)
		}
		offsets := wordOffsets(text)
		sources[id] = &source{words: len(offsets), text: text, offset: offsets}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate source text: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close source text rows: %w", err)
	}
	totalChunkWords, minSize, maxSize := 0, 0, 0
	coverageOK := len(sources) > 0
	for _, chunk := range chunks {
		chunkWords := len(wordOffsets(chunk.Text))
		totalChunkWords += chunkWords
		if minSize == 0 || chunkWords < minSize {
			minSize = chunkWords
		}
		if chunkWords > maxSize {
			maxSize = chunkWords
		}
		source := sources[chunk.DocumentID]
		if source == nil || chunk.Start < 0 || chunk.End <= chunk.Start || chunk.End > source.words || chunkWords != chunk.End-chunk.Start {
			coverageOK = false
			continue
		}
		expected := source.text[source.offset[chunk.Start].start:source.offset[chunk.End-1].end]
		if chunk.Text != expected {
			coverageOK = false
		}
		source.ranges = append(source.ranges, chunkRange{chunk.Start, chunk.End})
	}
	sourceWords, coveredWords := 0, 0
	for _, source := range sources {
		sourceWords += source.words
		sort.Slice(source.ranges, func(i, j int) bool {
			if source.ranges[i].start == source.ranges[j].start {
				return source.ranges[i].end < source.ranges[j].end
			}
			return source.ranges[i].start < source.ranges[j].start
		})
		covered := 0
		for _, span := range source.ranges {
			if span.start > covered {
				coverageOK = false
			}
			if span.end > covered {
				covered += span.end - maxInt(span.start, covered)
			}
		}
		if covered != source.words {
			coverageOK = false
		}
		coveredWords += covered
	}
	fmt.Printf("\n%s persisted chunks: %d | size min/max: %d/%d words | summed chunk words: %d | source words: %d | overlap overhead: %d words\n", strategy, len(chunks), minSize, maxSize, totalChunkWords, sourceWords, totalChunkWords-sourceWords)
	if coverageOK && coveredWords == sourceWords {
		fmt.Printf("Full source text coverage (persisted chunk offsets and text): PASS (%d/%d source words covered)\n", coveredWords, sourceWords)
	} else {
		fmt.Printf("Full source text coverage (persisted chunk offsets and text): FAIL (%d/%d source words covered)\n", coveredWords, sourceWords)
		return fmt.Errorf("%s chunk coverage does not match persisted source text", strategy)
	}
	return nil
}

func printChunkSource(ctx context.Context, store *Store, build IndexBuild, strategy, id string) error {
	chunks, err := store.InspectChunks(ctx, build.ID, strategy)
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		if chunk.ID != id {
			continue
		}
		var title, permalink, revisionTimestamp, fullText string
		var pageID, revisionID int64
		var license, attribution string
		err := store.DB.QueryRowContext(ctx, `SELECT title, permalink, revision_timestamp, page_id, revision_id, license, attribution, text FROM documents WHERE snapshot_id = ? AND id = ?`, build.SnapshotID, chunk.DocumentID).Scan(&title, &permalink, &revisionTimestamp, &pageID, &revisionID, &license, &attribution, &fullText)
		if err != nil {
			return fmt.Errorf("load source for chunk %q: %w", id, err)
		}
		fmt.Printf("\nChunk inspection: %s\nTitle: %s\nChunk: words %d-%d | sections: %s\nRevision: %d (%s) | page: %d\nPinned source: %s\nLicense: %s\nAttribution: %s\n\nFull source text:\n%s\n\nChunk text:\n%s\n", chunk.ID, title, chunk.Start, chunk.End, strings.Join(chunk.SectionPaths, " > "), revisionID, revisionTimestamp, pageID, permalink, license, attribution, fullText, chunk.Text)
		return nil
	}
	return fmt.Errorf("chunk %q not found in %s build %q", id, strategy, build.ID)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
