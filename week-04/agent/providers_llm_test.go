package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGroqIPv4TransportUsesTCP4(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer listener.Close()

	profile, err := providerProfile("groq")
	if err != nil {
		t.Fatalf("load Groq profile: %v", err)
	}

	t.Setenv("GROQ_IPV4", "1")
	client := NewOpenAICompatibleClient(profile, "test-key")
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok || transport == http.DefaultTransport {
		t.Fatalf("Groq transport = %T, want a cloned *http.Transport", client.http.Transport)
	}

	conn, err := transport.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("Groq IPv4 transport connected to an IPv6-only address")
	}
}

func TestGroqIPv4OptInDoesNotChangeOtherProviders(t *testing.T) {
	t.Setenv("GROQ_IPV4", "1")
	profile, err := providerProfile("deepseek")
	if err != nil {
		t.Fatalf("load DeepSeek profile: %v", err)
	}

	client := NewOpenAICompatibleClient(profile, "test-key")
	if client.http.Transport != nil {
		t.Fatalf("DeepSeek transport = %T, want default transport", client.http.Transport)
	}
}

func TestProviderProfileRejectsUnknownName(t *testing.T) {
	if _, err := providerProfile("other"); err == nil {
		t.Fatal("providerProfile(\"other\") returned nil error")
	}
}
func TestLocalModelRequiresPinnedSnapshotIdentifier(t *testing.T) {
	validHistoricalPinnedSnapshot := "/Users/example/.cache/huggingface/hub/models--mlx-community--Qwen3.8-27B-mxfp4/snapshots/97ab0819817ab1c61d7d39f9169fc71999915641"
	for _, model := range []string{localModel, validHistoricalPinnedSnapshot} {
		if err := validateLocalSnapshotModel(model); err != nil {
			t.Errorf("validate pinned model %q: %v", model, err)
		}
	}
	for _, model := range []string{"mlx-community/Qwen3.5-4B-4bit", "/models/qwen/latest", "/models/snapshots/not-a-commit"} {
		if err := validateLocalSnapshotModel(model); err == nil {
			t.Errorf("validate unpinned model %q succeeded", model)
		}
	}
}

func TestRunRejectsNonPositiveLimits(t *testing.T) {
	for _, args := range [][]string{
		{"-provider", "local", "-context-window", "0"},
		{"-provider", "local", "-max-tokens", "-1"},
		{"-provider", "local", "-aux-max-tokens", "0"},
		{"-provider", "local", "-request-timeout", "0s"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%v) succeeded with a non-positive limit", args)
		}
	}
}
func TestLocalCompletionDoesNotExposeCredentialsOrReasoning(t *testing.T) {
	profile, err := providerProfile("local")
	if err != nil {
		t.Fatalf("load local profile: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no credential header", got)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		var messages []CompletionMessage
		if err := json.Unmarshal(body["messages"], &messages); err != nil {
			t.Errorf("decode request messages: %v", err)
		} else if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" || messages[0].Content != "base instructions\n\nactive state" {
			t.Errorf("local messages = %#v, want one merged leading system and original user", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer","reasoning_content":"private reasoning"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	profile.Endpoint = server.URL
	got, err := NewOpenAICompatibleClient(profile, "").Complete(context.Background(), CompletionRequest{
		Messages: []CompletionMessage{
			{Role: "system", Content: "base instructions"},
			{Role: "system", Content: "active state"},
			{Role: "user", Content: "question"},
		},
		MaxTokens: 12,
	})
	if err != nil {
		t.Fatalf("complete locally: %v", err)
	}
	if got.Content != "answer" {
		t.Fatalf("completion content = %q, want final answer only", got.Content)
	}
	price := profile.Price(Usage{PromptTokens: 10, CompletionTokens: 4}, time.Now())
	if price.Tier != "local" || price.InputCostUSD != 0 || price.OutputCostUSD != 0 {
		t.Fatalf("local price = %#v, want zero-cost local tier", price)
	}
}

func TestGroqPriceSeparatesCachedAndUncachedPromptTokens(t *testing.T) {
	profile, err := providerProfile("groq")
	if err != nil {
		t.Fatalf("load Groq profile: %v", err)
	}

	got := profile.Price(Usage{
		PromptTokens:         3_000_000,
		CachedPromptTokens:   1_000_000,
		UncachedPromptTokens: 2_000_000,
		CompletionTokens:     3_000_000,
	}, time.Date(2026, time.March, 2, 1, 0, 0, 0, time.UTC))

	if got.Tier != "standard" {
		t.Fatalf("price tier = %q, want standard", got.Tier)
	}
	requireFloatEqual(t, got.InputCostUSD, 0.187)
	requireFloatEqual(t, got.OutputCostUSD, 0.9)
}

func TestDeepSeekPricePeakBoundaries(t *testing.T) {
	profile, err := providerProfile("deepseek")
	if err != nil {
		t.Fatalf("load DeepSeek profile: %v", err)
	}
	usage := Usage{
		PromptTokens:         3_000_000,
		CachedPromptTokens:   1_000_000,
		UncachedPromptTokens: 2_000_000,
		CompletionTokens:     3_000_000,
	}

	tests := []struct {
		name      string
		startedAt time.Time
		tier      string
		input     float64
		output    float64
	}{
		{name: "before first weekday peak window", startedAt: time.Date(2026, time.March, 2, 0, 59, 0, 0, time.UTC), tier: "off-peak", input: 0.303, output: 1.8},
		{name: "start of first weekday peak window", startedAt: time.Date(2026, time.March, 2, 1, 0, 0, 0, time.UTC), tier: "peak", input: 0.606, output: 3.6},
		{name: "end of first weekday peak window", startedAt: time.Date(2026, time.March, 2, 3, 59, 0, 0, time.UTC), tier: "peak", input: 0.606, output: 3.6},
		{name: "after first weekday peak window", startedAt: time.Date(2026, time.March, 2, 4, 0, 0, 0, time.UTC), tier: "off-peak", input: 0.303, output: 1.8},
		{name: "start of second weekday peak window", startedAt: time.Date(2026, time.March, 2, 6, 0, 0, 0, time.UTC), tier: "peak", input: 0.606, output: 3.6},
		{name: "end of second weekday peak window", startedAt: time.Date(2026, time.March, 2, 9, 59, 0, 0, time.UTC), tier: "peak", input: 0.606, output: 3.6},
		{name: "after second weekday peak window", startedAt: time.Date(2026, time.March, 2, 10, 0, 0, 0, time.UTC), tier: "off-peak", input: 0.303, output: 1.8},
		{name: "weekend remains off peak", startedAt: time.Date(2026, time.March, 7, 1, 0, 0, 0, time.UTC), tier: "off-peak", input: 0.303, output: 1.8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := profile.Price(usage, test.startedAt)
			if got.Tier != test.tier {
				t.Fatalf("tier at %s = %q, want %q", test.startedAt, got.Tier, test.tier)
			}
			requireFloatEqual(t, got.InputCostUSD, test.input)
			requireFloatEqual(t, got.OutputCostUSD, test.output)
		})
	}
}

func TestOpenAICompatibleClientNormalizesProviderUsage(t *testing.T) {
	tests := []struct {
		name          string
		profileName   string
		responseUsage string
		wantUsage     Usage
	}{
		{
			name:          "Groq prompt token details",
			profileName:   "groq",
			responseUsage: `{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_tokens_details":{"cached_tokens":4}}`,
			wantUsage:     Usage{PromptTokens: 12, CachedPromptTokens: 4, UncachedPromptTokens: 8, CompletionTokens: 5, TotalTokens: 17},
		},
		{
			name:          "DeepSeek cache hit and miss tokens",
			profileName:   "deepseek",
			responseUsage: `{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17,"prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":8}`,
			wantUsage:     Usage{PromptTokens: 12, CachedPromptTokens: 4, UncachedPromptTokens: 8, CompletionTokens: 5, TotalTokens: 17},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := providerProfile(test.profileName)
			if err != nil {
				t.Fatalf("load provider profile: %v", err)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("request method = %s, want POST", r.Method)
				}
				if got, want := r.Header.Get("Authorization"), "Bearer test-key"; got != want {
					t.Errorf("Authorization = %q, want %q", got, want)
				}

				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
					return
				}
				var maxTokens int
				if err := json.Unmarshal(body[profile.MaxTokensField], &maxTokens); err != nil {
					t.Errorf("decode %s: %v", profile.MaxTokensField, err)
				} else if maxTokens != 73 {
					t.Errorf("%s = %d, want 73", profile.MaxTokensField, maxTokens)
				}
				otherField := "max_tokens"
				if otherField == profile.MaxTokensField {
					otherField = "max_completion_tokens"
				}
				if _, ok := body[otherField]; ok {
					t.Errorf("request included unexpected %s", otherField)
				}

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":` + test.responseUsage + `}`))
			}))
			defer server.Close()

			profile.Endpoint = server.URL
			client := NewOpenAICompatibleClient(profile, "test-key")
			got, err := client.Complete(context.Background(), CompletionRequest{
				Messages:  []CompletionMessage{{Role: "user", Content: "question"}},
				MaxTokens: 73,
			})
			if err != nil {
				t.Fatalf("complete: %v", err)
			}
			if got.Content != "answer" || got.FinishReason != "stop" {
				t.Fatalf("completion = %#v, want answer stopped normally", got)
			}
			if got.Usage != test.wantUsage {
				t.Fatalf("usage = %#v, want %#v", got.Usage, test.wantUsage)
			}
		})
	}
}

func TestLocalCompletionDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var destinationCalls int32
			destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&destinationCalls, 1)
				w.WriteHeader(http.StatusOK)
			}))
			defer destination.Close()

			profile, err := providerProfile("local")
			if err != nil {
				t.Fatalf("load local profile: %v", err)
			}
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("source request method = %q, want POST", r.Method)
				}
				var request map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode source request body: %v", err)
				}
				w.Header().Set("Location", destination.URL)
				w.WriteHeader(status)
			}))
			defer source.Close()

			profile.Endpoint = source.URL
			_, err = NewOpenAICompatibleClient(profile, "").Complete(context.Background(), CompletionRequest{
				Messages:  []CompletionMessage{{Role: "user", Content: "private prompt and memory"}},
				MaxTokens: 12,
			})
			if err == nil {
				t.Fatal("Complete() followed local endpoint redirect")
			}
			if got := atomic.LoadInt32(&destinationCalls); got != 0 {
				t.Fatalf("redirect destination calls = %d, want zero", got)
			}
		})
	}
}

func TestLocalFinishReasonControlsTurnPersistence(t *testing.T) {
	for _, test := range []struct {
		name                 string
		finishReason         string
		wantErr              bool
		wantRequests         int32
		wantPersistedLineage int
	}{
		{name: "token limit", finishReason: "length", wantErr: true, wantRequests: 1},
		{name: "normal stop", finishReason: "stop", wantRequests: 2, wantPersistedLineage: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "local-completion.sqlite"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			chat, branch, err := store.ActiveChat()
			if err != nil {
				t.Fatalf("load active chat: %v", err)
			}

			var requests int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				call := atomic.AddInt32(&requests, 1)
				w.Header().Set("Content-Type", "application/json")
				if call == 1 {
					_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"truncated answer"},"finish_reason":%q}]}`, test.finishReason)
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			profile, err := providerProfile("local")
			if err != nil {
				t.Fatalf("load local profile: %v", err)
			}
			profile.Endpoint = server.URL
			counter, err := newTokenCounter(profile)
			if err != nil {
				t.Fatalf("create local token counter: %v", err)
			}
			agent := NewAgent(store, NewOpenAICompatibleClient(profile, ""), counter, profile)
			_, err = agent.Send(context.Background(), chat.ID, branch.ID, "question")
			if test.wantErr {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "finite token limit") {
					t.Fatalf("Send() error = %v, want clear finite token limit error", err)
				}
			} else if err != nil {
				t.Fatalf("Send() error = %v, want normal completion success", err)
			}
			if got := atomic.LoadInt32(&requests); got != test.wantRequests {
				t.Fatalf("local completion requests = %d, want %d", got, test.wantRequests)
			}
			lineage, err := store.Lineage(branch.ID)
			if err != nil {
				t.Fatalf("load lineage: %v", err)
			}
			if len(lineage) != test.wantPersistedLineage {
				t.Fatalf("persisted lineage = %#v, want %d messages", lineage, test.wantPersistedLineage)
			}
			if test.wantPersistedLineage == 2 &&
				(lineage[0].Content != "question" || lineage[1].Content != "truncated answer") {
				t.Fatalf("persisted normal-stop turn = %#v, want user question and assistant answer", lineage)
			}
		})
	}
}

func requireFloatEqual(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("value = %.12f, want %.12f", got, want)
	}
}
