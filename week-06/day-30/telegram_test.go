package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func privateUpdate(id int64, kind, text string) telegramUpdate {
	return telegramUpdate{UpdateID: 1, Message: &struct {
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	}{Chat: struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	}{ID: id, Type: kind}, Text: text}}
}

func TestTelegramRejectsOtherChatsAndGroupsBeforeLocalModel(t *testing.T) {
	localCalls, telegramCalls := 0, 0
	client := &telegramClient{chatID: "42", serviceToken: testServiceToken, serviceURL: "http://local", history: []chatMessage{{Role: "system", Content: "system"}}, localHTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { localCalls++; return sseResponse(500, ""), nil })}, http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		telegramCalls++
		return sseResponse(200, `{"ok":true,"result":{}}`), nil
	})}}
	if err := client.processUpdate(context.Background(), privateUpdate(41, "private", "private outsider")); err != nil {
		t.Fatal(err)
	}
	if err := client.processUpdate(context.Background(), privateUpdate(42, "supergroup", "group text")); err != nil {
		t.Fatal(err)
	}
	if localCalls != 0 || telegramCalls != 0 || len(client.history) != 1 {
		t.Fatalf("unauthorized activity local=%d telegram=%d history=%d", localCalls, telegramCalls, len(client.history))
	}
}

func TestTelegramCommandsAvoidModelAndResetConversation(t *testing.T) {
	localCalls, sent := 0, make([]string, 0)
	client := &telegramClient{chatID: "42", history: []chatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "old"}, {Role: "assistant", Content: "answer"}},
		localHTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			localCalls++
			return nil, errors.New("must not call local model")
		})},
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var form url.Values
			body, _ := io.ReadAll(r.Body)
			form, _ = url.ParseQuery(string(body))
			sent = append(sent, form.Get("text"))
			return sseResponse(200, `{"ok":true,"result":{}}`), nil
		})}}
	if err := client.processUpdate(context.Background(), privateUpdate(42, "private", "/start")); err != nil {
		t.Fatal(err)
	}
	if localCalls != 0 || len(client.history) != 3 {
		t.Fatalf("/start called model or changed history: %d %d", localCalls, len(client.history))
	}
	if err := client.processUpdate(context.Background(), privateUpdate(42, "private", "/reset")); err != nil {
		t.Fatal(err)
	}
	if localCalls != 0 || len(client.history) != 1 || sent[len(sent)-1] != "Conversation history cleared." {
		t.Fatalf("/reset result calls=%d history=%v sent=%v", localCalls, client.history, sent)
	}
}

func TestTelegramConversationCommitsOnlyAfterWholeDelivery(t *testing.T) {
	localCalls, telegramChunks := 0, 0
	client := &telegramClient{token: "telegram-test-secret-token", chatID: "42", serviceToken: testServiceToken, serviceURL: "http://local", history: []chatMessage{{Role: "system", Content: "system"}},
		localHTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			localCalls++
			if r.Header.Get("Authorization") != "Bearer "+testServiceToken {
				t.Error("service bearer missing")
			}
			return sseResponse(200, `{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`), nil
		})},
		http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			telegramChunks++
			if telegramChunks == 2 {
				return sseResponse(502, ""), nil
			}
			return sseResponse(200, `{"ok":true,"result":{}}`), nil
		})}}
	long := strings.Repeat("x", telegramMessageUTF16Limit+1)
	client.localHTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		localCalls++
		return sseResponse(200, `{"choices":[{"message":{"role":"assistant","content":"`+long+`"},"finish_reason":"length"}]}`), nil
	})
	err := client.processUpdate(context.Background(), privateUpdate(42, "private", "question"))
	if err == nil || !strings.Contains(err.Error(), "delivered 1 of 2 message chunks") {
		t.Fatalf("partial delivery error=%v", err)
	}
	if strings.Contains(err.Error(), client.token) || strings.Contains(err.Error(), telegramAPI+"/bot"+client.token) || len(client.history) != 1 || telegramChunks != 2 {
		t.Fatalf("partial result leaked/committed or continued: err=%v history=%v chunks=%d", err, client.history, telegramChunks)
	}
}

func TestTelegramUTF16ChunkingPreservesUnicodeExactly(t *testing.T) {
	text := strings.Repeat("界", 4095) + "😀" + strings.Repeat("🧠", 4099)
	chunks := splitTelegramChunks(text)
	var joined strings.Builder
	for _, chunk := range chunks {
		units := 0
		for _, r := range chunk {
			if r > 0xffff {
				units += 2
			} else {
				units++
			}
		}
		if units > telegramMessageUTF16Limit {
			t.Fatalf("chunk uses %d UTF-16 units", units)
		}
		joined.WriteString(chunk)
	}
	if joined.String() != text {
		t.Fatal("UTF-16 chunking changed or dropped response text")
	}
}

func TestTelegramHistoryPrunesCompletePairsAndRetainsSystemAndQuestion(t *testing.T) {
	client := &telegramClient{history: []chatMessage{{Role: "system", Content: "permanent instruction"}}}
	for i := range 12 {
		client.history = append(client.history, chatMessage{Role: "user", Content: strings.Repeat("u", 6000) + string(rune('a'+i))}, chatMessage{Role: "assistant", Content: strings.Repeat("a", 6000) + string(rune('a'+i))})
	}
	messages := client.messagesFor("latest question")
	if messages[0].Role != "system" || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].Content != "latest question" {
		t.Fatalf("required history lost: %#v", messages)
	}
	if len(messages)%2 != 0 {
		t.Fatalf("messages do not have complete pairs: %d", len(messages))
	}
	for i := 1; i < len(messages)-1; i += 2 {
		if messages[i].Role != "user" || messages[i+1].Role != "assistant" {
			t.Fatalf("history contains partial pair: %#v", messages[i:i+2])
		}
	}
	if encodedMessageSize(messages) > maxHistoryBytes {
		t.Fatalf("history exceeds byte cap: %d", encodedMessageSize(messages))
	}
}

func TestTelegramOversizedDeliveredAnswerIsDroppedBeforeNextRequest(t *testing.T) {
	const oversizedAnswer = 9000
	answer := strings.Repeat("x", oversizedAnswer)
	localCalls := 0
	var delivered strings.Builder
	client := &telegramClient{
		token:        "telegram-test-secret-token",
		chatID:       "42",
		serviceToken: testServiceToken,
		serviceURL:   "http://local",
		history:      []chatMessage{{Role: "system", Content: "system"}},
		localHTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			localCalls++
			var request struct {
				Messages []chatMessage `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode local request: %v", err)
			}
			if request.Messages[0].Role != "system" || request.Messages[len(request.Messages)-1].Content != []string{"first question", "next question"}[localCalls-1] {
				t.Fatalf("system/current question not retained: %#v", request.Messages)
			}
			if strings.Contains(requestBodyContents(request.Messages), answer) {
				t.Fatal("oversized answer was sent in the next history request")
			}
			responseText := "next answer"
			if localCalls == 1 {
				responseText = answer
			}
			body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": responseText}, "finish_reason": "stop",
			}}})
			if err != nil {
				t.Fatal(err)
			}
			return sseResponse(http.StatusOK, string(body)), nil
		})},
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse Telegram request: %v", err)
			}
			delivered.WriteString(r.Form.Get("text"))
			return sseResponse(http.StatusOK, `{"ok":true,"result":{}}`), nil
		})},
	}
	if err := client.processUpdate(context.Background(), privateUpdate(42, "private", "first question")); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if len(client.history) != 1 || client.history[0] != (chatMessage{Role: "system", Content: "system"}) {
		t.Fatalf("oversized answer pair retained: %#v", client.history)
	}
	if err := client.processUpdate(context.Background(), privateUpdate(42, "private", "next question")); err != nil {
		t.Fatalf("next update: %v", err)
	}
	if localCalls != 2 || !strings.HasPrefix(delivered.String(), answer) {
		t.Fatalf("oversized answer was not fully delivered or next turn did not run: calls=%d delivered=%d", localCalls, delivered.Len())
	}
}

func requestBodyContents(messages []chatMessage) string {
	var contents strings.Builder
	for _, message := range messages {
		contents.WriteString(message.Content)
	}
	return contents.String()
}

func TestSuperviseHTTPPreservesOnlyTypedDeliveryCounts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "typed delivery error", err: &telegramDeliveryError{delivered: 1, total: 3}, want: "Telegram delivered 1 of 3 message chunks"},
		{name: "untrusted error", err: errors.New("https://api.telegram.org/botraw-secret/sendMessage rejected"), want: "Telegram polling stopped unexpectedly"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			telegramDone := make(chan error, 1)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})}
			result := make(chan error, 1)
			go func() { result <- superviseHTTP(ctx, cancel, server, listener, telegramDone, nil) }()
			response, err := http.Get("http://" + listener.Addr().String())
			if err != nil {
				t.Fatalf("start supervised server: %v", err)
			}
			response.Body.Close()
			telegramDone <- tt.err
			got := <-result
			if got == nil || got.Error() != tt.want || strings.Contains(got.Error(), "raw-secret") {
				t.Fatalf("supervisor error=%v, want %q", got, tt.want)
			}
		})
	}
}

func TestTelegramWebhookPreflightRefusesWithoutDeleting(t *testing.T) {
	calls := 0
	client := &telegramClient{token: "secret-token", http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/getWebhookInfo") {
			t.Errorf("unexpected call %s", r.URL.Path)
		}
		return sseResponse(200, `{"ok":true,"result":{"url":"https://webhook.invalid/"}}`), nil
	})}}
	err := client.verifyWebhook(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing") || calls != 1 || strings.Contains(err.Error(), client.token) {
		t.Fatalf("webhook preflight err=%v calls=%d", err, calls)
	}
}

func TestRunTelegramRejectsFixedAPIEndpointRedirect(t *testing.T) {
	var destinationCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		t.Errorf("credential-bearing Telegram redirect reached destination: method=%s path=%s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"url":""}}`))
	}))
	defer external.Close()

	const token = "telegram-test-secret-token"
	const sourceURL = telegramAPI + "/bot" + token + "/getWebhookInfo"
	var sourceCalls atomic.Int32
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != sourceURL {
			return baseTransport.RoundTrip(r)
		}
		sourceCalls.Add(1)
		response := sseResponse(http.StatusPermanentRedirect, "")
		response.Header.Set("Location", external.URL)
		return response, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalDefaultTransport })

	err := runTelegram(context.Background(), config{telegramToken: token}, make(chan error, 1), nil)
	if err == nil || err.Error() != "cannot verify Telegram webhook state; polling was not started" ||
		strings.Contains(err.Error(), token) || sourceCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("Telegram redirect err=%v source calls=%d destination calls=%d", err, sourceCalls.Load(), destinationCalls.Load())
	}
}

func TestRunTelegramLocalRedirectDoesNotForwardPrivateConversation(t *testing.T) {
	const token = "telegram-test-secret-token"
	var destinationCalls atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read redirected local request body: %v", err)
		}
		t.Errorf("private local generation redirect reached destination: method=%s authorization=%q body=%q", r.Method, r.Header.Get("Authorization"), body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]}`))
	}))
	defer external.Close()
	var localCalls atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, external.URL, http.StatusTemporaryRedirect)
	}))
	defer local.Close()

	const apiRoot = telegramAPI + "/bot" + token + "/"
	var apiCalls atomic.Int32
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || !strings.HasPrefix(r.URL.String(), apiRoot) {
			return baseTransport.RoundTrip(r)
		}
		apiCalls.Add(1)
		switch r.URL.Path {
		case "/bot" + token + "/getWebhookInfo":
			return sseResponse(http.StatusOK, `{"ok":true,"result":{"url":""}}`), nil
		case "/bot" + token + "/getUpdates":
			return sseResponse(http.StatusOK, `{"ok":true,"result":[{"update_id":1,"message":{"chat":{"id":42,"type":"private"},"text":"private conversation"}}]}`), nil
		default:
			t.Errorf("unexpected Telegram API request path %q", r.URL.Path)
			return sseResponse(http.StatusInternalServerError, ""), nil
		}
	})
	t.Cleanup(func() { http.DefaultTransport = originalDefaultTransport })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan error, 1)
	serveReady := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runTelegram(ctx, config{
			listen:         strings.TrimPrefix(local.URL, "http://"),
			serviceToken:   testServiceToken,
			telegramToken:  token,
			telegramChatID: "42",
		}, ready, serveReady)
	}()
	if err := <-ready; err != nil {
		t.Fatalf("Telegram preflight failed: %v", err)
	}
	close(serveReady)
	err := <-done
	if err == nil || err.Error() != "local chat service returned HTTP 307" ||
		strings.Contains(err.Error(), token) || strings.Contains(err.Error(), testServiceToken) ||
		apiCalls.Load() != 2 || localCalls.Load() != 1 || destinationCalls.Load() != 0 {
		t.Fatalf("local redirect err=%v Telegram API calls=%d local calls=%d destination calls=%d", err, apiCalls.Load(), localCalls.Load(), destinationCalls.Load())
	}
}

func TestTelegramAPIErrorIsRedactedAndDeliveryStops(t *testing.T) {
	calls := 0
	client := &telegramClient{token: "secret-token", chatID: "42", http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return sseResponse(200, `{"ok":false,"description":"secret-token rejected"}`), nil
	})}}
	err := client.sendTelegram(context.Background(), strings.Repeat("z", telegramMessageUTF16Limit+1))
	if err == nil || calls != 1 || strings.Contains(err.Error(), client.token) {
		t.Fatalf("delivery error=%v calls=%d", err, calls)
	}
}
func TestTelegramDoesNotCommitInvalidOrEmptyCompletion(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
	} {
		localCalls, telegramCalls := 0, 0
		client := &telegramClient{
			chatID:       "42",
			serviceURL:   "http://local",
			serviceToken: testServiceToken,
			history:      []chatMessage{{Role: "system", Content: "system"}},
			localHTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				localCalls++
				return sseResponse(http.StatusOK, body), nil
			})},
			http: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				telegramCalls++
				return sseResponse(http.StatusOK, `{"ok":true,"result":{}}`), nil
			})},
		}
		if err := client.processUpdate(context.Background(), privateUpdate(42, "private", "question")); err == nil {
			t.Fatalf("accepted invalid/empty completion: %s", body)
		}
		if localCalls != 1 || telegramCalls != 0 || len(client.history) != 1 {
			t.Fatalf("invalid completion leaked or committed: local=%d telegram=%d history=%v", localCalls, telegramCalls, client.history)
		}
	}
}

func TestRunTelegramLocalServiceBypassesEnvironmentProxy(t *testing.T) {
	// ProxyFromEnvironment caches process state. Isolate the real environment
	// proxy check from other tests instead of depending on their execution order.
	const childEnv = "DAY30_PROXY_REGRESSION_CHILD"
	if os.Getenv(childEnv) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal("cannot locate isolated test executable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^"+t.Name()+"$", "-test.count=1", "-test.timeout=15s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated proxy consumer regression failed (%T): %s", err, output)
		}
		return
	}

	const privateAddress = "192.168.50.30:8090"
	const firstQuestion = "Remember this code for this conversation: amber-7421."
	const secondQuestion = "What code did I ask you to remember?"
	const firstAnswer = "The conversation code is amber-7421."
	const secondAnswer = "You asked me to remember amber-7421."
	const botToken = "telegram-proxy-fixture-token"
	var proxyCalls, localCalls, backendCalls, polls, deliveries atomic.Int32
	var proxyBearer, proxyBody atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		if r.Header.Get("Authorization") != "" {
			proxyBearer.Store(true)
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, requestBodyLimit+1))
		if err != nil {
			t.Error("proxy fixture could not read bounded body")
		}
		if len(body) != 0 {
			proxyBody.Store(true)
		}
		http.Error(w, "proxy trap", http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	for _, key := range []string{"http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "REQUEST_METHOD"} {
		t.Setenv(key, "")
	}

	// These replies are independent fixtures, not echoes. The second backend
	// request must contain the first fully delivered answer as conversation.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := backendCalls.Add(1)
		var request struct {
			Model     string        `json:"model"`
			Messages  []chatMessage `json:"messages"`
			MaxTokens int           `json:"max_tokens"`
			Stream    bool          `json:"stream"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			json.NewDecoder(io.LimitReader(r.Body, requestBodyLimit+1)).Decode(&request) != nil ||
			request.Model != testConfig().backendModel || !request.Stream ||
			request.MaxTokens != defaultOutputTokens || turn < 1 || turn > 2 {
			t.Error("unexpected fixture backend request")
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		want := []chatMessage{
			{Role: "system", Content: "You are a helpful assistant. Answer clearly and honestly."},
			{Role: "user", Content: firstQuestion},
		}
		id, answer := "fixture-first", firstAnswer
		if turn == 2 {
			want = append(want, chatMessage{Role: "assistant", Content: firstAnswer}, chatMessage{Role: "user", Content: secondQuestion})
			id, answer = "fixture-second", secondAnswer
		}
		matches := len(request.Messages) == len(want)
		if matches {
			for i := range want {
				if request.Messages[i] != want[i] {
					matches = false
					break
				}
			}
		}
		if !matches {
			t.Error("consumer omitted or changed delivered conversation history")
			http.Error(w, "invalid fixture conversation", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\""+id+"\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\""+answer+"\"}}]}\n\ndata: {\"id\":\""+id+"\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()
	backendTarget, err := url.Parse(backend.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal("invalid fixture backend URL")
	}
	originalDefault := http.DefaultTransport
	backendTransport := originalDefault.(*http.Transport).Clone()
	backendTransport.Proxy = nil
	defer backendTransport.CloseIdleConnections()
	api := newService(testConfig())
	api.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != backendURL {
			return nil, errors.New("unexpected backend destination in fixture")
		}
		forwarded := r.Clone(r.Context())
		forwarded.URL = backendTarget
		return backendTransport.RoundTrip(forwarded)
	})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || !bearerMatches(r.Header.Get("Authorization"), testServiceToken) {
			t.Error("direct fixture request lacks expected path or authorization")
			http.Error(w, "invalid fixture request", http.StatusUnauthorized)
			return
		}
		api.chatCompletions(w, r)
	}))
	defer local.Close()

	// Keep the logical origin RFC1918, but dial only our owned loopback fixtures.
	// This exercises environment proxy selection without touching a real LAN.
	dialer := &net.Dialer{Timeout: time.Second}
	dialFixture := func(ctx context.Context, network, address string) (net.Conn, error) {
		switch address {
		case privateAddress:
			return dialer.DialContext(ctx, network, local.Listener.Addr().String())
		case proxy.Listener.Addr().String():
			return dialer.DialContext(ctx, network, address)
		default:
			return nil, errors.New("destination outside owned fixtures")
		}
	}
	proxiedTransport := originalDefault.(*http.Transport).Clone()
	proxiedTransport.DialContext = dialFixture
	defer proxiedTransport.CloseIdleConnections()
	originalLocal := telegramLocalTransport
	directTransport := originalLocal.Clone()
	directTransport.DialContext = dialFixture
	telegramLocalTransport = directTransport
	t.Cleanup(func() {
		telegramLocalTransport = originalLocal
		directTransport.CloseIdleConnections()
	})

	// Positive control: the same private origin really is proxied in this fresh
	// process. The control is a bodyless GET, never an authenticated model call.
	control, err := (&http.Client{Transport: proxiedTransport, Timeout: 2 * time.Second}).Get("http://" + privateAddress + "/proxy-control")
	if err != nil {
		t.Fatal("environment proxy positive control failed")
	}
	control.Body.Close()
	if control.StatusCode != http.StatusBadGateway || proxyCalls.Load() != 1 {
		t.Fatal("environment proxy did not reach the controlled trap")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" {
			if r.URL.Host != privateAddress {
				return nil, errors.New("unexpected consumer destination in fixture")
			}
			return proxiedTransport.RoundTrip(r)
		}
		switch r.URL.Path {
		case "/bot" + botToken + "/getWebhookInfo":
			return sseResponse(http.StatusOK, `{"ok":true,"result":{"url":""}}`), nil
		case "/bot" + botToken + "/getUpdates":
			if polls.Add(1) == 1 {
				outsider := privateUpdate(99, "private", "unauthorized fixture text")
				first := privateUpdate(42, "private", firstQuestion)
				first.UpdateID = 2
				second := privateUpdate(42, "private", secondQuestion)
				second.UpdateID = 3
				body, err := json.Marshal(map[string]any{"ok": true, "result": []telegramUpdate{outsider, first, second}})
				if err != nil {
					return nil, errors.New("cannot encode fixture updates")
				}
				return sseResponse(http.StatusOK, string(body)), nil
			}
			if r.URL.Query().Get("offset") != "4" || deliveries.Load() != 2 {
				t.Error("polling advanced without full authorized reply delivery")
				return nil, errors.New("unexpected fixture polling state")
			}
			cancel()
			return nil, context.Canceled
		case "/bot" + botToken + "/sendMessage":
			turn := deliveries.Add(1)
			want := firstAnswer
			if turn == 2 {
				want = secondAnswer
			}
			if r.ParseForm() != nil || turn > 2 || r.Form.Get("chat_id") != "42" || r.Form.Get("text") != want {
				t.Error("unexpected authorized fixture reply delivery")
				return nil, errors.New("invalid fixture delivery")
			}
			return sseResponse(http.StatusOK, `{"ok":true,"result":{}}`), nil
		default:
			return nil, errors.New("unexpected fixture Telegram method")
		}
	})
	t.Cleanup(func() { http.DefaultTransport = originalDefault })
	ready := make(chan error, 1)
	serveReady := make(chan struct{})
	close(serveReady)
	done := make(chan error, 1)
	go func() {
		done <- runTelegram(ctx, config{listen: privateAddress, serviceToken: testServiceToken, telegramToken: botToken, telegramChatID: "42"}, ready, serveReady)
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal("fixture polling preflight failed")
		}
	case <-ctx.Done():
		t.Fatal("fixture polling preflight did not finish")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("consumer did not finish by controlled cancellation (%T)", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("consumer did not join within the fixture deadline")
	}
	if proxyCalls.Load() != 1 || proxyBearer.Load() || proxyBody.Load() {
		t.Errorf("private service traffic reached proxy: additional_calls=%d bearer=%t body=%t", proxyCalls.Load()-1, proxyBearer.Load(), proxyBody.Load())
	}
	if localCalls.Load() != 2 || backendCalls.Load() != 2 || deliveries.Load() != 2 || polls.Load() != 2 {
		t.Errorf("consumer completion/history counts: local=%d backend=%d deliveries=%d polls=%d", localCalls.Load(), backendCalls.Load(), deliveries.Load(), polls.Load())
	}
}
