package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newViewportTestApp(t *testing.T) app {
	t.Helper()

	store, err := OpenStore(filepath.Join(t.TempDir(), "agent.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	profile := providerProfiles["groq"]
	agent := NewAgent(store, &agentTestCompleter{}, agentTestCounter{tokens: 1, label: "synthetic"}, profile)
	return NewApp(store, agent, profile).(app)
}

func resizeViewportApp(t *testing.T, m app, width, height int) app {
	t.Helper()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(app)
}

func pressViewportKey(t *testing.T, m app, key rune) app {
	t.Helper()

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: key}))
	return updated.(app)
}

func viewportLineage(count int) []Message {
	lineage := make([]Message, count)
	for i := range lineage {
		lineage[i] = Message{
			Role:    "user",
			Content: fmt.Sprintf("VIEWPORT_MESSAGE_%02d", i),
		}
	}
	return lineage
}

func TestAppLongTranscriptOpensAtBottom(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	model.lineage = viewportLineage(12)
	model.refreshTranscript()
	model.layout()

	view := model.viewport.View()
	if !strings.Contains(view, "VIEWPORT_MESSAGE_11") {
		t.Fatalf("opening viewport = %q, want final transcript message at bottom", view)
	}
	if strings.Contains(view, "VIEWPORT_MESSAGE_00") {
		t.Fatalf("opening viewport = %q, unexpectedly starts at the first transcript message", view)
	}
}

func TestAppTranscriptPagingMovesAndClamps(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	model.lineage = viewportLineage(12)
	model.refreshTranscript()
	model.layout()

	bottom := model.viewport.View()
	model = pressViewportKey(t, model, tea.KeyPgUp)
	up := model.viewport.View()
	if !strings.Contains(up, "VIEWPORT_MESSAGE_08") || strings.Contains(up, "VIEWPORT_MESSAGE_11") {
		t.Fatalf("PgUp viewport = %q, want earlier transcript content instead of the bottom", up)
	}

	model = pressViewportKey(t, model, tea.KeyPgDown)
	if got := model.viewport.View(); got != bottom {
		t.Fatalf("PgDn viewport = %q, want return to later bottom content %q", got, bottom)
	}
	model = pressViewportKey(t, model, tea.KeyPgDown)
	if got := model.viewport.View(); got != bottom {
		t.Fatalf("PgDn at bottom = %q, want clamped bottom content %q", got, bottom)
	}

	for range 10 {
		model = pressViewportKey(t, model, tea.KeyPgUp)
	}
	top := model.viewport.View()
	if !strings.Contains(top, "VIEWPORT_MESSAGE_00") {
		t.Fatalf("repeated PgUp viewport = %q, want first transcript content", top)
	}
	model = pressViewportKey(t, model, tea.KeyPgUp)
	if got := model.viewport.View(); got != top {
		t.Fatalf("PgUp at top = %q, want clamped top content %q", got, top)
	}
}

func TestAppCommandResultPagingMovesBetweenOutputPages(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	lines := make([]string, 24)
	for i := range lines {
		lines[i] = fmt.Sprintf("COMMAND_RESULT_LINE_%02d", i)
	}

	updated, _ := model.Update(commandFinishedMsg{
		result: CommandResult{Status: strings.Join(lines, "\n")},
	})
	model = updated.(app)

	top := model.View().Content
	if !strings.Contains(top, "COMMAND_RESULT_LINE_00") {
		t.Fatalf("initial result view = %q, want first output page", top)
	}

	model = pressViewportKey(t, model, tea.KeyPgDown)
	if down := model.View().Content; down == top {
		t.Fatalf("PgDn result view = %q, want a later output page", down)
	}
	for range 10 {
		model = pressViewportKey(t, model, tea.KeyPgDown)
	}
	bottom := model.View().Content
	if !strings.Contains(bottom, "COMMAND_RESULT_LINE_23") {
		t.Fatalf("repeated PgDn result view = %q, want final output line", bottom)
	}

	model = pressViewportKey(t, model, tea.KeyPgUp)
	if up := model.View().Content; up == bottom {
		t.Fatalf("PgUp result view = %q, want an earlier output page", up)
	}
	for range 10 {
		model = pressViewportKey(t, model, tea.KeyPgUp)
	}
	if topAgain := model.View().Content; !strings.Contains(topAgain, "COMMAND_RESULT_LINE_00") {
		t.Fatalf("repeated PgUp result view = %q, want first output line", topAgain)
	}
}

func TestAppResizePreservesManualTranscriptScroll(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	model.lineage = viewportLineage(20)
	model.refreshTranscript()
	model.layout()
	model = pressViewportKey(t, model, tea.KeyPgUp)

	beforeResize := model.viewport.View()
	if !strings.Contains(beforeResize, "VIEWPORT_MESSAGE_16") || strings.Contains(beforeResize, "VIEWPORT_MESSAGE_19") {
		t.Fatalf("manually scrolled viewport = %q, want intermediate transcript content", beforeResize)
	}

	model = resizeViewportApp(t, model, 40, 15)
	afterResize := model.viewport.View()
	if !strings.Contains(afterResize, "VIEWPORT_MESSAGE_16") || strings.Contains(afterResize, "VIEWPORT_MESSAGE_19") {
		t.Fatalf("resized viewport = %q, want the manual transcript position rather than the bottom", afterResize)
	}
}

func TestAppResponseReturnsTranscriptToBottom(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	model.lineage = viewportLineage(12)
	model.refreshTranscript()
	model.layout()
	for range 10 {
		model = pressViewportKey(t, model, tea.KeyPgUp)
	}
	if got := model.viewport.View(); !strings.Contains(got, "VIEWPORT_MESSAGE_00") {
		t.Fatalf("pre-response viewport = %q, want manually scrolled top content", got)
	}

	lineage := append(append([]Message(nil), model.lineage...), Message{
		Role:    "assistant",
		Content: "NEW_RESPONSE_AT_BOTTOM",
	})
	updated, _ := model.Update(turnFinishedMsg{
		result:  TurnResult{Metrics: "synthetic metrics"},
		lineage: lineage,
		chat:    model.chat,
		branch:  model.branch,
	})
	model = updated.(app)

	view := model.viewport.View()
	if !strings.Contains(view, "NEW_RESPONSE_AT_BOTTOM") {
		t.Fatalf("response viewport = %q, want newly appended response at bottom", view)
	}
	if strings.Contains(view, "VIEWPORT_MESSAGE_00") {
		t.Fatalf("response viewport = %q, unexpectedly preserved the old top position", view)
	}
}

func TestAppSidebarBreakpointPreservesMainContentWidth(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 79, 12)
	widthProbe := strings.Repeat("WIDTH_PROBE_", 6)
	model.lineage = []Message{{
		Role:    "user",
		Content: widthProbe,
	}}
	model.refreshTranscript()
	model.layout()

	withoutSidebar := model.viewport.View()
	if strings.Contains(model.View().Content, "Chats") {
		t.Fatalf("79-column view = %q, unexpectedly rendered sidebar", model.View().Content)
	}
	if !strings.Contains(withoutSidebar, widthProbe) {
		t.Fatalf("79-column viewport = %q, want %q to fit its full main width", withoutSidebar, widthProbe)
	}

	model = resizeViewportApp(t, model, 80, 12)
	withSidebar := model.viewport.View()
	if !strings.Contains(model.View().Content, "Chats") {
		t.Fatalf("80-column view = %q, want sidebar", model.View().Content)
	}
	if strings.Contains(withSidebar, widthProbe) {
		t.Fatalf("80-column viewport = %q, want %q wrapped by the sidebar-reduced main width", withSidebar, widthProbe)
	}

	model = resizeViewportApp(t, model, 79, 12)
	if strings.Contains(model.View().Content, "Chats") {
		t.Fatalf("79-column view after shrinking = %q, unexpectedly retained sidebar", model.View().Content)
	}
	if got := model.viewport.View(); got != withoutSidebar {
		t.Fatalf("79-column viewport after shrinking = %q, want restored main content width %q", got, withoutSidebar)
	}
}

func TestAppMouseWheelScrollsTranscript(t *testing.T) {
	model := resizeViewportApp(t, newViewportTestApp(t), 40, 12)
	model.lineage = viewportLineage(12)
	model.refreshTranscript()
	model.layout()

	view := model.View()
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion", view.MouseMode)
	}
	if view.OnMouse == nil {
		t.Fatal("view must relay mouse wheel events to the transcript")
	}

	wheel := tea.MouseWheelMsg{Button: tea.MouseWheelUp}
	relay := view.OnMouse(wheel)
	if relay == nil {
		t.Fatal("mouse wheel relay command is nil")
	}
	message := relay()

	updated, _ := model.Update(message)
	model = updated.(app)
	if got := model.viewport.View(); !strings.Contains(got, "VIEWPORT_MESSAGE_09") {
		t.Fatalf("wheel-up viewport = %q, want earlier transcript messages", got)
	}

	for range 10 {
		message = view.OnMouse(wheel)()
		updated, _ = model.Update(message)
		model = updated.(app)
	}
	top := model.viewport.View()
	message = view.OnMouse(wheel)()
	updated, _ = model.Update(message)
	model = updated.(app)
	if got := model.viewport.View(); got != top {
		t.Fatalf("wheel-up at top changed viewport from %q to %q", top, got)
	}
}
