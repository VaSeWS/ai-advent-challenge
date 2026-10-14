package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dbPath := flags.String("db", "agent.db", "SQLite database path")
	providerName := flags.String("provider", "groq", "LLM provider: groq, deepseek, or local")
	endpoint := flags.String("endpoint", "", "OpenAI-compatible chat completions endpoint")
	model := flags.String("model", "", "model identifier; local requires an absolute pinned snapshot path")
	contextWindow := flags.Int("context-window", 0, "model context window in tokens")
	maxTokens := flags.Int("max-tokens", 0, "main completion token limit")
	auxMaxTokens := flags.Int("aux-max-tokens", 0, "auxiliary completion token limit")
	thinkingBudget := flags.Int("thinking-budget", 0, "local main completion thinking token budget")
	requestTimeout := flags.Duration("request-timeout", 0, "completion request timeout")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	profile, err := providerProfile(*providerName)
	if err != nil {
		return err
	}
	provided := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if provided["endpoint"] {
		profile.Endpoint = *endpoint
	}
	if provided["model"] {
		profile.Model = *model
	}
	for _, option := range []struct {
		name  string
		value int
		dest  *int
	}{
		{name: "context-window", value: *contextWindow, dest: &profile.ContextWindow},
		{name: "max-tokens", value: *maxTokens, dest: &profile.MainMax},
		{name: "aux-max-tokens", value: *auxMaxTokens, dest: &profile.AuxiliaryMax},
	} {
		if provided[option.name] {
			if option.value <= 0 {
				return fmt.Errorf("-%s must be positive", option.name)
			}
			*option.dest = option.value
		}
	}
	if provided["thinking-budget"] {
		if profile.Name != "local" {
			return fmt.Errorf("-thinking-budget is only supported by the local provider")
		}
		if *thinkingBudget <= 0 {
			return fmt.Errorf("-thinking-budget must be positive")
		}
		if !profile.ThinkingEnabled {
			return fmt.Errorf("-thinking-budget requires provider thinking to be enabled")
		}
		if *thinkingBudget >= profile.MainMax {
			return fmt.Errorf("-thinking-budget must be less than the main completion token limit (%d)", profile.MainMax)
		}
		profile.ThinkingBudget = *thinkingBudget
	}
	if provided["request-timeout"] {
		if *requestTimeout <= 0 {
			return fmt.Errorf("-request-timeout must be positive")
		}
		profile.RequestTimeout = *requestTimeout
	}
	if profile.Name == "local" {
		if err := validateLocalSnapshotModel(profile.Model); err != nil {
			return err
		}
		if err := validateLocalEndpoint(profile.Endpoint); err != nil {
			return err
		}
	}
	var key string
	if profile.KeyEnv != "" {
		key = os.Getenv(profile.KeyEnv)
		if key == "" {
			return fmt.Errorf("%s environment variable is not set", profile.KeyEnv)
		}
	}

	store, err := OpenStore(*dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close store: %w", closeErr)
		}
	}()

	counter, err := newTokenCounter(profile)
	if err != nil {
		return fmt.Errorf("create token counter: %w", err)
	}
	client := NewOpenAICompatibleClient(profile, key)
	mcpManager, err := NewMCPManager(store)
	if err != nil {
		return fmt.Errorf("create MCP manager: %w", err)
	}
	defer func() {
		if closeErr := mcpManager.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close MCP sessions: %w", closeErr)
		}
	}()
	mcpFailures := mcpManager.ReconnectEnabled(context.Background())
	agent := NewAgent(store, client, counter, profile)
	agent.AttachMCPManager(mcpManager)

	program := tea.NewProgram(NewAppWithMCP(store, agent, profile, mcpManager, mcpFailures))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run terminal UI: %w", err)
	}
	return nil
}
