package main

import (
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
	providerName := flags.String("provider", "groq", "LLM provider: groq or deepseek")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	profile, err := providerProfile(*providerName)
	if err != nil {
		return err
	}

	key := os.Getenv(profile.KeyEnv)
	if key == "" {
		return fmt.Errorf("%s environment variable is not set", profile.KeyEnv)
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
	agent := NewAgent(store, client, counter, profile)

	program := tea.NewProgram(NewApp(store, agent, profile))
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run terminal UI: %w", err)
	}
	return nil
}
