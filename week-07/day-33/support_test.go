package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingFlushWriter struct {
	header http.Header
	body   strings.Builder
}

func (w *failingFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *failingFlushWriter) WriteHeader(int)             {}
func (w *failingFlushWriter) Write(p []byte) (int, error) { return w.body.Write(p) }
func (w *failingFlushWriter) FlushError() error           { return errors.New("client disconnected") }

func TestConversationTurnIsHiddenUntilCommitted(t *testing.T) {
	store := newConversationStore()
	key := conversationKey{"session-a", "driver-1", "IRON-101"}
	firstUser := message{Role: "user", Content: "initial question"}
	firstAssistant := message{Role: "assistant", Content: "initial answer"}
	if err := store.appendTurn(key, firstUser, firstAssistant); err != nil {
		t.Fatal(err)
	}
	before := store.get(key)
	pendingUser := message{Role: "user", Content: "pending question"}
	pendingAssistant := message{Role: "assistant", Content: "pending answer"}
	if got := store.get(key); len(got) != len(before) {
		t.Fatalf("uncommitted turn became visible: %#v", got)
	}
	if err := store.appendTurn(key, pendingUser, pendingAssistant); err != nil {
		t.Fatal(err)
	}
	if got := store.get(key); len(got) != 4 || got[2].Role != pendingUser.Role || got[2].Content != pendingUser.Content ||
		got[3].Role != pendingAssistant.Role || got[3].Content != pendingAssistant.Content {
		t.Fatalf("committed turn missing or partial: %#v", got)
	}
}

func TestConversationTurnReservesTwoHistoryEntries(t *testing.T) {
	store := newConversationStore()
	key := conversationKey{"session-a", "driver-1", "IRON-101"}
	store.values[key] = make([]message, maxHistoryMessages-1)
	if err := store.appendTurn(key, message{Role: "user", Content: "question"}, message{Role: "assistant", Content: "answer"}); err == nil {
		t.Fatal("turn was admitted without room for both messages")
	}
	if got := store.get(key); len(got) != maxHistoryMessages-1 {
		t.Fatalf("failed commit changed history length to %d", len(got))
	}
}

func TestFailedAnswerFlushRollsBackTurn(t *testing.T) {
	store := newConversationStore()
	key := conversationKey{"session-a", "driver-1", "IRON-101"}
	if err := store.appendTurn(key,
		message{Role: "user", Content: "question"},
		message{Role: "assistant", Content: "answer"},
	); err != nil {
		t.Fatal(err)
	}
	if err := sendEvent(&failingFlushWriter{}, map[string]string{"type": "answer"}); err == nil {
		t.Fatal("expected failed response flush")
	}
	server := &server{sessions: store}
	server.rollbackTurn(key, "question")
	if got := store.get(key); len(got) != 0 {
		t.Fatalf("undelivered turn remained in history: %#v", got)
	}
}
func TestConversationOwnershipAndIsolation(t *testing.T) {
	s := newConversationStore()
	a := conversationKey{"session-a", "driver-1", "IRON-101"}
	b := conversationKey{"session-b", "driver-1", "IRON-101"}
	c := conversationKey{"session-a", "driver-1", "IRON-102"}
	if e := s.appendTurn(a, message{Role: "user", Content: "ghost off"}, message{Role: "assistant", Content: "Включите ghost"}); e != nil {
		t.Fatal(e)
	}
	if e := s.appendTurn(b, message{Role: "user", Content: "другой"}, message{Role: "assistant", Content: "ответ"}); e != nil {
		t.Fatal(e)
	}
	if e := s.appendTurn(c, message{Role: "user", Content: "другая заявка"}, message{Role: "assistant", Content: "ответ"}); e != nil {
		t.Fatal(e)
	}
	if got := s.get(a); len(got) != 2 || got[0].Content != "ghost off" {
		t.Fatalf("unexpected owned history: %#v", got)
	}
	if got := s.get(b); len(got) != 2 || got[0].Content != "другой" {
		t.Fatalf("session history crossed: %#v", got)
	}
	if got := s.get(c); len(got) != 2 || got[0].Content != "другая заявка" {
		t.Fatalf("ticket history crossed: %#v", got)
	}
}
func TestMCPEnforcesBoundTicketOwnership(t *testing.T) {
	data := crm{Users: []user{{ID: "driver-1"}, {ID: "driver-2"}}, Tickets: []ticket{{ID: "IRON-101", UserID: "driver-1"}}}
	var out strings.Builder
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_ticket","arguments":{"ticket_id":"IRON-101"}}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_ticket","arguments":{"ticket_id":"IRON-201"}}}` + "\n")
	if e := runMCP("driver-1", input, &out, data); e != nil {
		t.Fatal(e)
	}
	rows := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(rows) != 2 {
		t.Fatalf("got %d responses", len(rows))
	}
	var first, second rpcResponse
	if e := json.Unmarshal([]byte(rows[0]), &first); e != nil {
		t.Fatal(e)
	}
	if e := json.Unmarshal([]byte(rows[1]), &second); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(first.Result)
	if !strings.Contains(string(b), "IRON-101") {
		t.Fatalf("owned ticket absent: %s", b)
	}
	b, err := json.Marshal(second.Result)
	if err != nil {
		t.Fatal(err)
	}
	var denied mcpToolResult
	if err = json.Unmarshal(b, &denied); err != nil {
		t.Fatal(err)
	}
	if !denied.IsError {
		t.Fatalf("foreign ticket was not denied: %#v", second)
	}
}
func TestMCPRejectsUnboundIdentity(t *testing.T) {
	if e := runMCP("missing", strings.NewReader(""), io.Discard, crm{}); e == nil {
		t.Fatal("expected unknown bound user error")
	}
}
func TestUnknownCitationRejected(t *testing.T) {
	s := testServer()
	s.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"ответ\",\"source_ids\":[\"forged\"]}"}}]}`), nil
	})}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	_, e := s.complete(context.Background(), "ghost", nil, user{}, ticket{}, []source{{ID: "doc-1", Text: "доказательство"}}, nil)
	if e == nil || !strings.Contains(e.Error(), "invalid citation") {
		t.Fatalf("expected unknown citation rejection, got %v", e)
	}
}
func TestCompletionUsesDeepSeekJSONMode(t *testing.T) {
	s := testServer()
	s.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.deepseek.com/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected provider request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("provider authorization missing")
		}
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != defaultModel || request.Stream || request.Thinking["type"] != "disabled" ||
			request.ResponseFormat["type"] != "json_object" || len(request.Messages) != 3 ||
			request.Messages[1].Role != "assistant" || request.Messages[1].Content != "previous answer" {
			t.Fatalf("unexpected completion request: %#v", request)
		}
		return jsonResponse(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"ответ\",\"source_ids\":[\"doc-1\"]}"}}]}`), nil
	})}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	payload, err := s.complete(context.Background(), "follow-up", []message{{Role: "assistant", Content: "previous answer"}}, user{}, ticket{}, []source{{ID: "doc-1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Answer != "ответ" || len(payload.SourceIDs) != 1 || payload.SourceIDs[0] != "doc-1" {
		t.Fatalf("unexpected completion payload: %#v", payload)
	}
}
func TestCompletionRejectsEmptyAndDuplicateCitations(t *testing.T) {
	for _, content := range []string{
		`{"answer":"ответ","source_ids":[]}`,
		`{"answer":"ответ","source_ids":["doc-1","doc-1"]}`,
		`{"answer":"  ","source_ids":["doc-1"]}`,
	} {
		t.Run(content, func(t *testing.T) {
			s := testServer()
			encoded, err := json.Marshal(content)
			if err != nil {
				t.Fatal(err)
			}
			s.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(`{"choices":[{"finish_reason":"stop","message":{"content":` + string(encoded) + `}}]}`), nil
			})}
			t.Setenv("DEEPSEEK_API_KEY", "test-key")
			_, err = s.complete(context.Background(), "question", nil, user{}, ticket{}, []source{{ID: "doc-1"}}, nil)
			if err == nil {
				t.Fatal("expected invalid completion to be rejected")
			}
		})
	}
}

func TestCompletionRejectsProviderErrorWithValidChoice(t *testing.T) {
	s := testServer()
	s.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"error":{"message":"provider failure"},"choices":[{"finish_reason":"stop","message":{"content":"{\"answer\":\"ответ\",\"source_ids\":[\"doc-1\"]}"}}]}`), nil
	})}
	t.Setenv("DEEPSEEK_API_KEY", "test-key")
	_, err := s.complete(context.Background(), "question", nil, user{}, ticket{}, []source{{ID: "doc-1"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete output") {
		t.Fatalf("expected provider error to reject otherwise valid choice, got %v", err)
	}
}

func TestProviderErrorsAndIncompleteOutput(t *testing.T) {
	cases := []struct {
		name      string
		roundTrip roundTripFunc
		want      string
	}{{"network", func(*http.Request) (*http.Response, error) { return nil, errors.New("secret endpoint failure") }, "provider request failed"}, {"http", func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("secret")), Header: make(http.Header)}, nil
	}, "provider request failed"}, {"partial", func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`), nil
	}, "incomplete output"}, {"malformed", func(*http.Request) (*http.Response, error) { return jsonResponse(`not-json`), nil }, "incomplete output"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer()
			s.http = &http.Client{Transport: tc.roundTrip}
			t.Setenv("DEEPSEEK_API_KEY", "secret-key")
			_, e := s.complete(context.Background(), "question", nil, user{}, ticket{}, []source{{ID: "doc-1", Text: "docs"}}, nil)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, e)
			}
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("provider details leaked")
			}
		})
	}
}
func TestRussianGhostRetrieval(t *testing.T) {
	s := testServer()
	hits := s.retrieve("В Delta не вижу призрак, ghost выключен")
	if len(hits) == 0 {
		t.Fatal("Russian ghost query returned no documentation")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(strings.ToLower(h.Text), "ghost") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ghost evidence absent: %#v", hits)
	}
}
func testServer() *server {
	docs, meta, e := loadCorpus()
	if e != nil {
		panic(e)
	}
	return &server{model: defaultModel, docs: docs, manifest: meta, http: http.DefaultClient, sessions: newConversationStore()}
}
func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestOriginMustMatchLoopbackHost(t *testing.T) {
	tests := []struct {
		name, origin string
		want         bool
	}{{"same origin", "http://127.0.0.1:8033", true}, {"different port", "http://127.0.0.1:9999", false}, {"non-loopback", "http://example.test:8033", false}, {"https scheme", "https://127.0.0.1:8033", false}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8033/api/chat", nil)
			r.Header.Set("Origin", tc.origin)
			if got := originAllowed(r); got != tc.want {
				t.Fatalf("originAllowed()=%v, want %v", got, tc.want)
			}
		})
	}
}
func TestConversationRejectsConcurrentChat(t *testing.T) {
	s := newConversationStore()
	k := conversationKey{"session-a", "driver-1", "IRON-101"}
	if !s.begin(k) {
		t.Fatal("first chat was not admitted")
	}
	if s.begin(k) {
		t.Fatal("concurrent chat was admitted")
	}
	s.end(k)
	if !s.begin(k) {
		t.Fatal("chat remained locked after completion")
	}
}
