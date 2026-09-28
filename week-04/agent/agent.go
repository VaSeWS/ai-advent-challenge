package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Agent coordinates context construction, completions, MCP tools, and turn storage.
type Agent struct {
	store   *Store
	llm     Completer
	tokens  TokenCounter
	profile ProviderProfile
	mcp     mcpToolManager
}

func NewAgent(store *Store, llm Completer, tokens TokenCounter, profile ProviderProfile) *Agent {
	return &Agent{store: store, llm: llm, tokens: tokens, profile: profile}
}

// AttachMCPManager makes the manager's currently discovered tools available to
// subsequent main-turn completions. Passing nil detaches tool calling.
func (a *Agent) AttachMCPManager(manager *MCPManager) {
	a.mcp = manager
}

// InspectMainPrompt returns the same state and exact main-request messages that
// Send would build for input, without contacting the provider or persisting data.
func (a *Agent) InspectMainPrompt(chatID, branchID int64, input string) (PromptState, []CompletionMessage, error) {
	if strings.TrimSpace(input) == "" {
		return PromptState{}, nil, fmt.Errorf("candidate message is empty")
	}
	return a.nextMainPrompt(chatID, branchID, input)
}

func (a *Agent) nextMainPrompt(chatID, branchID int64, input string) (PromptState, []CompletionMessage, error) {
	state, err := a.loadState(chatID, branchID)
	if err != nil {
		return PromptState{}, nil, err
	}
	prompt, err := BuildMainPrompt(state, input)
	if err != nil {
		return PromptState{}, nil, err
	}
	return state, prompt, nil
}

// Send completes one user turn, including bounded MCP continuations, before
// persisting the conversation and existing memory updates.
func (a *Agent) Send(ctx context.Context, chatID, branchID int64, input string) (TurnResult, error) {
	if strings.TrimSpace(input) == "" {
		return TurnResult{}, fmt.Errorf("user message is empty")
	}
	state, err := a.loadState(chatID, branchID)
	if err != nil {
		return TurnResult{}, err
	}
	if invariant := ForbiddenInvariantForInput(state.Invariants, input); invariant != nil {
		assistant, err := a.store.SaveTurn(SaveTurnInput{
			ChatID:           chatID,
			BranchID:         branchID,
			UserContent:      input,
			AssistantContent: deterministicInvariantRefusal(invariant.ID),
		})
		if err != nil {
			return TurnResult{}, err
		}
		return TurnResult{Assistant: assistant, Metrics: fmt.Sprintf("no API call: blocked by invariant %d", invariant.ID)}, nil
	}
	prompt, err := BuildMainPrompt(state, input)
	if err != nil {
		return TurnResult{}, err
	}
	if err := CheckContextOverflow(a.tokens, a.profile, prompt); err != nil {
		return TurnResult{}, err
	}
	calls := make([]APICall, 0, 3)
	var summaryUpdate *SummaryUpdate
	var factsUpdate *map[string]string

	main, mainCalls, events, err := a.completeMainWithTools(ctx, state.Branch, prompt, input, a.tokens.Count(input), countLineageTokens(a.tokens, state.Lineage))
	calls = append(calls, mainCalls...)
	if err != nil {
		if hasSuccessfulSideEffect(events) {
			return a.persistPartialTurn(chatID, branchID, input, events, calls, true)
		}
		return TurnResult{ToolEvents: events}, fmt.Errorf("complete main response: %w", err)
	}

	switch state.Branch.Strategy {
	case strategySummary:
		var summaryCalls []APICall
		state.Summary, summaryUpdate, summaryCalls, err = a.updateSummary(ctx, state.Branch, state.Lineage, state.Summary)
		calls = append(calls, summaryCalls...)
		if err != nil {
			return a.partialTurnOrError(chatID, branchID, input, events, calls, err)
		}

	case strategyFacts:
		var factCalls []APICall
		state.Facts, factCalls, err = a.updateFacts(ctx, state.Branch, state.Facts, []Message{{Role: "user", Content: input}})
		calls = append(calls, factCalls...)
		if err != nil {
			return a.partialTurnOrError(chatID, branchID, input, events, calls, err)
		}
		factsUpdate = &state.Facts
	}

	extractionState, err := a.memoryExtractionState(branchID)
	if err != nil {
		return a.partialTurnOrError(chatID, branchID, input, events, calls, err)
	}
	memoryPrompt := BuildMemoryExtractionPrompt(input, main.Content, extractionState)
	memoryProfile := a.profile
	memoryProfile.MainMax = a.profile.AuxiliaryMax
	if err := CheckContextOverflow(a.tokens, memoryProfile, memoryPrompt); err != nil {
		return a.partialTurnOrError(chatID, branchID, input, events, calls, err)
	}
	memory, memoryCall, err := a.complete(ctx, state.Branch, "memory", memoryPrompt, a.profile.AuxiliaryMax, 0, 0)
	if err != nil {
		return a.partialTurnOrError(chatID, branchID, input, events, calls, fmt.Errorf("complete memory extraction: %w", err))
	}
	calls = append(calls, memoryCall)
	memoryUpdate, err := ParseMemoryUpdateJSON(memory.Content)
	if err != nil {
		return a.partialTurnOrError(chatID, branchID, input, events, calls, fmt.Errorf("parse memory extraction: %w", err))
	}
	memoryUpdate = routeMemoryUpdate(memoryUpdate, extractionState)

	assistant, err := a.store.SaveTurn(SaveTurnInput{
		ChatID:           chatID,
		BranchID:         branchID,
		UserContent:      input,
		AssistantContent: main.Content,
		Summary:          summaryUpdate,
		Facts:            factsUpdate,
		Memory:           memoryUpdate,
		APICalls:         calls,
	})
	if err != nil {
		return TurnResult{ToolEvents: events, Partial: hasSuccessfulSideEffect(events)}, err
	}

	return TurnResult{Assistant: assistant, Metrics: formatMetrics(a.profile, a.tokens, mainCalls[0]), ToolEvents: events}, nil
}

func (a *Agent) partialTurnOrError(chatID, branchID int64, input string, events []ToolEvent, calls []APICall, err error) (TurnResult, error) {
	if hasSuccessfulSideEffect(events) {
		return a.persistPartialTurn(chatID, branchID, input, events, calls, false)
	}
	return TurnResult{ToolEvents: events}, err
}

func (a *Agent) persistPartialTurn(chatID, branchID int64, input string, events []ToolEvent, calls []APICall, completionFailed bool) (TurnResult, error) {
	status := partialTurnStatus(events, completionFailed)
	assistant, err := a.store.SaveTurn(SaveTurnInput{
		ChatID:           chatID,
		BranchID:         branchID,
		UserContent:      input,
		AssistantContent: status,
		APICalls:         calls,
	})
	if err != nil {
		return TurnResult{ToolEvents: events, Partial: true}, fmt.Errorf("persist partial turn after confirmed external action: %w", err)
	}
	return TurnResult{
		Assistant:  assistant,
		Metrics:    "assistant turn incomplete after confirmed external action",
		ToolEvents: events,
		Partial:    true,
	}, nil
}

func partialTurnStatus(events []ToolEvent, completionFailed bool) string {
	var effects []string
	for _, event := range events {
		switch {
		case event.Name == "save_plan" && event.Status == "completed":
			effects = append(effects, "The review plan was saved.")
		case event.Name == "send_message" && event.Status == "completed":
			effects = append(effects, "The plan was sent.")
		case event.Name == "send_message" && strings.HasPrefix(event.Status, "partial delivery ("):
			effects = append(effects, "The message was partially delivered: "+event.Status+".")
		}
	}
	if completionFailed {
		effects = append(effects, "Assistant completion failed after the external action.")
	} else {
		effects = append(effects, "Assistant turn completion failed during auxiliary processing after the external action.")
	}
	effects = append(effects, "The request and this status were saved to prevent a duplicate resubmission.")
	return strings.Join(effects, " ")
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

func (a *Agent) loadState(chatID, branchID int64) (PromptState, error) {
	branch, err := a.store.Branch(chatID, branchID)
	if err != nil {
		return PromptState{}, err
	}
	state := PromptState{Branch: branch, Facts: make(map[string]string)}
	state.Lineage, err = a.store.Lineage(branchID)
	if err != nil {
		return PromptState{}, err
	}

	if branch.Strategy == strategySummary {
		state.Summary, err = a.store.Summary(branchID)
		if err != nil {
			return PromptState{}, err
		}
	}
	if branch.Strategy == strategyFacts {
		storedFacts, err := a.store.Facts(branchID)
		if err != nil {
			return PromptState{}, err
		}
		for _, fact := range storedFacts {
			state.Facts[fact.Key] = fact.Value
		}
	}

	state.Profile, err = a.store.ActiveProfile()
	if err != nil {
		return PromptState{}, err
	}
	state.Task, err = a.store.ActiveTask(branchID)
	if err != nil {
		return PromptState{}, err
	}
	if state.Task != nil {
		state.Invariants, err = a.store.ActiveInvariants(state.Task.ID)
		if err != nil {
			return PromptState{}, err
		}
	}
	state.LongTermMemories, err = a.store.ActiveLongTermMemories(branchID)
	if err != nil {
		return PromptState{}, err
	}
	return state, nil
}

func (a *Agent) memoryExtractionState(branchID int64) (MemoryExtractionState, error) {
	profile, err := a.store.ActiveProfile()
	if err != nil {
		return MemoryExtractionState{}, err
	}
	task, err := a.store.ActiveTask(branchID)
	if err != nil {
		return MemoryExtractionState{}, err
	}
	state := MemoryExtractionState{Profile: profile, Task: task}
	if task == nil {
		return state, nil
	}
	invariants, err := a.store.ActiveInvariants(task.ID)
	if err != nil {
		return MemoryExtractionState{}, err
	}
	state.Invariants = invariants
	return state, nil
}

func routeMemoryUpdate(update MemoryUpdate, state MemoryExtractionState) MemoryUpdate {
	if state.Profile == nil {
		update.Profile = nil
	}
	if state.Task == nil {
		update.Task = nil
	}
	records := update.Records[:0]
	for _, record := range update.Records {
		if record.Scope == MemoryScopeProfile && state.Profile == nil {
			continue
		}
		if record.Scope == MemoryScopeTask && state.Task == nil {
			continue
		}
		records = append(records, record)
	}
	update.Records = records
	return update
}

func deterministicInvariantRefusal(invariantID int64) string {
	return fmt.Sprintf("Я не могу выполнить этот запрос: он нарушает активный invariant %d. Могу помочь с безопасной альтернативой.", invariantID)
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
			return nil, nil, calls, fmt.Errorf("complete summary: %w", err)
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
		if call.Kind != "" {
			return nil, []APICall{call}, err
		}
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
		return nil, call, err
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

func (a *Agent) completeRequest(ctx context.Context, branch Branch, kind string, prompt []CompletionMessage, maxTokens, currentTokens, fullHistoryTokens int, tools []CompletionTool, toolChoice string) (Completion, APICall, error) {
	startedAt := time.Now()
	completion, err := a.llm.Complete(ctx, CompletionRequest{
		Messages: prompt, MaxTokens: maxTokens, Kind: kind, Tools: tools, ToolChoice: toolChoice,
	})
	if err != nil {
		return Completion{}, APICall{}, err
	}
	price := a.profile.Price(completion.Usage, startedAt)
	responseTokens := a.tokens.Count(completion.Content)
	for _, toolCall := range completion.ToolCalls {
		responseTokens += a.tokens.Count(toolCall.Function.Name) + a.tokens.Count(toolCall.Function.Arguments)
	}
	return completion, APICall{
		Provider:            a.profile.Name,
		Model:               a.profile.Model,
		Kind:                kind,
		Strategy:            branch.Strategy,
		CounterLabel:        a.tokens.Label(),
		CurrentTokens:       currentTokens,
		FullHistoryTokens:   fullHistoryTokens,
		SentTokensLocal:     PromptTokenCount(a.tokens, prompt),
		ResponseTokensLocal: responseTokens,
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
