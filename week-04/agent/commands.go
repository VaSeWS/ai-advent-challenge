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
	store       *Store
	agent       *Agent
	mcp         *MCPManager
	mcpFailures map[string]string
}

func NewCommandService(store *Store, agent *Agent, managers ...*MCPManager) *CommandService {
	service := &CommandService{store: store, agent: agent, mcpFailures: make(map[string]string)}
	if len(managers) > 0 {
		service.mcp = managers[0]
	}
	return service
}

func (s *CommandService) setMCPFailures(failures []MCPConnectError) {
	for _, failure := range failures {
		name := failure.ServerName
		if name == "" {
			name = "configuration"
		}
		s.mcpFailures[name] = "connection failed"
	}
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
	case "/mcp":
		return s.executeMCP(ctx, fields)
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

	case "/task":
		if len(fields) < 2 {
			return commandError("usage: /task <create|show|update|pause|resume|transition|history> ...")
		}
		switch fields[1] {
		case "create":
			snapshot, err := taskCreateInput(input)
			if err != nil {
				return commandError("%v", err)
			}
			task, err := s.store.CreateTask(branchID, snapshot)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: "created " + formatTask(task)}, nil
		case "show":
			if len(fields) != 2 {
				return commandError("usage: /task show")
			}
			task, err := s.store.ActiveTask(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: formatActiveTask(task)}, nil
		case "update":
			task, err := s.store.ActiveTask(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			if task == nil {
				return commandError("no active task")
			}
			snapshot, err := taskUpdateSnapshot(fields, task)
			if err != nil {
				return commandError("%v", err)
			}
			updated, err := s.store.UpdateTask(task.ID, snapshot)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: "updated " + formatTask(updated)}, nil
		case "pause", "resume":
			if len(fields) != 2 {
				return commandError("usage: /task %s", fields[1])
			}
			task, err := s.store.ActiveTask(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			if task == nil {
				return commandError("no active task")
			}
			if task.Phase == TaskPhaseDone {
				return commandError("completed task cannot be paused or resumed")
			}
			snapshot := snapshotForTask(task)
			snapshot.Paused = fields[1] == "pause"
			updated, err := s.store.UpdateTask(task.ID, snapshot)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("task %d %sd; phase remains %s", updated.ID, fields[1], updated.Phase)}, nil
		case "transition":
			event, err := taskTransitionEvent(fields)
			if err != nil {
				return commandError("%v", err)
			}
			task, err := s.store.ActiveTask(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			if task == nil {
				return commandError("no active task")
			}
			updated, err := s.store.TransitionTask(task.ID, event)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("task %d transitioned %s -> %s via %s", updated.ID, task.Phase, updated.Phase, event)}, nil
		case "history":
			if len(fields) != 2 {
				return commandError("usage: /task history")
			}
			task, err := s.store.ActiveTask(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			if task == nil {
				return CommandResult{Status: "task history:\n(no active task)"}, nil
			}
			events, err := s.store.TaskEvents(task.ID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: formatTaskHistory(events)}, nil
		default:
			return commandError("usage: /task <create|show|update|pause|resume|transition|history> ...")
		}

	case "/profile":
		if len(fields) < 2 {
			return commandError("usage: /profile <create|select|show> ...")
		}
		switch fields[1] {
		case "create":
			profileInput, err := profileCreateInput(input)
			if err != nil {
				return commandError("%v", err)
			}
			profile, err := s.store.CreateProfile(profileInput)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("created profile %d: %s", profile.ID, profile.Name)}, nil
		case "select":
			profileID, err := positiveID(fields[1:], "/profile select <profile-id>")
			if err != nil {
				return commandError("%v", err)
			}
			profile, err := s.store.SelectProfile(profileID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("selected profile %d: %s", profile.ID, profile.Name)}, nil
		case "show":
			if len(fields) != 2 {
				return commandError("usage: /profile show")
			}
			profiles, err := s.store.Profiles()
			if err != nil {
				return commandError("%v", err)
			}
			activeProfile, err := s.store.ActiveProfile()
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: formatProfiles(profiles, activeProfile)}, nil
		default:
			return commandError("usage: /profile <create|select|show> ...")
		}

	case "/memory":
		if len(fields) < 2 {
			return commandError("usage: /memory <add|list> ...")
		}
		switch fields[1] {
		case "add":
			scope, kind, key, value, err := memoryAddInput(input)
			if err != nil {
				return commandError("%v", err)
			}
			memory, err := s.store.PutActiveLongTermMemory(branchID, scope, kind, key, value)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("saved %s memory %s.%s", scope, memory.Kind, memory.Key)}, nil
		case "list":
			if len(fields) != 2 {
				return commandError("usage: /memory list")
			}
			memories, err := s.store.ActiveLongTermMemories(branchID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: formatLongTermMemories(memories)}, nil
		default:
			return commandError("usage: /memory <add|list> ...")
		}

	case "/invariant":
		if len(fields) < 2 {
			return commandError("usage: /invariant <add|list|deactivate> ...")
		}
		task, err := s.store.ActiveTask(branchID)
		if err != nil {
			return commandError("%v", err)
		}
		if task == nil {
			return commandError("invariant commands require an active task")
		}
		switch fields[1] {
		case "add":
			invariant, err := invariantAddInput(input, task.ID)
			if err != nil {
				return commandError("%v", err)
			}
			added, err := s.store.AddInvariant(invariant)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("added invariant %d: %s.%s=%s", added.ID, added.Category, added.Key, added.RequiredValue)}, nil
		case "list":
			if len(fields) != 2 {
				return commandError("usage: /invariant list")
			}
			invariants, err := s.store.Invariants(task.ID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("task %d %s", task.ID, formatInvariants(invariants))}, nil
		case "deactivate":
			invariantID, err := positiveID(fields[1:], "/invariant deactivate <invariant-id>")
			if err != nil {
				return commandError("%v", err)
			}
			invariant, err := s.store.invariantByID(invariantID)
			if err != nil {
				return commandError("%v", err)
			}
			if invariant.TaskID != task.ID {
				return commandError("invariant %d does not belong to active task %d", invariantID, task.ID)
			}
			deactivated, err := s.store.DeactivateInvariant(invariantID)
			if err != nil {
				return commandError("%v", err)
			}
			return CommandResult{Status: fmt.Sprintf("deactivated invariant %d: %s.%s", deactivated.ID, deactivated.Category, deactivated.Key)}, nil
		default:
			return commandError("usage: /invariant <add|list|deactivate> ...")
		}

	case "/inspect":
		candidate, err := oneRestArgument(input, command)
		if err != nil {
			return commandError("usage: /inspect <candidate-message>")
		}
		if s.agent == nil {
			return commandError("inspect requires an agent")
		}
		state, prompt, err := s.agent.InspectMainPrompt(chatID, branchID, candidate)
		if err != nil {
			return commandError("%v", err)
		}
		blocked := ForbiddenInvariantForInput(state.Invariants, candidate)
		return CommandResult{Status: formatPromptInspection(state, prompt, blocked)}, nil

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
func (s *CommandService) executeMCP(ctx context.Context, fields []string) (CommandResult, error) {
	if s.mcp == nil {
		return commandError("MCP is unavailable")
	}
	if len(fields) < 2 {
		return commandError("usage: /mcp <add|list|remove|connect|disconnect> ...")
	}
	switch fields[1] {
	case "add":
		return s.addMCPServer(fields[2:])
	case "list":
		if len(fields) != 2 {
			return commandError("usage: /mcp list")
		}
		return s.listMCPServers()
	case "remove":
		name, err := oneArgument(fields[1:], "/mcp remove <name>")
		if err != nil {
			return commandError("%v", err)
		}
		if err := s.mcp.Remove(name); err != nil {
			return commandError("remove MCP server: %v", err)
		}
		delete(s.mcpFailures, name)
		return CommandResult{Status: fmt.Sprintf("removed MCP server %q", name)}, nil
	case "connect", "disconnect":
		name, err := oneArgument(fields[1:], "/mcp <connect|disconnect> <name>")
		if err != nil {
			return commandError("%v", err)
		}
		if fields[1] == "connect" {
			if err := s.mcp.Connect(ctx, name); err != nil {
				s.mcpFailures[name] = "connection failed"
				return commandError("MCP server %q connection failed", name)
			}
			delete(s.mcpFailures, name)
			return CommandResult{Status: fmt.Sprintf("connected MCP server %q", name)}, nil
		}
		if err := s.mcp.Disconnect(name); err != nil {
			return commandError("disconnect MCP server %q: %v", name, err)
		}
		delete(s.mcpFailures, name)
		return CommandResult{Status: fmt.Sprintf("disconnected MCP server %q", name)}, nil
	default:
		return commandError("usage: /mcp <add|list|remove|connect|disconnect> ...")
	}
}

func (s *CommandService) addMCPServer(fields []string) (CommandResult, error) {
	separator := -1
	for i, field := range fields {
		if field == "--" {
			separator = i
			break
		}
	}
	if len(fields) < 5 || separator != 3 || separator+1 >= len(fields) {
		return commandError("usage: /mcp add <name> --cwd <path> -- <executable> [argv...]")
	}
	name := fields[0]
	if fields[1] != "--cwd" || fields[2] == "" {
		return commandError("usage: /mcp add <name> --cwd <path> -- <executable> [argv...]")
	}
	config := MCPServerConfig{Name: name, Executable: fields[separator+1], Args: append([]string(nil), fields[separator+2:]...), Cwd: fields[2]}
	if err := s.mcp.Add(config); err != nil {
		return commandError("add MCP server: %v", err)
	}
	return CommandResult{Status: fmt.Sprintf("added MCP server %q; use /mcp connect %s to start it", name, name)}, nil
}

func (s *CommandService) listMCPServers() (CommandResult, error) {
	servers, err := s.mcp.ListServers()
	if err != nil {
		return commandError("list MCP servers: %v", err)
	}
	tools := s.mcp.Tools()
	byServer := make(map[string][]string, len(servers))
	for _, tool := range tools {
		byServer[tool.ServerName] = append(byServer[tool.ServerName], tool.Definition.Name)
	}
	if len(servers) == 0 {
		return CommandResult{Status: "no MCP servers configured"}, nil
	}
	var output strings.Builder
	for i, server := range servers {
		if i > 0 {
			output.WriteByte('\n')
		}
		state := "disabled"
		if server.Enabled {
			state = "connected"
			if failure := s.mcpFailures[server.Name]; failure != "" {
				state = "failed: " + failure
			} else if _, connected := byServer[server.Name]; !connected {
				state = "enabled; no tools discovered"
			}
		}
		fmt.Fprintf(&output, "%s — %s", server.Name, state)
		if names := byServer[server.Name]; len(names) > 0 {
			fmt.Fprintf(&output, "\n  tools: %s", strings.Join(names, ", "))
		}
	}
	return CommandResult{Status: output.String()}, nil
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

const taskCreateUsage = "usage: /task create <goal> | <plan> | <current-step> | <expected-action>"

func taskCreateInput(input string) (TaskSnapshot, error) {
	arguments := strings.TrimSpace(input)
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "/task"))
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "create"))
	var fields []string
	if strings.Contains(arguments, "|") {
		fields = strings.Split(arguments, "|")
		for index := range fields {
			fields[index] = strings.TrimSpace(fields[index])
		}
	} else {
		fields = strings.Fields(arguments)
	}
	if len(fields) != 4 {
		return TaskSnapshot{}, fmt.Errorf(taskCreateUsage)
	}
	for _, field := range fields {
		if field == "" {
			return TaskSnapshot{}, fmt.Errorf(taskCreateUsage)
		}
	}
	return TaskSnapshot{
		Goal:           fields[0],
		Plan:           fields[1],
		CurrentStep:    fields[2],
		ExpectedAction: fields[3],
	}, nil
}

func snapshotForTask(task *Task) TaskSnapshot {
	return TaskSnapshot{
		Goal:           task.Goal,
		Plan:           task.Plan,
		CurrentStep:    task.CurrentStep,
		ExpectedAction: task.ExpectedAction,
		Paused:         task.Paused,
	}
}

func taskUpdateSnapshot(fields []string, task *Task) (TaskSnapshot, error) {
	const usage = "usage: /task update <plan|step|action> <value>"
	if len(fields) < 4 {
		return TaskSnapshot{}, fmt.Errorf(usage)
	}
	value := strings.TrimSpace(strings.Join(fields[3:], " "))
	if value == "" {
		return TaskSnapshot{}, fmt.Errorf(usage)
	}
	snapshot := snapshotForTask(task)
	switch fields[2] {
	case "plan":
		snapshot.Plan = value
	case "step":
		snapshot.CurrentStep = value
	case "action":
		snapshot.ExpectedAction = value
	default:
		return TaskSnapshot{}, fmt.Errorf(usage)
	}
	return snapshot, nil
}

func taskTransitionEvent(fields []string) (TaskEventKind, error) {
	const usage = "usage: /task transition <approve_plan|submit_result|validation_passed|validation_failed>"
	if len(fields) != 3 {
		return "", fmt.Errorf(usage)
	}
	event := TaskEventKind(fields[2])
	switch event {
	case TaskEventApprovePlan, TaskEventSubmitResult, TaskEventValidationPassed, TaskEventValidationFailed:
		return event, nil
	default:
		return "", fmt.Errorf(usage)
	}
}

const profileCreateUsage = "usage: /profile create <name> | <language> | <response-style> | <response-format> | <constraints>"

func profileCreateInput(input string) (ProfileInput, error) {
	arguments := strings.TrimSpace(input)
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "/profile"))
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "create"))
	var fields []string
	if strings.Contains(arguments, "|") {
		fields = strings.Split(arguments, "|")
		for index := range fields {
			fields[index] = strings.TrimSpace(fields[index])
		}
	} else {
		fields = strings.Fields(arguments)
	}
	if len(fields) != 5 {
		return ProfileInput{}, fmt.Errorf(profileCreateUsage)
	}
	for _, field := range fields[:4] {
		if field == "" {
			return ProfileInput{}, fmt.Errorf(profileCreateUsage)
		}
	}
	return ProfileInput{
		Name:           fields[0],
		Language:       fields[1],
		ResponseStyle:  fields[2],
		ResponseFormat: fields[3],
		Constraints:    fields[4],
	}, nil
}

const invariantAddUsage = "usage: /invariant add <category> | <key> | <required-value> | <rule> | <source> [| <forbidden-terms>]"

func invariantAddInput(input string, taskID int64) (Invariant, error) {
	arguments := strings.TrimSpace(input)
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "/invariant"))
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "add"))
	fields := strings.Split(arguments, "|")
	if len(fields) != 5 && len(fields) != 6 {
		return Invariant{}, fmt.Errorf(invariantAddUsage)
	}
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
		if index < 5 && fields[index] == "" {
			return Invariant{}, fmt.Errorf(invariantAddUsage)
		}
	}
	return Invariant{
		TaskID:        taskID,
		Category:      fields[0],
		Key:           fields[1],
		RequiredValue: fields[2],
		Rule:          fields[3],
		Source:        fields[4],
		Forbidden:     strings.Join(fields[5:], ""),
	}, nil
}

const memoryAddUsage = "usage: /memory add <profile|task> | <decision|knowledge> | <key> | <value>"

func memoryAddInput(input string) (MemoryScope, MemoryKind, string, string, error) {
	arguments := strings.TrimSpace(input)
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "/memory"))
	arguments = strings.TrimSpace(strings.TrimPrefix(arguments, "add"))
	fields := strings.Split(arguments, "|")
	if len(fields) != 4 {
		return "", "", "", "", fmt.Errorf(memoryAddUsage)
	}
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
		if fields[index] == "" {
			return "", "", "", "", fmt.Errorf(memoryAddUsage)
		}
	}
	scope := MemoryScope(fields[0])
	kind := MemoryKind(fields[1])
	if (scope != MemoryScopeProfile && scope != MemoryScopeTask) || !validMemoryKind(kind) {
		return "", "", "", "", fmt.Errorf(memoryAddUsage)
	}
	return scope, kind, fields[2], fields[3], nil
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

func formatProfiles(profiles []Profile, active *Profile) string {
	if len(profiles) == 0 {
		return "profiles:\n(no profiles)"
	}
	var status strings.Builder
	status.WriteString("profiles:")
	for _, profile := range profiles {
		marker := " "
		if active != nil && profile.ID == active.ID {
			marker = "*"
		}
		fmt.Fprintf(&status, "\n%s %d: %s | language=%s | style=%s | format=%s | constraints=%s", marker, profile.ID, profile.Name, profile.Language, profile.ResponseStyle, profile.ResponseFormat, profile.Constraints)
	}
	return status.String()
}
func formatInvariants(invariants []Invariant) string {
	if len(invariants) == 0 {
		return "invariants:\n(none)"
	}
	var status strings.Builder
	status.WriteString("invariants:")
	for _, invariant := range invariants {
		state := "inactive"
		if invariant.Active {
			state = "active"
		}
		fmt.Fprintf(&status, "\n%d: %s.%s=%s | %s | rule=%s | source=%s", invariant.ID, invariant.Category, invariant.Key, invariant.RequiredValue, state, invariant.Rule, invariant.Source)
		if invariant.Forbidden != "" {
			fmt.Fprintf(&status, " | forbidden=%s", invariant.Forbidden)
		}
	}
	return status.String()
}

func formatLongTermMemories(memories []LongTermMemory) string {
	if len(memories) == 0 {
		return "long-term memory:\n(none)"
	}
	var status strings.Builder
	status.WriteString("long-term memory:")
	for _, memory := range memories {
		fmt.Fprintf(&status, "\n%s %s.%s=%s", longTermMemoryScope(memory), memory.Kind, memory.Key, memory.Value)
	}
	return status.String()
}

func formatActiveTask(task *Task) string {
	if task == nil {
		return "task:\n(no active task)"
	}
	return formatTask(*task)
}

func formatTask(task Task) string {
	return fmt.Sprintf("task %d:\ngoal: %s\nplan: %s\nphase: %s\npaused: %t\ncurrent step: %s\nexpected action: %s",
		task.ID, task.Goal, task.Plan, task.Phase, task.Paused, task.CurrentStep, task.ExpectedAction)
}

func formatTaskHistory(events []TaskEvent) string {
	if len(events) == 0 {
		return "task history:\n(no transitions)"
	}
	var status strings.Builder
	status.WriteString("task history:")
	for index, event := range events {
		fmt.Fprintf(&status, "\n%d. %s --%s--> %s | %s", index+1, event.FromPhase, event.Event, event.ToPhase, event.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	return status.String()
}

func formatPromptInspection(state PromptState, prompt []CompletionMessage, blocked ...*Invariant) string {
	var status strings.Builder
	status.WriteString("prompt inspection:")
	writeInspectionLayer(&status, "invariants", ActiveInvariantsSystemBlock(state.Invariants))
	writeInspectionLayer(&status, "profile", ActiveProfileSystemBlock(state.Profile))
	writeInspectionLayer(&status, "long-term memory", LongTermMemorySystemBlock(state.LongTermMemories))
	writeInspectionLayer(&status, "task", ActiveTaskSystemBlock(state.Task))

	status.WriteString("\nraw dialogue:")
	if len(state.Lineage) == 0 {
		status.WriteString("\n(none)")
	}
	for index, message := range state.Lineage {
		fmt.Fprintf(&status, "\n%d. %s:\n%s", index+1, message.Role, message.Content)
	}

	label := "next main request:"
	if len(blocked) != 0 && blocked[0] != nil {
		fmt.Fprintf(&status, "\nmatched invariant rail: %d (%s.%s)", blocked[0].ID, blocked[0].Category, blocked[0].Key)
		label = "hypothetical main request if allowed:"
	}
	status.WriteString("\n" + label)
	for index, message := range prompt {
		fmt.Fprintf(&status, "\n%d. %s:\n%s", index+1, message.Role, message.Content)
	}
	return status.String()
}

func writeInspectionLayer(status *strings.Builder, name, content string) {
	fmt.Fprintf(status, "\n%s:", name)
	if content == "" {
		status.WriteString("\n(none)")
		return
	}
	status.WriteByte('\n')
	status.WriteString(content)
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
/task create <goal> | <plan> | <current-step> | <expected-action>
/task show
/task update <plan|step|action> <value>
/task pause
/task resume
/task transition <approve_plan|submit_result|validation_passed|validation_failed>
/task history
/profile create <name> | <language> | <response-style> | <response-format> | <constraints>
/profile select <profile-id>
/profile show
/memory add <profile|task> | <decision|knowledge> | <key> | <value>
/memory list
/invariant add <category> | <key> | <required-value> | <rule> | <source> [| <forbidden-terms>]
/invariant list
/invariant deactivate <invariant-id>
/mcp add <name> --cwd <path> -- <executable> [argv...]
/mcp list
/mcp remove <name>
/mcp connect <name>
/mcp disconnect <name>
Arguments are passed literally, without a shell; do not put credentials in argv.
MCP child processes inherit the application environment.
/inspect <candidate-message>
/help
/quit`
