package main

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	sidebarBreakpoint = 80
	sidebarWidth      = 28
	inputHeight       = 3
)

type app struct {
	store    *Store
	agent    *Agent
	commands *CommandService
	profile  ProviderProfile

	chat     Chat
	branch   Branch
	lineage  []Message
	chats    []Chat
	input    textarea.Model
	viewport viewport.Model
	spinner  spinner.Model

	width                     int
	height                    int
	busy                      bool
	status                    string
	errText                   string
	metrics                   string
	submittedInput            string
	scrollToBottomAfterLayout bool
}

type turnFinishedMsg struct {
	result  TurnResult
	lineage []Message
	chat    Chat
	branch  Branch
	err     error
}

type commandFinishedMsg struct {
	result CommandResult
	err    error
}

// transcriptWheelMsg keeps Bubble Tea's raw mouse event out of the renderer
// dispatch loop while delivering it to the viewport update path.
type transcriptWheelMsg struct {
	wheel tea.MouseWheelMsg
}

// NewApp creates the full-screen terminal interface and restores the persisted
// active chat, branch, and transcript before Bubble Tea starts its event loop.
func NewApp(store *Store, agent *Agent, profile ProviderProfile) tea.Model {
	input := textarea.New()
	input.Prompt = "You> "
	input.Placeholder = "Write a message or slash command"
	input.ShowLineNumbers = false
	input.MaxHeight = inputHeight
	input.SetHeight(1)
	// Focus mutates textarea.Model. Init runs on a value copy of app, so the
	// initial focus state must be established before constructing app.
	input.Focus()

	m := app{
		store:    store,
		agent:    agent,
		commands: NewCommandService(store, agent),
		profile:  profile,
		input:    input,
		viewport: viewport.New(),
		spinner:  spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.viewport.SoftWrap = true
	m.loadActive()
	m.layout()
	return m
}

func (m app) Init() tea.Cmd {
	return m.input.Focus()
}

func (m app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case transcriptWheelMsg:
		m.viewport, _ = m.viewport.Update(msg.wheel)
		return m, nil

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case turnFinishedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Request failed."
			m.errText = msg.err.Error()
			m.input.SetValue(m.submittedInput)
			m.layout()
			return m, m.input.Focus()
		}
		m.lineage = msg.lineage
		m.chat = msg.chat
		m.branch = msg.branch
		m.metrics = msg.result.Metrics
		m.status = "Response received."
		m.errText = ""
		m.submittedInput = ""
		m.refreshTranscript()
		m.refreshSidebar()
		m.layout()
		return m, m.input.Focus()

	case commandFinishedMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "Command failed."
			m.errText = msg.err.Error()
			m.input.SetValue(m.submittedInput)
			m.layout()
			return m, m.input.Focus()
		}
		if msg.result.Quit {
			return m, tea.Quit
		}
		m.status = msg.result.Status
		m.errText = ""
		m.submittedInput = ""
		if msg.result.Chat != nil {
			m.chat = *msg.result.Chat
		}
		if msg.result.Branch != nil {
			m.branch = *msg.result.Branch
		}
		if msg.result.Lineage != nil {
			m.lineage = msg.result.Lineage
			m.refreshTranscript()
		}
		m.refreshSidebar()
		m.layout()
		return m, m.input.Focus()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "pgup", "pgdown":
			m.viewport, _ = m.viewport.Update(msg)
			return m, nil
		case "enter":
			if m.busy {
				return m, nil
			}
			return m.submit()
		case "ctrl+j":
			if !m.busy {
				m.input.InsertString("\n")
				m.layout()
			}
			return m, nil
		}
	}

	if m.busy {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return m, cmd
}

func (m app) View() tea.View {
	mainWidth := m.contentWidth()
	lines := []string{m.header(mainWidth)}
	if notice := m.notice(mainWidth); notice != "" {
		lines = append(lines, notice)
	}
	lines = append(lines, "Transcript:", m.viewport.View(), m.input.View(), "Enter submit · Ctrl+J newline · PgUp/PgDn transcript · Ctrl+C quit")
	main := lipgloss.NewStyle().Width(mainWidth).Render(strings.Join(lines, "\n"))

	content := main
	if m.showSidebar() {
		content = lipgloss.JoinHorizontal(lipgloss.Top, main, m.sidebar())
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.OnMouse = func(mouse tea.MouseMsg) tea.Cmd {
		wheel, ok := mouse.(tea.MouseWheelMsg)
		if !ok {
			return nil
		}
		return func() tea.Msg { return transcriptWheelMsg{wheel: wheel} }
	}
	return view
}

func (m *app) submit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		m.status = "Enter a message or slash command."
		m.errText = ""
		m.layout()
		return *m, nil
	}

	m.busy = true
	m.errText = ""
	m.submittedInput = input
	m.input.SetValue("")
	m.input.Blur()
	m.layout()
	if strings.HasPrefix(input, "/") {
		m.status = "Running command..."
		return *m, tea.Batch(m.spinner.Tick, m.executeCommand(input))
	}
	m.status = "Waiting for response..."
	return *m, tea.Batch(m.spinner.Tick, m.send(input))
}

func (m app) send(input string) tea.Cmd {
	store, agent := m.store, m.agent
	chatID, branchID := m.chat.ID, m.branch.ID
	return func() tea.Msg {
		result, err := agent.Send(context.Background(), chatID, branchID, input)
		if err != nil {
			return turnFinishedMsg{err: err}
		}
		lineage, err := store.Lineage(branchID)
		if err != nil {
			return turnFinishedMsg{err: fmt.Errorf("reload transcript: %w", err)}
		}
		chat, branch, err := store.ActiveChat()
		if err != nil {
			return turnFinishedMsg{err: fmt.Errorf("reload active conversation: %w", err)}
		}
		return turnFinishedMsg{result: result, lineage: lineage, chat: chat, branch: branch}
	}
}

func (m app) executeCommand(input string) tea.Cmd {
	commands := m.commands
	chatID, branchID := m.chat.ID, m.branch.ID
	return func() tea.Msg {
		result, err := commands.Execute(context.Background(), chatID, branchID, input)
		return commandFinishedMsg{result: result, err: err}
	}
}

func (m *app) loadActive() {
	chat, branch, err := m.store.ActiveChat()
	if err != nil {
		m.errText = err.Error()
		return
	}
	lineage, err := m.store.Lineage(branch.ID)
	if err != nil {
		m.chat, m.branch = chat, branch
		m.errText = fmt.Sprintf("load transcript: %v", err)
		return
	}
	m.chat, m.branch, m.lineage = chat, branch, lineage
	m.refreshTranscript()
	m.refreshSidebar()
}

func (m *app) refreshSidebar() {
	chats, err := m.store.Chats()
	if err != nil {
		m.errText = fmt.Sprintf("load chats: %v", err)
		return
	}
	m.chats = chats
}

func (m *app) refreshTranscript() {
	m.viewport.SetContent(renderTranscript(m.lineage))
	m.scrollToBottomAfterLayout = true
}

func (m *app) layout() {
	width := m.contentWidth()
	m.input.SetWidth(width)
	m.input.SetHeight(min(inputHeight, max(1, m.input.LineCount())))
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(max(1, m.height-1-m.noticeHeight(width)-1-m.input.Height()-1))
	if m.scrollToBottomAfterLayout && m.width > 0 && m.height > 0 {
		m.viewport.GotoBottom()
		m.scrollToBottomAfterLayout = false
	}
}

func (m app) contentWidth() int {
	if m.showSidebar() {
		return max(1, m.width-sidebarWidth)
	}
	return max(1, m.width)
}

func (m app) showSidebar() bool {
	return m.width >= sidebarBreakpoint
}

func (m app) header(width int) string {
	chat := "no active chat"
	branch := ""
	if m.chat.ID != 0 {
		chat = fmt.Sprintf("chat %d: %s", m.chat.ID, m.chat.Title)
		branch = fmt.Sprintf(" | branch %s · %s · window %d", m.branch.Name, m.branch.Strategy, m.branch.WindowSize)
	}
	return clipText(fmt.Sprintf("%s%s | %s/%s", chat, branch, m.profile.Name, m.profile.Model), width, 1)
}

func (m app) notice(width int) string {
	var lines []string
	if m.busy {
		lines = append(lines, clipText(m.spinner.View()+" "+m.status, width, 1))
	} else if m.status != "" {
		lines = append(lines, clipText(m.status, width, 3))
	}
	if m.errText != "" {
		lines = append(lines, clipText("error: "+m.errText, width, 2))
	}
	if m.metrics != "" {
		lines = append(lines, clipText(m.metrics, width, 2))
	}
	return strings.Join(lines, "\n")
}

func (m app) noticeHeight(width int) int {
	notice := m.notice(width)
	if notice == "" {
		return 0
	}
	return strings.Count(notice, "\n") + 1
}

func (m app) sidebar() string {
	lines := []string{"Chats"}
	for _, chat := range m.chats {
		marker := " "
		if chat.ID == m.chat.ID {
			marker = ">"
		}
		line := fmt.Sprintf("%s %d: %s", marker, chat.ID, chat.Title)
		lines = append(lines, clipText(line, sidebarWidth, 1))
		if chat.ID == m.chat.ID {
			lines = append(lines, clipText(fmt.Sprintf("  %s · %s", m.branch.Name, m.branch.Strategy), sidebarWidth, 1))
		}
	}
	for len(lines) < max(1, m.height) {
		lines = append(lines, "")
	}
	return lipgloss.NewStyle().Width(sidebarWidth).Render(strings.Join(lines, "\n"))
}

func renderTranscript(messages []Message) string {
	if len(messages) == 0 {
		return "No messages yet."
	}
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		role := strings.ToUpper(message.Role)
		parts = append(parts, role+":\n"+message.Content)
	}
	return strings.Join(parts, "\n\n")
}

func clipText(text string, width, limit int) string {
	if width < 1 || limit < 1 {
		return ""
	}
	var lines []string
	for _, source := range strings.Split(text, "\n") {
		runes := []rune(source)
		if len(runes) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(runes) > 0 {
			end := min(width, len(runes))
			lines = append(lines, string(runes[:end]))
			runes = runes[end:]
		}
	}
	if len(lines) > limit {
		lines = lines[:limit]
		last := []rune(lines[len(lines)-1])
		if len(last) >= 1 {
			last[len(last)-1] = '…'
			lines[len(lines)-1] = string(last)
		}
	}
	return strings.Join(lines, "\n")
}
