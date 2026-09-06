package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	groqModel    = "openai/gpt-oss-20b"

	systemPrompt = "Отвечай по-русски, кратко и по делу."

	// gpt-oss spends part of the completion budget on reasoning tokens, so a
	// tight max_tokens truncates the visible answer mid-sentence.
	// reasoning_effort=low keeps that hidden part short.
	maxTokensPerCall = 600
	reasoningEffort  = "low"

	columnWidth = 38
)

// temperatures compared side by side, one column each.
var temperatures = []float64{0, 0.7, 1.2}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// ReasoningEffort is a gpt-oss-specific field on Groq; ignored by models
	// that do not expose reasoning.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func main() {
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	question := readQuestion()
	if question == "" {
		fmt.Fprintln(os.Stderr, "error: question is empty")
		os.Exit(1)
	}

	answers := make([]string, len(temperatures))
	for i, temp := range temperatures {
		answer, err := ask(apiKey, question, temp)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		answers[i] = answer
	}

	printTable(temperatures, answers)
}

// readQuestion takes the question from CLI args if given, otherwise prompts
// on stdin.
func readQuestion() string {
	if len(os.Args) > 1 {
		return strings.Join(os.Args[1:], " ")
	}
	fmt.Print("Вопрос: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return ""
	}
	return strings.TrimSpace(scanner.Text())
}

func ask(apiKey, question string, temperature float64) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model: groqModel,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: question},
		},
		Temperature:     temperature,
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

	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// printTable renders one column per temperature, each answer word-wrapped to
// columnWidth, side by side in a single ASCII table.
func printTable(temps []float64, answers []string) {
	headers := make([]string, len(temps))
	for i, t := range temps {
		headers[i] = fmt.Sprintf("temperature=%.1f", t)
	}

	wrapped := make([][]string, len(answers))
	maxLines := 0
	for i, a := range answers {
		wrapped[i] = wrapText(a, columnWidth)
		if len(wrapped[i]) > maxLines {
			maxLines = len(wrapped[i])
		}
	}

	border := "+"
	for range headers {
		border += strings.Repeat("-", columnWidth+2) + "+"
	}

	printRow := func(cells []string) {
		fmt.Print("|")
		for _, c := range cells {
			pad := columnWidth - utf8.RuneCountInString(c)
			if pad < 0 {
				pad = 0
			}
			fmt.Printf(" %s%s |", c, strings.Repeat(" ", pad))
		}
		fmt.Println()
	}

	fmt.Println(border)
	printRow(headers)
	fmt.Println(border)
	for line := 0; line < maxLines; line++ {
		row := make([]string, len(wrapped))
		for i, lines := range wrapped {
			if line < len(lines) {
				row[i] = lines[line]
			}
		}
		printRow(row)
	}
	fmt.Println(border)
}

// wrapText breaks s into lines of at most width runes, preserving existing
// newlines and breaking on word boundaries.
func wrapText(s string, width int) []string {
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, w := range words[1:] {
			if utf8.RuneCountInString(current)+1+utf8.RuneCountInString(w) > width {
				lines = append(lines, current)
				current = w
				continue
			}
			current += " " + w
		}
		lines = append(lines, current)
	}
	return lines
}
