package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/VaSeWS/ai-advent-challenge/week-07/day-34/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const projectServerName = "project"

type projectToolManager struct {
	manager   *MCPManager
	workspace *workspace.Workspace
}

func newProjectToolManager(manager *MCPManager, project *workspace.Workspace) (*projectToolManager, error) {
	combined := &projectToolManager{manager: manager, workspace: project}
	seen := make(map[string]string)
	if manager != nil {
		for _, tool := range manager.Tools() {
			seen[tool.Definition.Name] = tool.ServerName
		}
	}
	for _, definition := range project.Definitions() {
		if owner, duplicate := seen[definition.Name]; duplicate {
			return nil, fmt.Errorf("tool name %q is already provided by MCP server %q", definition.Name, owner)
		}
		if !isProjectFileTool(definition.Name) {
			return nil, fmt.Errorf("workspace exposed unexpected tool %q", definition.Name)
		}
		seen[definition.Name] = projectServerName
	}
	return combined, nil
}

func (m *projectToolManager) Tools() []MCPTool {
	var tools []MCPTool
	seen := make(map[string]struct{})
	if m.manager != nil {
		for _, tool := range m.manager.Tools() {
			if isProjectFileTool(tool.Definition.Name) {
				continue
			}
			seen[tool.Definition.Name] = struct{}{}
			tools = append(tools, tool)
		}
	}
	for _, definition := range m.workspace.Definitions() {
		if _, duplicate := seen[definition.Name]; duplicate {
			continue
		}
		seen[definition.Name] = struct{}{}
		tools = append(tools, MCPTool{ServerName: projectServerName, Definition: definition})
	}
	return tools
}

func (m *projectToolManager) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	if isProjectFileTool(name) {
		return m.workspace.CallTool(ctx, name, arguments)
	}
	if m.manager == nil {
		return nil, fmt.Errorf("MCP tool %q has no connected server", name)
	}
	return m.manager.CallTool(ctx, name, arguments)
}

func isProjectFileTool(name string) bool {
	switch name {
	case "project_list_files", "project_read_file", "project_search_files", "project_write_file":
		return true
	default:
		return false
	}
}

func serveProjectMCP(ctx context.Context, root string, in io.Reader, out io.Writer) error {
	project, err := workspace.New(root)
	if err != nil {
		return fmt.Errorf("open project workspace: %w", err)
	}
	defer project.Close()
	return project.Serve(ctx, in, out)
}
