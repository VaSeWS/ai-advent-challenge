// Package workspace provides a confined, review-before-apply view of a project tree.
// It is not a sandbox against a concurrent hostile process with the same account:
// component checks and apply preflight are not atomic with external filesystem changes.
package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxFileBytes    = 1 << 20
	maxOverlayBytes = 8 << 20
	maxOverlayFiles = 100
	maxResponse     = 64 << 10
	maxEntries      = 10000
	maxMatches      = 100
)

var (
	errUnsupportedContent = errors.New("unsupported file content")
	errUnsupportedSize    = errors.New("file exceeds supported size limit")
)

type stagedFile struct {
	original []byte
	exists   bool
	mode     fs.FileMode
	content  []byte
}

// Workspace provides a confined read/stage/apply view of one project directory.
type Workspace struct {
	mu       sync.Mutex
	root     *os.Root
	path     string
	staged   map[string]stagedFile
	readFull map[string][]byte
	closed   bool
}

// New opens root as the workspace boundary, rejecting a symlink at the selected
// root while resolving symlinked ancestors to the root's canonical identity.
func New(root string) (*Workspace, error) {
	if root == "" || strings.IndexByte(root, 0) >= 0 {
		return nil, errors.New("workspace root must be a non-empty path")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("workspace root must not be a symlink")
	}
	if !info.IsDir() {
		return nil, errors.New("workspace root must be a directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalize workspace root: %w", err)
	}
	r, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	return &Workspace{root: r, path: canonical, staged: make(map[string]stagedFile), readFull: make(map[string][]byte)}, nil
}

// Close releases the rooted filesystem handle. Repeated calls are safe.
func (w *Workspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.root.Close()
}

// Root returns the canonical absolute workspace root path.
func (w *Workspace) Root() string { return w.path }

// Definitions returns the four workspace MCP tool definitions.
func (w *Workspace) Definitions() []mcp.Tool { return toolDefinitions() }

// Serve exposes only the four project file tools over the supplied MCP streams.
func (w *Workspace) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "project-workspace", Version: "1.0.0"}, nil)
	for _, definition := range toolDefinitions() {
		def := definition
		mcp.AddTool(server, &def, func(ctx context.Context, request *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
			result, err := w.CallTool(ctx, request.Params.Name, request.Params.Arguments)
			return result, nil, err
		})
	}
	return server.Run(ctx, &mcp.IOTransport{Reader: nonClosingReader{in}, Writer: nonClosingWriter{out}})
}

type nonClosingReader struct{ io.Reader }

func (nonClosingReader) Close() error { return nil }

type nonClosingWriter struct{ io.Writer }

func (nonClosingWriter) Close() error { return nil }

func (w *Workspace) ensureOpen() error {
	if w.closed {
		return errors.New("workspace is closed")
	}
	return nil
}

// validatePath checks untrusted lexical input before cleaning it.
func validatePath(path string, allowEmpty bool) (string, error) {
	drivePath := len(path) >= 3 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
	if strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || drivePath {
		return "", errors.New("path must be relative and contain no NUL")
	}
	if path == "" {
		if allowEmpty {
			return ".", nil
		}
		return "", errors.New("path is required")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", errors.New("path must not contain control characters")
		}
	}
	for _, component := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if component == ".." {
			return "", errors.New("parent traversal is not allowed")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(path, `\`, "/")))
	if clean == "." && !allowEmpty {
		return "", errors.New("path must name a file")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes workspace")
	}
	return clean, nil
}

func excludedPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == "third_party" || lower == "vendor" || lower == "node_modules" || lower == "build" || lower == "dist" || lower == "target" || strings.HasPrefix(lower, ".env") {
			return true
		}
		if strings.Contains(lower, "credential") || strings.Contains(lower, "secret") || strings.Contains(lower, "private") || strings.Contains(lower, "api_key") || strings.Contains(lower, "access_token") || strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") || lower == "authorized_keys" || lower == "known_hosts" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") {
			return true
		}
	}
	return false
}

func allowedWrite(path string) bool {
	path = filepath.ToSlash(path)
	if path == "README.md" {
		return true
	}
	return (strings.HasPrefix(path, "docs/") || strings.HasPrefix(path, "memory-bank/")) && strings.HasSuffix(path, ".md") && len(strings.Split(path, "/")) >= 2
}

func (w *Workspace) checkComponents(path string, allowMissingLeaf bool) (fs.FileInfo, error) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == "." || part == "" {
			continue
		}
		prefix := filepath.Join(parts[:i+1]...)
		info, err := w.root.Lstat(prefix)
		if errors.Is(err, fs.ErrNotExist) && allowMissingLeaf {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink component %q is not allowed", prefix)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("component %q is not a directory", prefix)
		}
		if i == len(parts)-1 {
			return info, nil
		}
	}
	return nil, nil
}

func (w *Workspace) readDisk(path string) ([]byte, fs.FileInfo, error) {
	info, err := w.checkComponents(path, false)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("only regular files are supported")
	}
	if info.Size() > maxFileBytes {
		return nil, nil, fmt.Errorf("%w: file exceeds %d-byte limit", errUnsupportedSize, maxFileBytes)
	}
	file, err := w.root.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxFileBytes {
		return nil, nil, fmt.Errorf("%w: file exceeds %d-byte limit", errUnsupportedSize, maxFileBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, fmt.Errorf("%w: file must contain UTF-8 text without NUL bytes", errUnsupportedContent)
	}
	return data, info, nil
}

func (w *Workspace) overlay(path string) ([]byte, bool) {
	change, ok := w.staged[path]
	if !ok {
		return nil, false
	}
	return change.content, true
}

func bounded(value string) (string, bool) {
	return boundedTo(value, maxResponse)
}

func boundedTo(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	cut := limit - len("\n[truncated]")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "\n[truncated]", true
}

func boundedRead(path, content string, start, end int) (readResult, bool, error) {
	result := readResult{Path: path, Content: content, StartLine: start, EndLine: end}
	encoded, err := json.Marshal(result)
	if err != nil {
		return readResult{}, false, err
	}
	if len(encoded) <= maxResponse {
		return result, false, nil
	}
	result.Truncated = true
	result.Content = "\n[truncated]"
	encoded, err = json.Marshal(result)
	if err != nil {
		return readResult{}, false, err
	}
	if len(encoded) > maxResponse {
		return readResult{}, false, errors.New("read response metadata exceeds response limit")
	}
	best := result.Content
	low, high := 0, len(content)
	for low < high {
		cut := low + (high-low+1)/2
		for cut > 0 && cut < len(content) && !utf8.RuneStart(content[cut]) {
			cut--
		}
		if cut <= low {
			cut = low + (high-low+1)/2
			for cut < len(content) && !utf8.RuneStart(content[cut]) {
				cut++
			}
		}
		if cut > high {
			break
		}
		candidate := result
		candidate.Content = content[:cut] + "\n[truncated]"
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return readResult{}, false, err
		}
		if len(encoded) <= maxResponse {
			best = candidate.Content
			low = cut
		} else {
			high = cut - 1
		}
	}
	result.Content = best
	return result, true, nil
}

type fileListResult struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}
type readResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Truncated bool   `json:"truncated"`
}
type searchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type searchResult struct {
	Matches      []searchMatch `json:"matches"`
	Truncated    bool          `json:"truncated"`
	SkippedFiles int           `json:"skipped_files"`
}

// List returns visible regular-file paths, including staged files, in sorted order.
// Listing does not imply a file is supported by text search.
func (w *Workspace) List(path string) (fileListResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return fileListResult{}, err
	}
	clean, err := validatePath(path, true)
	if err != nil {
		return fileListResult{}, err
	}
	if excludedPath(clean) {
		return fileListResult{}, errors.New("path is excluded")
	}
	virtual := false
	if clean != "." {
		info, componentErr := w.checkComponents(clean, false)
		if componentErr != nil {
			virtual = w.hasStagedDescendant(clean)
			if !virtual {
				return fileListResult{}, componentErr
			}
		} else if !info.IsDir() {
			return fileListResult{}, errors.New("path is not a directory")
		}
	}
	files := make(map[string]struct{})
	count, truncated := 0, false
	var walk func(string) error
	walk = func(dir string) error {
		entries, more, err := readDir(w.root, dir, maxEntries-count)
		if err != nil {
			return err
		}
		count += len(entries)
		for _, entry := range entries {
			name := entry.Name()
			rel := name
			if dir != "." {
				rel = filepath.Join(dir, name)
			}
			if excludedPath(rel) {
				continue
			}
			info, err := w.root.Lstat(rel)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				if err := walk(rel); err != nil {
					return err
				}
				if truncated {
					return nil
				}
				continue
			}
			if info.Mode().IsRegular() {
				files[filepath.ToSlash(rel)] = struct{}{}
			}
		}
		if more {
			truncated = true
		}
		return nil
	}
	if !virtual {
		if clean == "." {
			err = walk(".")
		} else {
			err = walk(clean)
		}
		if err != nil {
			return fileListResult{}, err
		}
	}
	for name := range w.staged {
		if clean == "." || name == clean || strings.HasPrefix(name, clean+string(os.PathSeparator)) {
			files[filepath.ToSlash(name)] = struct{}{}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	result := fileListResult{Files: make([]string, 0, len(names)), Truncated: truncated}
	encoded, _ := json.Marshal(result)
	responseSize := len(encoded)
	for _, name := range names {
		encodedName, _ := json.Marshal(name)
		additional := len(encodedName)
		if len(result.Files) > 0 {
			additional++
		}
		truncationByte := 0
		if !result.Truncated {
			truncationByte = 1
		}
		if responseSize+additional+truncationByte > maxResponse {
			result.Truncated = true
			break
		}
		result.Files = append(result.Files, name)
		responseSize += additional
	}
	return result, nil
}

func (w *Workspace) hasStagedDescendant(path string) bool {
	prefix := filepath.ToSlash(path) + "/"
	for name := range w.staged {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Read returns file content for the requested one-based inclusive line range.
func (w *Workspace) Read(path string, start, end int) (readResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return readResult{}, err
	}
	clean, err := validatePath(path, false)
	if err != nil {
		return readResult{}, err
	}
	if excludedPath(clean) {
		return readResult{}, errors.New("path is excluded")
	}
	if start < 0 || end < 0 || (end > 0 && start > end) {
		return readResult{}, errors.New("invalid line range")
	}
	content, staged := w.overlay(clean)
	if !staged {
		content, _, err = w.readDisk(clean)
		if err != nil {
			return readResult{}, err
		}
	}
	lines := strings.Split(string(content), "\n")
	first, last := 1, len(lines)
	if start > 0 {
		first = start
	}
	if end > 0 {
		last = end
	}
	if first > len(lines) {
		return readResult{}, fmt.Errorf("start_line %d exceeds file line count %d", first, len(lines))
	}
	if last > len(lines) {
		last = len(lines)
	}
	selected := strings.Join(lines[first-1:last], "\n")
	result, truncated, err := boundedRead(filepath.ToSlash(clean), selected, first, last)
	if err != nil {
		return readResult{}, err
	}
	if !staged && start == 0 && end == 0 && !truncated {
		w.readFull[filepath.ToSlash(clean)] = append([]byte(nil), content...)
	}
	return result, nil
}

// Search returns bounded literal matches from the workspace and staged overlay.
func (w *Workspace) Search(query, path string) (searchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return searchResult{}, err
	}
	if !nonEmpty(query) {
		return searchResult{}, errors.New("query must not be empty")
	}
	clean, err := validatePath(path, true)
	if err != nil {
		return searchResult{}, err
	}
	if excludedPath(clean) {
		return searchResult{}, errors.New("path is excluded")
	}
	files := make(map[string]struct{})
	truncated := false
	explicitFile := false
	if clean != "." {
		if _, isStagedFile := w.staged[clean]; isStagedFile {
			files[clean] = struct{}{}
			explicitFile = true
		} else {
			info, componentErr := w.checkComponents(clean, false)
			if componentErr != nil {
				if !w.hasStagedDescendant(clean) {
					return searchResult{}, componentErr
				}
			} else if info.Mode().IsRegular() {
				files[clean] = struct{}{}
				explicitFile = true
			} else if info.IsDir() {
				truncated, err = w.collectFiles(clean, files)
				if err != nil {
					return searchResult{}, err
				}
			} else {
				return searchResult{}, errors.New("search path is not a regular file or directory")
			}
		}
	} else {
		truncated, err = w.collectFiles(".", files)
		if err != nil {
			return searchResult{}, err
		}
	}
	for name := range w.staged {
		if clean == "." || name == clean || strings.HasPrefix(name, clean+"/") {
			files[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	result := searchResult{Truncated: truncated}
	for _, name := range names {
		if excludedPath(name) {
			continue
		}
		data, staged := w.overlay(name)
		if !staged {
			data, _, err = w.readDisk(name)
			if err != nil {
				if errors.Is(err, errUnsupportedContent) || errors.Is(err, errUnsupportedSize) {
					if explicitFile {
						return searchResult{}, err
					}
					result.SkippedFiles++
					continue
				}
				return searchResult{}, err
			}
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxFileBytes+1)
		for line := 1; scanner.Scan(); line++ {
			text := scanner.Text()
			if strings.Contains(text, query) {
				clipped, _ := bounded(text)
				result.Matches = append(result.Matches, searchMatch{Path: filepath.ToSlash(name), Line: line, Text: clipped})
				if len(result.Matches) >= maxMatches {
					result.Matches, _ = trimMatches(result.Matches)
					result.Truncated = true
					return result, nil
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return searchResult{}, err
		}
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > maxResponse {
		result.Matches, _ = trimMatches(result.Matches)
		result.Truncated = true
	}
	return result, nil
}

const readDirBatchSize = 256

// readDir reads at most limit entries, in bounded batches, and reports whether
// additional entries were present.
func readDir(root *os.Root, path string, limit int) (entries []os.DirEntry, more bool, err error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
			entries = nil
		}
	}()
	for len(entries) < limit {
		batch := min(readDirBatchSize, limit-len(entries))
		part, readErr := file.ReadDir(batch)
		entries = append(entries, part...)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return nil, false, readErr
			}
			return entries, false, nil
		}
	}
	probe, readErr := file.ReadDir(1)
	if len(probe) != 0 {
		return entries, true, nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, false, readErr
	}
	return entries, false, nil
}

func (w *Workspace) collectFiles(dir string, files map[string]struct{}) (bool, error) {
	count := 0
	var walk func(string) (bool, error)
	walk = func(path string) (bool, error) {
		entries, more, err := readDir(w.root, path, maxEntries-count)
		if err != nil {
			return false, err
		}
		count += len(entries)
		for _, entry := range entries {
			rel := entry.Name()
			if path != "." {
				rel = filepath.Join(path, rel)
			}
			if excludedPath(rel) {
				continue
			}
			info, err := w.root.Lstat(rel)
			if err != nil {
				return false, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if info.IsDir() {
				truncated, err := walk(rel)
				if err != nil || truncated {
					return truncated, err
				}
			} else if info.Mode().IsRegular() {
				files[rel] = struct{}{}
			}
		}
		return more, nil
	}
	return walk(dir)
}

func trimMatches(matches []searchMatch) ([]searchMatch, bool) {
	for len(matches) > 0 {
		data, _ := json.Marshal(searchResult{Matches: matches})
		if len(data) <= maxResponse-64 {
			return matches, true
		}
		matches = matches[:len(matches)-1]
	}
	return matches, true
}

// Stage records a complete Markdown replacement without changing project files.
func (w *Workspace) Stage(path, content string) (map[string]any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpen(); err != nil {
		return nil, err
	}
	clean, err := validatePath(path, false)
	if err != nil {
		return nil, err
	}
	clean = filepath.ToSlash(clean)
	if !allowedWrite(clean) || excludedPath(clean) {
		return nil, errors.New("writes are allowed only to README.md, docs/*.md, and memory-bank/*.md")
	}
	data := []byte(content)
	if len(data) > maxFileBytes {
		return nil, fmt.Errorf("staged file exceeds %d-byte limit", maxFileBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("content must be UTF-8 without NUL bytes")
	}
	if prior, ok := w.staged[clean]; ok {
		if prior.exists && bytes.Equal(data, prior.original) {
			delete(w.staged, clean)
			return map[string]any{"path": clean, "staged": false}, nil
		}
		total := len(data)
		for name, change := range w.staged {
			if name != clean {
				total += len(change.content)
			}
		}
		if total > maxOverlayBytes {
			return nil, fmt.Errorf("overlay exceeds %d bytes", maxOverlayBytes)
		}
		prior.content = append([]byte(nil), data...)
		w.staged[clean] = prior
		return map[string]any{"path": clean, "staged": true}, nil
	}
	if w.stagedPathCollision(clean) {
		return nil, errors.New("staged path conflicts with an existing file or directory")
	}
	info, err := w.checkComponents(filepath.FromSlash(clean), true)
	if err != nil {
		return nil, err
	}
	var original []byte
	exists := info != nil
	mode := fs.FileMode(0644)
	if exists {
		original, info, err = w.readDisk(filepath.FromSlash(clean))
		if err != nil {
			return nil, err
		}
		if len(original) > maxResponse-512 {
			return nil, fmt.Errorf("cannot replace %s: its complete read exceeds the %d-byte response limit; inspect with line ranges and edit a smaller file", clean, maxResponse-512)
		}
		if authorized, ok := w.readFull[clean]; !ok || !bytes.Equal(authorized, original) {
			return nil, errors.New("read the complete, untruncated original file before staging a replacement")
		}
		mode = info.Mode().Perm()
	}
	if exists && bytes.Equal(data, original) {
		return map[string]any{"path": clean, "staged": false}, nil
	}
	change := stagedFile{original: append([]byte(nil), original...), exists: exists, mode: mode, content: append([]byte(nil), data...)}
	w.staged[clean] = change
	if err := w.checkOverlayLimits(); err != nil {
		delete(w.staged, clean)
		return nil, err
	}
	return map[string]any{"path": clean, "staged": true}, nil
}

func (w *Workspace) stagedPathCollision(path string) bool {
	prefix := path + "/"
	for name := range w.staged {
		if strings.HasPrefix(name, prefix) || strings.HasPrefix(path, name+"/") {
			return true
		}
	}
	return false
}
func (w *Workspace) checkOverlayLimits() error {
	if len(w.staged) > maxOverlayFiles {
		return fmt.Errorf("overlay exceeds %d files", maxOverlayFiles)
	}
	total := 0
	for _, change := range w.staged {
		total += len(change.content)
	}
	if total > maxOverlayBytes {
		return fmt.Errorf("overlay exceeds %d bytes", maxOverlayBytes)
	}
	return nil
}
