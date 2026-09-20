package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAppStartsWithFocusedTextareaAndAcceptsText(t *testing.T) {
	store, err := OpenStore(t.TempDir() + "/agent.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	profile := providerProfiles["groq"]
	model := NewApp(store, NewAgent(store, nil, nil, profile), profile).(app)
	if !model.input.Focused() {
		t.Fatal("textarea must be focused when the app starts")
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'А', Text: "А"}))
	model = updated.(app)
	if got := model.input.Value(); got != "А" {
		t.Fatalf("textarea value = %q, want %q", got, "А")
	}
}
