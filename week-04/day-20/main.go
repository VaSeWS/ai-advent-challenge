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
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	groqChatURL = "https://api.groq.com/openai/v1/chat/completions"
	telegramURL = "https://api.telegram.org"
	maxTurns    = 4
)

type chatMessage struct {
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
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type question struct {
	Title string `json:"title"`
	Topic string `json:"topic"`
	Due   string `json:"due"`
	Path  string `json:"path"`
}

type findQuestionsOutput struct {
	Questions []question `json:"questions"`
}

type reviewPlanOutput struct {
	Plan  string `json:"plan"`
	Count int    `json:"count"`
}

type toolOrigin struct {
	name    string
	session *mcp.ClientSession
}

type toolDefinition struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

func requestChat(ctx context.Context, key string, messages []chatMessage, tools []toolDefinition) (chatMessage, error) {
	body, err := json.Marshal(map[string]any{
		"model": "openai/gpt-oss-20b", "messages": messages,
		"max_completion_tokens": 600, "reasoning_effort": "low",
		"tools": tools, "tool_choice": "auto",
	})
	if err != nil {
		return chatMessage{}, fmt.Errorf("encode Groq request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqChatURL, bytes.NewReader(body))
	if err != nil {
		return chatMessage{}, fmt.Errorf("create Groq request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return chatMessage{}, fmt.Errorf("request Groq chat completion: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return chatMessage{}, fmt.Errorf("Groq returned HTTP %d", resp.StatusCode)
	}
	var result chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return chatMessage{}, fmt.Errorf("decode Groq response: %w", err)
	}
	if len(result.Choices) == 0 {
		return chatMessage{}, errors.New("Groq returned no choices")
	}
	return result.Choices[0].Message, nil
}

func telegramSend(ctx context.Context, client *http.Client, endpoint, token, chatID, text string) error {
	chunks := splitTelegramMessages(text)
	for i, chunk := range chunks {
		if err := telegramSendChunk(ctx, client, endpoint, token, chatID, chunk); err != nil {
			return fmt.Errorf("Telegram delivered %d of %d message chunks: %w", i, len(chunks), err)
		}
	}
	return nil
}

func splitTelegramMessages(text string) []string {
	const maxCharacters = 4096
	if text == "" {
		return []string{""}
	}
	chunks := make([]string, 0, (len(text)+maxCharacters-1)/maxCharacters)
	start := 0
	characters := 0
	for byteIndex, r := range text {
		width := utf16Width(r)
		if characters+width > maxCharacters {
			chunks = append(chunks, text[start:byteIndex])
			start = byteIndex
			characters = 0
		}
		characters += width
	}
	chunks = append(chunks, text[start:])
	return chunks
}

func utf16Width(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func telegramSendChunk(ctx context.Context, client *http.Client, endpoint, token, chatID, text string) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	requestURL := strings.TrimRight(endpoint, "/") + "/bot" + url.PathEscape(token) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("create Telegram request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("send Telegram request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if !result.OK {
		return errors.New("Telegram API rejected the message")
	}
	return nil
}

func argumentsForTool(name, modelArguments, topic, date string, found findQuestionsOutput, plan reviewPlanOutput) (any, error) {
	switch name {
	case "find_due_questions":
		var selected map[string]any
		if err := json.Unmarshal([]byte(modelArguments), &selected); err != nil {
			return nil, fmt.Errorf("decode %s arguments: %w", name, err)
		}
		if selected["topic"] != topic || selected["date"] != date {
			return nil, errors.New("agent lookup arguments do not match requested topic and date")
		}
		return map[string]any{"topic": topic, "date": date}, nil
	case "build_review_plan":
		return map[string]any{"questions": found.Questions, "topic": topic, "date": date}, nil
	case "save_plan":
		return map[string]any{"plan": plan.Plan}, nil
	case "send_message":
		return map[string]any{"text": plan.Plan}, nil
	default:
		return nil, fmt.Errorf("unsupported tool %q", name)
	}
}

func toolHistorySummary(name string) string {
	switch name {
	case "find_due_questions":
		return "Due questions found; the host holds the exact result for the next tool."
	case "build_review_plan":
		return "Review plan generated; the host holds the exact plan for the next tools."
	case "save_plan":
		return "Review plan saved successfully."
	case "send_message":
		return "Telegram confirmed delivery."
	default:
		return "Tool completed successfully."
	}
}

type sendMessageInput struct {
	Text string `json:"text" jsonschema:"Message text to send"`
}

type sendMessageOutput struct {
	Sent bool `json:"sent"`
}

func runTelegramServer(ctx context.Context) error {
	token, chatID := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-notifications", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "send_message", Description: "Send a text notification to the configured Telegram chat."}, func(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, sendMessageOutput, error) {
		if strings.TrimSpace(input.Text) == "" {
			return nil, sendMessageOutput{}, errors.New("message text is required")
		}
		if err := telegramSend(ctx, http.DefaultClient, telegramURL, token, chatID, input.Text); err != nil {
			return nil, sendMessageOutput{}, err
		}
		return nil, sendMessageOutput{Sent: true}, nil
	})
	return server.Run(ctx, &mcp.StdioTransport{})
}

func structuredJSON(result *mcp.CallToolResult) ([]byte, error) {
	if result.IsError {
		for _, content := range result.Content {
			if text, ok := content.(*mcp.TextContent); ok {
				return nil, errors.New(text.Text)
			}
		}
		return nil, errors.New("MCP tool returned an error")
	}
	if result.StructuredContent == nil {
		return nil, errors.New("MCP tool returned no structured content")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, fmt.Errorf("encode MCP tool result: %w", err)
	}
	return encoded, nil
}

func callTool(ctx context.Context, origin toolOrigin, name string, args any) ([]byte, error) {
	result, err := origin.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("call %s tool: %w", name, err)
	}
	return structuredJSON(result)
}

func listToolDefinitions(ctx context.Context, sessions map[string]*mcp.ClientSession) ([]toolDefinition, map[string]toolOrigin, error) {
	var definitions []toolDefinition
	origins := make(map[string]toolOrigin)
	for server, session := range sessions {
		listed, err := session.ListTools(ctx, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("list %s tools: %w", server, err)
		}
		for _, tool := range listed.Tools {
			if _, exists := origins[tool.Name]; exists {
				return nil, nil, fmt.Errorf("MCP servers expose duplicate tool name %q", tool.Name)
			}
			definition := toolDefinition{Type: "function"}
			definition.Function.Name = tool.Name
			definition.Function.Description = tool.Description
			definition.Function.Parameters = tool.InputSchema
			definitions = append(definitions, definition)
			origins[tool.Name] = toolOrigin{name: server, session: session}
		}
	}
	return definitions, origins, nil
}

func connect(ctx context.Context) (map[string]*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "review-plan-agent", Version: "1.0.0"}, nil)
	day19 := exec.Command("go", "run", "./week-04/day-19", "server")
	vaultSession, err := client.Connect(ctx, &mcp.CommandTransport{Command: day19}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to day19 question server: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = vaultSession.Close()
		return nil, fmt.Errorf("locate day20 executable: %w", err)
	}
	telegramCommand := exec.Command(executable, "telegram-server")
	telegramSession, err := client.Connect(ctx, &mcp.CommandTransport{Command: telegramCommand}, nil)
	if err != nil {
		_ = vaultSession.Close()
		return nil, fmt.Errorf("connect to Telegram MCP server: %w", err)
	}
	return map[string]*mcp.ClientSession{"day19": vaultSession, "telegram": telegramSession}, nil
}

func runAgent(ctx context.Context, topic, date string) error {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return errors.New("date must use YYYY-MM-DD")
	}
	key := os.Getenv("GROQ_API_KEY")
	if key == "" {
		return errors.New("GROQ_API_KEY is not set")
	}
	sessions, err := connect(ctx)
	if err != nil {
		return err
	}
	defer func() {
		for _, session := range sessions {
			_ = session.Close()
		}
	}()
	definitions, origins, err := listToolDefinitions(ctx, sessions)
	if err != nil {
		return err
	}
	required := []string{"find_due_questions", "build_review_plan", "save_plan", "send_message"}
	for _, name := range required {
		if _, ok := origins[name]; !ok {
			return fmt.Errorf("MCP servers do not expose required tool %q", name)
		}
	}
	messages := []chatMessage{
		{Role: "system", Content: "Fulfill the user's request using the available capabilities. Do not claim an action succeeded unless its result confirms it. When a tool input requires large data produced by an earlier call, use empty placeholders for questions, plan, or text: the host will bind the exact earlier result before calling the tool. Choose one action at a time."},
		{Role: "user", Content: fmt.Sprintf("Составь план повторения темы %q на %s, сохрани его и отправь мне в Telegram.", topic, date)},
	}
	var found findQuestionsOutput
	var plan reviewPlanOutput
	var savedPath string
	for turn := 0; turn < maxTurns; turn++ {
		expected := required[turn]
		selected, err := requestChat(ctx, key, messages, definitions)
		if err != nil {
			return err
		}
		if len(selected.ToolCalls) != 1 {
			return errors.New("agent must select exactly one MCP tool per turn")
		}
		chosen := selected.ToolCalls[0]
		if chosen.Function.Name != expected {
			return fmt.Errorf("agent selected %q; expected %q at step %d", chosen.Function.Name, expected, turn+1)
		}
		args, err := argumentsForTool(expected, chosen.Function.Arguments, topic, date, found, plan)
		if err != nil {
			return err
		}
		chosenArguments, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("encode %s arguments: %w", expected, err)
		}
		selected.ToolCalls[0].Function.Arguments = string(chosenArguments)
		origin := origins[expected]
		fmt.Printf("route: %s -> %s\n", origin.name, expected)
		output, err := callTool(ctx, origin, expected, args)
		if err != nil {
			return fmt.Errorf("%s failed: %w", expected, err)
		}
		if expected == "find_due_questions" {
			if err := json.Unmarshal(output, &found); err != nil {
				return fmt.Errorf("decode due-question result: %w", err)
			}
			if len(found.Questions) == 0 {
				fmt.Println("No due questions; no plan was saved or sent.")
				return nil
			}
		}
		if expected == "build_review_plan" {
			if err := json.Unmarshal(output, &plan); err != nil {
				return fmt.Errorf("decode review-plan result: %w", err)
			}
			if plan.Plan == "" || plan.Count != len(found.Questions) {
				return errors.New("day19 returned an invalid review plan")
			}
		}
		if expected == "save_plan" {
			var saved struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(output, &saved); err != nil || saved.Path == "" {
				return errors.New("day19 did not confirm the saved plan path")
			}
			savedPath = saved.Path
		}
		if expected == "send_message" {
			var sent sendMessageOutput
			if err := json.Unmarshal(output, &sent); err != nil || !sent.Sent {
				return errors.New("Telegram server did not confirm message delivery")
			}
		}
		messages = append(messages, selected, chatMessage{Role: "tool", ToolCallID: chosen.ID, Content: toolHistorySummary(expected)})
	}
	fmt.Printf("review plan saved: %s\nTelegram notification sent.\n", savedPath)
	return nil
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "telegram-server" {
		if err := runTelegramServer(context.Background()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "agent" {
		log.Fatal("usage: go run ./week-04/day-20 agent <topic> <YYYY-MM-DD> | telegram-server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := runAgent(ctx, os.Args[2], os.Args[3]); err != nil {
		log.Fatal(err)
	}
}
