package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPTool is a discovered tool together with the configured server that owns it.
type MCPTool struct {
	ServerName string
	Definition mcp.Tool
}

type mcpSession struct {
	session          *mcp.ClientSession
	tools            []MCPTool
	gate             sync.RWMutex
	refreshMu        sync.Mutex
	attempt          uint64
	changeVersion    atomic.Uint64
	refreshedVersion uint64
	ctx              context.Context
	cancel           context.CancelFunc
	processCancel    context.CancelFunc
}

type mcpConnectAttempt struct {
	id             uint64
	cancel         context.CancelFunc
	processCancel  context.CancelFunc
	pendingChanges uint64
}

// MCPConnectError identifies one saved server that could not reconnect.
type MCPConnectError struct {
	ServerName string
	Err        error
}

func (e MCPConnectError) Error() string {
	return fmt.Sprintf("connect MCP server %q: %v", e.ServerName, e.Err)
}

// MCPManager owns stdio sessions and routes calls to their discovered tools.
// It does not create shell commands: Executable and Args are passed directly
// to exec.CommandContext. Local servers inherit the host environment.
type MCPManager struct {
	store       *Store
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	closed      bool
	nextAttempt    uint64
	connectTimeout time.Duration
	connecting     map[string]mcpConnectAttempt
	sessions       map[string]*mcpSession
	tools          map[string]MCPTool
}

// NewMCPManager creates an empty manager. Call ReconnectEnabled during startup
// to reconnect saved enabled definitions and collect individual failures.
func NewMCPManager(store *Store) (*MCPManager, error) {
	if store == nil {
		return nil, errors.New("MCP manager requires a store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &MCPManager{
		store: store, ctx: ctx, cancel: cancel, connectTimeout: 10 * time.Second,
		connecting: make(map[string]mcpConnectAttempt),
		sessions:   make(map[string]*mcpSession), tools: make(map[string]MCPTool),
	}, nil
}

// Add persists a server definition. It does not execute it; Connect is an
// explicit local-user action.
func (m *MCPManager) Add(config MCPServerConfig) error {
	return m.store.AddMCPServer(config)
}

// ListServers returns persisted server definitions.
func (m *MCPManager) ListServers() ([]MCPServerConfig, error) {
	return m.store.MCPServers()
}

func (m *MCPManager) Remove(name string) error {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	if err := m.store.RemoveMCPServer(name); err != nil {
		m.mu.Unlock()
		return err
	}
	active := m.detachLocked(name)
	m.mu.Unlock()
	return closeMCPSession(name, active, true)
}

// Connect starts a configured executable directly (without a shell), discovers
// its tools, rejects name collisions, and marks the definition enabled.
func (m *MCPManager) Connect(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("MCP manager is closed")
	}
	if _, ok := m.sessions[name]; ok {
		m.mu.Unlock()
		return nil
	}
	if _, ok := m.connecting[name]; ok {
		m.mu.Unlock()
		return fmt.Errorf("MCP server %q is already connecting", name)
	}
	m.nextAttempt++
	attempt := m.nextAttempt
	m.connecting[name] = mcpConnectAttempt{id: attempt}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if current, ok := m.connecting[name]; ok && current.id == attempt {
			delete(m.connecting, name)
		}
		m.mu.Unlock()
	}()

	configs, err := m.store.MCPServers()
	if err != nil {
		return err
	}
	var config *MCPServerConfig
	for i := range configs {
		if configs[i].Name == name {
			config = &configs[i]
			break
		}
	}
	if config == nil {
		return fmt.Errorf("connect MCP server: %w", ErrMCPServerNotFound)
	}

	connectCtx, cancel := context.WithTimeout(ctx, m.connectTimeout)
	stopCancel := context.AfterFunc(m.ctx, cancel)
	m.mu.Lock()
	current, pending := m.connecting[name]
	if m.closed || !pending || current.id != attempt {
		m.mu.Unlock()
		stopCancel()
		cancel()
		return fmt.Errorf("MCP server %q connection was canceled", name)
	}
	current.cancel = cancel
	m.connecting[name] = current
	m.mu.Unlock()
	defer func() {
		stopCancel()
		cancel()
	}()

	processCtx, processCancel := context.WithCancel(m.ctx)
	keepProcess := false
	stopProcessOnConnectDone := context.AfterFunc(connectCtx, func() {
		m.mu.Lock()
		if current, ok := m.connecting[name]; ok && current.id == attempt {
			processCancel()
		}
		m.mu.Unlock()
	})
	defer func() {
		stopProcessOnConnectDone()
		if !keepProcess {
			processCancel()
		}
	}()
	m.mu.Lock()
	current, pending = m.connecting[name]
	if m.closed || !pending || current.id != attempt {
		m.mu.Unlock()
		processCancel()
		return fmt.Errorf("MCP server %q connection was canceled", name)
	}
	current.processCancel = processCancel
	m.connecting[name] = current
	m.mu.Unlock()
	command := exec.CommandContext(processCtx, config.Executable, config.Args...)
	command.Dir = config.Cwd
	client := mcp.NewClient(&mcp.Implementation{Name: "week-04-agent", Version: "1.0.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, request *mcp.ToolListChangedRequest) {
			if session, ok := request.GetSession().(*mcp.ClientSession); ok {
				m.notifyToolsChanged(name, attempt, session)
			}
		},
	})
	session, err := client.Connect(connectCtx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		return fmt.Errorf("start MCP server: %w", err)
	}
	discovered, err := listMCPTools(connectCtx, session, name)
	if err != nil {
		processCancel()
		_ = session.Close()
		return fmt.Errorf("discover MCP tools: %w", err)
	}

	// Re-list if the server changed its tool set while initial discovery was in
	// progress. Holding the manager lock across the version check and publication
	// ensures later notifications observe the published session.
	for {
		m.mu.Lock()
		current, pending := m.connecting[name]
		if m.closed || !pending || current.id != attempt {
			m.mu.Unlock()
			processCancel()
			_ = session.Close()
			return fmt.Errorf("MCP server %q connection was canceled", name)
		}
		version := current.pendingChanges
		if version == 0 {
			connectErr := connectCtx.Err()
			if connectErr != nil {
				m.mu.Unlock()
				processCancel()
				_ = session.Close()
				return fmt.Errorf("MCP server %q connection was canceled: %w", name, connectErr)
			}
			if err := checkToolCollisions(m.tools, discovered, name); err != nil {
				m.mu.Unlock()
				processCancel()
				_ = session.Close()
				return err
			}
			if err := m.store.SetMCPServerEnabled(name, true); err != nil {
				m.mu.Unlock()
				processCancel()
				_ = session.Close()
				return err
			}
			if connectErr := connectCtx.Err(); connectErr != nil {
				if !config.Enabled {
					if err := m.store.SetMCPServerEnabled(name, false); err != nil {
						connectErr = fmt.Errorf("%w (restore disabled configuration: %v)", connectErr, err)
					}
				}
				m.mu.Unlock()
				processCancel()
				_ = session.Close()
				return fmt.Errorf("MCP server %q connection was canceled: %w", name, connectErr)
			}
			callCtx, callCancel := context.WithCancel(context.Background())
			active := &mcpSession{session: session, tools: discovered, attempt: attempt, ctx: callCtx, cancel: callCancel, processCancel: processCancel}
			m.sessions[name] = active
			for _, tool := range discovered {
				m.tools[tool.Definition.Name] = tool
			}
			delete(m.connecting, name)
			stopProcessOnConnectDone()
			keepProcess = true
			m.mu.Unlock()
			return nil
		}
		current.pendingChanges = 0
		m.connecting[name] = current
		m.mu.Unlock()

		discovered, err = listMCPTools(connectCtx, session, name)
		if err != nil {
			processCancel()
			_ = session.Close()
			return fmt.Errorf("discover MCP tools: %w", err)
		}
	}
}

func listMCPTools(ctx context.Context, session *mcp.ClientSession, serverName string) ([]MCPTool, error) {
	var tools []MCPTool
	names := make(map[string]struct{})
	for definition, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		if definition == nil || definition.Name == "" {
			return nil, errors.New("MCP server advertised an empty tool name")
		}
		if _, duplicate := names[definition.Name]; duplicate {
			return nil, fmt.Errorf("MCP server %q advertised duplicate tool name %q", serverName, definition.Name)
		}
		names[definition.Name] = struct{}{}
		tools = append(tools, MCPTool{ServerName: serverName, Definition: *definition})
	}
	return tools, nil
}

func checkToolCollisions(existing map[string]MCPTool, discovered []MCPTool, serverName string) error {
	for _, tool := range discovered {
		if owner, duplicate := existing[tool.Definition.Name]; duplicate && owner.ServerName != serverName {
			return fmt.Errorf("duplicate MCP tool name %q from servers %q and %q", tool.Definition.Name, owner.ServerName, serverName)
		}
	}
	return nil
}

func (m *MCPManager) notifyToolsChanged(name string, attempt uint64, session *mcp.ClientSession) {
	m.mu.Lock()
	active := m.sessions[name]
	if !m.closed && active != nil && active.session == session && active.attempt == attempt {
		active.changeVersion.Add(1)
		m.mu.Unlock()
		go m.refreshTools(name, attempt, session)
		return
	}
	if !m.closed {
		if pending, ok := m.connecting[name]; ok && pending.id == attempt {
			pending.pendingChanges++
			m.connecting[name] = pending
		}
	}
	m.mu.Unlock()
}

func (m *MCPManager) refreshTools(name string, attempt uint64, session *mcp.ClientSession) {
	m.mu.Lock()
	active := m.sessions[name]
	if m.closed || active == nil || active.session != session || active.attempt != attempt {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	active.refreshMu.Lock()
	defer active.refreshMu.Unlock()
	version := active.changeVersion.Load()
	tools, err := listMCPTools(active.ctx, session, name)
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sessions[name] != active || active.session != session || active.attempt != attempt {
		return
	}
	others := make(map[string]MCPTool, len(m.tools))
	for toolName, tool := range m.tools {
		if tool.ServerName != name {
			others[toolName] = tool
		}
	}
	if err := checkToolCollisions(others, tools, name); err != nil {
		return
	}
	for toolName, tool := range m.tools {
		if tool.ServerName == name {
			delete(m.tools, toolName)
		}
	}
	active.tools = tools
	for _, tool := range tools {
		m.tools[tool.Definition.Name] = tool
	}
	active.refreshedVersion = version
}

// Disconnect stops a server and disables automatic reconnect. If persistence
// fails, its live session and discovered tools are left unchanged.
func (m *MCPManager) Disconnect(name string) error {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	if err := m.store.SetMCPServerEnabled(name, false); err != nil {
		m.mu.Unlock()
		return err
	}
	active := m.detachLocked(name)
	m.mu.Unlock()
	return closeMCPSession(name, active, true)
}

func (m *MCPManager) detachLocked(name string) *mcpSession {
	if pending, ok := m.connecting[name]; ok {
		if pending.cancel != nil {
			pending.cancel()
		}
		if pending.processCancel != nil {
			pending.processCancel()
		}
		delete(m.connecting, name)
	}
	active := m.sessions[name]
	if active == nil {
		return nil
	}
	if active.cancel != nil {
		active.cancel()
	}
	if active.processCancel != nil {
		active.processCancel()
	}
	delete(m.sessions, name)
	for _, tool := range active.tools {
		delete(m.tools, tool.Definition.Name)
	}
	return active
}

func closeMCPSession(name string, active *mcpSession, managerShutdown bool) error {
	if active == nil {
		return nil
	}
	active.gate.Lock()
	defer active.gate.Unlock()
	if err := active.session.Close(); err != nil {
		var exitErr *exec.ExitError
		if managerShutdown && errors.As(err, &exitErr) && exitErr.ProcessState != nil {
			if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signal() == syscall.SIGKILL {
				return nil
			}
		}
		return fmt.Errorf("close MCP server %q: %w", name, err)
	}
	return nil
}

// ReconnectEnabled attempts every saved enabled server. A failed server is
// reported in the returned slice and does not prevent attempts for others.
func (m *MCPManager) ReconnectEnabled(ctx context.Context) []MCPConnectError {
	configs, err := m.store.MCPServers()
	if err != nil {
		return []MCPConnectError{{Err: err}}
	}
	failures := make([]MCPConnectError, 0)
	for _, config := range configs {
		if !config.Enabled {
			continue
		}
		if err := m.Connect(ctx, config.Name); err != nil {
			failures = append(failures, MCPConnectError{ServerName: config.Name, Err: err})
		}
	}
	return failures
}

// Tools lists the currently discovered tools in deterministic name order.
func (m *MCPManager) Tools() []MCPTool {
	m.mu.Lock()
	stale := make([]struct {
		name    string
		attempt uint64
		session *mcp.ClientSession
	}, 0)
	for name, active := range m.sessions {
		if active.changeVersion.Load() != active.refreshedVersion {
			stale = append(stale, struct {
				name    string
				attempt uint64
				session *mcp.ClientSession
			}{name: name, attempt: active.attempt, session: active.session})
		}
	}
	m.mu.Unlock()
	for _, active := range stale {
		m.refreshTools(active.name, active.attempt, active.session)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tools := make([]MCPTool, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Definition.Name < tools[j].Definition.Name
	})
	return tools
}

// CallTool resolves an advertised name to its owning stdio session. arguments
// must be a single JSON object (use {} when the tool accepts no arguments).
func (m *MCPManager) CallTool(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	var object map[string]json.RawMessage
	if len(arguments) == 0 || !json.Valid(arguments) || json.Unmarshal(arguments, &object) != nil || object == nil {
		return nil, errors.New("MCP tool arguments must be a valid JSON object")
	}
	m.mu.Lock()
	tool, ok := m.tools[name]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown MCP tool %q", name)
	}
	active := m.sessions[tool.ServerName]
	if active == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP tool %q has no connected owner", name)
	}
	active.gate.RLock()
	if m.sessions[tool.ServerName] != active || m.tools[name].ServerName != tool.ServerName {
		active.gate.RUnlock()
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP tool %q is no longer connected", name)
	}
	m.mu.Unlock()
	defer active.gate.RUnlock()
	callCtx, cancel := context.WithCancel(ctx)
	stopManagerCancel := context.AfterFunc(m.ctx, cancel)
	stopSessionCancel := context.AfterFunc(active.ctx, cancel)
	defer func() {
		stopManagerCancel()
		stopSessionCancel()
		cancel()
	}()
	result, err := active.session.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("call MCP tool %q on %q: %w", name, tool.ServerName, err)
	}
	return result, nil
}

// Close shuts down every session and prevents further connections. All sessions
// are attempted even when one fails.
func (m *MCPManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	sessions := make(map[string]*mcpSession, len(m.sessions))
	for name, active := range m.sessions {
		if active.cancel != nil {
			active.cancel()
		}
		if active.processCancel != nil {
			active.processCancel()
		}
		sessions[name] = active
		delete(m.sessions, name)
	}
	clear(m.tools)
	clear(m.connecting)
	m.mu.Unlock()
	var failures []error
	for name, active := range sessions {
		if err := closeMCPSession(name, active, true); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
