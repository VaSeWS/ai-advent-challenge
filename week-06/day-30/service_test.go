package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testServiceToken = "0123456789abcdef0123456789abcdef"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sseResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: nil}
}
func testConfig() config {
	return config{backendModel: "/cache/pinned/snapshot", serviceToken: testServiceToken}
}
func requestWithAuth(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testServiceToken)
	return r
}
func validRequest() string {
	return `{"model":"local","messages":[{"role":"user","content":"question"}],"max_tokens":17}`
}

func TestUnauthorizedAndForbiddenControlsNeverReachMLX(t *testing.T) {
	cases := []struct {
		name, body    string
		authenticated bool
		status        int
	}{
		{"missing auth", validRequest(), false, 401},
		{"caller snapshot", `{"model":"/arbitrary/model","messages":[{"role":"user","content":"x"}]}`, true, 400},
		{"tools", `{"messages":[{"role":"user","content":"x"}],"tools":[]}`, true, 400},
		{"media", `{"messages":[{"role":"user","content":[{"type":"image_url","url":"https://example.invalid/a"}]}]}`, true, 400},
		{"backend control", `{"messages":[{"role":"user","content":"x"}],"enable_thinking":false}`, true, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forwarded := 0
			s := newService(testConfig())
			s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				forwarded++
				return sseResponse(200, "data: [DONE]\n\n"), nil
			})
			server := httptest.NewServer(http.HandlerFunc(s.chatCompletions))
			defer server.Close()
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(tc.body))
			if tc.authenticated {
				req.Header.Set("Authorization", "Bearer "+testServiceToken)
			}
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tc.status || forwarded != 0 {
				t.Fatalf("status=%d want %d upstream calls=%d", response.StatusCode, tc.status, forwarded)
			}
		})
	}

	s := newService(testConfig())
	forwarded := 0
	s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		forwarded++
		return sseResponse(200, "data: [DONE]\n\n"), nil
	})
	wrongToken := requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest())
	wrongToken.Header.Set("Authorization", "Bearer 1123456789abcdef0123456789abcdef")
	wrongResponse := httptest.NewRecorder()
	s.chatCompletions(wrongResponse, wrongToken)
	if wrongResponse.Code != http.StatusUnauthorized || forwarded != 0 {
		t.Fatalf("wrong token status=%d upstream calls=%d", wrongResponse.Code, forwarded)
	}
}
func TestBackendRedirectDoesNotForwardPrivateMessages(t *testing.T) {
	var destinationCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read redirected backend request body: %v", err)
		}
		t.Errorf("backend redirect reached destination: method=%s body=%q", r.Method, body)
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer external.Close()

	var sourceCalls atomic.Int32
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	s := newService(testConfig())
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != backendURL {
			return baseTransport.RoundTrip(r)
		}
		sourceCalls.Add(1)
		response := sseResponse(http.StatusTemporaryRedirect, "")
		response.Header.Set("Location", external.URL)
		return response, nil
	})
	request := requestWithAuth(http.MethodPost, "/v1/chat/completions",
		`{"messages":[{"role":"user","content":"private conversation"}]}`)
	response := httptest.NewRecorder()
	s.chatCompletions(response, request)
	if response.Code != http.StatusBadGateway || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("redirect status=%d source calls=%d destination calls=%d", response.Code, sourceCalls.Load(), destinationCalls.Load())
	}
	if !s.jobActive || !s.jobUncertain {
		t.Fatalf("ambiguous redirect did not retain admission: active=%t uncertain=%t", s.jobActive, s.jobUncertain)
	}
	second := httptest.NewRecorder()
	s.chatCompletions(second, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
	if second.Code != http.StatusServiceUnavailable || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("ambiguous redirect admission status=%d source calls=%d destination calls=%d", second.Code, sourceCalls.Load(), destinationCalls.Load())
	}
	body := response.Body.String()
	for _, private := range []string{"private conversation", testServiceToken, external.URL} {
		if strings.Contains(body, private) {
			t.Fatalf("redirect response exposed private data: %s", body)
		}
	}
}

func TestBackendHealthRedirectDoesNotReachDestination(t *testing.T) {
	var destinationCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		t.Errorf("health redirect reached destination: method=%s", r.Method)
		w.Write([]byte(`{"status":"healthy","loaded_model":"/cache/pinned/snapshot","effective_context_limit":4096}`))
	}))
	defer external.Close()

	const healthURL = "http://127.0.0.1:8080/health"
	var sourceCalls atomic.Int32
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != healthURL {
			return baseTransport.RoundTrip(r)
		}
		sourceCalls.Add(1)
		response := sseResponse(http.StatusPermanentRedirect, "")
		response.Header.Set("Location", external.URL)
		return response, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalDefaultTransport })

	err := checkBackendHealth(context.Background(), "/cache/pinned/snapshot")
	if err == nil || err.Error() != "MLX health check failed" || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("health redirect err=%v source calls=%d destination calls=%d", err, sourceCalls.Load(), destinationCalls.Load())
	}
}

func TestSupervisorWaitsForHandlerDrain(t *testing.T) {
	for _, failTelegram := range []bool{false, true} {
		name := "signal"
		if failTelegram {
			name = "telegram failure"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			release := make(chan struct{})
			released := false
			unblock := func() {
				if !released {
					close(release)
					released = true
				}
			}
			defer unblock()
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				w.WriteHeader(http.StatusOK)
			})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var telegram chan error
			var telegramDone <-chan error
			if failTelegram {
				telegram = make(chan error, 1)
				telegramDone = telegram
			}
			result := make(chan error, 1)
			go func() { result <- superviseHTTP(ctx, cancel, server, listener, telegramDone, nil) }()
			response := make(chan error, 1)
			go func() {
				resp, err := http.Get("http://" + listener.Addr().String())
				if err == nil {
					resp.Body.Close()
				}
				response <- err
			}()
			<-started
			if failTelegram {
				telegram <- errors.New("secret Telegram failure")
			} else {
				cancel()
			}
			select {
			case err := <-result:
				t.Fatalf("supervisor returned before handler drain: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-result:
				if failTelegram {
					if err == nil || err.Error() != "Telegram polling stopped unexpectedly" {
						t.Fatalf("Telegram failure was not sanitized and propagated: %v", err)
					}
				} else if err != nil {
					t.Fatalf("signal shutdown returned error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("supervisor did not finish after handler drain")
			}
			if err := <-response; err != nil {
				t.Fatalf("HTTP request failed: %v", err)
			}
		})
	}
}

func TestTypedRequestBuildsPinnedStreamingRequestAndReturnsContentOnly(t *testing.T) {
	s := newService(testConfig())
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != backendURL || r.Method != http.MethodPost {
			t.Errorf("unexpected backend destination %s %s", r.Method, r.URL)
		}
		var got map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 {
			t.Errorf("backend got fields %v; want only pinned allowlist", keys(got))
		}
		var model string
		_ = json.Unmarshal(got["model"], &model)
		if model != testConfig().backendModel {
			t.Errorf("model=%q", model)
		}
		var stream bool
		_ = json.Unmarshal(got["stream"], &stream)
		if !stream {
			t.Errorf("stream=%t", stream)
		}
		var tokens int
		_ = json.Unmarshal(got["max_tokens"], &tokens)
		if tokens != 17 {
			t.Errorf("max_tokens=%d", tokens)
		}
		return sseResponse(200, "data: {\"id\":\"server-id\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"private thoughts\",\"content\":\"answer\"}}]}\n\ndata: {\"id\":\"server-id\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
	})
	r := requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest())
	w := httptest.NewRecorder()
	s.chatCompletions(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Choices[0].Message.Content != "answer" || response.Choices[0].FinishReason != "stop" || strings.Contains(w.Body.String(), "private thoughts") {
		t.Fatalf("response exposed non-content output or lost finish reason: %s", w.Body.String())
	}
}

func TestBodyCapAndBackendContextRejection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport roundTripFunc
		body      string
		want      int
	}{
		{"oversized body", nil, `{"messages":[]}` + strings.Repeat(" ", requestBodyLimit), 413},
		{"backend rejects exact token context", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return sseResponse(400, `{"detail":"private backend internals"}`), nil
		}), validRequest(), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newService(testConfig())
			s.client.Transport = tc.transport
			w := httptest.NewRecorder()
			s.chatCompletions(w, requestWithAuth(http.MethodPost, "/v1/chat/completions", tc.body))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private backend internals") {
				t.Fatal("backend error leaked")
			}
		})
	}
}

func TestRateLimitUsesRemoteHostNotForwardedAddressOrSourcePort(t *testing.T) {
	s := newService(testConfig())
	now := time.Now()
	if s.allowRequest("192.0.2.5", now) != 0 {
		t.Fatal("first request limited")
	}
	for i := 1; i < rateRequests; i++ {
		if s.allowRequest("192.0.2.5", now.Add(time.Duration(i)*time.Second)) != 0 {
			t.Fatal("request within limit rejected")
		}
	}
	if retry := s.allowRequest("192.0.2.5", now.Add(4*time.Second)); retry <= 0 || retry > rateWindow {
		t.Fatalf("retry=%s", retry)
	}
	if s.allowRequest("192.0.2.6", now) != 0 {
		t.Fatal("independent host unexpectedly shared limiter")
	}
	r := requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest())
	r.RemoteAddr = "192.0.2.5:54321"
	r.Header.Set("X-Forwarded-For", "203.0.113.77")
	if host, ok := requestHost(r.RemoteAddr); !ok || host != "192.0.2.5" {
		t.Fatalf("identity=%q ok=%t", host, ok)
	}
	limited := newService(testConfig())
	for range rateRequests {
		limited.allowRequest("192.0.2.5", now)
	}
	forwarded := 0
	limited.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { forwarded++; return nil, nil })
	response := httptest.NewRecorder()
	limited.chatCompletions(response, r)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || forwarded != 0 {
		t.Fatalf("rate response status=%d Retry-After=%q upstream calls=%d", response.Code, response.Header().Get("Retry-After"), forwarded)
	}

}

func TestCanceledClientDrainsTerminalMarkerBeforeAdmissionRecovery(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			_, _ = io.WriteString(w, "data: {\"id\":\"first\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-finish
			_, _ = io.WriteString(w, "data: {\"id\":\"first\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		} else {
			_, _ = io.WriteString(w, "data: {\"id\":\"second\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer backend.Close()

	s := newService(testConfig())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL = mustURL(backend.URL)
		r.Host = r.URL.Host
		return transport.RoundTrip(r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.chatCompletions(w, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()).WithContext(ctx))
	}()
	<-started
	cancel()

	select {
	case <-done:
		t.Fatal("handler returned before the backend terminal marker")
	default:
	}
	second := httptest.NewRecorder()
	s.chatCompletions(second, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("admission status before terminal marker=%d want 503", second.Code)
	}
	close(finish)
	<-done
	if w.Body.Len() != 0 {
		t.Fatalf("abandoned downstream received a partial response: %s", w.Body.String())
	}
	if s.jobActive || s.jobUncertain {
		t.Fatal("terminal marker did not release healthy backend admission")
	}
	third := httptest.NewRecorder()
	s.chatCompletions(third, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
	if third.Code != http.StatusOK {
		t.Fatalf("healthy service did not recover after terminal marker: status=%d body=%s", third.Code, third.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("backend calls=%d want 2 (abandoned drain plus recovered request)", calls.Load())
	}
}

func TestTerminalFinishReasonAndMalformedOrEmptyOutputHandling(t *testing.T) {
	t.Run("length is visible and not rewritten as stop", func(t *testing.T) {
		s := newService(testConfig())
		s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return sseResponse(200, "data: {\"id\":\"length-id\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial answer\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"), nil
		})
		w := httptest.NewRecorder()
		s.chatCompletions(w, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
		var response chatResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("length completion status=%d body=%s", w.Code, w.Body.String())
		}
		if response.Choices[0].Message.Content != "partial answer" || response.Choices[0].FinishReason != "length" {
			t.Fatalf("caller-visible truncated completion was changed: %#v", response.Choices[0])
		}
	})

	t.Run("confirmed empty completion releases slot and next request succeeds", func(t *testing.T) {
		s := newService(testConfig())
		var calls atomic.Int32
		s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				return sseResponse(200, "data: {\"id\":\"empty-id\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
			}
			return sseResponse(200, "data: {\"id\":\"answer-id\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
		})
		w := httptest.NewRecorder()
		s.chatCompletions(w, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
		if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "empty-id") {
			t.Fatalf("empty terminal output was not sanitized: status=%d body=%s", w.Code, w.Body.String())
		}
		next := httptest.NewRecorder()
		s.chatCompletions(next, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
		if next.Code != http.StatusOK || calls.Load() != 2 || s.jobActive || s.jobUncertain {
			t.Fatalf("healthy admission did not recover after confirmed empty completion: status=%d calls=%d active=%t uncertain=%t", next.Code, calls.Load(), s.jobActive, s.jobUncertain)
		}
	})

	t.Run("multiline JSON data fields form one SSE event", func(t *testing.T) {
		s := newService(testConfig())
		s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return sseResponse(200, "data: {\"id\":\"multi-id\",\ndata: \"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
		})
		w := httptest.NewRecorder()
		s.chatCompletions(w, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
		var response chatResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil ||
			response.Choices[0].Message.Content != "answer" {
			t.Fatalf("multiline SSE event failed: status=%d body=%s", w.Code, w.Body.String())
		}
	})

	validChunks := "data: {\"id\":\"first\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n" +
		"data: {\"id\":\"first\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	var oversizedEvent strings.Builder
	oversizedEvent.WriteString("data: {\n")
	for i := 0; i < 257; i++ {
		oversizedEvent.WriteString("data: ")
		oversizedEvent.WriteString(strings.Repeat(" ", 4096))
		oversizedEvent.WriteByte('\n')
	}
	oversizedEvent.WriteString("data: \"id\":\"oversized-id\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
	oversizedEvent.WriteString("data: [DONE]\n\n")

	uncertainStreams := []struct {
		name   string
		stream string
	}{
		{name: "unterminated DONE", stream: validChunks + "data: [DONE]"},
		{name: "multiline DONE", stream: validChunks + "data: [DO]\ndata: [NE]\n\n"},
		{name: "error event before DONE", stream: validChunks + "event: error\ndata: [DONE]\n\n"},
		{name: "missing choice index", stream: "data: {\"id\":\"first\",\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "null choice index", stream: "data: {\"id\":\"first\",\"choices\":[{\"index\":null,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "single-line oversized SSE event", stream: "data: " + strings.Repeat("x", 1<<20) + "\n\n"},
		{name: "aggregate oversized SSE event", stream: oversizedEvent.String()},
	}
	for _, tc := range uncertainStreams {
		t.Run(tc.name+" retains admission", func(t *testing.T) {
			s := newService(testConfig())
			var calls atomic.Int32
			s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return sseResponse(http.StatusOK, tc.stream), nil
			})
			first := httptest.NewRecorder()
			s.chatCompletions(first, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
			second := httptest.NewRecorder()
			s.chatCompletions(second, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
			if first.Code != http.StatusBadGateway || second.Code != http.StatusServiceUnavailable ||
				!s.jobActive || !s.jobUncertain || calls.Load() != 1 {
				t.Fatalf("uncertain SSE released admission: first=%d second=%d calls=%d active=%t uncertain=%t", first.Code, second.Code, calls.Load(), s.jobActive, s.jobUncertain)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		stream string
	}{
		{name: "malformed chunk", stream: "data: not-json\n\ndata: [DONE]\n\n"},
		{name: "error event", stream: "data: {\"error\":{\"message\":\"failed\"}}\n\ndata: [DONE]\n\n"},
		{name: "conflicting identities", stream: "data: {\"id\":\"first\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"id\":\"second\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "missing finish reason", stream: "data: {\"id\":\"no-reason\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\ndata: [DONE]\n\n"},
		{name: "missing identity", stream: "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "unsupported finish reason", stream: "data: {\"id\":\"bad-reason\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name+" retains admission", func(t *testing.T) {
			s := newService(testConfig())
			s.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return sseResponse(200, tc.stream), nil
			})
			first := httptest.NewRecorder()
			s.chatCompletions(first, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
			second := httptest.NewRecorder()
			s.chatCompletions(second, requestWithAuth(http.MethodPost, "/v1/chat/completions", validRequest()))
			if first.Code != http.StatusBadGateway || second.Code != http.StatusServiceUnavailable || !s.jobActive || !s.jobUncertain {
				t.Fatalf("unconfirmed stream did not fail closed: first=%d second=%d active=%t uncertain=%t", first.Code, second.Code, s.jobActive, s.jobUncertain)
			}
		})
	}
}

func TestBoundedLimiterMapAndPrivateAddressRules(t *testing.T) {
	s := newService(testConfig())
	now := time.Now()
	for i := 0; i < maxRateClients; i++ {
		_ = s.allowRequest(fmt.Sprintf("client-%d", i), now)
	}
	if got := len(s.rates); got != maxRateClients {
		t.Fatalf("limiter entries=%d", got)
	}
	if s.allowRequest("another", now) == 0 || len(s.rates) > maxRateClients {
		t.Fatal("limiter map grew without bound")
	}
	for _, address := range []string{"192.168.1.5", "10.1.2.3", "172.31.1.9"} {
		if !isPrivateLANIPv4(byteIP(address)) {
			t.Errorf("rejected private address %s", address)
		}
	}
	for _, address := range []string{"8.8.8.8", "172.32.0.1", "169.254.1.2", "127.0.0.1"} {
		if isPrivateLANIPv4(byteIP(address)) {
			t.Errorf("accepted unsafe address %s", address)
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	return result
}
func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}
func byteIP(raw string) net.IP { return net.ParseIP(raw) }
