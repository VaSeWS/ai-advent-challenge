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
	"text/tabwriter"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"

	// Classic logic riddle: all five brothers share the same one sister, so
	// the trick is not to multiply 5x1 — the intuitive wrong answer is 5.
	defaultPrompt = `В семье пятеро братьев, и у каждого из них есть только одна сестра. ` +
		`Сколько сестёр в этой семье? Дай краткое рассуждение (не больше 2-3 предложений), ` +
		`затем на отдельной последней строке напиши строго: "Ответ: <число>".`

	maxTokens = 2048

	// column width for word-wrapping answers before the tabwriter table —
	// without it, a single unbroken paragraph turns into one huge line and
	// blows up every column's width, not just its own.
	colWidth = 46
)

// models spans Groq's lineup from top to outsider by public benchmark rank
// (MMLU / MMLU-Pro, checked 2026-09-06): openai/gpt-oss-120b leads
// (MMLU 90.0), qwen/qwen3.6-27b sits close behind (MMLU-Pro 86.2),
// openai/gpt-oss-20b trails (MMLU 85.3). llama-3.1-8b-instant and
// llama-3.3-70b-versatile are excluded — Groq moved both to Enterprise-only
// "Contact Sales" pricing on 2026-08-26, so this API key has no access to
// them (confirmed: 404 model_not_found).
var models = []struct {
	id, tier, hfURL string
}{
	{"openai/gpt-oss-120b", "топ", "https://huggingface.co/openai/gpt-oss-120b"},
	{"qwen/qwen3.6-27b", "средняя", "https://huggingface.co/Qwen/Qwen3.6-27B"},
	{"openai/gpt-oss-20b", "аутсайдер", "https://huggingface.co/openai/gpt-oss-20b"},
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
}

func main() {
	flag.Parse()
	task := defaultPrompt
	if flag.NArg() > 0 {
		task = strings.Join(flag.Args(), " ")
	}

	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	answers := make([][]string, len(models))
	for i, m := range models {
		answer, err := ask(apiKey, m.id, task)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: model %s: %v\n", m.id, err)
			os.Exit(1)
		}
		answers[i] = wrapText(strings.TrimSpace(answer), colWidth)
	}

	printTable(task, answers)
}

func printTable(task string, answers [][]string) {
	fmt.Println("Промпт:", task)
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)

	header := make([]string, len(models))
	for i, m := range models {
		header[i] = fmt.Sprintf("%s (%s)", m.id, m.tier)
	}
	fmt.Fprintln(w, strings.Join(header, "\t"))

	maxLines := 0
	for _, a := range answers {
		if len(a) > maxLines {
			maxLines = len(a)
		}
	}
	for line := 0; line < maxLines; line++ {
		row := make([]string, len(answers))
		for i, a := range answers {
			if line < len(a) {
				row[i] = a[line]
			}
		}
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// wrapText word-wraps s to width, treating existing newlines as hard breaks.
func wrapText(s string, width int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if len(line)+1+len(word) > width {
				lines = append(lines, line)
				line = word
				continue
			}
			line += " " + word
		}
		lines = append(lines, line)
	}
	return lines
}

func ask(apiKey, model, task string) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model:     model,
		Messages:  []chatMessage{{Role: "user", Content: task}},
		MaxTokens: maxTokens,
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
