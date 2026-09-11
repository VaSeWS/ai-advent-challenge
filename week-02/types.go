package main

import "time"

type Message struct {
	ID        int64
	ChatID    int64
	Previous  *int64
	Role      string
	Content   string
	CreatedAt time.Time
}

type Chat struct {
	ID             int64
	Title          string
	ActiveBranchID int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Branch struct {
	ID            int64
	ChatID        int64
	Name          string
	HeadMessageID *int64
	Strategy      string
	WindowSize    int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Summary struct {
	BranchID         int64
	ThroughMessageID int64
	Content          string
	UpdatedAt        time.Time
}

type APICall struct {
	ID, ChatID, BranchID                                       int64
	Provider, Model, Kind, Strategy, CounterLabel, PricingTier string
	CurrentTokens, FullHistoryTokens                           int
	SentTokensLocal, ResponseTokensLocal                       int
	Usage                                                      Usage
	InputCostUSD, OutputCostUSD                                float64
	CreatedAt                                                  time.Time
}

type Usage struct {
	PromptTokens         int
	CachedPromptTokens   int
	UncachedPromptTokens int
	CompletionTokens     int
	TotalTokens          int
}

type CompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionRequest struct {
	Messages  []CompletionMessage
	MaxTokens int
	Kind      string
}

type Completion struct {
	Content      string
	Usage        Usage
	FinishReason string
}

type TurnResult struct {
	Assistant Message
	Metrics   string
}

type StatsRow struct {
	Provider, Model, Strategy, Kind, PricingTier string
	Calls                                        int
	PromptTokens, CompletionTokens               int
	InputCostUSD, OutputCostUSD                  float64
}

const (
	strategyFull      = "full"
	strategySummary   = "summary"
	strategySliding   = "sliding"
	strategyFacts     = "facts"
	strategyBranching = "branching"
)

func validStrategy(s string) bool {
	switch s {
	case strategyFull, strategySummary, strategySliding, strategyFacts, strategyBranching:
		return true
	default:
		return false
	}
}
