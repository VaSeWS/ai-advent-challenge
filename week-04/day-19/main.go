package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const outputPlanPath = "week-04/day-19/plan.md"

type buildPlanInput struct {
	Questions []question `json:"questions" jsonschema:"Questions returned by find_due_questions"`
	Topic     string     `json:"topic" jsonschema:"Requested hashtag topic; empty means all topics"`
	Date      string     `json:"date" jsonschema:"Review plan date in YYYY-MM-DD format"`
}

type buildPlanOutput struct {
	Plan  string `json:"plan" jsonschema:"Markdown review plan containing titles, topics, and due dates only"`
	Count int    `json:"count" jsonschema:"Number of questions in the plan"`
}

type savePlanInput struct {
	Plan string `json:"plan" jsonschema:"Review plan text to save outside the Obsidian vault"`
}

type savePlanOutput struct {
	Path string `json:"path" jsonschema:"Saved plan path relative to the current repository directory"`
}

func buildReviewPlan(input buildPlanInput) (buildPlanOutput, error) {
	date, err := parseDate(input.Date)
	if err != nil {
		return buildPlanOutput{}, err
	}
	topic := normalizeTopic(input.Topic)
	label := topic
	if label == "" {
		label = "all topics"
	}
	var plan strings.Builder
	fmt.Fprintf(&plan, "# Review plan — %s\n\nTopic: %s\n\n", date.Format("2006-01-02"), label)
	for _, item := range input.Questions {
		itemTopic := item.Topic
		if itemTopic != "" {
			itemTopic = "#" + itemTopic
		}
		fmt.Fprintf(&plan, "- [ ] %s — %s — due %s\n", item.Title, itemTopic, item.Due)
	}
	if len(input.Questions) == 0 {
		plan.WriteString("No due questions found.\n")
	}
	return buildPlanOutput{Plan: plan.String(), Count: len(input.Questions)}, nil
}

func savePlan(plan string) (savePlanOutput, error) {
	if strings.TrimSpace(plan) == "" {
		return savePlanOutput{}, errors.New("plan must not be empty")
	}
	path := filepath.Clean(outputPlanPath)
	if filepath.IsAbs(path) || path != outputPlanPath || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return savePlanOutput{}, errors.New("invalid plan output path")
	}
	for _, directory := range []string{"week-04", filepath.Dir(path)} {
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			if err := os.Mkdir(directory, 0o700); err != nil {
				return savePlanOutput{}, fmt.Errorf("create plan output directory: %w", err)
			}
			continue
		}
		if err != nil {
			return savePlanOutput{}, fmt.Errorf("inspect plan output directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return savePlanOutput{}, fmt.Errorf("plan output directory %q is not a real directory", directory)
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return savePlanOutput{}, errors.New("plan output must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return savePlanOutput{}, fmt.Errorf("inspect plan output: %w", err)
	}
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		return savePlanOutput{}, fmt.Errorf("write review plan: %w", err)
	}
	return savePlanOutput{Path: path}, nil
}

func registerTools(server *mcp.Server, vault string) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "find_due_questions", Description: "Find Obsidian question notes with an exact hashtag topic and sr-due on or before the requested date. Empty topic includes all topics.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input findQuestionsInput) (*mcp.CallToolResult, findQuestionsOutput, error) {
		questions, _, err := findDueQuestions(ctx, vault, input.Topic, input.Date)
		return nil, findQuestionsOutput{Questions: questions}, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "build_review_plan", Description: "Build a review plan from find_due_questions results without including note bodies.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input buildPlanInput) (*mcp.CallToolResult, buildPlanOutput, error) {
		output, err := buildReviewPlan(input)
		return nil, output, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "save_plan", Description: "Save review plan text to the day19 output file, never to the Obsidian vault.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input savePlanInput) (*mcp.CallToolResult, savePlanOutput, error) {
		output, err := savePlan(input.Plan)
		return nil, output, err
	})
}

func decodeStructured(result *mcp.CallToolResult, target any) error {
	if result == nil {
		return errors.New("MCP tool returned no result")
	}
	if result.IsError {
		return errors.New("MCP tool returned an error")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("encode MCP structured result: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("decode MCP structured result: %w", err)
	}
	return nil
}

func callTool(ctx context.Context, session *mcp.ClientSession, name string, arguments any, target any) error {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("call %s: %w", name, err)
	}
	if err := decodeStructured(result, target); err != nil {
		return fmt.Errorf("read %s result: %w", name, err)
	}
	return nil
}

func printStage(name string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("%s:\n%s\n", name, encoded)
	return nil
}

func runPlan(ctx context.Context, topic, date string) error {
	if _, err := parseDate(date); err != nil {
		return err
	}
	topic = normalizeTopic(topic)
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate day19 executable: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "day19-review-pipeline", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(executable, "server")}, nil)
	if err != nil {
		return fmt.Errorf("connect to day19 MCP server: %w", err)
	}
	defer session.Close()

	var found findQuestionsOutput
	if err := callTool(ctx, session, "find_due_questions", map[string]any{"topic": topic, "date": date}, &found); err != nil {
		return err
	}
	if err := printStage("find_due_questions", found); err != nil {
		return err
	}

	var built buildPlanOutput
	if err := callTool(ctx, session, "build_review_plan", map[string]any{
		"questions": found.Questions, "topic": topic, "date": date,
	}, &built); err != nil {
		return err
	}
	if err := printStage("build_review_plan", built); err != nil {
		return err
	}

	var saved savePlanOutput
	if err := callTool(ctx, session, "save_plan", map[string]any{"plan": built.Plan}, &saved); err != nil {
		return err
	}
	if err := printStage("save_plan", saved); err != nil {
		return err
	}
	contents, err := os.ReadFile(saved.Path)
	if err != nil {
		return fmt.Errorf("read saved plan %q: %w", saved.Path, err)
	}
	fmt.Printf("saved content (%s):\n%s", saved.Path, contents)
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		fmt.Println()
	}
	return nil
}

func vaultPath() (string, error) {
	vault := os.Getenv("OBSIDIAN_VAULT")
	if vault == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		vault = filepath.Join(home, "Documents", "Obsidian Vault", "Interviews prep", "Questions")
	} else if strings.HasPrefix(vault, "~"+string(filepath.Separator)) || vault == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find home directory: %w", err)
		}
		vault = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(vault, "~"), string(filepath.Separator)))
	}
	absolute, err := filepath.Abs(vault)
	if err != nil {
		return "", fmt.Errorf("resolve Obsidian vault path: %w", err)
	}
	return absolute, nil
}

func runServer() error {
	vault, err := vaultPath()
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "day19-review-planner", Version: "1.0.0"}, nil)
	registerTools(server, vault)
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

func usage() error {
	return errors.New("usage: go run ./week-04/day-19 server | plan <topic|all> <YYYY-MM-DD>")
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "server" {
		if err := runServer(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "plan" {
		log.Fatal(usage())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := runPlan(ctx, os.Args[2], os.Args[3]); err != nil {
		log.Fatal(err)
	}
}
