package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeWikiHTMLPreservesContentAndOmitsNavigation(t *testing.T) {
	source := `<div id="mw-content-text">
		<nav><p>Navigation only</p></nav>
		<div id="toc"><p>Contents only</p></div>
		<h2>Power</h2><p>Useful paragraph with <b>formatted</b> text.</p>
		<ul><li>First step</li><li>Second step</li></ul>
		<table><thead><tr><th>Voltage</th><th>Tier</th></tr></thead><tbody><tr><td>32 EU/t</td><td>LV</td></tr></tbody></table>
		<div class="warning">This information may be outdated.</div>
		<pre>machine = true
  # comment
	energy = 32</pre></div>`
	text, sections, warnings, err := normalizeWikiHTML(source, "Electricity")
	if err != nil {
		t.Fatal(err)
	}
	code := "machine = true\n  # comment\n\tenergy = 32"
	for _, want := range []string{"Power", "Useful paragraph with formatted text.", "- First step", "Voltage | Tier", "Voltage: 32 EU/t", "Warning: This information may be outdated.", code} {
		if !strings.Contains(text, want) {
			t.Errorf("normalized text missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Navigation only", "Contents only"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("normalized text retained %q", unwanted)
		}
	}
	if len(sections) != 1 || sections[0].Start != 0 || sections[0].End != len(strings.Fields(text)) {
		t.Fatalf("unexpected section boundaries: %#v for %d words", sections, len(strings.Fields(text)))
	}
	if len(warnings) != 1 || warnings[0] != "Warning: This information may be outdated." {
		t.Fatalf("unexpected warnings: %#v", warnings)
	}
}

func TestFetchSnapshotResolvesAndPinsCanonicalRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("action") {
		case "query":
			if q.Get("redirects") != "1" || q.Get("titles") != "Old name" {
				t.Errorf("missing redirect/canonical-title request parameters: %v", q)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": []any{map[string]any{
				"pageid": 42, "title": "Canonical page", "revisions": []any{map[string]any{"revid": 731, "timestamp": "2025-01-02T03:04:05Z"}},
			}}}})
		case "parse":
			if q.Get("oldid") != "731" {
				t.Errorf("parse was not pinned to oldid: %v", q)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"parse": map[string]any{
				"pageid": 42, "revid": 731, "text": `<div id="mw-content-text"><h2>Section</h2><p>Stable text.</p></div>`,
			}})
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	snapshot, err := FetchSnapshot(context.Background(), server.URL+"/w/api.php", []string{"Old name"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Documents) != 1 {
		t.Fatalf("got %d documents", len(snapshot.Documents))
	}
	doc := snapshot.Documents[0]
	if doc.Title != "Canonical page" || doc.PageID != 42 || doc.RevisionID != 731 {
		t.Fatalf("canonical metadata not retained: %#v", doc)
	}
	if !strings.Contains(doc.Permalink, "oldid=731") || doc.License != "CC BY-SA 4.0" || !strings.Contains(doc.Attribution, "GT New Horizons Wiki") {
		t.Fatalf("missing attribution or pinned permalink: %#v", doc)
	}
	if err := validateSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
}

func TestFetchSnapshotRejectsMissingMismatchAndEmptyRevisions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  bool
		revision int
		empty    bool
	}{
		{name: "revision mismatch", revision: 8},
		{name: "missing page", missing: true},
		{name: "empty rendered text", revision: 7, empty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("action") == "query" {
					if tc.missing {
						_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": []any{map[string]any{"pageid": -1, "title": "Missing", "missing": true}}}})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": []any{map[string]any{"pageid": 42, "title": "Page", "revisions": []any{map[string]any{"revid": 7, "timestamp": "2025-01-01T00:00:00Z"}}}}}})
					return
				}
				text := "<p>some text</p>"
				if tc.empty {
					text = `<div id="mw-content-text"></div>`
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"parse": map[string]any{"pageid": 42, "revid": tc.revision, "text": text}})
			}))
			defer server.Close()
			if _, err := FetchSnapshot(context.Background(), server.URL+"/w/api.php", []string{"Page"}); err == nil {
				t.Fatal("expected fetch failure")
			}
		})
	}
}

func TestSaveSnapshotIsExclusiveAndRoundTrips(t *testing.T) {
	doc := Document{ID: "d1", Title: "Title", Permalink: "https://example.test/?oldid=3", PageID: 2, RevisionID: 3, Hash: "hash", Text: "meaningful text"}
	hash := sha256.Sum256([]byte(doc.Text))
	doc.Hash = hex.EncodeToString(hash[:])
	snapshot := Snapshot{ID: snapshotID([]Document{doc}), CreatedAt: "2025-01-01T00:00:00Z", Documents: []Document{doc}}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := SaveSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != snapshot.ID || loaded.Documents[0].Text != doc.Text {
		t.Fatalf("round trip changed snapshot: %#v", loaded)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveSnapshot(path, snapshot); err == nil {
		t.Fatal("SaveSnapshot overwrote existing file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing snapshot changed after exclusive-save failure")
	}
}

func TestSnapshotIDIgnoresDocumentOrder(t *testing.T) {
	a := Document{ID: "a", Hash: "ha"}
	b := Document{ID: "b", Hash: "hb"}
	if snapshotID([]Document{a, b}) != snapshotID([]Document{b, a}) {
		t.Fatal("snapshot ID depends on input order")
	}
}

func TestLoadSnapshotRejectsChangedTextUnderOriginalIdentity(t *testing.T) {
	doc := Document{ID: "d1", Title: "Title", Permalink: "https://example.test/?oldid=3", PageID: 2, RevisionID: 3, Text: "original source text"}
	hash := sha256.Sum256([]byte(doc.Text))
	doc.Hash = hex.EncodeToString(hash[:])
	snapshot := Snapshot{ID: snapshotID([]Document{doc}), CreatedAt: "2025-01-01T00:00:00Z", Documents: []Document{doc}}
	snapshot.Documents[0].Text = "different source text"
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "modified.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("accepted changed source text with the original hash and snapshot identity")
	}
}
