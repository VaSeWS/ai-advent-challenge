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

	stepByStepSystemPrompt = "Решай пошагово: распиши ход рассуждений перед финальным ответом."

	metaPromptSystemPrompt = "Составь самодостаточный промпт (включая формулировку задачи), " +
		"который поможет другой LLM максимально точно решить следующую задачу. " +
		"В ответе выведи только текст промпта, без пояснений."

	// gpt-oss-20b is a reasoning model: it spends most of max_tokens on a
	// hidden "reasoning" field before writing the visible content. At the
	// API default reasoning effort, a single call burned ~950 of 1024 tokens
	// on hidden reasoning alone and returned empty content. reasoningEffort
	// "low" cuts that down, but hidden reasoning length is still stochastic —
	// a verbose method/persona can still hit maxTokensPerCall with empty
	// content on an unlucky run; rerun or bump this constant if that happens.
	reasoningEffort  = "low"
	maxTokensPerCall = 2048
)

// experts are generic personas for the panel method, independent of any task.
var experts = []struct{ role, persona string }{
	{"Аналитик", "Ты аналитик. Разбери задачу с точки зрения корректности и граничных случаев, дай решение и обоснование."},
	{"Инженер", "Ты инженер-программист. Дай практичное решение с оценкой временной и пространственной сложности."},
	{"Критик", "Ты критик. Укажи на распространённые ошибки в решении этой задачи и дай собственный ответ с обоснованием, почему он верный."},
}

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
	method := flag.String("method", "", "reasoning method: direct, step, meta, or panel")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: day-03 -method=direct|step|meta|panel <task>")
		os.Exit(1)
	}
	task := strings.Join(flag.Args(), " ")

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	switch *method {
	case "direct":
		runDirect(apiKey, task)
	case "step":
		runStepByStep(apiKey, task)
	case "meta":
		runMetaPrompt(apiKey, task)
	case "panel":
		runPanel(apiKey, task)
	default:
		fmt.Fprintln(os.Stderr, "error: -method must be one of direct, step, meta, panel")
		os.Exit(1)
	}
}

func runDirect(apiKey, task string) {
	answer, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("Прямой ответ (пустой system prompt)", answer)
}

func runStepByStep(apiKey, task string) {
	answer, err := ask(apiKey, []chatMessage{
		{Role: "system", Content: stepByStepSystemPrompt},
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("Пошаговое рассуждение", answer)
}

func runMetaPrompt(apiKey, task string) {
	generatedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "system", Content: metaPromptSystemPrompt},
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("Сгенерированный промпт", generatedPrompt)

	viaGeneratedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: generatedPrompt},
	})
	fatalIf(err)
	printSection("Решение по сгенерированному промпту", viaGeneratedPrompt)
}

func runPanel(apiKey, task string) {
	for _, e := range experts {
		answer, err := ask(apiKey, []chatMessage{
			{Role: "system", Content: e.persona},
			{Role: "user", Content: task},
		})
		fatalIf(err)
		printSection("Панель экспертов — "+e.role, answer)
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printSection(title, body string) {
	fmt.Println("=== " + title + " ===")
	fmt.Println(body)
	fmt.Println()
}

func ask(apiKey string, messages []chatMessage) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:           groqModel,
		Messages:        messages,
		MaxTokens:       maxTokensPerCall,
		ReasoningEffort: reasoningEffort,
	})
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
