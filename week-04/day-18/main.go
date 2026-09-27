package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type scheduleInput struct {
	Topic    string `json:"topic" jsonschema:"Optional hashtag topic filter such as #Go or #Python; empty means all topics"`
	Interval string `json:"interval" jsonschema:"Execution interval as a Go duration, for example 5s for a demo or 24h for daily"`
}

type scheduleOutput struct {
	ID       int       `json:"id"`
	Topic    string    `json:"topic"`
	Interval string    `json:"interval"`
	NextRun  time.Time `json:"next_run"`
}

type summaryOutput struct {
	RunningProcessOnly bool        `json:"running_process_only"`
	ScheduleCount      int         `json:"schedule_count"`
	ExecutionCount     int         `json:"execution_count"`
	QuestionCount      int         `json:"question_count"`
	Executions         []execution `json:"executions"`
}

func registerTools(server *mcp.Server, scheduler *scheduler) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "schedule_due_questions",
		Description: "Schedule recurring read-only scans of Markdown questions in the Obsidian vault by hashtag topic.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input scheduleInput) (*mcp.CallToolResult, scheduleOutput, error) {
		interval, err := time.ParseDuration(strings.TrimSpace(input.Interval))
		if err != nil || interval < time.Second {
			return nil, scheduleOutput{}, fmt.Errorf("interval must be a Go duration of at least 1s")
		}
		task, err := scheduler.add(input.Topic, interval, time.Now())
		if err != nil {
			return nil, scheduleOutput{}, err
		}
		return nil, scheduleOutput{ID: task.ID, Topic: task.Topic, Interval: task.Interval, NextRun: task.NextRun}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "scheduler_summary",
		Description: "Return aggregate schedule and execution counts plus recorded results.",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, summaryOutput, error) {
		state := scheduler.snapshot()
		questionCount := 0
		for _, run := range state.History {
			questionCount += run.Count
		}
		return nil, summaryOutput{
			RunningProcessOnly: true,
			ScheduleCount:      len(state.Schedules),
			ExecutionCount:     len(state.History),
			QuestionCount:      questionCount,
			Executions:         state.History,
		}, nil
	})
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("day-18", flag.ContinueOnError)
	mode := flags.String("mode", "server", "mode: server, schedule, or summary")
	listen := flags.String("listen", "127.0.0.1:8080", "HTTP MCP listen address or client target address")
	endpoint := flags.String("endpoint", "", "full HTTP MCP endpoint URL (overrides -listen in client modes)")
	topic := flags.String("topic", "", "hashtag topic to schedule, such as #Go")
	interval := flags.String("interval", "", "schedule interval as a Go duration, for example 2s")
	vault := flags.String("vault", defaultVault(), "read-only Obsidian Markdown directory (default: OBSIDIAN_VAULT or ~/Documents/Obsidian Vault/Interviews prep/Questions)")
	statePath := flags.String("state", "week-04/day-18/state.json", "JSON schedule and execution state path (or REVIEW_SCHEDULER_STATE)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *mode == "schedule" || *mode == "summary" {
		target := *endpoint
		if target == "" {
			target = "http://" + *listen
		}
		return runClient(ctx, *mode, target, *topic, *interval)
	}
	if *mode != "server" {
		return fmt.Errorf("unknown mode %q (want server, schedule, or summary)", *mode)
	}
	if *endpoint != "" || *topic != "" || *interval != "" {
		return errors.New("-endpoint, -topic, and -interval are only valid in client modes")
	}
	if configured := os.Getenv("REVIEW_SCHEDULER_STATE"); configured != "" && *statePath == "week-04/day-18/state.json" {
		*statePath = configured
	}
	vaultPath, err := filepath.Abs(*vault)
	if err != nil {
		return fmt.Errorf("resolve vault path: %w", err)
	}
	statePathAbs, err := filepath.Abs(*statePath)
	if err != nil {
		return fmt.Errorf("resolve state path: %w", err)
	}
	scheduler, err := newScheduler(vaultPath, statePathAbs)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "obsidian-due-scheduler", Version: "1.0.0"}, nil)
	registerTools(server, scheduler)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := scheduler.runDue(now); err != nil {
					log.Printf("scheduler tick failed: %v", err)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("MCP scheduler listening on http://%s; vault reads are read-only", *listen)
	log.Printf("Scheduler remains active while this process is running; state path: %s", statePathAbs)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve MCP endpoint: %w", err)
	}
	return nil
}

func runClient(ctx context.Context, mode, endpoint, topic, interval string) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "day18-cli", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		return fmt.Errorf("connect to MCP server at %s: %w", endpoint, err)
	}
	defer session.Close()

	var name string
	var arguments map[string]any
	var output any
	switch mode {
	case "schedule":
		if strings.TrimSpace(interval) == "" {
			return errors.New("schedule mode requires -interval")
		}
		name = "schedule_due_questions"
		arguments = map[string]any{"topic": topic, "interval": interval}
		output = new(scheduleOutput)
	case "summary":
		if topic != "" || interval != "" {
			return errors.New("-topic and -interval are only valid in schedule mode")
		}
		name = "scheduler_summary"
		arguments = map[string]any{}
		output = new(summaryOutput)
	default:
		return fmt.Errorf("unknown client mode %q", mode)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("call %s: %w", name, err)
	}
	if result == nil || result.IsError {
		return fmt.Errorf("%s returned an MCP error", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("encode %s result: %w", name, err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode %s result: %w", name, err)
	}
	pretty, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("format %s result: %w", name, err)
	}
	fmt.Printf("%s:\n%s\n", name, pretty)
	return nil
}

func defaultVault() string {
	if configured := os.Getenv("OBSIDIAN_VAULT"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("Documents", "Obsidian Vault", "Interviews prep", "Questions")
	}
	return filepath.Join(home, "Documents", "Obsidian Vault", "Interviews prep", "Questions")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Printf("day-18: %v", err)
		os.Exit(1)
	}
}
