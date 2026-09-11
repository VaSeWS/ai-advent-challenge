package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// CommandResult is display-ready data returned after a slash command. Chat,
// Branch, and Lineage are supplied when the active conversation must be
// reloaded by the caller.
type CommandResult struct {
	Quit    bool
	Status  string
	Chat    *Chat
	Branch  *Branch
	Lineage []Message
}

// CommandService owns command parsing and delegates all state changes to Store
// and Agent.
type CommandService struct {
	store *Store
	agent *Agent
}

func NewCommandService(store *Store, agent *Agent) *CommandService {
	return &CommandService{store: store, agent: agent}
}

// Execute parses and executes the supported slash-command surface. Invalid
// commands return a display-ready status and do not change persisted state.
func (s *CommandService) Execute(ctx context.Context, chatID, branchID int64, input string) (CommandResult, error) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return commandError("empty command; use /help")
	}

	command := fields[0]
	if !strings.HasPrefix(command, "/") {
		return commandError("not a command; use /help")
	}

	switch command {
	case "/new":
		title, err := oneRestArgument(input, command)
		if err != nil {
			return commandError("usage: /new <title>")
		}
		if _, _, err := s.store.CreateChat(title); err != nil {
			return commandError("%v", err)
		}
		result, err := s.reloadActive()
		if err != nil {
			return commandError("%v", err)
		}
		result.Status = fmt.Sprintf("created chat %d: %s", result.Chat.ID, result.Chat.Title)
		return result, nil

	case "/chats":
		if len(fields) != 1 {
			return commandError("usage: /chats")
		}
		chats, err := s.store.Chats()
		if err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: formatChats(s.store, chats)}, nil

	case "/use":
		chatToUse, err := positiveID(fields, "/use <chat-id>")
		if err != nil {
			return commandError("%v", err)
		}
		if _, _, err := s.store.UseChat(chatToUse); err != nil {
			return commandError("%v", err)
		}
		result, err := s.reloadActive()
		if err != nil {
			return commandError("%v", err)
		}
		result.Status = fmt.Sprintf("using chat %d: %s", result.Chat.ID, result.Chat.Title)
		return result, nil

	case "/mode":
		mode, err := oneArgument(fields, "/mode <full|summary|sliding|facts|branching>")
		if err != nil {
			return commandError("%v", err)
		}
		if !validStrategy(mode) {
			return commandError("unknown context mode %q", mode)
		}
		branch, err := s.store.Branch(chatID, branchID)
		if err != nil {
			return commandError("%v", err)
		}
		enteringFacts := mode == strategyFacts && branch.Strategy != strategyFacts
		if enteringFacts && s.agent == nil {
			return commandError("facts rebuild requires an agent")
		}
		branch, err = s.store.SetBranchMode(branchID, mode)
		if err != nil {
			return commandError("%v", err)
		}
		if enteringFacts {
			if err := s.agent.RebuildFacts(ctx, chatID, branchID); err != nil {
				return commandError("%v", err)
			}
		}
		status := fmt.Sprintf("mode set to %s", mode)
		if enteringFacts {
			status += "; rebuilt facts"
		}
		return CommandResult{Status: status, Branch: &branch}, nil

	case "/window":
		window, err := positiveWindow(fields)
		if err != nil {
			return commandError("%v", err)
		}
		branch, err := s.store.SetWindow(branchID, window)
		if err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: fmt.Sprintf("window set to %d", window), Branch: &branch}, nil

	case "/checkpoint":
		name, err := oneArgument(fields, "/checkpoint <name>")
		if err != nil {
			return commandError("%v", err)
		}
		branch, err := s.store.Branch(chatID, branchID)
		if err != nil {
			return commandError("%v", err)
		}
		if branch.Strategy != strategyBranching {
			return commandError("checkpoints require branching mode")
		}
		if _, err := s.store.CreateCheckpoint(branchID, name); err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: fmt.Sprintf("created checkpoint %s", name)}, nil

	case "/fork":
		checkpoint, name, err := forkArguments(fields)
		if err != nil {
			return commandError("%v", err)
		}
		branch, err := s.store.Branch(chatID, branchID)
		if err != nil {
			return commandError("%v", err)
		}
		if branch.Strategy != strategyBranching {
			return commandError("forks require branching mode")
		}
		if _, err := s.store.Fork(branchID, checkpoint, name); err != nil {
			return commandError("%v", err)
		}
		result, err := s.reloadActive()
		if err != nil {
			return commandError("%v", err)
		}
		result.Status = fmt.Sprintf("forked branch %s from checkpoint %s", result.Branch.Name, checkpoint)
		return result, nil

	case "/branches":
		if len(fields) != 1 {
			return commandError("usage: /branches")
		}
		branches, err := s.store.Branches(chatID)
		if err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: formatBranches(branches, branchID)}, nil

	case "/switch":
		name, err := oneArgument(fields, "/switch <branch-name>")
		if err != nil {
			return commandError("%v", err)
		}
		if _, err := s.store.SwitchBranch(chatID, name); err != nil {
			return commandError("%v", err)
		}
		result, err := s.reloadActive()
		if err != nil {
			return commandError("%v", err)
		}
		result.Status = fmt.Sprintf("switched to branch %s", result.Branch.Name)
		return result, nil

	case "/facts":
		if len(fields) != 1 {
			return commandError("usage: /facts")
		}
		facts, err := s.store.Facts(branchID)
		if err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: formatFacts(facts)}, nil

	case "/stats":
		all := len(fields) == 2 && fields[1] == "all"
		if len(fields) != 1 && !all {
			return commandError("usage: /stats [all]")
		}
		var stats []StatsRow
		var err error
		if all {
			stats, err = s.store.AllStats()
		} else {
			stats, err = s.store.Stats(branchID)
		}
		if err != nil {
			return commandError("%v", err)
		}
		return CommandResult{Status: formatStats(stats, all)}, nil

	case "/help":
		if len(fields) != 1 {
			return commandError("usage: /help")
		}
		return CommandResult{Status: helpText}, nil

	case "/quit":
		if len(fields) != 1 {
			return commandError("usage: /quit")
		}
		return CommandResult{Quit: true, Status: "quitting"}, nil

	default:
		return commandError("unknown command: %s; use /help", command)
	}
}

func (s *CommandService) reloadActive() (CommandResult, error) {
	chat, branch, err := s.store.ActiveChat()
	if err != nil {
		return CommandResult{}, err
	}
	lineage, err := s.store.Lineage(branch.ID)
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Chat: &chat, Branch: &branch, Lineage: lineage}, nil
}

func commandError(format string, args ...any) (CommandResult, error) {
	err := fmt.Errorf(format, args...)
	return CommandResult{Status: err.Error()}, err
}

func oneRestArgument(input, command string) (string, error) {
	argument := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), command))
	if argument == "" {
		return "", fmt.Errorf("usage: /new <title>")
	}
	return argument, nil
}

func oneArgument(fields []string, usage string) (string, error) {
	if len(fields) != 2 || fields[1] == "" {
		return "", fmt.Errorf("usage: %s", usage)
	}
	return fields[1], nil
}

func positiveID(fields []string, usage string) (int64, error) {
	value, err := oneArgument(fields, usage)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("usage: %s", usage)
	}
	return id, nil
}

func positiveWindow(fields []string) (int, error) {
	value, err := oneArgument(fields, "/window <N>")
	if err != nil {
		return 0, err
	}
	window, err := strconv.Atoi(value)
	if err != nil || window <= 0 {
		return 0, fmt.Errorf("usage: /window <N>")
	}
	return window, nil
}

func forkArguments(fields []string) (string, string, error) {
	if len(fields) != 3 || fields[1] == "" || fields[2] == "" {
		return "", "", fmt.Errorf("usage: /fork <checkpoint> <new-branch>")
	}
	return fields[1], fields[2], nil
}

func formatChats(store *Store, chats []Chat) string {
	if len(chats) == 0 {
		return "chats:\n(no chats)"
	}
	var status strings.Builder
	status.WriteString("chats:")
	for _, chat := range chats {
		branch, err := store.Branch(chat.ID, chat.ActiveBranchID)
		if err != nil {
			return fmt.Sprintf("chats unavailable: %v", err)
		}
		fmt.Fprintf(&status, "\n%d: %s | mode=%s | branch=%s", chat.ID, chat.Title, branch.Strategy, branch.Name)
	}
	return status.String()
}

func formatBranches(branches []Branch, activeBranchID int64) string {
	if len(branches) == 0 {
		return "branches:\n(no branches)"
	}
	var status strings.Builder
	status.WriteString("branches:")
	for _, branch := range branches {
		marker := " "
		if branch.ID == activeBranchID {
			marker = "*"
		}
		fmt.Fprintf(&status, "\n%s %s | mode=%s | window=%d", marker, branch.Name, branch.Strategy, branch.WindowSize)
	}
	return status.String()
}

func formatFacts(facts []Fact) string {
	if len(facts) == 0 {
		return "facts:\n(no facts)"
	}
	var status strings.Builder
	status.WriteString("facts:")
	for _, fact := range facts {
		fmt.Fprintf(&status, "\n%s: %s", fact.Key, fact.Value)
	}
	return status.String()
}

func formatStats(stats []StatsRow, all bool) string {
	heading := "stats:"
	if all {
		heading = "stats (all):"
	}
	if len(stats) == 0 {
		return heading + "\n(no API calls)"
	}
	var status strings.Builder
	status.WriteString(heading)
	for _, row := range stats {
		fmt.Fprintf(&status, "\nprovider=%s/%s | strategy=%s | kind=%s | tier=%s | calls=%d | api prompt=%d completion=%d | cost=$%.6f", row.Provider, row.Model, row.Strategy, row.Kind, row.PricingTier, row.Calls, row.PromptTokens, row.CompletionTokens, row.InputCostUSD+row.OutputCostUSD)
	}
	return status.String()
}

const helpText = `commands:
/new <title>
/chats
/use <chat-id>
/mode <full|summary|sliding|facts|branching>
/window <N>
/checkpoint <name>
/fork <checkpoint> <new-branch>
/branches
/switch <branch-name>
/facts
/stats [all]
/help
/quit`
