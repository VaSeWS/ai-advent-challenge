package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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

func main() {
	if len(os.Args) != 2 || os.Args[1] != "server" {
		log.Fatal("usage: go run ./week-04/day-19 server")
	}
	if err := runServer(); err != nil {
		log.Fatal(err)
	}
}
