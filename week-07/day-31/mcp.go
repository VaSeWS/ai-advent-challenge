package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxProjectFiles = 120

type ProjectContext struct {
	Branch         string   `json:"branch"`
	Commit         string   `json:"commit"`
	Detached       bool     `json:"detached"`
	Files          []string `json:"files"`
	FilesTruncated bool     `json:"files_truncated"`
}

type gitBranchOutput struct {
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Detached bool   `json:"detached"`
}

type listFilesOutput struct {
	Files          []string `json:"files"`
	FilesTruncated bool     `json:"files_truncated"`
}

func projectContext(ctx context.Context, root string) (pc ProjectContext, retErr error) {
	executable, err := os.Executable()
	if err != nil {
		return ProjectContext{}, fmt.Errorf("find MCP client executable: %w", err)
	}
	command := exec.CommandContext(ctx, executable, "server", "-project", root)
	command.Env = withoutCredentials(os.Environ())
	client := mcp.NewClient(&mcp.Implementation{Name: "foodgram-context-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return ProjectContext{}, fmt.Errorf("initialize local MCP server: %w", err)
	}
	defer func() {
		if err := session.Close(); retErr == nil && err != nil {
			retErr = fmt.Errorf("close local MCP session: %w", err)
		}
	}()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return ProjectContext{}, fmt.Errorf("list MCP tools: %w", err)
	}
	available := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		available[tool.Name] = true
	}
	for _, name := range []string{"git_branch", "list_files"} {
		if !available[name] {
			return ProjectContext{}, fmt.Errorf("MCP server does not provide required tool %q", name)
		}
	}

	branchResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "git_branch", Arguments: map[string]any{}})
	if err != nil {
		return ProjectContext{}, fmt.Errorf("call MCP git_branch: %w", err)
	}
	if branchResult.IsError {
		return ProjectContext{}, fmt.Errorf("MCP git_branch failed: %s", toolErrorText(branchResult))
	}
	var branch gitBranchOutput
	if err := decodeToolResult(branchResult, &branch); err != nil {
		return ProjectContext{}, fmt.Errorf("decode MCP git_branch result: %w", err)
	}

	filesResult, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_files", Arguments: map[string]any{}})
	if err != nil {
		return ProjectContext{}, fmt.Errorf("call MCP list_files: %w", err)
	}
	if filesResult.IsError {
		return ProjectContext{}, fmt.Errorf("MCP list_files failed: %s", toolErrorText(filesResult))
	}
	var files listFilesOutput
	if err := decodeToolResult(filesResult, &files); err != nil {
		return ProjectContext{}, fmt.Errorf("decode MCP list_files result: %w", err)
	}
	return ProjectContext{
		Branch: branch.Branch, Commit: branch.Commit, Detached: branch.Detached,
		Files: files.Files, FilesTruncated: files.FilesTruncated,
	}, nil
}

func runMCPServer(ctx context.Context, root string) error {
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	if err != nil {
		return fmt.Errorf("resolve project root symlinks: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return fmt.Errorf("stat project root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("project root is not a directory: %s", resolvedRoot)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "foodgram-project-context", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "git_branch", Description: "Return the live Foodgram Git branch (or HEAD when detached), commit, and detached status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, gitBranchOutput, error) {
		result, err := gitBranch(ctx, resolvedRoot)
		return nil, result, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_files", Description: "Return up to 120 sorted source and documentation paths; files_truncated indicates whether more eligible files exist.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listFilesOutput, error) {
		files, truncated, err := projectFiles(resolvedRoot)
		return nil, listFilesOutput{Files: files, FilesTruncated: truncated}, err
	})
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("run MCP stdio server: %w", err)
	}
	return nil
}

func gitBranch(ctx context.Context, root string) (gitBranchOutput, error) {
	inside, err := gitOutput(ctx, root, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return gitBranchOutput{}, fmt.Errorf("validate Git repository: %w", err)
	}
	if strings.TrimSpace(inside) != "true" {
		return gitBranchOutput{}, errors.New("project root is not a Git working tree")
	}
	commit, err := gitOutput(ctx, root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return gitBranchOutput{}, fmt.Errorf("read Git commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return gitBranchOutput{}, errors.New("Git repository has no commit at HEAD")
	}
	branch, err := gitOutput(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return gitBranchOutput{}, fmt.Errorf("read Git branch: %w", err)
		}
		return gitBranchOutput{Branch: "HEAD", Commit: commit, Detached: true}, nil
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return gitBranchOutput{}, errors.New("Git returned an empty branch name")
	}
	return gitBranchOutput{Branch: branch, Commit: commit}, nil
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	gitArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", gitArgs...)
	command.Env = withoutCredentials(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

func projectFiles(root string) ([]string, bool, error) {
	files := make([]string, 0, maxProjectFiles)
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if excludedPath(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if !usefulProjectFile(rel) {
			return nil
		}
		if len(files) == maxProjectFiles {
			truncated = true
			return filepath.SkipAll
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("walk project source files: %w", err)
	}
	return files, truncated, nil
}

func excludedPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		name := strings.ToLower(part)
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "build" || name == "dist" || name == "coverage" || name == "generated" || name == "__pycache__" || name == "data" || name == "datasets" || name == "media" || name == "uploads" || name == "dump" || name == "dumps" {
			return true
		}
		if strings.Contains(name, "dump") {
			return true
		}
		if strings.Contains(name, "secret") || strings.Contains(name, "credential") || strings.Contains(name, "password") || strings.Contains(name, "token") || strings.Contains(name, "private_key") {
			return true
		}
		if strings.HasPrefix(name, ".env") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".p12") || strings.HasSuffix(name, ".pfx") || strings.HasSuffix(name, ".dump") || strings.HasSuffix(name, ".sqlite") || strings.HasSuffix(name, ".db") {
			return true
		}
	}
	return false
}

func usefulProjectFile(rel string) bool {
	base := strings.ToLower(filepath.Base(rel))
	if strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "password") || strings.Contains(base, "token") || strings.Contains(base, "private_key") {
		return false
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".rst", ".txt", ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".html", ".css", ".scss", ".vue", ".json", ".yml", ".yaml", ".toml", ".ini", ".sh", ".dockerfile":
		return true
	default:
		return false
	}
}

func withoutCredentials(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		name := strings.ToUpper(key)
		if strings.HasPrefix(name, "GIT_") || strings.Contains(name, "KEY") || strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "CREDENTIAL") || strings.Contains(name, "AUTH") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func decodeToolResult(result *mcp.CallToolResult, output any) error {
	if result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(encoded, output)
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(text.Text), output); err == nil {
				return nil
			}
		}
	}
	return errors.New("MCP tool returned no decodable structured content")
}

func toolErrorText(result *mcp.CallToolResult) string {
	var messages []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			messages = append(messages, text.Text)
		}
	}
	if len(messages) == 0 {
		return "unknown tool error"
	}
	return strings.Join(messages, "; ")
}
