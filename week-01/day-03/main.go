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

	// Fixed task: LeetCode 3904 (Smallest Stable Index II). Correct algorithm is
	// well-defined (prefix-max + suffix-min arrays, then a linear scan), so each
	// method's code can be checked for correctness against known edge cases.
	// task is the bare problem statement; every method-specific instruction
	// (code requirement, brevity, step-by-step, meta-prompt) lives in a system
	// prompt instead, kept separate from the task itself.
	task = `LeetCode 3904, Smallest Stable Index II:

You are given an integer array nums of length n and an integer k.

For each index i, define its instability score as max(nums[0..i]) - min(nums[i..n - 1]).

In other words:
max(nums[0..i]) is the largest value among the elements from index 0 to index i.
min(nums[i..n - 1]) is the smallest value among the elements from index i to index n - 1.
An index i is called stable if its instability score is less than or equal to k.

Return the smallest stable index. If no such index exists, return -1.

Example 1:
Input: nums = [5,0,1,4], k = 3
Output: 3
Explanation:
At index 0: The maximum in [5] is 5, and the minimum in [5, 0, 1, 4] is 0, so the instability score is 5 - 0 = 5.
At index 1: The maximum in [5, 0] is 5, and the minimum in [0, 1, 4] is 0, so the instability score is 5 - 0 = 5.
At index 2: The maximum in [5, 0, 1] is 5, and the minimum in [1, 4] is 1, so the instability score is 5 - 1 = 4.
At index 3: The maximum in [5, 0, 1, 4] is 5, and the minimum in [4] is 4, so the instability score is 5 - 4 = 1.
This is the first index with an instability score less than or equal to k = 3. Thus, the answer is 3.

Example 2:
Input: nums = [3,2,1], k = 1
Output: -1
Explanation:
At index 0, the instability score is 3 - 1 = 2.
At index 1, the instability score is 3 - 1 = 2.
At index 2, the instability score is 3 - 1 = 2.
None of these values is less than or equal to k = 1, so the answer is -1.

Example 3:
Input: nums = [0], k = 0
Output: 0
Explanation:
At index 0, the instability score is 0 - 0 = 0, which is less than or equal to k = 0. Therefore, the answer is 0.

Constraints:
1 <= nums.length <= 10^5
0 <= nums[i] <= 10^9
0 <= k <= 10^9`

	// baseSystemPrompt carries the requirements common to every method: what
	// language/signature to return and how verbose to be. Groq free tier caps
	// this model at 8000 tokens/minute; without the brevity instruction
	// responses ran to 5-6k tokens each (formal proofs) and blew the budget
	// after 1-2 of the 7 calls this program makes.
	baseSystemPrompt = `Реши задачу и верни код решения на Python: функция
def stable_index(nums: list[int], k: int) -> int:
Требование: код должен работать за O(n) по времени (n до 10^5), без пересчёта
max/min с нуля на каждом индексе. Будь краток: обоснование не длиннее
нескольких предложений, без формальных доказательств теорем. Код функции
обязателен и должен быть полным.`

	stepByStepAddendum = "\n\nРешай пошагово: распиши ход рассуждений перед финальным кодом."

	metaPromptSystemPrompt = "Составь самодостаточный промпт (включая формулировку задачи), " +
		"который поможет другой LLM максимально точно решить следующую задачу. " +
		"В ответе выведи только текст промпта, без пояснений."

	maxTokensPerCall = 1024
)

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
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "error: GROQ_API_KEY environment variable is not set")
		os.Exit(1)
	}

	direct, err := ask(apiKey, []chatMessage{
		{Role: "system", Content: baseSystemPrompt},
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("1) Прямой ответ (без инструкций)", direct)

	stepByStep, err := ask(apiKey, []chatMessage{
		{Role: "system", Content: baseSystemPrompt + stepByStepAddendum},
		{Role: "user", Content: task},
	})
	fatalIf(err)
	printSection("2) Пошаговое рассуждение", stepByStep)

	generatedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "system", Content: metaPromptSystemPrompt},
		{Role: "user", Content: task},
	})
	fatalIf(err)
	viaGeneratedPrompt, err := ask(apiKey, []chatMessage{
		{Role: "user", Content: generatedPrompt},
	})
	fatalIf(err)
	printSection("3a) Сгенерированный промпт", generatedPrompt)
	printSection("3b) Решение по сгенерированному промпту", viaGeneratedPrompt)

	experts := []struct{ role, persona string }{
		{"Аналитик", "Ты аналитик. Разбери задачу с точки зрения корректности и граничных случаев, дай решение и обоснование."},
		{"Инженер", "Ты инженер-программист. Дай практичное решение с оценкой временной и пространственной сложности."},
		{"Критик", "Ты критик. Укажи на распространённые ошибки в решении этой задачи и дай собственный ответ с обоснованием, почему он верный."},
	}
	for _, e := range experts {
		answer, err := ask(apiKey, []chatMessage{
			{Role: "system", Content: baseSystemPrompt + "\n\n" + e.persona},
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
		Model:     groqModel,
		Messages:  messages,
		MaxTokens: maxTokensPerCall,
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
