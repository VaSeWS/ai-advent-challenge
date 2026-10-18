package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}
type mcpToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

func runMCP(userID string, in io.Reader, out io.Writer, data crm) error {
	if _, ok := findUser(data, userID); !ok {
		return errors.New("unknown bound user")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	w := bufio.NewWriter(out)
	defer w.Flush()
	write := func(v any) error {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			return err
		}
		return w.Flush()
	}
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if e := write(rpcResponse{"2.0", json.RawMessage("null"), nil, &rpcError{-32700, "Parse error"}}); e != nil {
				return e
			}
			continue
		}
		if req.JSONRPC != "2.0" {
			if len(req.ID) > 0 {
				if err := write(rpcResponse{"2.0", req.ID, nil, &rpcError{-32600, "Invalid Request"}}); err != nil {
					return err
				}
			}
			continue
		}
		if req.Method == "notifications/initialized" || strings.HasPrefix(req.Method, "notifications/") {
			continue
		}
		var result any
		var rpcErr *rpcError
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]string{"name": "iron-support-crm", "version": "1.0.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "get_user", "description": "Return the bound synthetic CRM user.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, map[string]any{"name": "get_ticket", "description": "Return one ticket only when it belongs to the bound user.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"ticket_id": map[string]string{"type": "string"}}, "required": []string{"ticket_id"}, "additionalProperties": false}}}}
		case "tools/call":
			var p toolCallParams
			if json.Unmarshal(req.Params, &p) != nil {
				rpcErr = &rpcError{-32602, "Invalid params"}
				break
			}
			var val any
			switch p.Name {
			case "get_user":
				if len(p.Arguments) != 0 {
					rpcErr = &rpcError{-32602, "Invalid params"}
					break
				}
				u, _ := findUser(data, userID)
				val = u
			case "get_ticket":
				id, ok := p.Arguments["ticket_id"].(string)
				if !ok || len(p.Arguments) != 1 {
					rpcErr = &rpcError{-32602, "Invalid params"}
					break
				}
				t, ok := findTicket(data, userID, id)
				if !ok {
					result = mcpToolResult{Content: []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}{{"text", "Ticket not found for bound user"}}, IsError: true}
					break
				}
				val = t
			default:
				rpcErr = &rpcError{-32602, "Unknown tool"}
			}
			if rpcErr == nil && val != nil {
				b, _ := json.Marshal(val)
				result = mcpToolResult{Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{{"text", string(b)}}}
			}
		default:
			rpcErr = &rpcError{-32601, "Method not found"}
		}
		if len(req.ID) > 0 {
			if err := write(rpcResponse{"2.0", req.ID, result, rpcErr}); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
func findUser(data crm, id string) (user, bool) {
	for _, u := range data.Users {
		if u.ID == id {
			return u, true
		}
	}
	return user{}, false
}
func findTicket(data crm, userID, ticketID string) (ticket, bool) {
	for _, t := range data.Tickets {
		if t.ID == ticketID && t.UserID == userID {
			return t, true
		}
	}
	return ticket{}, false
}
func (s *server) mcpContext(ctx context.Context, userID, ticketID string) (user, ticket, error) {
	_, ok := s.userByID(userID)
	if !ok {
		return user{}, ticket{}, errors.New("user not found")
	}
	if _, ok = s.ticketFor(userID, ticketID); !ok {
		return user{}, ticket{}, errors.New("ticket not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	cmd := exec.CommandContext(ctx, exe, "mcp", "-user", userID)
	cmd.Env = withoutProviderKey(os.Environ())
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	if err = cmd.Start(); err != nil {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(stdout)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		b, _ := json.Marshal(params)
		if err := enc.Encode(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: b}); err != nil {
			return nil, err
		}
		for {
			var response rpcResponse
			if err := dec.Decode(&response); err != nil {
				return nil, err
			}
			if string(response.ID) == fmt.Sprint(id) {
				if response.Error != nil {
					return nil, errors.New("MCP request failed")
				}
				return json.Marshal(response.Result)
			}
		}
	}
	if _, err = call(1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "iron-support", "version": "1"}}); err != nil {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	_ = enc.Encode(rpcRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	var ur mcpToolResult
	raw, err := call(2, "tools/call", map[string]any{"name": "get_user", "arguments": map[string]any{}})
	if err != nil || json.Unmarshal(raw, &ur) != nil || ur.IsError || len(ur.Content) != 1 {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	var gotUser user
	if json.Unmarshal([]byte(ur.Content[0].Text), &gotUser) != nil || gotUser.ID != userID {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	var tr mcpToolResult
	raw, err = call(3, "tools/call", map[string]any{"name": "get_ticket", "arguments": map[string]string{"ticket_id": ticketID}})
	if err != nil || json.Unmarshal(raw, &tr) != nil || tr.IsError || len(tr.Content) != 1 {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	var gotTicket ticket
	if json.Unmarshal([]byte(tr.Content[0].Text), &gotTicket) != nil || gotTicket.UserID != userID || gotTicket.ID != ticketID {
		return user{}, ticket{}, errors.New("MCP unavailable")
	}
	return gotUser, gotTicket, nil
}
func withoutProviderKey(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "DEEPSEEK_API_KEY" {
			out = append(out, entry)
		}
	}
	return out
}
