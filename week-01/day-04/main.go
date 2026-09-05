package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	groqModel    = "openai/gpt-oss-20b"

	// One fixed request for every temperature. It deliberately has two halves:
	// a factual half (the TCP handshake is exactly SYN / SYN-ACK / ACK — either
	// the answer names all three steps or it is wrong) and an open half (a
	// metaphor, where there is no single right answer). So a single prompt
	// exposes both accuracy and creativity as the temperature changes.
	systemPrompt = `Отвечай по-русски. Формат ответа строго такой:
1) Объяснение: один абзац, не длиннее 4 предложений.
2) Метафора: одно предложение, начинается со слова "Метафора:".
Ничего кроме этих двух пунктов не выводи.`

	userPrompt = `Объясни, что такое TCP handshake и из каких шагов он состоит,
и придумай для него метафору.`

	// Each temperature is sampled several times: one answer per temperature
	// shows wording, but only repeated runs show diversity.
	runsPerTemperature = 3

	// gpt-oss spends part of the completion budget on reasoning tokens, so a
	// tight max_tokens truncates the visible answer mid-sentence and the
	// diversity numbers end up measuring truncation instead of sampling.
	// reasoning_effort=low keeps that hidden part short. The cap is also
	// bounded from above: Groq's free tier charges max_tokens against the
	// 8000 tokens/minute limit as *requested*, not as actually used, so all
	// 9 calls (~(600 + prompt) each) have to fit into that budget.
	maxTokensPerCall = 600
	reasoningEffort  = "low"
)

// temperatures under comparison.
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

	fmt.Println("=== Запрос (одинаковый для всех температур) ===")
	fmt.Println("System:")
	fmt.Println(systemPrompt)
	fmt.Println("User:")
	fmt.Println(userPrompt)
	fmt.Println()

	for _, temp := range temperatures {
		answers := make([]string, 0, runsPerTemperature)
		for run := 1; run <= runsPerTemperature; run++ {
			answer, err := ask(apiKey, temp)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			answers = append(answers, answer)
			fmt.Printf("=== temperature=%.1f, прогон %d/%d ===\n", temp, run, runsPerTemperature)
			fmt.Println(answer)
			fmt.Println()
		}
		fmt.Printf("--- temperature=%.1f: разнообразие между прогонами ---\n", temp)
		fmt.Printf("средняя попарная схожесть (Jaccard по словам): %.2f\n", meanPairwiseSimilarity(answers))
		fmt.Printf("дословно совпавших пар прогонов: %d из %d\n\n", identicalPairs(answers), pairCount(len(answers)))
	}
}

func ask(apiKey string, temperature float64) (string, error) {
	reqBody, err := json.Marshal(chatRequest{
		Model: groqModel,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
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

	return parsed.Choices[0].Message.Content, nil
}

// meanPairwiseSimilarity is a rough numeric stand-in for "разнообразие": 1.00
// means every pair of runs used exactly the same set of words, lower values
// mean the wording drifted apart.
func meanPairwiseSimilarity(answers []string) float64 {
	sets := make([]map[string]bool, len(answers))
	for i, a := range answers {
		sets[i] = wordSet(a)
	}

	sum, pairs := 0.0, 0
	for i := range sets {
		for j := i + 1; j < len(sets); j++ {
			sum += jaccard(sets[i], sets[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 1
	}
	return sum / float64(pairs)
}

func identicalPairs(answers []string) int {
	count := 0
	for i := range answers {
		for j := i + 1; j < len(answers); j++ {
			if strings.TrimSpace(answers[i]) == strings.TrimSpace(answers[j]) {
				count++
			}
		}
	}
	return count
}

func pairCount(n int) int {
	return n * (n - 1) / 2
}

func wordSet(s string) map[string]bool {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	intersection := 0
	for w := range a {
		if b[w] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 1
	}
	return float64(intersection) / float64(union)
}
