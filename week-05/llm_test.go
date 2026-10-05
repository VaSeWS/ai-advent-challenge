package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type llmTestResult struct {
	Answer string `json:"answer"`
}

var llmTestSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"answer"},
	"properties": map[string]any{
		"answer": map[string]any{"type": "string", "minLength": 1},
	},
}

func chatCompletionResponse(content string) map[string]any {
	return map[string]any{"choices": []any{
		map[string]any{
			"finish_reason": "stop",
			"message":       map[string]string{"content": content},
		},
	}}
}

func TestDeepSeekCompleteUsesJSONOutputAndValidatesLocally(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "valid structured JSON", content: `{"answer":"hello"}`},
		{name: "schema-invalid JSON", content: `{"answer":""}`, wantErr: true},
		{name: "malformed content", content: `{"answer":`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if r.Method != http.MethodPost {
					t.Errorf("request method = %q, want POST", r.Method)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer synthetic-deepseek-key" {
					t.Errorf("authorization header was not the configured synthetic credential")
				}
				var request struct {
					Messages []LLMMessage `json:"messages"`
					Thinking struct {
						Type string `json:"type"`
					} `json:"thinking"`
					ResponseFormat struct {
						Type string `json:"type"`
					} `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode DeepSeek request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if request.ResponseFormat.Type != "json_object" {
					t.Errorf("response_format.type = %q, want json_object", request.ResponseFormat.Type)
				}
				if request.Thinking.Type != "disabled" {
					t.Errorf("thinking.type = %q, want disabled", request.Thinking.Type)
				}
				if len(request.Messages) != 2 || request.Messages[0].Role != "system" || !strings.Contains(request.Messages[0].Content, `"answer"`) {
					t.Errorf("DeepSeek request did not include the schema instruction")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(chatCompletionResponse(test.content))
			}))
			defer server.Close()

			var got llmTestResult
			client := GroqClient{Provider: providerDeepSeek, Endpoint: server.URL, APIKey: "synthetic-deepseek-key", HTTP: server.Client()}
			err := client.Complete(context.Background(), "test-model", []LLMMessage{{Role: "user", Content: "test"}}, "test_result", llmTestSchema, &got)
			if test.wantErr {
				if err == nil {
					t.Fatal("Complete() accepted invalid structured content")
				}
			} else if err != nil {
				t.Fatalf("Complete() error = %v", err)
			} else if got.Answer != "hello" {
				t.Errorf("decoded answer = %q, want hello", got.Answer)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("provider calls = %d, want exactly one", got)
			}
		})
	}
}

func TestCompleteFailedHTTPIsCalledOnce(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := GroqClient{Provider: providerDeepSeek, Endpoint: server.URL, APIKey: "synthetic-failure-key", HTTP: server.Client()}
	var got llmTestResult
	err := client.Complete(context.Background(), "test-model", nil, "test_result", llmTestSchema, &got)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("Complete() error = %v, want HTTP 503", err)
	}
	if strings.Contains(err.Error(), "synthetic-failure-key") {
		t.Fatal("Complete() error exposed the configured credential")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider calls = %d, want exactly one", got)
	}
}

func TestCompleteMissingSelectedProviderKeyFailsBeforeRequest(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	client := GroqClient{Provider: providerDeepSeek, Endpoint: server.URL, HTTP: server.Client()}
	var got llmTestResult
	err := client.Complete(context.Background(), "test-model", nil, "test_result", llmTestSchema, &got)
	if err == nil || !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("Complete() error = %v, want missing selected-provider key", err)
	}
	if strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "Bearer") {
		t.Fatal("Complete() error exposed credential material")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("provider calls = %d, want zero when credentials are missing", got)
	}
}

func TestGroqCompleteRequestsStrictJSONSchema(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var request struct {
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Groq request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Name != "test_result" || !request.ResponseFormat.JSONSchema.Strict {
			t.Errorf("Groq response_format = %#v, want named strict JSON Schema", request.ResponseFormat)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse(`{"answer":""}`))
	}))
	defer server.Close()

	client := GroqClient{Provider: providerGroq, Endpoint: server.URL, APIKey: "synthetic-groq-key", HTTP: server.Client()}
	var got llmTestResult
	err := client.Complete(context.Background(), "test-model", nil, "test_result", llmTestSchema, &got)
	if err == nil || !strings.Contains(err.Error(), "does not match schema") {
		t.Fatalf("Complete() error = %v, want schema validation failure", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("provider calls = %d, want exactly one", got)
	}
}
