package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

	// Same question sent to every model, verbatim. Classic cognitive-reflection
	// test (Frederick, 2005): the intuitive answer (10) is wrong, the correct
	// answer requires a beat of actual reasoning (5). Good at separating models
	// that reason from models that pattern-match the first number they see.
	prompt = `Бита и мяч вместе стоят 110 рублей. Бита стоит на 100 рублей дороже мяча. ` +
		`Сколько стоит мяч? Дай краткое рассуждение (не больше 3-4 предложений), ` +
		`затем на отдельной последней строке напиши строго: "Ответ: <число> рублей".`

	maxTokens = 1024
)

// Three models spanning Groq's current lineup (checked against
// console.groq.com/docs/models, 2026-09-05): weak/fast/cheap, a mid-tier
// reasoning model, and the largest model with public self-serve pricing.
// llama-3.1-8b-instant and llama-3.3-70b-versatile were excluded even though
// they're still listed as production models — Groq moved both to
// Enterprise-only "Contact Sales" pricing on 2026-08-26, so there's no public
// per-token cost to report for them.
var models = []struct {
	id         string
	tier       string
	inputPerM  float64 // USD per 1M input tokens
	outputPerM float64 // USD per 1M output tokens
}{
	{"openai/gpt-oss-20b", "слабая (20B)", 0.075, 0.30},
	{"qwen/qwen3.6-27b", "средняя (27B)", 0.60, 3.00},
	{"openai/gpt-oss-120b", "сильная (120B)", 0.15, 0.60},
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func main() {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	for _, m := range models {
		start := time.Now()
		resp, err := ask(apiKey, m.id)
		elapsed := time.Since(start)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: model %s: %v\n", m.id, err)
			os.Exit(1)
		}

		cost := float64(resp.Usage.PromptTokens)/1e6*m.inputPerM +
			float64(resp.Usage.CompletionTokens)/1e6*m.outputPerM

		fmt.Printf("=== %s — %s ===\n", m.id, m.tier)
		fmt.Printf("время: %s\n", elapsed.Round(time.Millisecond))
		fmt.Printf("токены: prompt=%d completion=%d total=%d\n",
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
		fmt.Printf("стоимость: $%.6f\n", cost)
		fmt.Println("ответ:")
		fmt.Println(resp.Choices[0].Message.Content)
		fmt.Println()
	}
}

func ask(apiKey, model string) (*chatResponse, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:     model,
		Messages:  []chatMessage{{Role: "user", Content: prompt}},
		MaxTokens: maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to build request body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, groqEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("groq API returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("response contained no choices")
	}

	return &parsed, nil
}
