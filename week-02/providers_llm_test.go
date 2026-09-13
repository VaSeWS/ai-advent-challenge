package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestProviderProfiles(t *testing.T) {
	tests := []struct {
		name string
		want ProviderProfile
	}{
		{
			name: "groq",
			want: ProviderProfile{
				Name: "groq", Endpoint: "https://api.groq.com/openai/v1/chat/completions", Model: "openai/gpt-oss-20b", KeyEnv: "GROQ_API_KEY",
				ContextWindow: 131072, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_completion_tokens", ReasoningEffort: "low", CounterEncoding: "o200k_harmony",
			},
		},
		{
			name: "deepseek",
			want: ProviderProfile{
				Name: "deepseek", Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", KeyEnv: "DEEPSEEK_API_KEY",
				ContextWindow: 1000000, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_tokens", ReasoningEffort: "none", ThinkingDisabled: true, CounterEncoding: "deepseek-v4-estimate",
			},
		},
		{
			name: "local",
			want: ProviderProfile{
				Name: "local", Endpoint: "http://localhost:11434/v1/chat/completions", Model: "llama3-ctx2k", KeyEnv: "",
				ContextWindow: 2048, MainMax: 512, AuxiliaryMax: 256, MaxTokensField: "max_tokens", CounterEncoding: "cl100k_base",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := providerProfile(test.name)
			if err != nil {
				t.Fatalf("providerProfile(%q): %v", test.name, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("providerProfile(%q) = %#v, want %#v", test.name, got, test.want)
			}
		})
	}
}

func TestProviderProfileRejectsUnknownName(t *testing.T) {
	_, err := providerProfile("other")
	if err == nil {
		t.Fatal("providerProfile(\"other\") returned nil error")
	}
	if got, want := err.Error(), "unknown provider \"other\" (expected groq, deepseek or local)"; got != want {
		t.Fatalf("providerProfile(\"other\") error = %q, want %q", got, want)
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

func TestLocalPriceIsFree(t *testing.T) {
	profile, err := providerProfile("local")
	if err != nil {
		t.Fatalf("load local profile: %v", err)
	}

	got := profile.Price(Usage{
		PromptTokens:         1800,
		UncachedPromptTokens: 1800,
		CompletionTokens:     400,
		TotalTokens:          2200,
	}, time.Date(2026, time.March, 2, 1, 0, 0, 0, time.UTC))

	if got.Tier != "local" {
		t.Fatalf("price tier = %q, want local", got.Tier)
	}
	requireFloatEqual(t, got.InputCostUSD, 0)
	requireFloatEqual(t, got.OutputCostUSD, 0)
}

func TestLocalProfileUsesApproximateCounter(t *testing.T) {
	profile, err := providerProfile("local")
	if err != nil {
		t.Fatalf("load local profile: %v", err)
	}
	counter, err := newTokenCounter(profile)
	if err != nil {
		t.Fatalf("create token counter: %v", err)
	}

	if got, want := counter.Label(), "cl100k-approx"; got != want {
		t.Fatalf("counter label = %q, want %q", got, want)
	}
	if counter.Exact() {
		t.Fatal("local counter reports exact counts, want estimate")
	}
	if got := counter.Count("Сколько токенов в этой строке?"); got <= 0 {
		t.Fatalf("counter counted %d tokens for a non-empty string", got)
	}
}

func TestOpenAICompatibleClientOmitsAuthorizationWithoutKey(t *testing.T) {
	profile, err := providerProfile("local")
	if err != nil {
		t.Fatalf("load local profile: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want no header", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`))
	}))
	defer server.Close()

	profile.Endpoint = server.URL
	client := NewOpenAICompatibleClient(profile, "")
	got, err := client.Complete(context.Background(), CompletionRequest{
		Messages:  []CompletionMessage{{Role: "user", Content: "question"}},
		MaxTokens: profile.MainMax,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.Content != "answer" {
		t.Fatalf("completion content = %q, want answer", got.Content)
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

func requireFloatEqual(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("value = %.12f, want %.12f", got, want)
	}
}
