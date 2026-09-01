package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	groqModel    = "openai/gpt-oss-20b"

	formatSystemPrompt = "Respond with exactly 3 short bullet points, plain text, no markdown headers, " +
		"no more than 200 characters total. Stop immediately after the third bullet point - write nothing else."
	maxTokensCap = 200
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	MaxTokens       int           `json:"max_tokens,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: day-02 <prompt>")
		os.Exit(1)
	}
	prompt := strings.Join(os.Args[1:], " ")

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	unconstrained, err := ask(apiKey, chatRequest{
		Model: groqModel,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	constrained, err := ask(apiKey, chatRequest{
		Model: groqModel,
		Messages: []chatMessage{
			{Role: "system", Content: formatSystemPrompt},
			{Role: "user", Content: prompt},
		},
		MaxTokens:       maxTokensCap,
		ReasoningEffort: "low",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Println("=== Without constraints ===")
	fmt.Println(unconstrained)
	fmt.Println()
	fmt.Println("=== With constraints (format + length + stop) ===")
	fmt.Println(constrained)
}

func ask(apiKey string, request chatRequest) (string, error) {
	reqBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to build request body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, groqEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq API returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("response contained no choices")
	}

	return parsed.Choices[0].Message.Content, nil
}
