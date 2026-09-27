package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestTelegramSendChecksHTTPAndAPIErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response string
		wantErr  bool
	}{
		{name: "successful delivery", status: http.StatusOK, response: `{"ok":true}`},
		{name: "Telegram API rejection", status: http.StatusOK, response: `{"ok":false}`, wantErr: true},
		{name: "HTTP failure", status: http.StatusBadGateway, response: `{"ok":true}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/bottest-token/sendMessage" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse request form: %v", err)
				}
				if got := r.Form.Get("chat_id"); got != "chat-123" {
					t.Errorf("chat_id = %q, want chat-123", got)
				}
				if got := r.Form.Get("text"); got != "Review plan" {
					t.Errorf("text = %q, want Review plan", got)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()

			err := telegramSend(context.Background(), server.Client(), server.URL, "test-token", "chat-123", "Review plan")
			if (err != nil) != test.wantErr {
				t.Fatalf("telegramSend() error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "test-token") {
				t.Fatal("error exposed the bot token")
			}
		})
	}
}

func TestTelegramSendEncodesFormText(t *testing.T) {
	const message = "Plan: Go & MCP"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("parse form: %v", err)
			return
		}
		if got := form.Get("text"); got != message {
			t.Errorf("text = %q, want %q", got, message)
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	if err := telegramSend(context.Background(), server.Client(), server.URL, "test-token", "chat-123", message); err != nil {
		t.Fatalf("telegramSend() error = %v", err)
	}
}

func TestTelegramSendChunksLongUnicodeMessage(t *testing.T) {
	message := strings.Repeat("界", 4095) + "😀" + strings.Repeat("🧠", 3)
	var chunks []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse request form: %v", err)
			return
		}
		chunk := r.Form.Get("text")
		characters := 0
		for _, r := range chunk {
			characters += utf16Width(r)
		}
		if characters > 4096 {
			t.Errorf("chunk has %d Telegram characters, limit is 4096", characters)
		}
		chunks = append(chunks, chunk)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	if err := telegramSend(context.Background(), server.Client(), server.URL, "test-token", "chat-123", message); err != nil {
		t.Fatalf("telegramSend() error = %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want multiple", len(chunks))
	}
	if got := strings.Join(chunks, ""); got != message {
		t.Fatal("Telegram chunks did not preserve the complete message")
	}
}

func TestTelegramSendReportsPartialDelivery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":false}`)
	}))
	defer server.Close()

	err := telegramSend(context.Background(), server.Client(), server.URL, "test-token", "chat-123", strings.Repeat("x", 4097))
	if err == nil || !strings.Contains(err.Error(), "delivered 1 of 2 message chunks") {
		t.Fatalf("telegramSend() error = %v, want partial-delivery count", err)
	}
	if requests != 2 {
		t.Fatalf("sent %d chunks, want stop after rejection on chunk 2", requests)
	}
}

func TestArgumentsForToolBindsHostResults(t *testing.T) {
	found := findQuestionsOutput{Questions: []question{{Title: "host question", Topic: "go", Due: "2026-09-27", Path: "host.md"}}}
	plan := reviewPlanOutput{Plan: "host plan"}
	tests := []struct {
		name     string
		tool     string
		model    string
		wantKey  string
		wantData any
	}{
		{name: "plan questions", tool: "build_review_plan", model: `{"questions":[{"title":"changed"}],"topic":"wrong","date":"wrong"}`, wantKey: "questions", wantData: found.Questions},
		{name: "save plan", tool: "save_plan", model: `{"plan":"changed"}`, wantKey: "plan", wantData: plan.Plan},
		{name: "send plan", tool: "send_message", model: `{"text":"changed"}`, wantKey: "text", wantData: plan.Plan},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, err := argumentsForTool(test.tool, test.model, "go", "2026-09-27", found, plan)
			if err != nil {
				t.Fatalf("argumentsForTool() error = %v", err)
			}
			got := args.(map[string]any)[test.wantKey]
			if !reflect.DeepEqual(got, test.wantData) {
				t.Fatalf("host-bound %s = %#v, want %#v", test.wantKey, got, test.wantData)
			}
		})
	}
	if _, err := argumentsForTool("find_due_questions", `{"topic":"wrong","date":"2026-09-27"}`, "go", "2026-09-27", found, plan); err == nil {
		t.Fatal("lookup selection with a conflicting topic was accepted")
	}
}
