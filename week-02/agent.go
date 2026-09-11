package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Agent coordinates context construction, completions, and atomic turn storage.
type Agent struct {
	store   *Store
	llm     Completer
	tokens  TokenCounter
	profile ProviderProfile
}

func NewAgent(store *Store, llm Completer, tokens TokenCounter, profile ProviderProfile) *Agent {
	return &Agent{store: store, llm: llm, tokens: tokens, profile: profile}
}

// Send completes one user turn without persisting anything until every required
// auxiliary and main completion has succeeded.
func (a *Agent) Send(ctx context.Context, chatID, branchID int64, input string) (TurnResult, error) {
	if strings.TrimSpace(input) == "" {
		return TurnResult{}, fmt.Errorf("user message is empty")
	}
	branch, lineage, summary, facts, err := a.loadState(chatID, branchID)
	if err != nil {
		return TurnResult{}, err
	}

	calls := make([]APICall, 0, 2)
	var summaryUpdate *SummaryUpdate
	var factsUpdate *map[string]string

	switch branch.Strategy {
	case strategySummary:
		summary, summaryUpdate, calls, err = a.updateSummary(ctx, branch, lineage, summary)
		if err != nil {
			return TurnResult{}, err
		}
	case strategyFacts:
		facts, calls, err = a.updateFacts(ctx, branch, facts, []Message{{Role: "user", Content: input}})
		if err != nil {
			return TurnResult{}, err
		}
		factsUpdate = &facts
	}

	prompt, err := BuildMainPrompt(branch, lineage, summary, facts, input)
	if err != nil {
		return TurnResult{}, err
	}
	if err := CheckContextOverflow(a.tokens, a.profile, prompt); err != nil {
		return TurnResult{}, err
	}

	main, mainCall, err := a.complete(ctx, branch, "main", prompt, a.profile.MainMax, a.tokens.Count(input), countLineageTokens(a.tokens, lineage))
	if err != nil {
		return TurnResult{}, fmt.Errorf("complete main response: %w", err)
	}
	calls = append(calls, mainCall)

	assistant, err := a.store.SaveTurn(SaveTurnInput{
		ChatID:           chatID,
		BranchID:         branchID,
		UserContent:      input,
		AssistantContent: main.Content,
		Summary:          summaryUpdate,
		Facts:            factsUpdate,
		APICalls:         calls,
	})
	if err != nil {
		return TurnResult{}, err
	}

	return TurnResult{Assistant: assistant, Metrics: formatMetrics(a.profile, a.tokens, mainCall)}, nil
}

// RebuildFacts reconstructs facts from a branch's complete current lineage.
// It is intended for the /mode facts transition and records nothing unless all
// batches complete and their JSON responses are valid.
func (a *Agent) RebuildFacts(ctx context.Context, chatID, branchID int64) error {
	branch, err := a.store.Branch(chatID, branchID)
	if err != nil {
		return err
	}
	if branch.Strategy != strategyFacts {
		return fmt.Errorf("facts rebuild requires facts mode")
	}
	lineage, err := a.store.Lineage(branchID)
	if err != nil {
		return err
	}

	facts := make(map[string]string)
	calls := make([]APICall, 0, len(FactsBatches(lineage)))
	for _, batch := range FactsBatches(lineage) {
		var call APICall
		facts, call, err = a.completeFacts(ctx, branch, facts, batch)
		if err != nil {
			return err
		}
		calls = append(calls, call)
	}
	return a.store.SaveFactsRebuild(chatID, branchID, facts, calls)
}

func (a *Agent) loadState(chatID, branchID int64) (Branch, []Message, *Summary, map[string]string, error) {
	branch, err := a.store.Branch(chatID, branchID)
	if err != nil {
		return Branch{}, nil, nil, nil, err
	}
	lineage, err := a.store.Lineage(branchID)
	if err != nil {
		return Branch{}, nil, nil, nil, err
	}

	var summary *Summary
	if branch.Strategy == strategySummary {
		summary, err = a.store.Summary(branchID)
		if err != nil {
			return Branch{}, nil, nil, nil, err
		}
	}

	facts := make(map[string]string)
	if branch.Strategy == strategyFacts {
		storedFacts, err := a.store.Facts(branchID)
		if err != nil {
			return Branch{}, nil, nil, nil, err
		}
		for _, fact := range storedFacts {
			facts[fact.Key] = fact.Value
		}
	}
	return branch, lineage, summary, facts, nil
}

func (a *Agent) updateSummary(ctx context.Context, branch Branch, lineage []Message, summary *Summary) (*Summary, *SummaryUpdate, []APICall, error) {
	throughMessageID := int64(0)
	previousSummary := ""
	if summary != nil {
		throughMessageID = summary.ThroughMessageID
		previousSummary = summary.Content
	}
	batches, err := SummaryBatches(lineage, throughMessageID, branch.WindowSize)
	if err != nil {
		return nil, nil, nil, err
	}
	calls := make([]APICall, 0, len(batches))
	for _, batch := range batches {
		prompt, err := BuildSummaryPrompt(previousSummary, batch)
		if err != nil {
			return nil, nil, nil, err
		}
		completion, call, err := a.complete(ctx, branch, "summary", prompt, a.profile.AuxiliaryMax, 0, 0)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("complete summary: %w", err)
		}
		calls = append(calls, call)
		previousSummary = completion.Content
		throughMessageID = batch[len(batch)-1].ID
	}
	if len(batches) == 0 {
		return summary, nil, calls, nil
	}
	updated := &Summary{BranchID: branch.ID, ThroughMessageID: throughMessageID, Content: previousSummary}
	return updated, &SummaryUpdate{ThroughMessageID: throughMessageID, Content: previousSummary}, calls, nil
}

func (a *Agent) updateFacts(ctx context.Context, branch Branch, facts map[string]string, batch []Message) (map[string]string, []APICall, error) {
	updated, call, err := a.completeFacts(ctx, branch, facts, batch)
	if err != nil {
		return nil, nil, err
	}
	return updated, []APICall{call}, nil
}

func (a *Agent) completeFacts(ctx context.Context, branch Branch, facts map[string]string, batch []Message) (map[string]string, APICall, error) {
	prompt, err := BuildFactsPrompt(facts, batch)
	if err != nil {
		return nil, APICall{}, err
	}
	completion, call, err := a.complete(ctx, branch, "facts", prompt, a.profile.AuxiliaryMax, 0, 0)
	if err != nil {
		return nil, APICall{}, fmt.Errorf("complete facts: %w", err)
	}
	updated, err := ParseFactsJSON(completion.Content)
	if err != nil {
		return nil, APICall{}, err
	}
	return updated, call, nil
}

func (a *Agent) complete(ctx context.Context, branch Branch, kind string, prompt []CompletionMessage, maxTokens, currentTokens, fullHistoryTokens int) (Completion, APICall, error) {
	startedAt := time.Now()
	completion, err := a.llm.Complete(ctx, CompletionRequest{Messages: prompt, MaxTokens: maxTokens, Kind: kind})
	if err != nil {
		return Completion{}, APICall{}, err
	}
	price := a.profile.Price(completion.Usage, startedAt)
	return completion, APICall{
		Provider:            a.profile.Name,
		Model:               a.profile.Model,
		Kind:                kind,
		Strategy:            branch.Strategy,
		CounterLabel:        a.tokens.Label(),
		CurrentTokens:       currentTokens,
		FullHistoryTokens:   fullHistoryTokens,
		SentTokensLocal:     PromptTokenCount(a.tokens, prompt),
		ResponseTokensLocal: a.tokens.Count(completion.Content),
		Usage:               completion.Usage,
		PricingTier:         price.Tier,
		InputCostUSD:        price.InputCostUSD,
		OutputCostUSD:       price.OutputCostUSD,
	}, nil
}

func countLineageTokens(counter TokenCounter, lineage []Message) int {
	total := 0
	for _, message := range lineage {
		total += counter.Count(message.Role) + counter.Count(message.Content)
	}
	return total
}

func formatMetrics(profile ProviderProfile, counter TokenCounter, call APICall) string {
	return fmt.Sprintf(
		"provider=%s/%s | local[%s] current=%d history=%d sent=%d response=%d | api prompt=%d completion=%d total=%d | cost=$%.6f",
		profile.Name,
		profile.Model,
		counter.Label(),
		call.CurrentTokens,
		call.FullHistoryTokens,
		call.SentTokensLocal,
		call.ResponseTokensLocal,
		call.Usage.PromptTokens,
		call.Usage.CompletionTokens,
		call.Usage.TotalTokens,
		call.InputCostUSD+call.OutputCostUSD,
	)
}
