package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type chatLaneMessage struct {
	generation uint64
	lane       int
	result     LaneResult
	err        error
}

type chatPairSavedMessage struct {
	generation uint64
	turn       ChatTurn
	runID      string
	err        error
}

type chatWheelMessage struct{ event tea.MouseWheelMsg }

type chatPane struct {
	status   string
	result   *LaneResult
	viewport viewport.Model
}

type chatApp struct {
	ctx                context.Context
	cancel             context.CancelFunc
	store              *Store
	engine             *Engine
	service            ChatService
	session            ChatSession
	rawTurns           int
	lanes              [2]LaneSettings
	pair               string
	experiment         bool
	generationSettings GenerationSettings
	generation         uint64
	busy               bool
	pending            int
	turns              []ChatTurn
	results            [2]LaneResult
	panes              [2]chatPane
	focused            int
	inspector          string
	inspect            viewport.Model
	input              textarea.Model
	width              int
	height             int
	status             string
	fatalErr           error
	question           string
	prepared           TurnPreparation
}

func newChatApp(ctx context.Context, cancel context.CancelFunc, store *Store, engine *Engine, rawTurns int, session ChatSession, lanes [2]LaneSettings, experiment bool, generation GenerationSettings) (*chatApp, error) {
	input := textarea.New()
	input.Prompt = "You> "
	input.Placeholder = "Ask a question about GT:NH"
	input.ShowLineNumbers = false
	input.MaxHeight = 3
	input.SetHeight(1)
	inspect := viewport.New()
	inspect.SoftWrap = true
	app := &chatApp{
		ctx: ctx, cancel: cancel, store: store, engine: engine, service: ChatService{Engine: engine},
		session: session, rawTurns: rawTurns, lanes: lanes,
		pair: chatPairName(lanes), experiment: experiment, generationSettings: generation,
		input: input, inspect: inspect,
		status: "Ready. Persistent turns use grounded answers; experiments have no history.",
	}
	if session.ID != "" {
		app.rawTurns = session.RawTurns
		app.generationSettings = session.Generation
		turns, err := store.LoadTurns(ctx, session.ID)
		if err != nil {
			return nil, fmt.Errorf("load chat turns: %w", err)
		}
		app.turns = turns
		if len(turns) > 0 {
			last := turns[len(turns)-1]
			app.results = last.Lanes
			app.question = last.Question
		}
	}
	for i := range app.panes {
		app.panes[i].status = "Ready"
		app.panes[i].viewport = viewport.New()
		app.panes[i].viewport.SoftWrap = true
		if app.results[i].OriginalQuery != "" {
			app.panes[i].result = laneResultPointer(app.results[i])
		}
		app.setLaneContent(i)
	}
	return app, nil
}

func chatPairName(lanes [2]LaneSettings) string {
	if lanes[0].Mode == "grounded" && lanes[1].Mode == "grounded" {
		if lanes[0].Strategy != lanes[1].Strategy {
			return "fixed-structural"
		}
		if lanes[0].TaskMemory || lanes[1].TaskMemory {
			return "grounded-memory"
		}
		return "grounded-no-memory"
	}
	switch {
	case lanes[0].Mode == "no-rag" && lanes[1].Mode == "rag":
		return "baseline-rag"
	case lanes[0].Mode == "rag" && lanes[1].Mode == "filtered":
		return "rag-filtered"
	case lanes[0].Mode == "filtered" && lanes[1].Mode == "rewritten-filtered":
		return "filtered-rewritten"
	case lanes[0].Mode == "rag" && lanes[1].Mode == "grounded":
		return "rag-grounded"
	default:
		return "custom"
	}
}

func laneResultPointer(result LaneResult) *LaneResult { return &result }

func (m *chatApp) Init() tea.Cmd { return m.input.Focus() }

func (m *chatApp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case chatWheelMessage:
		if m.inspector != "" {
			m.inspect, _ = m.inspect.Update(msg.event)
		} else {
			m.panes[m.focused].viewport, _ = m.panes[m.focused].viewport.Update(msg.event)
		}
		return m, nil
	case chatLaneMessage:
		if !m.busy || msg.generation != m.generation || msg.lane < 0 || msg.lane > 1 {
			return m, nil
		}
		m.pending--
		if msg.err != nil {
			m.fatalErr = fmt.Errorf("lane %d failed: %w", msg.lane+1, msg.err)
			m.cancel()
			return m, tea.Quit
		}
		m.results[msg.lane] = msg.result
		m.panes[msg.lane].result = laneResultPointer(msg.result)
		m.panes[msg.lane].status = "Complete"
		m.setLaneContent(msg.lane)
		if m.inspector == "context" || m.inspector == "state" {
			m.setInspectorContent()
		}
		if m.pending == 0 {
			return m, m.persistPair()
		}
		m.status = fmt.Sprintf("Lane %d complete; waiting for the other lane.", msg.lane+1)
		return m, nil
	case chatPairSavedMessage:
		if !m.busy || msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.fatalErr = msg.err
			m.cancel()
			return m, tea.Quit
		}
		m.busy = false
		if !m.experiment {
			m.turns = append(m.turns, msg.turn)
			m.status = fmt.Sprintf("Turn %d saved · session %s", msg.turn.Ordinal, m.session.ID)
		} else {
			m.status = "Complete isolated experiment saved as " + msg.runID
		}
		m.question = ""
		m.input.SetValue("")
		for i := range m.panes {
			m.setLaneContent(i)
			m.panes[i].viewport.GotoBottom()
		}
		m.layout()
		return m, m.input.Focus()
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" || key == "esc" && m.inspector != "" {
			if key == "esc" && m.inspector != "" {
				m.inspector = ""
				m.layout()
				return m, m.input.Focus()
			}
			return m, tea.Quit
		}
		if m.inspector != "" {
			switch key {
			case "pgup", "pgdown", "up", "down":
				m.inspect, _ = m.inspect.Update(msg)
			}
			return m, nil
		}
		switch key {
		case "tab":
			m.focused = 1 - m.focused
			return m, nil
		case "pgup", "pgdown", "up", "down":
			m.panes[m.focused].viewport, _ = m.panes[m.focused].viewport.Update(msg)
			return m, nil
		case "ctrl+j":
			if !m.busy {
				m.input.InsertString("\n")
				m.layout()
			}
			return m, nil
		case "enter":
			if !m.busy {
				return m.submit()
			}
			return m, nil
		case "ctrl+o":
			m.inspector = "context"
			m.setInspectorContent()
			m.layout()
			return m, nil
		case "ctrl+s":
			m.inspector = "state"
			m.setInspectorContent()
			m.layout()
			return m, nil
		case "ctrl+p":
			if !m.busy {
				m.changePair()
			}
			return m, nil
		case "ctrl+f":
			if !m.busy {
				m.changeStrategy()
			}
			return m, nil
		}
	}
	if m.busy || m.inspector != "" {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return m, cmd
}

func (m *chatApp) submit() (tea.Model, tea.Cmd) {
	question := strings.TrimSpace(m.input.Value())
	if question == "" {
		return m, nil
	}
	if strings.HasPrefix(question, "/") {
		m.input.SetValue("")
		return m, m.command(question)
	}
	generation := m.generation
	m.busy, m.pending = true, 2
	m.question = question
	for i := range m.panes {
		m.panes[i].status = "Running…"
		m.panes[i].result = nil
	}
	m.status = "Running both lanes independently…"
	var prep TurnPreparation
	if !m.experiment {
		var err error
		prep, err = m.service.PrepareTurn(m.ctx, m.session.ID, question)
		if err != nil {
			m.fatalErr = fmt.Errorf("prepare turn: %w", err)
			m.cancel()
			return m, tea.Quit
		}
		m.prepared = prep
	}
	cmds := make([]tea.Cmd, 2)
	for lane := range cmds {
		lane, prep := lane, prep
		cmds[lane] = func() tea.Msg {
			var result LaneResult
			var err error
			if m.experiment {
				result, err = m.engine.RunLane(m.ctx, question, m.lanes[lane], nil, TaskState{})
			} else {
				result, err = m.service.RunLane(m.ctx, prep, lane)
			}
			return chatLaneMessage{generation: generation, lane: lane, result: result, err: err}
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *chatApp) persistPair() tea.Cmd {
	generation := m.generation
	question := m.question
	results := m.results
	if m.experiment {
		runID, err := newRunID()
		if err != nil {
			m.fatalErr = err
			m.cancel()
			return tea.Quit
		}
		run := ComparisonRun{ID: runID, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Question: question, Lanes: results}
		return func() tea.Msg {
			err := m.store.SaveRun(m.ctx, run)
			return chatPairSavedMessage{generation: generation, runID: runID, err: err}
		}
	}
	prep := m.prepared
	return func() tea.Msg {
		turn, err := m.service.CommitTurn(m.ctx, prep, results)
		return chatPairSavedMessage{generation: generation, turn: turn, err: err}
	}
}

func (m *chatApp) changePair() {
	if m.busy {
		m.status = "Pair changes are disabled while both lanes are running."
		return
	}
	pairs := []string{"grounded-memory", "grounded-no-memory", "baseline-rag", "rag-filtered", "filtered-rewritten", "rag-grounded", "fixed-structural"}
	current := 0
	for i, pair := range pairs {
		if pair == m.pair {
			current = i
			break
		}
	}
	name := pairs[(current+1)%len(pairs)]
	strategy := m.lanes[0].Strategy
	if strategy == "" {
		strategy = strategyStructural
	}
	lanes, err := chatPair(name, strategy, true)
	if err != nil {
		m.status = "Could not select pair: " + err.Error()
		return
	}
	persistent := name == "grounded-memory" || name == "grounded-no-memory"
	if persistent {
		session, err := m.store.NewSession(m.ctx, lanes, m.generationSettings, m.engine.Retrieval, m.rawTurns)
		if err != nil {
			m.status = "Could not create clean pair session: " + err.Error()
			return
		}
		m.replaceSession(session)
	} else {
		m.generation++
		m.session = ChatSession{}
		m.lanes, m.pair = lanes, name
		m.experiment = true
		m.clearPairView()
		m.status = "Started isolated " + name + " experiment · NO HISTORY; saved sessions remain unchanged."
	}
	m.input.SetValue("")
}

func (m *chatApp) changeStrategy() {
	if m.busy {
		m.status = "Strategy changes are disabled while both lanes are running."
		return
	}
	lanes := m.lanes
	if m.pair == "fixed-structural" {
		if lanes[0].Strategy == strategyFixed {
			lanes[0].Strategy, lanes[1].Strategy = strategyStructural, strategyFixed
		} else {
			lanes[0].Strategy, lanes[1].Strategy = strategyFixed, strategyStructural
		}
	} else {
		strategy := strategyFixed
		if lanes[0].Strategy == strategyFixed {
			strategy = strategyStructural
		}
		for i := range lanes {
			lanes[i].Strategy = strategy
		}
	}
	if !m.experiment {
		session, err := m.store.NewSession(m.ctx, lanes, m.session.Generation, m.session.Retrieval, m.rawTurns)
		if err != nil {
			m.status = "Could not create clean strategy session: " + err.Error()
			return
		}
		m.replaceSession(session)
	} else {
		m.generation++
		m.lanes = lanes
		m.clearPairView()
		m.status = fmt.Sprintf("Started clean %s strategy comparison · NO HISTORY.", m.pair)
	}
	m.input.SetValue("")
}

func (m *chatApp) clearPairView() {
	m.turns = nil
	m.results = [2]LaneResult{}
	m.question = ""
	m.prepared = TurnPreparation{}
	m.inspector = ""
	m.inspect.SetContent("")
	for i := range m.panes {
		m.panes[i].result = nil
		m.panes[i].status = "Ready"
		m.panes[i].viewport.GotoTop()
		m.setLaneContent(i)
	}
	m.layout()
}

func (m *chatApp) replaceSession(session ChatSession) {
	m.generation++
	m.session, m.lanes = session, session.Lanes
	m.rawTurns = session.RawTurns
	m.pair = chatPairName(session.Lanes)
	m.generationSettings = session.Generation
	m.engine.Retrieval = session.Retrieval
	m.experiment = false
	m.clearPairView()
	m.status = "Started a clean session; prior dialogue remains in its original session."
}

func (m *chatApp) command(input string) tea.Cmd {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "/runs":
		runs, err := m.store.ListRuns(m.ctx)
		if err != nil {
			m.status = "Could not load archived runs: " + err.Error()
			return nil
		}
		var b strings.Builder
		b.WriteString("Archived comparisons (immutable):\n")
		for _, run := range runs {
			fmt.Fprintf(&b, "%s · %s · %q · %s / %s\n", run.ID, run.CreatedAt, run.Question, run.Lanes[0].Settings.Mode, run.Lanes[1].Settings.Mode)
		}
		m.showInspector("archive list", b.String())
	case "/run":
		if len(fields) != 2 {
			m.status = "Usage: /run <comparison-id>"
			return nil
		}
		run, err := m.store.LoadRun(m.ctx, fields[1])
		if err != nil {
			m.status = "Could not load archived run: " + err.Error()
			return nil
		}
		m.showInspector("archived run · "+run.ID, archivedRunText(run))
	case "/sessions":
		sessions, err := m.store.ListSessions(m.ctx)
		if err != nil {
			m.status = "Could not load sessions: " + err.Error()
			return nil
		}
		var b strings.Builder
		b.WriteString("Saved sessions (select with /open <session-id>):\n")
		for _, session := range sessions {
			rawTurns := "unknown (migration required)"
			if session.RawTurns > 0 {
				rawTurns = fmt.Sprint(session.RawTurns)
			}
			fmt.Fprintf(&b, "%s · %s · %s / %s · main=%s · threshold=%.3f · raw-turns=%s\n", session.ID, session.CreatedAt, session.Lanes[0].Mode, session.Lanes[1].Mode, session.Generation.MainModel, session.Retrieval.Threshold, rawTurns)
		}
		m.showInspector("saved sessions", b.String())
	case "/open":
		if m.busy {
			m.status = "Cannot change sessions while a pair is running."
			return nil
		}
		if len(fields) != 2 {
			m.status = "Usage: /open <session-id>"
			return nil
		}
		session, err := m.store.LoadSession(m.ctx, fields[1])
		if err != nil {
			m.status = "Could not open session: " + err.Error()
			return nil
		}
		if session.Generation.Provider == "" || session.Generation.Endpoint == "" {
			m.status = "Could not open session: saved session lacks provider/endpoint provenance."
			return nil
		}
		client, generation, err := newProviderClient(
			session.Generation.Provider, session.Generation.Endpoint,
			session.Generation.MainModel, session.Generation.AuxModel,
			session.Generation.Temperature, session.Generation.MaxTokens,
		)
		if err != nil {
			m.status = "Could not restore session provider: " + err.Error()
			return nil
		}
		if generation != session.Generation {
			m.status = "Could not open session: saved generation settings are incomplete."
			return nil
		}
		client.APIKey, err = providerAPIKey(generation.Provider)
		if err != nil {
			m.status = "Could not restore session provider key: " + err.Error()
			return nil
		}
		turns, err := m.store.LoadTurns(m.ctx, session.ID)
		if err != nil {
			m.status = "Could not load session turns: " + err.Error()
			return nil
		}
		m.generation++
		m.engine.Groq = client
		m.session, m.lanes, m.turns = session, session.Lanes, turns
		m.rawTurns = session.RawTurns
		m.pair = chatPairName(session.Lanes)
		m.generationSettings = generation
		m.engine.Retrieval = session.Retrieval
		m.experiment = false
		m.results = [2]LaneResult{}
		m.question = ""
		m.prepared = TurnPreparation{}
		if len(turns) > 0 {
			last := turns[len(turns)-1]
			m.results, m.question = last.Lanes, last.Question
		}
		for i := range m.panes {
			m.panes[i].result = nil
			if m.results[i].OriginalQuery != "" {
				m.panes[i].result = laneResultPointer(m.results[i])
				m.panes[i].status = "Restored"
			} else {
				m.panes[i].status = "Ready"
			}
			m.setLaneContent(i)
		}
		if m.inspector == "context" || m.inspector == "state" {
			m.setInspectorContent()
		}
		m.status = "Resumed stored session settings · " + session.ID
	default:
		m.status = "Commands: /runs, /run <id>, /sessions, /open <session-id>"
	}
	return nil
}

func (m *chatApp) showInspector(title, content string) {
	m.inspector = title
	m.inspect.SetContent(content)
	m.inspect.GotoTop()
	m.layout()
}

func (m *chatApp) setLaneContent(lane int) {
	var b strings.Builder
	if m.experiment {
		if m.panes[lane].result == nil {
			b.WriteString("No completed answer yet.")
		} else {
			fmt.Fprintf(&b, "Question: %s\n\n%s\n\n%s", m.panes[lane].result.OriginalQuery, RenderAnswer(*m.panes[lane].result), RenderContext(*m.panes[lane].result))
		}
	} else {
		for _, turn := range m.turns {
			fmt.Fprintf(&b, "You: %s\n\n%s\n\n", turn.Question, RenderAnswer(turn.Lanes[lane]))
		}
		if m.busy && m.panes[lane].result != nil && m.panes[lane].result.OriginalQuery == m.question {
			fmt.Fprintf(&b, "You: %s\n\n%s\n\n%s", m.question, RenderAnswer(*m.panes[lane].result), RenderContext(*m.panes[lane].result))
		}
		if b.Len() == 0 {
			b.WriteString("No completed turns yet.")
		}
	}
	m.panes[lane].viewport.SetContent(b.String())
}

func (m *chatApp) setInspectorContent() {
	if m.inspector == "context" {
		var b strings.Builder
		fmt.Fprintf(&b, "Question: %s\nFocused lane: %d\n\n", m.question, m.focused+1)
		if m.panes[m.focused].result != nil {
			b.WriteString(RenderContext(*m.panes[m.focused].result))
		}
		m.inspect.SetContent(b.String())
	} else {
		var b strings.Builder
		fmt.Fprintf(&b, "Question: %s\nFocused lane: %d\n\n", m.question, m.focused+1)
		if m.panes[m.focused].result != nil {
			state := m.panes[m.focused].result.State
			fmt.Fprintf(&b, "Goal: %s\nGoal quote: %s\n\nClarifications:\n", state.Goal, state.GoalQuote)
			for _, fact := range state.Clarifications {
				fmt.Fprintf(&b, "- %s: %s (quote: %s)\n", fact.Key, fact.Value, fact.Quote)
			}
			b.WriteString("\nConstraints:\n")
			for _, fact := range state.Constraints {
				fmt.Fprintf(&b, "- %s: %s (quote: %s)\n", fact.Key, fact.Value, fact.Quote)
			}
			b.WriteString("\nTerms:\n")
			for _, fact := range state.Terms {
				fmt.Fprintf(&b, "- %s: %s (quote: %s)\n", fact.Key, fact.Value, fact.Quote)
			}
		}
		m.inspect.SetContent(b.String())
	}
	m.inspect.GotoTop()
}

func archivedRunText(run ComparisonRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Archived comparison · %s\nCreated: %s\nQuestion: %s\n\n", run.ID, run.CreatedAt, run.Question)
	for lane, result := range run.Lanes {
		fmt.Fprintf(&b, "Lane %d · mode=%s strategy=%s rewrite=%t task-memory=%t\nGeneration: %+v\nRetrieval: %+v\nBuild: %s snapshot=%s\n\n%s\n\n%s\n\n", lane+1, result.Settings.Mode, result.Settings.Strategy, result.Settings.Rewrite, result.Settings.TaskMemory, result.Generation, result.Retrieval, result.Build.ID, result.Build.SnapshotID, RenderAnswer(result), RenderContext(result))
	}
	return b.String()
}

func (m *chatApp) header() string {
	policy := "PERSISTENT GROUNDED CHAT"
	if m.experiment {
		policy = "ISOLATED EXPERIMENT · NO HISTORY"
	}
	return fmt.Sprintf("GT:NH chat · %s · %s · session %s · %s", m.pair, policy, m.session.ID, pairDescription(m.lanes))
}

func (m *chatApp) footer() string {
	return fmt.Sprintf("%s\nEnter submit · Tab focus lane · PgUp/PgDn/wheel selected lane · Ctrl+O context · Ctrl+S task state · Ctrl+P cycle pair · Ctrl+F toggle fixed/structural strategy · /runs /run ID /sessions /open ID · Ctrl+J newline · Ctrl+C quit", m.status)
}

func (m *chatApp) inspectorFooter() string {
	return "Esc close · PgUp/PgDn or mouse wheel scroll · Ctrl+C quit"
}

func (m *chatApp) layout() {
	inputWidth := max(1, m.width)
	m.input.SetWidth(inputWidth)
	m.input.SetHeight(min(3, max(1, m.input.LineCount())))
	m.inspect.SetWidth(inputWidth)
	headerHeight := lipgloss.Height(clipChat(m.header(), inputWidth))
	footerHeight := lipgloss.Height(clipChat(m.footer(), inputWidth))
	if m.inspector != "" {
		headerHeight = lipgloss.Height(clipChat("Inspector · "+m.inspector, inputWidth))
		footerHeight = lipgloss.Height(clipChat(m.inspectorFooter(), inputWidth))
	}
	m.inspect.SetHeight(max(1, m.height-headerHeight-footerHeight-m.input.Height()-3))
	available := max(1, m.height-headerHeight-footerHeight-m.input.Height()-4)
	if m.width >= 120 {
		paneWidth := max(1, (m.width-3)/2)
		for i := range m.panes {
			m.panes[i].viewport.SetWidth(paneWidth)
			m.panes[i].viewport.SetHeight(available)
		}
	} else {
		paneHeight := max(1, (m.height-headerHeight-footerHeight-m.input.Height()-6)/2)
		for i := range m.panes {
			m.panes[i].viewport.SetWidth(inputWidth)
			m.panes[i].viewport.SetHeight(paneHeight)
		}
	}
}

func (m *chatApp) View() tea.View {
	var content string
	if m.inspector != "" {
		content = strings.Join([]string{clipChat("Inspector · "+m.inspector, m.width), m.inspect.View(), m.input.View(), clipChat(m.inspectorFooter(), m.width)}, "\n")
	} else {
		header := m.header()
		panes := make([]string, 2)
		for i := range m.panes {
			marker := " "
			if i == m.focused {
				marker = ">"
			}
			label := fmt.Sprintf("%s Lane %d · %s · %s · %s", marker, i+1, m.lanes[i].Mode, m.lanes[i].Strategy, m.panes[i].status)
			panes[i] = label + "\n" + m.panes[i].viewport.View()
		}
		var panel string
		if m.width >= 120 {
			panel = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(max(1, (m.width-3)/2)).Render(panes[0]), " │ ", lipgloss.NewStyle().Width(max(1, (m.width-3)/2)).Render(panes[1]))
		} else {
			panel = panes[0] + "\n" + strings.Repeat("─", max(1, m.width)) + "\n" + panes[1]
		}
		content = strings.Join([]string{clipChat(header, m.width), panel, m.input.View(), clipChat(m.footer(), m.width)}, "\n")
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.OnMouse = func(mouse tea.MouseMsg) tea.Cmd {
		wheel, ok := mouse.(tea.MouseWheelMsg)
		if !ok {
			return nil
		}
		return func() tea.Msg { return chatWheelMessage{event: wheel} }
	}
	return view
}

func pairDescription(lanes [2]LaneSettings) string {
	return fmt.Sprintf("%s/%s · task memory %t/%t", lanes[0].Mode, lanes[1].Mode, lanes[0].TaskMemory, lanes[1].TaskMemory)
}

func clipChat(s string, width int) string {
	if width < 1 {
		return ""
	}
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Render(s)
}
