package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "server" {
		if len(os.Args) != 4 || os.Args[2] != "-project" || os.Args[3] == "" {
			fmt.Fprintln(os.Stderr, "usage: server -project ROOT")
			os.Exit(2)
		}
		if err := runMCPServer(context.Background(), os.Args[3]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func isolateGitEnvironment(t *testing.T, home string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_DIR", "")
	t.Setenv("GIT_WORK_TREE", "")
	t.Setenv("HOME", home)
}

func TestProjectContextReportsIsolatedGitBranchAndDetachedState(t *testing.T) {
	root := newGitProject(t)
	isolateGitEnvironment(t, root)
	writeProjectFile(t, root, "main.go", "package main\n")
	gitTestCommand(t, root, "add", "main.go")
	gitTestCommand(t, root, "commit", "-m", "initial")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	branch, err := projectContext(ctx, root)
	if err != nil {
		t.Fatalf("projectContext on branch: %v", err)
	}
	if branch.Branch != "main" || branch.Detached {
		t.Fatalf("branch context = branch %q detached %t; want main, attached", branch.Branch, branch.Detached)
	}
	if branch.Commit == "" {
		t.Fatal("branch context omitted commit")
	}

	gitTestCommand(t, root, "checkout", "--detach", "HEAD")
	detached, err := projectContext(ctx, root)
	if err != nil {
		t.Fatalf("projectContext on detached HEAD: %v", err)
	}
	if !detached.Detached {
		t.Fatalf("detached HEAD reported attached branch %q", detached.Branch)
	}
	if detached.Commit != branch.Commit {
		t.Fatalf("detached context commit %q differs from checked-out commit %q", detached.Commit, branch.Commit)
	}
}

func TestProjectContextIgnoresInheritedGitRepositoryOverrides(t *testing.T) {
	root := newGitProject(t)
	writeProjectFile(t, root, "root.go", "package selected\n")
	gitTestCommand(t, root, "add", "root.go")
	gitTestCommand(t, root, "commit", "-m", "selected repository")
	rootCommit := gitTestCommand(t, root, "rev-parse", "HEAD")

	other := newGitProject(t)
	writeProjectFile(t, other, "other.go", "package other\n")
	gitTestCommand(t, other, "add", "other.go")
	gitTestCommand(t, other, "commit", "-m", "other repository")
	otherCommit := gitTestCommand(t, other, "rev-parse", "HEAD")
	gitTestCommand(t, other, "checkout", "-b", "other-branch")

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	t.Setenv("GIT_NAMESPACE", "hostile")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := projectContext(ctx, root)
	if err != nil {
		t.Fatalf("projectContext with inherited Git overrides: %v", err)
	}
	if got.Branch != "main" || got.Detached {
		t.Errorf("branch context = branch %q detached %t; want selected repository main branch", got.Branch, got.Detached)
	}
	if got.Commit != rootCommit {
		t.Errorf("commit context = %q; want selected repository commit %q (other repo commit %q)", got.Commit, rootCommit, otherCommit)
	}
}

func TestProjectContextBoundsAndFiltersFilePaths(t *testing.T) {
	root := newGitProject(t)
	isolateGitEnvironment(t, root)
	for i := range 121 {
		writeProjectFile(t, root, fmt.Sprintf("src/file-%03d.go", i), "package src\n")
	}
	writeProjectFile(t, root, ".env", "credential-marker\n")
	writeProjectFile(t, root, "data/dump.json", "dataset-marker\n")
	writeProjectFile(t, root, "dump/export.json", "dump-marker\n")
	writeProjectFile(t, root, "dumps/export.txt", "dump-marker\n")
	writeProjectFile(t, root, "dump.json", "dump-marker\n")
	writeProjectFile(t, root, "dbdump.json", "dump-marker\n")
	writeProjectFile(t, root, "node_modules/pkg/index.js", "source-marker\n")
	writeProjectFile(t, root, "build/generated.go", "package generated\n")
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package escaped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "src", "escape.go")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	gitTestCommand(t, root, "add", "src")
	gitTestCommand(t, root, "commit", "-m", "file listing fixture")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := projectContext(ctx, root)
	if err != nil {
		t.Fatalf("projectContext: %v", err)
	}
	if len(got.Files) != 120 || !got.FilesTruncated {
		t.Fatalf("file listing length=%d truncated=%t; want 120 and explicit truncation", len(got.Files), got.FilesTruncated)
	}
	for _, name := range got.Files {
		if strings.Contains(name, "escape.go") || strings.Contains(name, ".env") || strings.Contains(name, "dump") || strings.Contains(name, "vendor") || strings.Contains(name, "node_modules") || strings.Contains(name, "build/") {
			t.Errorf("excluded or symlinked path exposed: %q", name)
		}
	}
	if got.Files[0] != "src/file-000.go" || got.Files[len(got.Files)-1] != "src/file-119.go" {
		t.Fatalf("bounded file list is not the stable sorted prefix: first=%q last=%q", got.Files[0], got.Files[len(got.Files)-1])
	}
	again, err := projectContext(ctx, root)
	if err != nil {
		t.Fatalf("second projectContext: %v", err)
	}
	if !reflect.DeepEqual(got.Files, again.Files) || got.FilesTruncated != again.FilesTruncated {
		t.Fatal("repeated file listing changed for an unchanged project")
	}
}

func TestMCPServerExposesLiveGitAndBoundedFilePaths(t *testing.T) {
	root := newGitProject(t)
	writeProjectFile(t, root, "README.md", "project documentation\n")
	writeProjectFile(t, root, "src/main.go", "package main\n")
	writeProjectFile(t, root, ".env", "must-not-be-exposed\n")
	gitTestCommand(t, root, "add", "README.md", "src/main.go", ".env")
	gitTestCommand(t, root, "commit", "-m", "fixture")
	rootCommit := gitTestCommand(t, root, "rev-parse", "HEAD")

	other := newGitProject(t)
	writeProjectFile(t, other, "other.go", "package other\n")
	gitTestCommand(t, other, "add", "other.go")
	gitTestCommand(t, other, "commit", "-m", "other fixture")
	gitTestCommand(t, other, "checkout", "-b", "other-branch")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPHelperProcess$", "--", "server", "-project", root)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DAY31_MCP_HELPER=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=0",
		"GIT_CONFIG_PARAMETERS=",
		"GIT_DIR=" + filepath.Join(other, ".git"),
		"GIT_WORK_TREE=" + other,
		"GIT_COMMON_DIR=" + filepath.Join(other, ".git"),
		"GIT_INDEX_FILE=" + filepath.Join(other, ".git", "index"),
		"GIT_NAMESPACE=hostile",
		"HOME=" + root,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start MCP helper: %v", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	writer := bufio.NewWriter(stdin)
	request := func(id int, method string, params any) map[string]json.RawMessage {
		t.Helper()
		message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
		if params != nil {
			message["params"] = params
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			t.Fatalf("write MCP request: %v", err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatalf("flush MCP request: %v", err)
		}
		for scanner.Scan() {
			var response map[string]json.RawMessage
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatalf("decode MCP response %q: %v", scanner.Text(), err)
			}
			var responseID int
			if raw := response["id"]; len(raw) != 0 && json.Unmarshal(raw, &responseID) == nil && responseID == id {
				if raw := response["error"]; len(raw) != 0 {
					t.Fatalf("MCP %s returned error: %s", method, raw)
				}
				return response
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("read MCP response: %v (stderr: %s)", err, stderr.String())
		}
		t.Fatalf("MCP server exited before replying to %s (stderr: %s)", method, stderr.String())
		return nil
	}

	initialize := request(1, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "day31-test", "version": "1"},
	})
	if len(initialize["result"]) == 0 {
		t.Fatal("MCP initialize returned no result")
	}
	if _, err := writer.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}

	list := request(2, "tools/list", map[string]any{})
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(list["result"], &listed); err != nil {
		t.Fatalf("decode tool list: %v", err)
	}
	if !containsMCPTool(listed.Tools, "git_branch") || !containsMCPTool(listed.Tools, "list_files") {
		t.Fatalf("server tools missing expected project context tools: %#v", listed.Tools)
	}

	branch := request(3, "tools/call", map[string]any{"name": "git_branch", "arguments": map[string]any{}})
	branchText := mcpResultText(t, branch)
	if !strings.Contains(branchText, "main") {
		t.Fatalf("git_branch result omitted live branch: %s", branchText)
	}
	if !strings.Contains(branchText, rootCommit) {
		t.Fatalf("git_branch result omitted selected repository commit %q: %s", rootCommit, branchText)
	}
	files := request(4, "tools/call", map[string]any{"name": "list_files", "arguments": map[string]any{}})
	filesText := mcpResultText(t, files)
	if !strings.Contains(filesText, "README.md") || !strings.Contains(filesText, "src/main.go") || strings.Contains(filesText, ".env") || strings.Contains(filesText, "must-not-be-exposed") {
		t.Fatalf("list_files did not return only useful paths: %s", filesText)
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("DAY31_MCP_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) != 3 || args[0] != "server" || args[1] != "-project" {
		fmt.Fprintln(os.Stderr, "invalid MCP helper arguments")
		os.Exit(2)
	}
	if err := runMCPServer(context.Background(), args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func newGitProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitTestCommand(t, root, "init", "-b", "main")
	gitTestCommand(t, root, "config", "user.name", "Day31 test")
	gitTestCommand(t, root, "config", "user.email", "day31@example.invalid")
	return root
}

func gitTestCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + root}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeProjectFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsMCPTool(tools []struct {
	Name string `json:"name"`
}, wanted string) bool {
	for _, tool := range tools {
		if tool.Name == wanted {
			return true
		}
	}
	return false
}

func mcpResultText(t *testing.T, response map[string]json.RawMessage) string {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatalf("decode MCP tool result: %v", err)
	}
	var parts []string
	for _, item := range result.Content {
		parts = append(parts, item.Text)
	}
	if len(result.StructuredContent) != 0 {
		parts = append(parts, string(result.StructuredContent))
	}
	return strings.Join(parts, "\n")
}
