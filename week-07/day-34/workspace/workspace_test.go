package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Errorf("close workspace: %v", err)
		}
	})
	return w, root
}

func putFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestPathConfinementAndSensitiveTypes(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "safe\n")
	putFile(t, root, ".env.local", "SECRET=never-return\n")
	putFile(t, root, "docs/key.pem", "private")
	putFile(t, root, "docs/nested/file.md", "nested")
	if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "docs", "link.md")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../README.md", "docs/../../README.md", "/etc/passwd", `docs\..\README.md`, ".env.local", "docs/key.pem", "docs/link.md"} {
		if _, err := w.Read(path, 0, 0); err == nil {
			t.Errorf("Read(%q) succeeded", path)
		}
	}
	if _, err := w.Read("docs", 0, 0); err == nil {
		t.Fatal("reading a directory succeeded")
	}
	putFile(t, root, "docs/binary.md", "text\x00secret")
	if _, err := w.Read("docs/binary.md", 0, 0); err == nil {
		t.Fatal("reading NUL-containing file succeeded")
	}
	if _, err := w.Stage("docs/link.md", "changed"); err == nil {
		t.Fatal("staging symlink succeeded")
	}
	if _, err := w.Read("docs/nested/file.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("docs/nested/file.md", "changed"); err != nil {
		t.Fatalf("valid nested markdown stage: %v", err)
	}
	got, err := w.List("")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Files, "\n")
	for _, secret := range []string{".env", "key.pem", "link.md"} {
		if strings.Contains(joined, secret) {
			t.Errorf("list exposed %q: %s", secret, joined)
		}
	}
}

func TestRootSymlinkRejected(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "actual")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := New(alias); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestRootCanonicalizesSymlinkedAncestors(t *testing.T) {
	root := t.TempDir()
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close workspace: %v", err)
		}
	}()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.Root(); got != canonical {
		t.Fatalf("Root() = %q, want canonical path %q", got, canonical)
	}
}

func TestOverlayViewsAndVirtualDirectories(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "old needle\nsecond\n")
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("memory-bank/new/guide.md", "fresh needle\nlast\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "updated text\nsecond\n"); err != nil {
		t.Fatal(err)
	}
	read, err := w.Read("README.md", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if read.Content != "updated text\nsecond\n" {
		t.Fatalf("overlay read = %q", read.Content)
	}
	list, err := w.List("memory-bank/new")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Files) != 1 || list.Files[0] != "memory-bank/new/guide.md" {
		t.Fatalf("virtual directory listing = %#v", list)
	}
	search, err := w.Search("needle", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Matches) != 1 || search.Matches[0].Path != "memory-bank/new/guide.md" {
		t.Fatalf("overlay search = %#v", search)
	}
	virtualSearch, err := w.Search("fresh", "memory-bank/new")
	if err != nil || len(virtualSearch.Matches) != 1 {
		t.Fatalf("virtual directory search = %#v, %v", virtualSearch, err)
	}
	fileSearch, err := w.Search("fresh", "memory-bank/new/guide.md")
	if err != nil || len(fileSearch.Matches) != 1 {
		t.Fatalf("staged file search = %#v, %v", fileSearch, err)
	}
	diff, err := w.Diff()
	if err != nil || !strings.Contains(diff, "-old needle") || !strings.Contains(diff, "+updated text") || !strings.Contains(diff, "+fresh needle") {
		t.Fatalf("diff does not show staged changes: %q, %v", diff, err)
	}
	if disk, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(disk) != "old needle\nsecond\n" {
		t.Fatalf("write occurred before Apply: %q, %v", disk, err)
	}
	w.Reject()
	diff, err = w.Diff()
	if err != nil || diff != "" {
		t.Fatalf("reject left diff %q, %v", diff, err)
	}
}

func TestStagingNoopAndMarkdownAllowlist(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "same\n")
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "same\n"); err != nil {
		t.Fatal(err)
	}
	diff, err := w.Diff()
	if err != nil || diff != "" {
		t.Fatalf("no-op diff %q, %v", diff, err)
	}
	for _, path := range []string{"docs-old/a.md", "memory-bank-old/a.md", "docs/a.txt", "week-07/day-34/main.go", "docs/.env-notes.md"} {
		if _, err := w.Stage(path, "content"); err == nil {
			t.Errorf("Stage(%q) succeeded", path)
		}
	}
	for _, path := range []string{"README.md", "docs/guide.md", "docs/nested/guide.md", "memory-bank/architecture.md"} {
		if _, err := w.Stage(path, "valid\n"); err != nil {
			t.Errorf("Stage(%q): %v", path, err)
		}
	}
	if err := w.Apply(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"README.md", "docs/guide.md", "docs/nested/guide.md", "memory-bank/architecture.md"} {
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path))); err != nil || string(data) != "valid\n" {
			t.Errorf("applied %s = %q, %v", path, data, err)
		}
	}
}

func TestStaleStageAndThirdFilePreflightAreAtomic(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "one\n")
	putFile(t, root, "docs/a.md", "two\n")
	putFile(t, root, "docs/b.md", "three\n")
	for _, path := range []string{"README.md", "docs/a.md", "docs/b.md"} {
		if _, err := w.Read(path, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{"README.md": "new one\n", "docs/a.md": "new two\n", "docs/b.md": "new three\n"} {
		if _, err := w.Stage(path, content); err != nil {
			t.Fatal(err)
		}
	}
	putFile(t, root, "docs/b.md", "externally changed\n")
	overlay, err := w.Read("docs/b.md", 0, 0)
	if err != nil || overlay.Content != "new three\n" {
		t.Fatalf("staged read = %#v, %v", overlay, err)
	}
	if err := w.Apply(); err == nil || !strings.Contains(err.Error(), "docs/b.md") {
		t.Fatalf("conflict error = %v", err)
	}
	for path, want := range map[string]string{"README.md": "one\n", "docs/a.md": "two\n", "docs/b.md": "externally changed\n"} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	diff, err := w.Diff()
	if err != nil || !strings.Contains(diff, "new one") {
		t.Errorf("pending diff lost after conflict: %q, %v", diff, err)
	}
}

func TestStagedOriginalChangeAndNewFileConflict(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "before\n")
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "after\n"); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "README.md", "changed\n")
	if err := w.Apply(); err == nil {
		t.Fatal("stale original applied")
	}
	w.Reject()
	if _, err := w.Stage("docs/new.md", "planned\n"); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "docs/new.md", "surprise\n")
	if err := w.Apply(); err == nil {
		t.Fatal("new target conflict ignored")
	}
}

func TestTruncationAndLimits(t *testing.T) {
	w, root := testWorkspace(t)
	large := strings.Repeat("x", maxResponse+100)
	putFile(t, root, "README.md", large)
	read, err := w.Read("README.md", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !read.Truncated || !strings.HasSuffix(read.Content, "[truncated]") {
		t.Fatalf("large read not marked truncated: %#v", read)
	}
	readJSON, err := json.Marshal(read)
	if err != nil || len(readJSON) > maxResponse {
		t.Fatalf("read JSON exceeds response cap: %d bytes, %v", len(readJSON), err)
	}
	if _, err := w.Stage("README.md", "replacement"); err == nil {
		t.Fatal("truncated read authorized a full replacement")
	}
	if _, err := w.Read("README.md", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "replacement"); err == nil {
		t.Fatal("partial read authorized a full replacement")
	}
	if _, err := w.Stage("README.md", strings.Repeat("x", maxFileBytes+1)); err == nil {
		t.Fatal("oversized stage accepted")
	}
	putFile(t, root, "docs/matches.md", strings.Repeat("needle\n", maxMatches+5))
	search, err := w.Search("needle", "docs/matches.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Matches) != maxMatches || !search.Truncated {
		t.Fatalf("bounded search = %d matches, truncated=%v", len(search.Matches), search.Truncated)
	}
}

func TestApplyBreaksHardlinkWithoutChangingPeer(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "original\n")
	peer := filepath.Join(root, "peer")
	if err := os.Link(filepath.Join(root, "README.md"), peer); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "replacement\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Apply(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(peer)
	if err != nil || string(got) != "original\n" {
		t.Fatalf("hardlink peer = %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(got) != "replacement\n" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
}

func TestCallToolStructuredObjectAndUnknownTool(t *testing.T) {
	w, _ := testWorkspace(t)
	definitions := w.Definitions()
	if len(definitions) != 4 {
		t.Fatalf("workspace exposed %d tools, want four", len(definitions))
	}
	gotNames := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		gotNames = append(gotNames, definition.Name)
	}
	wantNames := []string{listTool, readTool, searchTool, writeTool}
	for _, want := range wantNames {
		if !slices.Contains(gotNames, want) {
			t.Errorf("missing tool %q in %#v", want, gotNames)
		}
	}
	result, err := w.CallTool(context.Background(), listTool, json.RawMessage(`{"path":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.StructuredContent.(map[string]any); !ok {
		t.Fatalf("structured content is %T", result.StructuredContent)
	}
	if len(result.Content) != 1 {
		t.Fatalf("text content = %#v", result.Content)
	}
	if _, err := w.CallTool(context.Background(), "apply", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unexpected approval tool accepted")
	}
	if _, err := w.CallTool(context.Background(), listTool, json.RawMessage(`null`)); err == nil {
		t.Fatal("non-object JSON accepted")
	}
}

func TestSearchAndListResponseCaps(t *testing.T) {
	w, root := testWorkspace(t)
	content := strings.Repeat(strings.Repeat("needle", 30)+"\n", 200)
	putFile(t, root, "docs/many.md", content)
	result, err := w.Search("needle", "docs/many.md")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > maxResponse {
		t.Fatalf("search JSON exceeds response cap: %d", len(encoded))
	}
	if !result.Truncated {
		t.Fatal("bounded search response not marked truncated")
	}
	for i := range 800 {
		putFile(t, root, filepath.Join("docs", strings.Repeat("f", 80)+fmt.Sprintf("%04d.md", i)), "x")
	}
	listed, err := w.List("docs")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(listed)
	if len(encoded) > maxResponse {
		t.Fatalf("list JSON exceeds response cap: %d", len(encoded))
	}
	if !listed.Truncated {
		t.Fatal("bounded listing response not marked truncated")
	}
}

func TestReplacementRequiresCompleteUntruncatedRead(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "first\nsecond\n")
	if _, err := w.Stage("README.md", "replacement\n"); err == nil {
		t.Fatal("replacement succeeded before reading original")
	}
	if _, err := w.Read("README.md", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "replacement\n"); err == nil {
		t.Fatal("partial read authorized full replacement")
	}
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "replacement\n"); err != nil {
		t.Fatalf("complete read did not authorize replacement: %v", err)
	}
}

func TestOverlayFileAndByteLimits(t *testing.T) {
	w, _ := testWorkspace(t)
	for i := range maxOverlayFiles {
		path := fmt.Sprintf("memory-bank/files/%03d.md", i)
		if _, err := w.Stage(path, "x"); err != nil {
			t.Fatalf("stage file %d: %v", i, err)
		}
	}
	if _, err := w.Stage("memory-bank/files/extra.md", "x"); err == nil {
		t.Fatal("overlay file-count limit was not enforced")
	}
	w.Reject()
	large := strings.Repeat("x", maxFileBytes)
	for i := range 8 {
		path := fmt.Sprintf("memory-bank/large/%d.md", i)
		if _, err := w.Stage(path, large); err != nil {
			t.Fatalf("stage large file %d: %v", i, err)
		}
	}
	if _, err := w.Stage("memory-bank/large/extra.md", "x"); err == nil {
		t.Fatal("overlay byte limit was not enforced")
	}
}
func TestFailedReplacementPreservesOverlayAtByteLimit(t *testing.T) {
	w, _ := testWorkspace(t)
	large := strings.Repeat("x", maxFileBytes)
	for i := range 8 {
		if _, err := w.Stage(fmt.Sprintf("memory-bank/large/%d.md", i), large); err != nil {
			t.Fatalf("stage large file %d: %v", i, err)
		}
	}
	const emptyPath = "memory-bank/large/empty.md"
	if _, err := w.Stage(emptyPath, ""); err != nil {
		t.Fatalf("stage empty file: %v", err)
	}
	before, err := w.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage(emptyPath, "x"); err == nil {
		t.Fatal("replacement beyond aggregate byte limit succeeded")
	}
	after, err := w.Diff()
	if err != nil || after != before {
		t.Fatalf("failed replacement changed overlay diff: equal=%v, err=%v", after == before, err)
	}
	read, err := w.Read(emptyPath, 0, 0)
	if err != nil || read.Content != "" {
		t.Fatalf("failed replacement changed prior content: %#v, %v", read, err)
	}
}

func TestStagedFileDirectoryCollisionsAreAtomic(t *testing.T) {
	for _, order := range []struct {
		name  string
		first string
		next  string
	}{
		{name: "file then descendant", first: "docs/a.md", next: "docs/a.md/b.md"},
		{name: "descendant then file", first: "docs/a.md/b.md", next: "docs/a.md"},
	} {
		t.Run(order.name, func(t *testing.T) {
			w, _ := testWorkspace(t)
			if _, err := w.Stage(order.first, "first"); err != nil {
				t.Fatal(err)
			}
			before, err := w.Diff()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Stage(order.next, "second"); err == nil {
				t.Fatal("file/directory collision was accepted")
			}
			after, err := w.Diff()
			if err != nil || after != before {
				t.Fatalf("failed collision stage changed overlay: equal=%v, err=%v", after == before, err)
			}
			if _, err := w.Stage("docs/valid/nested.md", "valid"); err != nil {
				t.Fatalf("valid virtual directory rejected: %v", err)
			}
		})
	}
}

func TestControlCharactersRejectedInPathsWithoutChangingOverlay(t *testing.T) {
	w, _ := testWorkspace(t)
	if _, err := w.Stage("docs/normal file.md", "valid"); err != nil {
		t.Fatalf("filename with spaces rejected: %v", err)
	}
	before, err := w.Diff()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"docs/a\nb.md", "docs/a\tb.md", "docs/a\rb.md", "docs/a\x01b.md"} {
		_, err := w.Stage(path, "invalid")
		if err == nil || err.Error() != "path must not contain control characters" {
			t.Errorf("Stage(%q) error = %v, want control-character rejection", path, err)
		}
		after, diffErr := w.Diff()
		if diffErr != nil || after != before {
			t.Fatalf("rejected path %q changed overlay: equal=%v, err=%v", path, after == before, diffErr)
		}
	}
}

func TestDirectoryEnumerationStopsAtGlobalEntryLimit(t *testing.T) {
	w, root := testWorkspace(t)
	dir := filepath.Join(root, "docs")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for i := range maxEntries + 1 {
		name := fmt.Sprintf("%05d.md", i)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatalf("create entry %d: %v", i, err)
		}
	}
	files := make(map[string]struct{}, maxEntries)
	truncated, err := w.collectFiles("docs", files)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(files) != maxEntries {
		t.Fatalf("bounded collection = %d entries, truncated=%v", len(files), truncated)
	}

	listing, err := w.List("docs")
	if err != nil || !listing.Truncated {
		t.Fatalf("bounded list truncated=%v, err=%v", listing.Truncated, err)
	}
}

func TestRepeatedWritesReplaceStageAndRestoreOriginalNoop(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "README.md", "original\n")
	if _, err := w.Read("README.md", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "first replacement\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stage("README.md", "latest replacement\n"); err != nil {
		t.Fatal(err)
	}
	diff, err := w.Diff()
	if err != nil || strings.Contains(diff, "first replacement") || !strings.Contains(diff, "latest replacement") {
		t.Fatalf("repeated stage = %q, %v", diff, err)
	}
	if _, err := w.Stage("README.md", "original\n"); err != nil {
		t.Fatal(err)
	}
	diff, err = w.Diff()
	if err != nil || diff != "" {
		t.Fatalf("restoring original left staged diff %q, %v", diff, err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(got) != "original\n" {
		t.Fatalf("staging modified disk: %q, %v", got, err)
	}
}
func TestSearchSkipsUnsupportedFilesWithoutAborting(t *testing.T) {
	w, root := testWorkspace(t)
	putFile(t, root, "a-before.txt", "needle before\n")
	if err := os.WriteFile(filepath.Join(root, "b-invalid.txt"), []byte{0xff, 'n', 'e', 'e', 'd', 'l', 'e'}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "c-nul.txt"), []byte("needle\x00hidden"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "d-large.txt"), make([]byte, maxFileBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "z-after.txt", "needle after\n")

	result, err := w.Search("needle", ".")
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedFiles != 3 {
		t.Fatalf("skipped_files = %d, want 3", result.SkippedFiles)
	}
	if result.Truncated || len(result.Matches) != 2 {
		t.Fatalf("search result = %+v, want two supported matches without truncation", result)
	}
	if result.Matches[0].Path != "a-before.txt" || result.Matches[1].Path != "z-after.txt" {
		t.Fatalf("matches = %+v", result.Matches)
	}
	for _, path := range []string{"b-invalid.txt", "c-nul.txt", "d-large.txt"} {
		if _, err := w.Read(path, 0, 0); err == nil {
			t.Errorf("Read(%q) unexpectedly accepted unsupported content", path)
		}
		if _, err := w.Search("needle", path); err == nil {
			t.Errorf("Search(%q) unexpectedly accepted unsupported content", path)
		}
	}
}

func TestNestedDirectoryEnumerationUsesGlobalEntryBudget(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.Mkdir(filepath.Join(root, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := range maxEntries - 1 {
		name := fmt.Sprintf("%05d.txt", i)
		if err := os.WriteFile(filepath.Join(root, "a", name), nil, 0644); err != nil {
			t.Fatalf("create nested entry %d: %v", i, err)
		}
	}
	putFile(t, root, "z.txt", "pending parent sibling\n")

	files := make(map[string]struct{}, maxEntries)
	truncated, err := w.collectFiles(".", files)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(files) > maxEntries {
		t.Fatalf("nested collection = %d entries, truncated=%v; want bounded and truncated", len(files), truncated)
	}
	listing, err := w.List(".")
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Truncated || len(listing.Files) > maxEntries {
		t.Fatalf("nested listing = %d entries, truncated=%v; want bounded and truncated", len(listing.Files), listing.Truncated)
	}
}
