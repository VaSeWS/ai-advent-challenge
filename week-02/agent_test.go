package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type agentTestCompleter struct {
	completion Completion
	calls      []CompletionRequest
}

func (c *agentTestCompleter) Complete(_ context.Context, request CompletionRequest) (Completion, error) {
	c.calls = append(c.calls, request)
	return c.completion, nil
}

type agentTestCounter struct {
	tokens int
	label  string
}

func (c agentTestCounter) Count(string) int { return c.tokens }
func (c agentTestCounter) Label() string    { return c.label }
func (agentTestCounter) Exact() bool        { return true }

func TestAgentSendRejectsOversizedInputWithoutCompletion(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	completer := &agentTestCompleter{}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 2, label: "synthetic"}, ProviderProfile{
		Name:          "synthetic-provider",
		Model:         "synthetic-model",
		ContextWindow: 10,
		MainMax:       3,
	})

	_, err = agent.Send(context.Background(), chat.ID, branch.ID, "oversized input")
	if err == nil || !strings.Contains(err.Error(), "context overflow") {
		t.Fatalf("send oversized input error = %v, want context overflow", err)
	}
	if len(completer.calls) != 0 {
		t.Fatalf("completion calls = %d, want 0", len(completer.calls))
	}
}

func TestAgentSendPersistsTurnAndProviderMetricsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	chat, branch, err := store.ActiveChat()
	if err != nil {
		t.Fatalf("load active chat: %v", err)
	}
	profile := ProviderProfile{
		Name:          "synthetic-provider",
		Model:         "synthetic-model",
		ContextWindow: 100,
		MainMax:       4,
	}
	completer := &agentTestCompleter{completion: Completion{
		Content: "synthetic answer",
		Usage: Usage{
			PromptTokens:         11,
			CachedPromptTokens:   2,
			UncachedPromptTokens: 9,
			CompletionTokens:     7,
			TotalTokens:          18,
		},
	}}
	agent := NewAgent(store, completer, agentTestCounter{tokens: 1, label: "synthetic"}, profile)

	result, err := agent.Send(context.Background(), chat.ID, branch.ID, "synthetic question")
	if err != nil {
		t.Fatalf("send turn: %v", err)
	}
	if result.Assistant.Role != "assistant" || result.Assistant.Content != "synthetic answer" {
		t.Fatalf("assistant result = (%q, %q), want persisted synthetic answer", result.Assistant.Role, result.Assistant.Content)
	}
	if len(completer.calls) != 1 {
		t.Fatalf("completion calls = %d, want 1", len(completer.calls))
	}
	if !strings.Contains(result.Metrics, "provider=synthetic-provider/synthetic-model") ||
		!strings.Contains(result.Metrics, "api prompt=11 completion=7 total=18") {
		t.Fatalf("metrics = %q, want synthetic provider and main API usage", result.Metrics)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	lineage, err := reopened.Lineage(branch.ID)
	if err != nil {
		t.Fatalf("load reopened lineage: %v", err)
	}
	if len(lineage) != 2 {
		t.Fatalf("reopened lineage length = %d, want 2", len(lineage))
	}
	if lineage[0].Role != "user" || lineage[0].Content != "synthetic question" {
		t.Fatalf("reopened user message = (%q, %q), want synthetic question", lineage[0].Role, lineage[0].Content)
	}
	if lineage[1].Role != "assistant" || lineage[1].Content != "synthetic answer" {
		t.Fatalf("reopened assistant message = (%q, %q), want synthetic answer", lineage[1].Role, lineage[1].Content)
	}

	stats, err := reopened.Stats(branch.ID)
	if err != nil {
		t.Fatalf("load reopened stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("reopened stats rows = %d, want 1", len(stats))
	}
	stat := stats[0]
	if stat.Provider != profile.Name || stat.Model != profile.Model || stat.Kind != "main" {
		t.Fatalf("reopened main metric = (%q, %q, %q), want (%q, %q, main)", stat.Provider, stat.Model, stat.Kind, profile.Name, profile.Model)
	}
	if stat.Calls != 1 || stat.PromptTokens != 11 || stat.CompletionTokens != 7 {
		t.Fatalf("reopened main metric counts = (%d, %d, %d), want (1, 11, 7)", stat.Calls, stat.PromptTokens, stat.CompletionTokens)
	}
}
