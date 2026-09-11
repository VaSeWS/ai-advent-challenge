package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	// BaseSystemMessage is included at the beginning of every main completion.
	BaseSystemMessage = "Ты полезный ассистент. Отвечай на языке пользователя."

	// SummaryInstruction is the complete instruction for a summary auxiliary completion.
	SummaryInstruction = "Обнови краткое резюме диалога. Сохрани цели, ограничения, предпочтения, решения и договорённости. Верни только текст резюме."

	// FactsInstruction is the complete instruction for a facts auxiliary completion.
	FactsInstruction = "Обнови память важных фактов. Сохраняй только цель, ограничения, предпочтения, решения и договорённости. Удаляй устаревшие значения. Верни только JSON вида {\"facts\":{\"ключ\":\"значение\"}}."

	// SummaryBatchSize is the number of old messages condensed by one summary call.
	SummaryBatchSize = 10
)

// BuildMainPrompt applies branch strategy to an already chronological lineage.
// The lineage must not include input: the returned prompt appends it exactly once.
func BuildMainPrompt(branch Branch, lineage []Message, summary *Summary, facts map[string]string, input string) ([]CompletionMessage, error) {
	if branch.WindowSize <= 0 {
		return nil, fmt.Errorf("context window must be positive")
	}

	prompt := make([]CompletionMessage, 0, len(lineage)+3)
	prompt = append(prompt, CompletionMessage{Role: "system", Content: BaseSystemMessage})

	switch branch.Strategy {
	case strategyFull, strategyBranching:
		prompt = appendLineage(prompt, lineage)
	case strategySliding:
		prompt = appendLineage(prompt, LastWindow(lineage, branch.WindowSize))
	case strategySummary:
		var coveredThrough int64
		if summary != nil {
			coveredThrough = summary.ThroughMessageID
			prompt = append(prompt, CompletionMessage{
				Role:    "system",
				Content: "Краткое резюме предыдущего диалога:\n" + summary.Content,
			})
		}
		uncovered, err := MessagesAfter(lineage, coveredThrough)
		if err != nil {
			return nil, err
		}
		prompt = appendLineage(prompt, uncovered)
	case strategyFacts:
		prompt = append(prompt, CompletionMessage{Role: "system", Content: FactsSystemBlock(facts)})
		prompt = appendLineage(prompt, LastWindow(lineage, branch.WindowSize))
	default:
		return nil, fmt.Errorf("unknown context strategy %q", branch.Strategy)
	}

	return append(prompt, CompletionMessage{Role: "user", Content: input}), nil
}

// LastWindow returns the newest windowSize messages without modifying lineage.
func LastWindow(lineage []Message, windowSize int) []Message {
	if windowSize <= 0 || len(lineage) == 0 {
		return nil
	}
	if len(lineage) <= windowSize {
		return lineage
	}
	return lineage[len(lineage)-windowSize:]
}

// MessagesAfter returns the chronological suffix strictly after throughMessageID.
// A zero ID denotes no summary watermark. A nonzero missing ID is invalid because
// it would otherwise silently omit or duplicate conversation context.
func MessagesAfter(lineage []Message, throughMessageID int64) ([]Message, error) {
	if throughMessageID == 0 {
		return lineage, nil
	}
	for i := range lineage {
		if lineage[i].ID == throughMessageID {
			return lineage[i+1:], nil
		}
	}
	return nil, fmt.Errorf("summary watermark message %d is not in lineage", throughMessageID)
}

// SummaryBatches returns complete, chronological batches of old, uncovered
// messages. The newest windowSize messages are deliberately left raw. The
// caller persists each resulting batch's final message as the new watermark.
func SummaryBatches(lineage []Message, throughMessageID int64, windowSize int) ([][]Message, error) {
	if windowSize <= 0 {
		return nil, fmt.Errorf("context window must be positive")
	}
	uncovered, err := MessagesAfter(lineage, throughMessageID)
	if err != nil {
		return nil, err
	}
	oldCount := len(uncovered) - windowSize
	if oldCount < SummaryBatchSize {
		return nil, nil
	}

	old := uncovered[:oldCount]
	batches := make([][]Message, 0, len(old)/SummaryBatchSize)
	for len(old) >= SummaryBatchSize {
		batches = append(batches, old[:SummaryBatchSize])
		old = old[SummaryBatchSize:]
	}
	return batches, nil
}

// FactsBatches divides a complete chronological lineage into batches of at
// most ten messages. Unlike summaries, a final short batch is included so a
// facts rebuild represents every message in the lineage.
func FactsBatches(lineage []Message) [][]Message {
	if len(lineage) == 0 {
		return nil
	}
	batches := make([][]Message, 0, (len(lineage)+SummaryBatchSize-1)/SummaryBatchSize)
	for len(lineage) > 0 {
		n := min(len(lineage), SummaryBatchSize)
		batches = append(batches, lineage[:n])
		lineage = lineage[n:]
	}
	return batches
}

// BuildSummaryPrompt creates the auxiliary request input for one exact batch
// of old messages. previousSummary is omitted on the first summary call.
func BuildSummaryPrompt(previousSummary string, batch []Message) ([]CompletionMessage, error) {
	if len(batch) != SummaryBatchSize {
		return nil, fmt.Errorf("summary batch must contain %d messages", SummaryBatchSize)
	}
	prompt := make([]CompletionMessage, 0, len(batch)+2)
	prompt = append(prompt, CompletionMessage{Role: "system", Content: SummaryInstruction})
	if previousSummary != "" {
		prompt = append(prompt, CompletionMessage{
			Role:    "system",
			Content: "Предыдущее краткое резюме:\n" + previousSummary,
		})
	}
	return appendLineage(prompt, batch), nil
}

// BuildFactsPrompt creates an auxiliary request from the current facts map and
// a chronological batch. The facts block is sorted to make the model input
// reproducible across runs.
func BuildFactsPrompt(facts map[string]string, batch []Message) ([]CompletionMessage, error) {
	if len(batch) == 0 {
		return nil, fmt.Errorf("facts batch must contain at least one message")
	}
	prompt := make([]CompletionMessage, 0, len(batch)+2)
	prompt = append(prompt,
		CompletionMessage{Role: "system", Content: FactsInstruction},
		CompletionMessage{Role: "system", Content: FactsSystemBlock(facts)},
	)
	return appendLineage(prompt, batch), nil
}

// FactsSystemBlock serializes facts in deterministic lexical key order for a
// main or auxiliary system message.
func FactsSystemBlock(facts map[string]string) string {
	if len(facts) == 0 {
		return "Важные факты:"
	}
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var block strings.Builder
	block.WriteString("Важные факты:\n")
	for i, key := range keys {
		if i > 0 {
			block.WriteByte('\n')
		}
		block.WriteString(key)
		block.WriteString(": ")
		block.WriteString(facts[key])
	}
	return block.String()
}

// ParseFactsJSON accepts only an object with one "facts" field whose value is
// an object of string values. It returns a fresh map so callers can safely use
// it as the next facts state.
func ParseFactsJSON(content string) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()

	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid facts JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(root) != 1 {
		return nil, fmt.Errorf("invalid facts JSON: expected only a facts object")
	}
	rawFacts, ok := root["facts"]
	if !ok {
		return nil, fmt.Errorf("invalid facts JSON: expected a facts object")
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(rawFacts, &values); err != nil || values == nil {
		return nil, fmt.Errorf("invalid facts JSON: facts must be an object")
	}
	facts := make(map[string]string, len(values))
	for key, rawValue := range values {
		var value string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, fmt.Errorf("invalid facts JSON: fact %q must be a string", key)
		}
		facts[key] = value
	}
	return facts, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid facts JSON: trailing value")
		}
		return fmt.Errorf("invalid facts JSON: %w", err)
	}
	return nil
}

// PromptTokenCount counts precisely the message text supplied to the counter.
func PromptTokenCount(counter TokenCounter, prompt []CompletionMessage) int {
	return countMessages(counter, prompt)
}

// CheckContextOverflow applies the provider's reserved main completion budget
// to a strategy-built main prompt before a request is made.
func CheckContextOverflow(counter TokenCounter, profile ProviderProfile, prompt []CompletionMessage) error {
	promptTokens := PromptTokenCount(counter, prompt)
	if promptTokens+profile.MainMax <= profile.ContextWindow {
		return nil
	}
	return fmt.Errorf("context overflow: prompt %d + reserve %d exceeds %d; use /mode summary, /mode sliding, or /mode facts", promptTokens, profile.MainMax, profile.ContextWindow)
}

func appendLineage(prompt []CompletionMessage, lineage []Message) []CompletionMessage {
	for _, message := range lineage {
		prompt = append(prompt, CompletionMessage{Role: message.Role, Content: message.Content})
	}
	return prompt
}
