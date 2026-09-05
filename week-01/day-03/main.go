package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	groqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	groqModel    = "openai/gpt-oss-20b"

	// Fixed task: LeetCode 300 (Longest Increasing Subsequence), classic array,
	// known correct answer (4) so every method's output can be checked exactly.
	task = "Дан массив целых чисел nums = [10, 9, 2, 5, 3, 7, 101, 18].\n" +
		"Найди длину самой длинной строго возрастающей подпоследовательности " +
		"(Longest Increasing Subsequence). Ответ должен быть одним числом."
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
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

	direct, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("1) Прямой ответ (без инструкций)", direct)

	stepByStep, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: task + "\n\nРешай пошагово: распиши ход рассуждений перед финальным ответом."},
	})
	fatalIf(err)
	printSection("2) Пошаговое рассуждение", stepByStep)

	generatedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: "Составь самодостаточный промпт (включая формулировку задачи), " +
			"который поможет другой LLM максимально точно решить следующую задачу. " +
			"В ответе выведи только текст промпта, без пояснений.\n\nЗадача:\n" + task},
	})
	fatalIf(err)
	viaGeneratedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: generatedPrompt},
	})
	fatalIf(err)
	printSection("3a) Сгенерированный промпт", generatedPrompt)
	printSection("3b) Решение по сгенерированному промпту", viaGeneratedPrompt)

	experts := []struct{ role, systemPrompt string }{
		{"Аналитик", "Ты аналитик. Разбери задачу с точки зрения корректности и граничных случаев, дай решение и обоснование."},
		{"Инженер", "Ты инженер-программист. Дай практичное решение с оценкой временной и пространственной сложности."},
		{"Критик", "Ты критик. Укажи на распространённые ошибки в решении этой задачи и дай собственный ответ с обоснованием, почему он верный."},
	}
	for _, e := range experts {
		answer, err := ask(apiKey, []chatMessage{
			{Role: "system", Content: e.systemPrompt},
			{Role: "user", Content: task},
		})
		fatalIf(err)
		printSection("4) Панель экспертов — "+e.role, answer)
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
		Model:    groqModel,
		Messages: messages,
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
