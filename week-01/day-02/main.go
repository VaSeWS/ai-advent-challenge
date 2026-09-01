package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	groqModel    = "openai/gpt-oss-20b"

	defaultMaxTokens = 600
	defaultReasoning = "low"
)

// bulletsSchema enforces a strict JSON response of exactly 3 short bullet points.
// strict:true requires every property listed in "required" and additionalProperties:false.
var bulletsSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"bullets": map[string]any{
			"type":     "array",
			"items":    map[string]any{"type": "string"},
			"minItems": 3,
			"maxItems": 3,
		},
	},
	"required":             []string{"bullets"},
	"additionalProperties": false,
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchema struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	MaxTokens       int             `json:"max_tokens,omitempty"`
	Stop            string          `json:"stop,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	ResponseFormat  *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func main() {
	maxTokens := flag.Int("max-tokens", defaultMaxTokens, "max response length in tokens")
	stop := flag.String("stop", "", "stop sequence (empty = none)")
	reasoning := flag.String("reasoning", defaultReasoning, "reasoning effort: low, medium, high, or off (off = omit the param, model default)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: day-02 [-max-tokens N] [-stop STR] [-reasoning low|medium|high|off] <prompt>")
		os.Exit(1)
	}
	prompt := strings.Join(flag.Args(), " ")

	reasoningEffort, err := resolveReasoning(*reasoning)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

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
			{Role: "user", Content: prompt},
		},
		MaxTokens:       *maxTokens,
		Stop:            *stop,
		ReasoningEffort: reasoningEffort,
		ResponseFormat: &responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name:   "bullets_response",
				Strict: true,
				Schema: bulletsSchema,
			},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	fmt.Println("=== Without constraints ===")
	fmt.Println(unconstrained)
	fmt.Println()
	fmt.Println("=== With constraints (strict JSON schema + length + stop) ===")
	fmt.Println(constrained)
}

// resolveReasoning maps the -reasoning flag to the API's reasoning_effort value.
// "off" returns "" so the field is omitted from the request (json:"omitempty").
func resolveReasoning(value string) (string, error) {
	switch value {
	case "low", "medium", "high":
		return value, nil
	case "off":
		return "", nil
	default:
		return "", fmt.Errorf("invalid -reasoning value %q: must be low, medium, high, or off", value)
	}
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
