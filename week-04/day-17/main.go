package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message message `json:"message"`
	} `json:"choices"`
}

func chat(ctx context.Context, key string, messages []message, tools []map[string]any) (message, error) {
	request := map[string]any{
		"model": "openai/gpt-oss-20b", "messages": messages,
		"max_completion_tokens": 600, "reasoning_effort": "low",
	}
	if len(tools) > 0 {
		request["tools"] = tools
		request["tool_choice"] = "required"
	}
	body, err := json.Marshal(request)
	if err != nil {
		return message{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return message{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return message{}, fmt.Errorf("Groq: HTTP %d", resp.StatusCode)
	}
	var result chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return message{}, err
	}
	if len(result.Choices) == 0 {
		return message{}, errors.New("Groq returned no choices")
	}
	return result.Choices[0].Message, nil
}

func callUpload(ctx context.Context, session *mcp.ClientSession, file string) (uploadOutput, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "publish_video", Arguments: map[string]any{"file": file}})
	if err != nil {
		return uploadOutput{}, err
	}
	if result.IsError {
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				return uploadOutput{}, errors.New(text.Text)
			}
		}
		return uploadOutput{}, errors.New("publish_video returned an error")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return uploadOutput{}, err
	}
	var output uploadOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		return uploadOutput{}, err
	}
	if output.PublicURL == "" {
		return uploadOutput{}, errors.New("publish_video returned no public link")
	}
	return output, nil
}

func run(ctx context.Context, mode, file string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "video-agent", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(executable, "server")}, nil)
	if err != nil {
		return fmt.Errorf("connect to local Disk MCP: %w", err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	if mode == "list" {
		for _, tool := range listed.Tools {
			schema, _ := json.Marshal(tool.InputSchema)
			fmt.Printf("%s: %s\ninput: %s\n", tool.Name, tool.Description, schema)
		}
		return nil
	}
	if mode == "upload" {
		output, err := callUpload(ctx, session, file)
		if err != nil {
			return err
		}
		fmt.Printf("uploaded: %s\npublic link: %s\n", output.Path, output.PublicURL)
		return nil
	}
	key := os.Getenv("GROQ_API_KEY")
	if key == "" {
		return errors.New("GROQ_API_KEY is not set")
	}
	var tools []map[string]any
	for _, tool := range listed.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema,
		}})
	}
	messages := []message{
		{Role: "system", Content: "Выполняй запрос пользователя через доступный инструмент. Не придумывай ссылку на файл."},
		{Role: "user", Content: "Опубликуй запись " + file + " на Яндекс Диске и верни ссылку."},
	}
	selected, err := chat(ctx, key, messages, tools)
	if err != nil {
		return err
	}
	if len(selected.ToolCalls) != 1 || selected.ToolCalls[0].Function.Name != "publish_video" {
		return errors.New("agent did not select publish_video")
	}
	chosen := selected.ToolCalls[0]
	var args uploadInput
	if err := json.Unmarshal([]byte(chosen.Function.Arguments), &args); err != nil {
		return fmt.Errorf("invalid agent tool arguments: %w", err)
	}
	// Only the CLI argument, not a model-supplied path, may select a local file.
	requested, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	selectedFile, err := filepath.Abs(args.File)
	if err != nil || requested != selectedFile {
		return errors.New("agent selected a different local file")
	}
	output, err := callUpload(ctx, session, file)
	if err != nil {
		return err
	}
	fmt.Printf("MCP publish_video: %s\n", output.PublicURL)
	messages = append(messages, selected, message{Role: "tool", ToolCallID: chosen.ID, Content: "Uploaded to " + output.Path + "; public URL: " + output.PublicURL})
	final, err := chat(ctx, key, messages, nil)
	if err != nil {
		return err
	}
	fmt.Println("Agent:", final.Content)
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "server" {
		root, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		server := mcp.NewServer(&mcp.Implementation{Name: "yandex-disk-video", Version: "1.0.0"}, nil)
		registerDiskTool(server, newDiskAPI(root))
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) < 2 || len(os.Args) > 3 || (os.Args[1] != "list" && os.Args[1] != "upload" && os.Args[1] != "agent") || (os.Args[1] != "list" && len(os.Args) != 3) {
		log.Fatal("usage: go run ./week-04/day-17 list | upload <week-NN/day-NN/video/*.mp4> | agent <week-NN/day-NN/video/*.mp4>")
	}
	file := ""
	if len(os.Args) == 3 {
		file = os.Args[2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1], file); err != nil {
		log.Fatal(err)
	}
}
