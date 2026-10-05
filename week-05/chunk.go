package main

import (
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ChunkConfig controls the maximum word count and sliding-window overlap.
type ChunkConfig struct {
	Size    int `json:"size"`
	Overlap int `json:"overlap"`
}

// Chunk is a traceable, word-offset slice of one snapshot document.
type Chunk struct {
	ID                string   `json:"id"`
	SnapshotID        string   `json:"snapshot_id"`
	DocumentID        string   `json:"document_id"`
	Title             string   `json:"title"`
	Permalink         string   `json:"permalink"`
	RevisionTimestamp string   `json:"revision_timestamp"`
	Strategy          string   `json:"strategy"`
	RevisionID        int64    `json:"revision_id"`
	SectionPaths      []string `json:"section_paths"`
	Ordinal           int      `json:"ordinal"`
	Start             int      `json:"start"`
	End               int      `json:"end"`
	Text              string   `json:"text"`
}

// MakeChunks creates deterministic fixed-size or heading-preserving chunks.
// The accepted strategy identifiers are "fixed" and "structural".
func MakeChunks(s Snapshot, strategy string, cfg ChunkConfig) ([]Chunk, error) {
	if strategy != "fixed" && strategy != "structural" {
		return nil, fmt.Errorf("unknown chunking strategy %q (want fixed or structural)", strategy)
	}
	if cfg.Size <= 0 || cfg.Overlap < 0 || cfg.Overlap >= cfg.Size {
		return nil, errors.New("chunk size must be positive and overlap must be between zero and size")
	}
	if s.ID == "" || len(s.Documents) == 0 {
		return nil, errors.New("chunking requires a non-empty snapshot")
	}
	var chunks []Chunk
	for _, doc := range s.Documents {
		if doc.ID == "" || doc.Title == "" || doc.Permalink == "" || doc.RevisionID <= 0 {
			return nil, fmt.Errorf("document has incomplete source metadata: %q", doc.ID)
		}
		words := wordOffsets(doc.Text)
		if len(words) == 0 {
			return nil, fmt.Errorf("document %q has empty text", doc.ID)
		}
		if strategy == "fixed" {
			chunks = append(chunks, fixedChunks(s, doc, words, cfg)...)
		} else {
			made, err := structuralChunks(s, doc, words, cfg)
			if err != nil {
				return nil, err
			}
			chunks = append(chunks, made...)
		}
	}
	return chunks, nil
}

func fixedChunks(snapshot Snapshot, doc Document, words []wordOffset, cfg ChunkConfig) []Chunk {
	var chunks []Chunk
	ordinal := 0
	for start := 0; start < len(words); {
		end := start + cfg.Size
		if end > len(words) {
			end = len(words)
		}
		paths := sectionPaths(doc.Sections, start, end)
		if len(paths) == 0 {
			paths = []string{doc.Title}
		}
		chunks = append(chunks, makeChunk(snapshot, doc, "fixed", cfg, ordinal, start, end, doc.Text, words, paths))
		ordinal++
		if end == len(words) {
			break
		}
		start = end - cfg.Overlap
	}
	return chunks
}

func structuralChunks(snapshot Snapshot, doc Document, words []wordOffset, cfg ChunkConfig) ([]Chunk, error) {
	sections := doc.Sections
	if len(sections) == 0 {
		sections = []Section{{Path: doc.Title, Start: 0, End: len(words)}}
	}
	var chunks []Chunk
	ordinal := 0
	covered := make([]bool, len(words))
	for _, section := range sections {
		start, end := section.Start, section.End
		if start < 0 || end < start || end > len(words) {
			return nil, fmt.Errorf("document %q section %q has invalid word offsets [%d,%d) for %d words", doc.ID, section.Path, start, end, len(words))
		}
		if start == end {
			continue
		}
		for i := start; i < end; i++ {
			covered[i] = true
		}
		for partStart := start; partStart < end; {
			partEnd := partStart + cfg.Size
			if partEnd > end {
				partEnd = end
			}
			paths := []string{section.Path}
			if paths[0] == "" {
				paths[0] = doc.Title
			}
			chunks = append(chunks, makeChunk(snapshot, doc, "structural", cfg, ordinal, partStart, partEnd, doc.Text, words, paths))
			ordinal++
			if partEnd == end {
				break
			}
			partStart = partEnd - cfg.Overlap
		}
	}
	for i, ok := range covered {
		if !ok {
			return nil, fmt.Errorf("document %q section metadata does not cover word offset %d", doc.ID, i)
		}
	}
	return chunks, nil
}

func makeChunk(snapshot Snapshot, doc Document, strategy string, cfg ChunkConfig, ordinal, start, end int, source string, words []wordOffset, paths []string) Chunk {
	text := source[words[start].start:words[end-1].end]
	id := stableID("chunk", snapshot.ID, doc.ID, fmt.Sprint(doc.RevisionID), strategy,
		fmt.Sprint(cfg.Size), fmt.Sprint(cfg.Overlap), fmt.Sprint(ordinal), fmt.Sprint(start), fmt.Sprint(end), text)
	return Chunk{
		ID: id, SnapshotID: snapshot.ID, DocumentID: doc.ID, Title: doc.Title,
		Permalink: doc.Permalink, RevisionTimestamp: doc.RevisionTimestamp,
		Strategy: strategy, RevisionID: doc.RevisionID, SectionPaths: append([]string(nil), paths...),
		Ordinal: ordinal, Start: start, End: end, Text: text,
	}
}

type wordOffset struct {
	start int
	end   int
}

func wordOffsets(text string) []wordOffset {
	var offsets []wordOffset
	start := -1
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRuneInString(text[index:])
		if unicode.IsSpace(r) {
			if start >= 0 {
				offsets = append(offsets, wordOffset{start: start, end: index})
				start = -1
			}
		} else if start < 0 {
			start = index
		}
		index += size
	}
	if start >= 0 {
		offsets = append(offsets, wordOffset{start: start, end: len(text)})
	}
	return offsets
}

func sectionPaths(sections []Section, start, end int) []string {
	paths := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, section := range sections {
		if section.Start >= end || section.End <= start || section.Path == "" {
			continue
		}
		if _, ok := seen[section.Path]; ok {
			continue
		}
		seen[section.Path] = struct{}{}
		paths = append(paths, section.Path)
	}
	return paths
}
