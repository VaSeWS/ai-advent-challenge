package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestFixedChunksOverlapAcrossHeadingsAndStayWithinDocuments(t *testing.T) {
	docA := Document{
		ID: "doc-a", Title: "A", Permalink: "https://example.test/a?oldid=1", RevisionID: 1,
		Text: "a b c d e f g", Sections: []Section{
			{ID: "a1", Path: "A > First", Start: 0, End: 3},
			{ID: "a2", Path: "A > Second", Start: 3, End: 7},
		},
	}
	docB := Document{ID: "doc-b", Title: "B", Permalink: "https://example.test/b?oldid=2", RevisionID: 2, Text: "other document words", Sections: []Section{{ID: "b1", Path: "B", Start: 0, End: 3}}}
	snapshot := Snapshot{ID: "snapshot-test", Documents: []Document{docA, docB}}
	chunks, err := MakeChunks(snapshot, "fixed", ChunkConfig{Size: 4, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want two from A and one from B: %#v", len(chunks), chunks)
	}
	first, second, third := chunks[0], chunks[1], chunks[2]
	if first.Text != "a b c d" || first.Start != 0 || first.End != 4 {
		t.Fatalf("bad first chunk: %#v", first)
	}
	if second.Text != "d e f g" || second.Start != 3 || second.End != 7 {
		t.Fatalf("bad overlapping chunk: %#v", second)
	}
	if !reflect.DeepEqual(first.SectionPaths, []string{"A > First", "A > Second"}) {
		t.Fatalf("cross-heading chunk metadata incomplete: %#v", first.SectionPaths)
	}
	if third.DocumentID != "doc-b" || strings.Contains(third.Text, "a ") {
		t.Fatalf("chunk crossed document boundary: %#v", third)
	}
	again, err := MakeChunks(snapshot, "fixed", ChunkConfig{Size: 4, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range chunks {
		if chunks[i].ID != again[i].ID {
			t.Fatalf("chunk ID changed on rebuild at %d", i)
		}
	}
}

func TestStructuralChunksSplitLongSectionWithLocalOverlap(t *testing.T) {
	words := make([]string, 11)
	for i := range words {
		words[i] = string(rune('a' + i))
	}
	doc := Document{
		ID: "doc", Title: "Title", Permalink: "https://example.test/?oldid=4", RevisionID: 4,
		Text: strings.Join(words, " "), Sections: []Section{{ID: "s", Path: "Title > Long", Start: 0, End: len(words)}},
	}
	chunks, err := MakeChunks(Snapshot{ID: "snap", Documents: []Document{doc}}, "structural", ChunkConfig{Size: 4, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		text       string
		start, end int
	}{
		{"a b c d", 0, 4}, {"d e f g", 3, 7}, {"g h i j", 6, 10}, {"j k", 9, 11},
	}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(want))
	}
	for i, expected := range want {
		chunk := chunks[i]
		if chunk.Text != expected.text || chunk.Start != expected.start || chunk.End != expected.end || chunk.Ordinal != i {
			t.Errorf("chunk %d = %#v, want %q [%d,%d)", i, chunk, expected.text, expected.start, expected.end)
		}
		if chunk.SnapshotID != "snap" || chunk.Permalink != doc.Permalink || chunk.RevisionID != doc.RevisionID {
			t.Errorf("chunk %d lost source metadata: %#v", i, chunk)
		}
	}
}

func TestStructuralChunksDoNotMergeAdjacentSections(t *testing.T) {
	doc := Document{
		ID: "doc", Title: "Title", Permalink: "https://example.test/?oldid=9", RevisionID: 9, Text: "first second third fourth",
		Sections: []Section{{Path: "Title > A", Start: 0, End: 2}, {Path: "Title > B", Start: 2, End: 4}},
	}
	chunks, err := MakeChunks(Snapshot{ID: "snap", Documents: []Document{doc}}, "structural", ChunkConfig{Size: 3, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want separate section chunks", len(chunks))
	}
	if chunks[0].Text != "first second" || chunks[1].Text != "third fourth" {
		t.Fatalf("sections were combined: %#v", chunks)
	}
	if chunks[0].SectionPaths[0] == chunks[1].SectionPaths[0] {
		t.Fatalf("distinct sections lost metadata: %#v", chunks)
	}
}

func TestMakeChunksRejectsInvalidSettingsAndSectionCoverage(t *testing.T) {
	doc := Document{ID: "doc", Title: "Title", Permalink: "https://example.test/?oldid=10", RevisionID: 10, Text: "one two", Sections: []Section{{Path: "Title", Start: 0, End: 1}}}
	snapshot := Snapshot{ID: "snap", Documents: []Document{doc}}
	for _, tc := range []struct {
		strategy string
		cfg      ChunkConfig
	}{
		{"other", ChunkConfig{Size: 2}}, {"fixed", ChunkConfig{Size: 0}}, {"fixed", ChunkConfig{Size: 2, Overlap: 2}}, {"fixed", ChunkConfig{Size: 2, Overlap: -1}},
	} {
		if _, err := MakeChunks(snapshot, tc.strategy, tc.cfg); err == nil {
			t.Errorf("MakeChunks(%q, %#v) succeeded", tc.strategy, tc.cfg)
		}
	}
	if _, err := MakeChunks(snapshot, "structural", ChunkConfig{Size: 2}); err == nil {
		t.Fatal("structural chunking accepted uncovered word offsets")
	}
}
func TestFixedChunksPreserveWhitespaceWithinCodeText(t *testing.T) {
	doc := Document{
		ID: "doc-code", Title: "Code", Permalink: "https://example.test/code?oldid=11", RevisionID: 11,
		Text:     "# comment\n\tassignment = true\nnext value",
		Sections: []Section{{Path: "Code", Start: 0, End: 7}},
	}
	chunks, err := MakeChunks(Snapshot{ID: "snap-code", Documents: []Document{doc}}, "fixed", ChunkConfig{Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want two: %#v", len(chunks), chunks)
	}
	want := "# comment\n\tassignment = true"
	if chunks[0].Text != want || chunks[0].Start != 0 || chunks[0].End != 5 {
		t.Fatalf("code whitespace not preserved in chunk: %#v", chunks[0])
	}
}
