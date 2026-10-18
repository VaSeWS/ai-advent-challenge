package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/VaSeWS/ai-advent-challenge/week-07/day-34/workspace"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "mcp" {
		flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		root := flags.String("root", "", "project root directory (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse MCP flags: %w", err)
		}
		if *root == "" || flags.NArg() != 0 {
			return fmt.Errorf("usage: agent mcp -root PATH")
		}
		return serveProjectMCP(context.Background(), *root, os.Stdin, os.Stdout)
	}

	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dbPath := flags.String("db", day34DBPath(), "SQLite database path")
	root := flags.String("root", "", "project root directory (required)")
	task := flags.String("task", "", "run one headless project task and print staged diff")
	providerName := flags.String("provider", "deepseek", "LLM provider: deepseek, groq, or local")
	endpoint := flags.String("endpoint", "", "OpenAI-compatible chat completions endpoint")
	model := flags.String("model", "", "model identifier; local requires an absolute pinned snapshot path")
	contextWindow := flags.Int("context-window", 0, "model context window in tokens")
	maxTokens := flags.Int("max-tokens", 0, "main completion token limit")
	auxMaxTokens := flags.Int("aux-max-tokens", 0, "auxiliary completion token limit")
	thinkingBudget := flags.Int("thinking-budget", 0, "local main completion thinking token budget")
	requestTimeout := flags.Duration("request-timeout", 0, "completion request timeout")
	rounds := flags.Int("tool-rounds", defaultToolContinuationRounds, "maximum tool continuation rounds (1-100)")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *root == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: agent -root PATH [-task GOAL]")
	}
	if *rounds < 1 || *rounds > maxToolContinuationRounds {
		return fmt.Errorf("-tool-rounds must be between 1 and %d", maxToolContinuationRounds)
	}

	project, err := workspace.New(*root)
	if err != nil {
		return fmt.Errorf("open project workspace: %w", err)
	}
	defer project.Close()

	profile, err := providerProfile(*providerName)
	if err != nil {
		return err
	}
	if profile.Name == "deepseek" {
		profile.MainMax = 8192
		profile.AuxiliaryMax = 4096
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
		if *thinkingBudget <= 0 || *thinkingBudget >= profile.MainMax || !profile.ThinkingEnabled {
			return fmt.Errorf("-thinking-budget must be positive, enabled, and less than the main completion token limit")
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
	defer store.Close()
	counter, err := newTokenCounter(profile)
	if err != nil {
		return fmt.Errorf("create token counter: %w", err)
	}
	manager, err := NewMCPManager(store)
	if err != nil {
		return fmt.Errorf("create MCP manager: %w", err)
	}
	defer manager.Close()
	failures := manager.ReconnectEnabled(context.Background())
	tools, err := newProjectToolManager(manager, project)
	if err != nil {
		return err
	}
	agent := NewAgent(store, NewOpenAICompatibleClient(profile, key), counter, profile)
	agent.maxToolRounds = *rounds
	agent.projectRoot = project.Root()
	agent.mcp = tools

	if *task != "" {
		chat, branch, err := store.ActiveChat()
		if err != nil {
			return err
		}
		result, err := agent.Send(context.Background(), chat.ID, branch.ID, *task)
		for _, event := range result.ToolEvents {
			fmt.Fprintf(os.Stdout, "tool %s (%s) %s", event.Name, event.ServerName, event.Status)
			if event.Path != "" {
				fmt.Fprintf(os.Stdout, " path=%s", event.Path)
			}
			fmt.Fprintln(os.Stdout)
		}
		if result.Assistant.Content != "" {
			fmt.Fprintln(os.Stdout, result.Assistant.Content)
		}
		diff, diffErr := project.Diff()
		if diffErr != nil {
			return diffErr
		}
		fmt.Fprintln(os.Stdout, "Staged diff (not applied):")
		fmt.Fprintln(os.Stdout, diff)
		if err != nil {
			return err
		}
		return nil
	}

	program := tea.NewProgram(NewAppWithProject(store, agent, profile, manager, failures, tools))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run terminal UI: %w", err)
	}
	return nil
}
func day34DBPath() string {
	cwd, err := os.Getwd()
	if err == nil && filepath.Base(cwd) == "day-34" {
		return "agent.db"
	}
	return filepath.Join("week-07", "day-34", "agent.db")
}
