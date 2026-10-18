package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	listTool   = "project_list_files"
	readTool   = "project_read_file"
	searchTool = "project_search_files"
	writeTool  = "project_write_file"
)

func toolDefinitions() []mcp.Tool {
	return []mcp.Tool{
		{Name: listTool, Description: "List a filtered project view including staged files. Intentionally excludes .git, .env*, sensitive names, third_party, vendor, node_modules, build, dist and target. An omitted path is NOT proof it is absent from disk. Listings can include binary files unsupported by text search.", InputSchema: objectSchema(map[string]any{"path": stringProperty("Directory path relative to the workspace root.")}, nil)},
		{Name: readTool, Description: "Read raw UTF-8 project content, optionally selecting a one-based line range. Content has no line prefixes: use project_search_files to obtain exact line numbers for citations instead of guessing offsets.", InputSchema: objectSchema(map[string]any{"path": stringProperty("File path relative to the workspace root."), "start_line": integerProperty("First line, one-based."), "end_line": integerProperty("Last line, one-based, inclusive.")}, []string{"path"})},
		{Name: searchTool, Description: "Search literal text and return exact one-based line numbers for citations. Recursive search skips binary or oversized files above 1 MiB and reports skipped_files; truncated marks incomplete matches or traversal. Staged changes are included.", InputSchema: objectSchema(map[string]any{"query": stringProperty("Literal text to search for."), "path": stringProperty("Optional directory or file path relative to the workspace root.")}, []string{"query"})},
		{Name: writeTool, Description: "Stage a full replacement for an allowed Markdown file; changes require local review and explicit apply.", InputSchema: objectSchema(map[string]any{"path": stringProperty("Allowed Markdown path relative to the workspace root."), "content": stringProperty("Complete replacement UTF-8 file content.")}, []string{"path", "content"})},
	}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": description}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}

}

type toolArgs struct {
	Path      string `json:"path"`
	Query     string `json:"query"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// CallTool validates and executes one of the four exposed workspace tools.
func (w *Workspace) CallTool(ctx context.Context, name string, raw json.RawMessage) (*mcp.CallToolResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if len(raw) == 0 || !json.Valid(raw) || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("tool arguments must be a valid JSON object")
	}
	required := map[string][]string{
		listTool:   nil,
		readTool:   {"path"},
		searchTool: {"query"},
		writeTool:  {"path", "content"},
	}
	allowed := map[string]map[string]bool{
		listTool:   {"path": true},
		readTool:   {"path": true, "start_line": true, "end_line": true},
		searchTool: {"query": true, "path": true},
		writeTool:  {"path": true, "content": true},
	}
	if _, ok := allowed[name]; !ok {
		return nil, fmt.Errorf("unknown workspace tool %q", name)
	}
	for key := range object {
		if !allowed[name][key] {
			return nil, fmt.Errorf("unknown argument %q for tool %q", key, name)
		}
	}
	for _, key := range required[name] {
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("tool %q requires argument %q", name, key)
		}
	}
	for key, value := range object {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil || decoded == nil {
			return nil, fmt.Errorf("tool argument %q has an invalid value", key)
		}
		switch key {
		case "path", "query", "content":
			if _, ok := decoded.(string); !ok {
				return nil, fmt.Errorf("tool argument %q must be a string", key)
			}
		case "start_line", "end_line":
			number, ok := decoded.(float64)
			if !ok || number < 1 || math.Trunc(number) != number {
				return nil, fmt.Errorf("tool argument %q must be a positive integer", key)
			}
		}
	}
	var args toolArgs
	if json.Unmarshal(raw, &args) != nil {
		return nil, fmt.Errorf("tool arguments have invalid field types")
	}
	var value any
	var err error
	switch name {
	case listTool:
		value, err = w.List(args.Path)
	case readTool:
		value, err = w.Read(args.Path, args.StartLine, args.EndLine)
	case searchTool:
		value, err = w.Search(args.Query, args.Path)
	case writeTool:
		value, err = w.Stage(args.Path, args.Content)
	default:
		return nil, fmt.Errorf("unknown workspace tool %q", name)
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode workspace tool result: %w", err)
	}
	var structured map[string]any
	if err := json.Unmarshal(encoded, &structured); err != nil || structured == nil {
		return nil, fmt.Errorf("workspace tool result must encode as an object")
	}
	return &mcp.CallToolResult{
		StructuredContent: structured,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
	}, nil
}

func nonEmpty(value string) bool { return strings.TrimSpace(value) != "" }
