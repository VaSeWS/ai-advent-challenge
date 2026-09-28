package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrMCPServerExists reports that a server name is already configured.
	ErrMCPServerExists = errors.New("MCP server name already exists")
	// ErrMCPServerNotFound reports that a named server is not configured.
	ErrMCPServerNotFound = errors.New("MCP server not found")
)

// MCPServerConfig is a persistent local stdio server definition. Environment
// variables are deliberately absent: subprocesses inherit the host process
// environment and no environment contents are stored in SQLite.
type MCPServerConfig struct {
	Name       string
	Executable string
	// Args are passed literally to the executable and persisted verbatim;
	// they must not contain credentials or tokens.
	Args    []string
	Cwd     string
	Enabled bool
}

// AddMCPServer inserts a named stdio server definition. Duplicate names are
// rejected without changing the stored definition.
func (s *Store) AddMCPServer(config MCPServerConfig) error {
	if err := validateMCPServerConfig(config); err != nil {
		return err
	}
	args, err := json.Marshal(config.Args)
	if err != nil {
		return fmt.Errorf("encode MCP server arguments: %w", err)
	}
	now := databaseTime(time.Now())
	_, err = s.db.Exec(`INSERT INTO mcp_servers(name, executable, argv_json, cwd, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, config.Name, config.Executable, string(args), config.Cwd, boolInteger(config.Enabled), now, now)
	if err != nil {
		var count int
		if lookupErr := s.db.QueryRow(`SELECT COUNT(*) FROM mcp_servers WHERE name = ?`, config.Name).Scan(&count); lookupErr == nil && count > 0 {
			return fmt.Errorf("add MCP server: %w", ErrMCPServerExists)
		}
		return fmt.Errorf("add MCP server: %w", err)
	}
	return nil
}

// MCPServers lists configured stdio servers ordered by name.
func (s *Store) MCPServers() ([]MCPServerConfig, error) {
	rows, err := s.db.Query(`SELECT name, executable, argv_json, cwd, enabled FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	defer rows.Close()
	servers := make([]MCPServerConfig, 0)
	for rows.Next() {
		var config MCPServerConfig
		var argsJSON string
		var enabled int
		if err := rows.Scan(&config.Name, &config.Executable, &argsJSON, &config.Cwd, &enabled); err != nil {
			return nil, fmt.Errorf("scan MCP server: %w", err)
		}
		if err := json.Unmarshal([]byte(argsJSON), &config.Args); err != nil {
			return nil, fmt.Errorf("decode MCP server arguments: %w", err)
		}
		config.Enabled = enabled != 0
		servers = append(servers, config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	return servers, nil
}

// RemoveMCPServer deletes a named server definition.
func (s *Store) RemoveMCPServer(name string) error {
	result, err := s.db.Exec(`DELETE FROM mcp_servers WHERE name = ?`, strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("remove MCP server: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read MCP server removal: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("remove MCP server: %w", ErrMCPServerNotFound)
	}
	return nil
}

// SetMCPServerEnabled changes whether the server is connected on manager
// startup. It reports not found rather than silently creating a definition.
func (s *Store) SetMCPServerEnabled(name string, enabled bool) error {
	result, err := s.db.Exec(`UPDATE mcp_servers SET enabled = ?, updated_at = ? WHERE name = ?`, boolInteger(enabled), databaseTime(time.Now()), strings.TrimSpace(name))
	if err != nil {
		return fmt.Errorf("update MCP server: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read MCP server update: %w", err)
	} else if affected != 1 {
		return fmt.Errorf("update MCP server: %w", ErrMCPServerNotFound)
	}
	return nil
}

func validateMCPServerConfig(config MCPServerConfig) error {
	if strings.TrimSpace(config.Name) == "" {
		return errors.New("MCP server name is empty")
	}
	if config.Name != strings.TrimSpace(config.Name) {
		return errors.New("MCP server name must not have surrounding whitespace")
	}
	if strings.TrimSpace(config.Executable) == "" {
		return errors.New("MCP server executable is empty")
	}
	if strings.TrimSpace(config.Cwd) == "" {
		return errors.New("MCP server working directory is empty")
	}
	return nil
}
