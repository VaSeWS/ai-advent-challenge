package main

import (
	"strings"
	"testing"
)

func TestParseCompletion(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
		wantErr  string
	}{
		{
			name:     "completed text",
			response: `{"choices":[{"finish_reason":"stop","message":{"content":"  готово  "}}]}`,
			want:     "готово",
		},
		{
			name:     "length limit",
			response: `{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`,
			wantErr:  "достиг ограничения длины",
		},
		{
			name:     "missing finish reason",
			response: `{"choices":[{"message":{"content":"partial"}}]}`,
			wantErr:  "не завершил текстовый ответ",
		},
		{
			name:     "null finish reason",
			response: `{"choices":[{"finish_reason":null,"message":{"content":"partial"}}]}`,
			wantErr:  "не завершил текстовый ответ",
		},
		{
			name:     "content filter",
			response: `{"choices":[{"finish_reason":"content_filter","message":{"content":"partial"}}]}`,
			wantErr:  "остановлен фильтром содержимого",
		},
		{
			name:     "tool call finish reason",
			response: `{"choices":[{"finish_reason":"tool_calls","message":{"content":"partial"}}]}`,
			wantErr:  "вызов инструмента",
		},
		{
			name:     "unexpected finish reason",
			response: `{"choices":[{"finish_reason":"other","message":{"content":"partial"}}]}`,
			wantErr:  "не завершил текстовый ответ",
		},
		{
			name:     "tool calls in message",
			response: `{"choices":[{"finish_reason":"stop","message":{"content":"partial","tool_calls":[{"id":"call_1"}]}}]}`,
			wantErr:  "вызов инструмента",
		},
		{
			name:     "empty content",
			response: `{"choices":[{"finish_reason":"stop","message":{"content":"  "}}]}`,
			wantErr:  "пустой ответ",
		},
		{
			name:     "null content",
			response: `{"choices":[{"finish_reason":"stop","message":{"content":null}}]}`,
			wantErr:  "пустой ответ",
		},
		{
			name:     "no choices",
			response: `{"choices":[]}`,
			wantErr:  "пустой ответ",
		},
		{
			name:     "malformed JSON",
			response: `{"choices":[`,
			wantErr:  "не удалось разобрать",
		},
		{
			name:     "trailing JSON value",
			response: `{"choices":[{"finish_reason":"stop","message":{"content":"complete"}}]} {}`,
			wantErr:  "не удалось разобрать",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCompletion(strings.NewReader(tt.response))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseCompletion() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCompletion() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseCompletion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseGroundedAnswer(t *testing.T) {
	hits := []Hit{
		{Chunk: Chunk{Path: "docs/api-guide.md", StartLine: 10, EndLine: 20}},
		{Chunk: Chunk{Path: "README.md", StartLine: 1, EndLine: 8}},
	}
	tests := []struct {
		name       string
		content    string
		wantIDs    []int
		wantMapped []Chunk
		wantErr    bool
	}{
		{
			name:       "valid source IDs",
			content:    `{"answer":"Маршрут описан в руководстве.","source_ids":[2,1]}`,
			wantIDs:    []int{2, 1},
			wantMapped: []Chunk{{Path: "README.md", StartLine: 1, EndLine: 8}, {Path: "docs/api-guide.md", StartLine: 10, EndLine: 20}},
		},
		{
			name:    "empty source IDs for live MCP answer",
			content: `{"answer":"Текущая ветка доступна через MCP.","source_ids":[]}`,
			wantIDs: []int{},
		},
		{
			name:    "unknown source ID",
			content: `{"answer":"Ответ.","source_ids":[99]}`,
			wantErr: true,
		},
		{
			name:    "negative source ID",
			content: `{"answer":"Ответ.","source_ids":[-1]}`,
			wantErr: true,
		},
		{
			name:    "zero source ID",
			content: `{"answer":"Ответ.","source_ids":[0]}`,
			wantErr: true,
		},
		{
			name:    "missing source IDs",
			content: `{"answer":"Ответ."}`,
			wantErr: true,
		},
		{
			name:    "null source IDs",
			content: `{"answer":"Ответ.","source_ids":null}`,
			wantErr: true,
		},
		{
			name:    "wrong source ID type",
			content: `{"answer":"Ответ.","source_ids":"1"}`,
			wantErr: true,
		},
		{
			name:    "duplicate source IDs",
			content: `{"answer":"Ответ.","source_ids":[1,1]}`,
			wantErr: true,
		},
		{
			name:    "empty answer",
			content: `{"answer":"  ","source_ids":[]}`,
			wantErr: true,
		},
		{
			name:    "trailing JSON value",
			content: `{"answer":"Ответ.","source_ids":[]} {}`,
			wantErr: true,
		},
		{
			name:    "unknown field",
			content: `{"answer":"Ответ.","source_ids":[],"extra":true}`,
			wantErr: true,
		},
		{
			name:    "documentary path citation",
			content: `{"answer":"Подробнее: [docs/api-guide.md:12-18].","source_ids":[1]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGroundedAnswer(tt.content, hits)
			if tt.wantErr {
				if err == nil {
					t.Fatal("parseGroundedAnswer() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGroundedAnswer() error = %v", err)
			}
			if strings.TrimSpace(got.Answer) == "" {
				t.Error("Answer is empty")
			}
			if len(got.SourceIDs) != len(tt.wantIDs) {
				t.Fatalf("SourceIDs = %v, want %v", got.SourceIDs, tt.wantIDs)
			}
			for i, id := range tt.wantIDs {
				if got.SourceIDs[i] != id {
					t.Fatalf("SourceIDs[%d] = %d, want %d", i, got.SourceIDs[i], id)
				}
			}
			for _, id := range got.SourceIDs {
				if id < 1 || id > len(hits) {
					t.Fatalf("SourceID %d cannot map into %d hits", id, len(hits))
				}
			}
			if len(tt.wantMapped) != 0 {
				if len(got.SourceIDs) != len(tt.wantMapped) {
					t.Fatalf("SourceIDs = %v, cannot map to expected hits %v", got.SourceIDs, tt.wantMapped)
				}
				for i, id := range got.SourceIDs {
					mapped := hits[id-1].Chunk
					want := tt.wantMapped[i]
					if mapped.Path != want.Path || mapped.StartLine != want.StartLine || mapped.EndLine != want.EndLine {
						t.Errorf("source ID %d mapped to %s:%d-%d, want %s:%d-%d",
							id, mapped.Path, mapped.StartLine, mapped.EndLine, want.Path, want.StartLine, want.EndLine)
					}
				}
			}
		})
	}
}
