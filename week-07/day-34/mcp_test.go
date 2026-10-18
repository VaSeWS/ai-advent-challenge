package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPV3MigrationPreservesChatDataAndCreatesConfigTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTurn(SaveTurnInput{ChatID: chat.ID, BranchID: branch.ID, UserContent: "before migration", AssistantContent: "preserved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE mcp_servers`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer migrated.Close()
	var version int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version = %d, want 4", version)
	}
	lineage, err := migrated.Lineage(branch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage) != 2 || lineage[0].Content != "before migration" || lineage[1].Content != "preserved" {
		t.Fatalf("migrated chat lineage = %#v", lineage)
	}
	if err := migrated.AddMCPServer(MCPServerConfig{Name: "persisted", Executable: "/bin/server", Args: []string{"--mode", "stdio"}, Cwd: t.TempDir(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestMCPServerConfigsPersistWithoutEnvironmentSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := "mcp-env-secret-do-not-persist"
	t.Setenv("MCP_PRIVATE_TEST_VALUE", secret)
	cwd := t.TempDir()
	config := MCPServerConfig{Name: "lookup", Executable: "/usr/local/bin/lookup", Args: []string{"--stdio", "--mode", "questions"}, Cwd: cwd, Enabled: true}
	if err := store.AddMCPServer(config); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(config); !errors.Is(err, ErrMCPServerExists) {
		t.Fatalf("duplicate config error = %v, want ErrMCPServerExists", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), secret) {
		t.Fatal("process environment value was persisted in the database")
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	configs, err := reopened.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Name != config.Name || configs[0].Executable != config.Executable || configs[0].Cwd != cwd || !configs[0].Enabled || len(configs[0].Args) != 3 || configs[0].Args[2] != "questions" {
		t.Fatalf("persisted configs = %#v", configs)
	}
	if err := reopened.SetMCPServerEnabled("lookup", false); err != nil {
		t.Fatal(err)
	}
	if err := reopened.RemoveMCPServer("lookup"); err != nil {
		t.Fatal(err)
	}
	if configs, err := reopened.MCPServers(); err != nil || len(configs) != 0 {
		t.Fatalf("configs after remove = %#v, %v", configs, err)
	}
	if err := reopened.RemoveMCPServer("missing"); !errors.Is(err, ErrMCPServerNotFound) {
		t.Fatalf("missing remove error = %v, want ErrMCPServerNotFound", err)
	}
}

func TestMCPStdioFixtureProcess(t *testing.T) {
	if os.Getenv("MCP_STDIO_FIXTURE") != "1" {
		return
	}
	name := filepath.Base(mustGetwd(t))
	toolName := name + "_tool"
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "test"}, &mcp.ServerOptions{PageSize: 1})
	mcp.AddTool(server, &mcp.Tool{Name: toolName, Description: "formats one value for fixture verification"}, func(_ context.Context, request *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, any, error) {
		if _, ok := args["precision"]; ok {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(request.Params.Arguments)}}}, nil, nil
		}
		value, ok := args["value"].(string)
		if !ok {
			return nil, nil, errors.New("value must be text")
		}
		if value == "change-tools" && os.Getenv("MCP_STDIO_LIVE_CHANGE") == "1" {
			mcp.AddTool(server, &mcp.Tool{Name: "changed_tool", Description: "added after connect"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{}, nil, nil
			})
		}
		if value == "hang" && os.Getenv("MCP_STDIO_HANG_MARKER") != "" {
			if err := os.WriteFile(os.Getenv("MCP_STDIO_HANG_MARKER"), []byte("started"), 0o600); err != nil {
				return nil, nil, err
			}
			select {}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name + ":" + strings.ToUpper(value)}}}, nil, nil
	})
	if os.Getenv("MCP_STDIO_PAGINATED") == "1" {
		mcp.AddTool(server, &mcp.Tool{Name: "second_tool", Description: "forces a second tools/list page"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	startupChangeSent := false
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, request)
			if method == "tools/list" && os.Getenv("MCP_STDIO_STARTUP_CHANGE") == "1" && !startupChangeSent {
				startupChangeSent = true
				mcp.AddTool(server, &mcp.Tool{Name: "startup_changed_tool", Description: "added during initial discovery"}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
					return &mcp.CallToolResult{}, nil, nil
				})
				time.Sleep(50 * time.Millisecond)
			}
			return result, err
		}
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatalf("serve stdio fixture: %v", err)
	}
}

func TestMCPManagerReconnectsDiscoversAndRoutesStdioTools(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	path := filepath.Join(t.TempDir(), "manager.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		cwd := filepath.Join(base, name)
		if err := os.Mkdir(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
		config := MCPServerConfig{Name: name, Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd, Enabled: true}
		if err := store.AddMCPServer(config); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "00-broken", Executable: filepath.Join(base, "missing-executable"), Cwd: base, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	failures := manager.ReconnectEnabled(context.Background())
	if len(failures) != 1 || failures[0].ServerName != "00-broken" {
		t.Fatalf("reconnect failures = %#v, want only 00-broken", failures)
	}
	tools := manager.Tools()
	if len(tools) != 2 || tools[0].Definition.Name != "alpha_tool" || tools[0].ServerName != "alpha" || tools[1].Definition.Name != "beta_tool" || tools[1].ServerName != "beta" {
		t.Fatalf("discovered tools = %#v", tools)
	}
	for _, tc := range []struct {
		name string
		want string
	}{{"alpha_tool", "alpha:INPUT"}, {"beta_tool", "beta:INPUT"}} {
		raw, _ := json.Marshal(map[string]string{"value": "input"})
		result, err := manager.CallTool(context.Background(), tc.name, raw)
		if err != nil {
			t.Fatalf("call %s: %v", tc.name, err)
		}
		if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != tc.want {
			t.Fatalf("call %s result = %#v, want %q", tc.name, result, tc.want)
		}
	}
	precision := json.RawMessage(`{"precision":9007199254740993}`)
	precisionResult, err := manager.CallTool(context.Background(), "alpha_tool", precision)
	if err != nil {
		t.Fatalf("call with large integer: %v", err)
	}
	if got := precisionResult.Content[0].(*mcp.TextContent).Text; got != string(precision) {
		t.Fatalf("large integer arguments = %q, want exact %q", got, precision)
	}
	invalidResult, invalidErr := manager.CallTool(context.Background(), "alpha_tool", json.RawMessage(`{"unexpected":"input"}`))
	if invalidErr == nil && (invalidResult == nil || !invalidResult.IsError) {
		t.Fatal("tool call with invalid fixture arguments was not reported as a failure")
	}
	if _, err := manager.CallTool(context.Background(), "missing_tool", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown tool call succeeded")
	}
	for _, malformed := range []json.RawMessage{nil, json.RawMessage(`[`), json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{} {}`)} {
		if _, err := manager.CallTool(context.Background(), "alpha_tool", malformed); err == nil {
			t.Fatalf("malformed tool arguments %q succeeded", malformed)
		}
	}
	disconnected := make(chan error, 1)
	go func() { disconnected <- manager.Disconnect("alpha") }()
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatalf("disconnect alpha after completed calls: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Disconnect blocked after completed successful and errored calls")
	}
	if got := manager.Tools(); len(got) != 1 || got[0].ServerName != "beta" {
		t.Fatalf("tools after disconnect = %#v, want beta only", got)
	}
	if err := manager.Connect(context.Background(), "alpha"); err != nil {
		t.Fatalf("reconnect alpha: %v", err)
	}
	if _, err := manager.CallTool(context.Background(), "alpha_tool", json.RawMessage(`{"value":"input"}`)); err != nil {
		t.Fatalf("call alpha before close: %v", err)
	}
	invalidResult, invalidErr = manager.CallTool(context.Background(), "alpha_tool", json.RawMessage(`{"unexpected":"input"}`))
	if invalidErr == nil && (invalidResult == nil || !invalidResult.IsError) {
		t.Fatal("errored tool call before close was not reported as a failure")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close manager after completed calls: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked after completed successful and errored calls")
	}
	if got := manager.Tools(); len(got) != 0 {
		t.Fatalf("tools after close = %#v, want none", got)
	}
	restarted, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if failures := restarted.ReconnectEnabled(context.Background()); len(failures) != 1 || failures[0].ServerName != "00-broken" {
		t.Fatalf("restart reconnect failures = %#v, want only 00-broken", failures)
	}
	if got := restarted.Tools(); len(got) != 2 || got[0].Definition.Name != "alpha_tool" || got[1].Definition.Name != "beta_tool" {
		t.Fatalf("tools after manager restart = %#v", got)
	}
	if err := restarted.Close(); err != nil {
		t.Fatalf("close restarted manager: %v", err)
	}

}
func TestMCPManagerTimesOutHungStartupAndContinuesReconnect(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "startup-timeout.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hungCwd := filepath.Join(base, "hung")
	healthyCwd := filepath.Join(base, "healthy")
	for _, cwd := range []string{hungCwd, healthyCwd} {
		if err := os.Mkdir(cwd, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "00-hung", Executable: "/bin/sleep", Args: []string{"30"}, Cwd: hungCwd, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "healthy", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: healthyCwd, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	manager.connectTimeout = time.Second
	defer manager.Close()

	failures := manager.ReconnectEnabled(context.Background())
	if len(failures) != 1 || failures[0].ServerName != "00-hung" {
		t.Fatalf("reconnect failures = %#v, want only timed-out 00-hung", failures)
	}
	configs, err := store.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 2 || configs[0].Name != "00-hung" || configs[1].Name != "healthy" || !configs[0].Enabled || !configs[1].Enabled {
		t.Fatalf("saved enabled configurations after timeout = %#v", configs)
	}
	if got := manager.Tools(); len(got) != 1 || got[0].ServerName != "healthy" || got[0].Definition.Name != "healthy_tool" {
		t.Fatalf("tools after reconnect = %#v, want healthy sibling only", got)
	}
}

func TestMCPManagerRejectsDuplicateToolNamesWithoutReplacingOwner(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "duplicates.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "first", "shared"), filepath.Join(root, "second", "shared")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range []MCPServerConfig{
		{Name: "first", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: filepath.Join(root, "first", "shared"), Enabled: true},
		{Name: "second", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: filepath.Join(root, "second", "shared"), Enabled: true},
	} {
		if err := store.AddMCPServer(config); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	failures := manager.ReconnectEnabled(context.Background())
	if len(failures) != 1 || failures[0].ServerName != "second" || !strings.Contains(failures[0].Error(), "duplicate MCP tool name") {
		t.Fatalf("duplicate reconnect failures = %#v", failures)
	}
	tools := manager.Tools()
	if len(tools) != 1 || tools[0].ServerName != "first" {
		t.Fatalf("tool registry after duplicate = %#v", tools)
	}
	configs, err := store.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	if !configs[1].Enabled {
		t.Fatal("failed duplicate reconnect changed the saved enabled state")
	}
}

func TestMCPManagerDiscoversToolsAcrossPages(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	t.Setenv("MCP_STDIO_PAGINATED", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "pages.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := filepath.Join(t.TempDir(), "pages")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "pages", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Connect(context.Background(), "pages"); err != nil {
		t.Fatal(err)
	}
	tools := manager.Tools()
	if len(tools) != 2 || tools[0].Definition.Name != "pages_tool" || tools[1].Definition.Name != "second_tool" {
		t.Fatalf("tools from paginated discovery = %#v", tools)
	}
}

func TestMCPManagerRefreshesToolsAfterListChanged(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	t.Setenv("MCP_STDIO_LIVE_CHANGE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "live.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "live", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Connect(context.Background(), "live"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CallTool(context.Background(), "live_tool", json.RawMessage(`{"value":"change-tools"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, tool := range manager.Tools() {
			if tool.Definition.Name == "changed_tool" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("tools/list_changed did not refresh the tool registry")
}
func TestMCPManagerIncludesToolChangesDuringStartupDiscovery(t *testing.T) {
	t.Setenv("MCP_STDIO_FIXTURE", "1")
	t.Setenv("MCP_STDIO_STARTUP_CHANGE", "1")
	store, err := OpenStore(filepath.Join(t.TempDir(), "startup-change.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cwd := filepath.Join(t.TempDir(), "startup")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddMCPServer(MCPServerConfig{Name: "startup", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Connect(context.Background(), "startup"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range manager.Tools() {
		if tool.Definition.Name == "startup_changed_tool" {
			return
		}
	}
	t.Fatal("startup tools/list_changed was not reflected in the registry")
}

func TestMCPManagerStopsInFlightCallsBeforeDisconnectOrRemove(t *testing.T) {
	for _, operation := range []string{"disconnect", "remove"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("MCP_STDIO_FIXTURE", "1")
			marker := filepath.Join(t.TempDir(), "call-started")
			t.Setenv("MCP_STDIO_HANG_MARKER", marker)
			store, err := OpenStore(filepath.Join(t.TempDir(), operation+".sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cwd := filepath.Join(t.TempDir(), "stuck")
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AddMCPServer(MCPServerConfig{Name: "stuck", Executable: executable, Args: []string{"-test.run=^TestMCPStdioFixtureProcess$"}, Cwd: cwd}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewMCPManager(store)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if err := manager.Connect(context.Background(), "stuck"); err != nil {
				t.Fatal(err)
			}
			callResult := make(chan error, 1)
			go func() {
				_, err := manager.CallTool(context.Background(), "stuck_tool", json.RawMessage(`{"value":"hang"}`))
				callResult <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fixture did not start the stuck tool call")
				}
				time.Sleep(10 * time.Millisecond)
			}
			stopped := make(chan error, 1)
			go func() {
				if operation == "disconnect" {
					stopped <- manager.Disconnect("stuck")
					return
				}
				stopped <- manager.Remove("stuck")
			}()
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("%s stuck server: %v", operation, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("%s blocked on an in-flight tool call", operation)
			}
			select {
			case <-callResult:
			case <-time.After(3 * time.Second):
				t.Fatal("in-flight tool call did not stop after session detach")
			}
		})
	}
}

func TestMCPManagerCloseCancelsHungConnect(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "hung.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddMCPServer(MCPServerConfig{Name: "hung", Executable: "/bin/sleep", Args: []string{"30"}, Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		t.Fatal(err)
	}
	connectResult := make(chan error, 1)
	go func() { connectResult <- manager.Connect(context.Background(), "hung") }()
	time.Sleep(100 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close manager: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked while Connect was waiting for MCP initialization")
	}
	select {
	case err := <-connectResult:
		if err == nil {
			t.Fatal("hung connection unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Connect did not stop after manager Close")
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return cwd
}
