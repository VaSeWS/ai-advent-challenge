package main

import "time"

// Profile is a named, persistent presentation preference set.
type Profile struct {
	ID             int64
	Name           string
	Language       string
	ResponseStyle  string
	ResponseFormat string
	Constraints    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProfileInput contains the mutable fields of a profile.
type ProfileInput struct {
	Name           string
	Language       string
	ResponseStyle  string
	ResponseFormat string
	Constraints    string
}

// TaskPhase is the lifecycle phase of a task.
type TaskPhase string

const (
	TaskPhasePlanning   TaskPhase = "planning"
	TaskPhaseExecution  TaskPhase = "execution"
	TaskPhaseValidation TaskPhase = "validation"
	TaskPhaseDone       TaskPhase = "done"
)

// Task is the branch-scoped working-memory snapshot.
type Task struct {
	ID             int64
	BranchID       int64
	Goal           string
	Plan           string
	Phase          TaskPhase
	CurrentStep    string
	ExpectedAction string
	Paused         bool
	Active         bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TaskSnapshot contains the mutable working-memory fields. Phase and Active
// deliberately are not mutable through this type.
type TaskSnapshot struct {
	Goal           string
	Plan           string
	CurrentStep    string
	ExpectedAction string
	Paused         bool
}

// MemoryKind classifies a normalized long-term record.
type MemoryKind string

const (
	MemoryKindDecision  MemoryKind = "decision"
	MemoryKindKnowledge MemoryKind = "knowledge"
)

func validMemoryKind(kind MemoryKind) bool {
	switch kind {
	case MemoryKindDecision, MemoryKindKnowledge:
		return true
	default:
		return false
	}
}

// LongTermMemory is a normalized record owned by exactly one profile or task.
type LongTermMemory struct {
	ID        int64
	Kind      MemoryKind
	Key       string
	Value     string
	ProfileID *int64
	TaskID    *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Invariant is a structured, task-scoped requirement. Forbidden stores the
// canonical comma-separated lowercase terms that trigger deterministic input
// refusal while the invariant is active.
type Invariant struct {
	ID            int64
	TaskID        int64
	Category      string
	Key           string
	RequiredValue string
	Rule          string
	Source        string
	Forbidden     string
	Active        bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TaskEventKind identifies an immutable task lifecycle transition.
type TaskEventKind string

const (
	TaskEventApprovePlan      TaskEventKind = "approve_plan"
	TaskEventSubmitResult     TaskEventKind = "submit_result"
	TaskEventValidationPassed TaskEventKind = "validation_passed"
	TaskEventValidationFailed TaskEventKind = "validation_failed"
)

// TaskEvent records one immutable task lifecycle transition.
type TaskEvent struct {
	ID        int64
	TaskID    int64
	FromPhase TaskPhase
	Event     TaskEventKind
	ToPhase   TaskPhase
	CreatedAt time.Time
}

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
