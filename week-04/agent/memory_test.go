package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseMemoryUpdateJSONAcceptsAllowlistedCandidates(t *testing.T) {
	update, err := ParseMemoryUpdateJSON(`{
		"profile":{"language":" English ","response_style":" concise ","response_format":" bullets ","constraints":" no emojis "},
		"task":{"goal":"Ship parser","plan":"Write strict decoder","current_step":"Implement parser","expected_action":"Run offline tests"},
		"records":[
			{"scope":"profile","kind":"decision","key":" Response Style ","value":" concise "},
			{"scope":"task","kind":"knowledge","key":"Schema Version","value":" 2 "}
		]
	}`)
	if err != nil {
		t.Fatalf("parse memory update: %v", err)
	}

	want := MemoryUpdate{
		Profile: &ProfileUpdate{
			Language:       stringPointer("English"),
			ResponseStyle:  stringPointer("concise"),
			ResponseFormat: stringPointer("bullets"),
			Constraints:    stringPointer("no emojis"),
		},
		Task: &TaskUpdate{
			Goal:           "Ship parser",
			Plan:           "Write strict decoder",
			CurrentStep:    "Implement parser",
			ExpectedAction: "Run offline tests",
		},
		Records: []LongTermMemoryUpdate{
			{Scope: MemoryScopeProfile, Kind: MemoryKindDecision, Key: "response-style", Value: "concise"},
			{Scope: MemoryScopeTask, Kind: MemoryKindKnowledge, Key: "schema-version", Value: "2"},
		},
	}
	if !reflect.DeepEqual(update, want) {
		t.Fatalf("parsed update = %#v, want %#v", update, want)
	}
}

func TestParseMemoryUpdateJSONAcceptsTaskFieldSubset(t *testing.T) {
	update, err := ParseMemoryUpdateJSON(`{"task":{"goal":"Ship parser"}}`)
	if err != nil {
		t.Fatalf("parse partial task candidate: %v", err)
	}
	want := &TaskUpdate{Goal: "Ship parser"}
	if !reflect.DeepEqual(update.Task, want) {
		t.Fatalf("task update = %#v, want %#v", update.Task, want)
	}
}

func TestParseMemoryUpdateJSONAcceptsEmptyObjectAsNoOp(t *testing.T) {
	update, err := ParseMemoryUpdateJSON(`{}`)
	if err != nil {
		t.Fatalf("parse no-op: %v", err)
	}
	if !reflect.DeepEqual(update, MemoryUpdate{}) {
		t.Fatalf("no-op update = %#v, want empty update", update)
	}
}

func TestParseMemoryUpdateJSONRejectsStrictContractViolations(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "invalid JSON", content: `{"profile":`},
		{name: "top level array", content: `[]`},
		{name: "unknown root field", content: `{"phase":"execution"}`},
		{name: "unknown profile field", content: `{"profile":{"name":"other"}}`},
		{name: "empty profile", content: `{"profile":{}}`},
		{name: "empty task", content: `{"task":{}}`},
		{name: "task lifecycle field", content: `{"task":{"phase":"execution"}}`},
		{name: "wrong profile type", content: `{"profile":{"language":false}}`},
		{name: "blank task field", content: `{"task":{"goal":" \t "}}`},
		{name: "records is not array", content: `{"records":{}}`},
		{name: "empty records", content: `{"records":[]}`},
		{name: "record missing field", content: `{"records":[{"scope":"profile","kind":"decision","key":"storage"}]}`},
		{name: "record extra field", content: `{"records":[{"scope":"profile","kind":"decision","key":"storage","value":"SQLite","task_id":"1"}]}`},
		{name: "record wrong scope", content: `{"records":[{"scope":"invariant","kind":"decision","key":"storage","value":"SQLite"}]}`},
		{name: "record wrong kind", content: `{"records":[{"scope":"task","kind":"preference","key":"style","value":"concise"}]}`},
		{name: "record blank value", content: `{"records":[{"scope":"profile","kind":"knowledge","key":"style","value":"\n"}]}`},
		{name: "record non identifier key", content: `{"records":[{"scope":"profile","kind":"knowledge","key":"not/a/key","value":"value"}]}`},
		{name: "duplicate root name", content: `{"profile":{"language":"English"},"profile":{"response_style":"concise"}}`},
		{name: "duplicate escaped nested name", content: `{"profile":{"language":"English","\u006canguage":"Russian"}}`},
		{name: "duplicate record field", content: `{"records":[{"scope":"profile","kind":"decision","key":"storage","\u006bey":"database","value":"SQLite"}]}`},
		{name: "normalized record collision", content: `{"records":[{"scope":"profile","kind":"decision","key":"Response Style","value":"concise"},{"scope":"profile","kind":"decision","key":"response-style","value":"detailed"}]}`},
		{name: "trailing JSON document", content: `{} {}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseMemoryUpdateJSON(test.content); err == nil {
				t.Fatalf("ParseMemoryUpdateJSON(%q) succeeded", test.content)
			}
		})
	}
}

func TestParseMemoryUpdateJSONDoesNotMutatePreexistingUpdateOnError(t *testing.T) {
	original := MemoryUpdate{
		Profile: &ProfileUpdate{Language: stringPointer("English")},
		Task:    &TaskUpdate{Goal: "Keep this goal"},
		Records: []LongTermMemoryUpdate{{Scope: MemoryScopeTask, Kind: MemoryKindDecision, Key: "storage", Value: "SQLite"}},
	}
	before := cloneMemoryUpdate(original)

	if _, err := ParseMemoryUpdateJSON(`{"records":[{"scope":"task","kind":"decision","key":"storage","value":"SQLite"},{"scope":"task","kind":"decision","key":" Storage ","value":"PostgreSQL"}]}`); err == nil {
		t.Fatal("normalized collision succeeded")
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatalf("preexisting update changed to %#v, want %#v", original, before)
	}
}

func TestBuildMemoryExtractionPromptUsesSingleLeadingSystemMessage(t *testing.T) {
	state := MemoryExtractionState{
		Profile: &Profile{Name: "default", Language: "English"},
		Task:    &Task{Goal: "Ship parser", Phase: TaskPhasePlanning},
	}
	prompt := BuildMemoryExtractionPrompt("remember this", "I will", state)
	if len(prompt) != 3 || prompt[0].Role != "system" || prompt[1].Role != "user" || prompt[2].Role != "assistant" {
		t.Fatalf("prompt roles = %#v, want one leading system, then user and assistant", prompt)
	}
	systemMessages := 0
	for _, message := range prompt {
		if message.Role == "system" {
			systemMessages++
		}
	}
	if systemMessages != 1 {
		t.Fatalf("system message count = %d, want exactly one", systemMessages)
	}
	if !strings.Contains(prompt[0].Content, MemoryExtractionInstruction) ||
		!strings.Contains(prompt[0].Content, MemoryExtractionStateBlock(state)) {
		t.Fatal("leading system message omitted extraction instructions or active state")
	}
	if prompt[1].Content != "Пользователь сообщил:\nremember this" ||
		prompt[2].Content != "Ассистент ответил:\nI will" {
		t.Fatalf("turn messages = %#v, want original user and assistant turn content", prompt[1:])
	}
}

func stringPointer(value string) *string {
	return &value
}
