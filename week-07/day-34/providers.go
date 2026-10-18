package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ProviderProfile struct {
	Name, Endpoint, Model, KeyEnv, MaxTokensField, CounterEncoding string
	ContextWindow, MainMax, AuxiliaryMax, ThinkingBudget           int
	RequestTimeout                                                 time.Duration
	ReasoningEffort                                                string
	ThinkingDisabled, ThinkingEnabled                              bool
}

const localModelRevision = "0e7ffd5c629ef7719d4cbc04069232580bfa9d9c"

var localModel = cachedLocalSnapshotModel()

func cachedLocalSnapshotModel() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cache", "huggingface", "hub", "models--mlx-community--Qwen3.5-4B-4bit", "snapshots", localModelRevision)
}

const defaultRemoteRequestTimeout = 60 * time.Second

var providerProfiles = map[string]ProviderProfile{
	"groq": {
		Name: "groq", Endpoint: "https://api.groq.com/openai/v1/chat/completions", Model: "openai/gpt-oss-20b", KeyEnv: "GROQ_API_KEY",
		ContextWindow: 131072, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_completion_tokens", RequestTimeout: defaultRemoteRequestTimeout, ReasoningEffort: "low", CounterEncoding: "o200k_harmony",
	},
	"deepseek": {
		Name: "deepseek", Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", KeyEnv: "DEEPSEEK_API_KEY",
		ContextWindow: 1000000, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_tokens", RequestTimeout: defaultRemoteRequestTimeout, ReasoningEffort: "none", ThinkingDisabled: true, CounterEncoding: "deepseek-v4-estimate",
	},
	"local": {
		Name: "local", Endpoint: "http://127.0.0.1:8080/v1/chat/completions", Model: localModel,
		ContextWindow: 16384, MainMax: 4096, AuxiliaryMax: 4096, MaxTokensField: "max_tokens", RequestTimeout: 30 * time.Minute, ThinkingEnabled: true, CounterEncoding: "qwen-estimate",
	},
}

func providerProfile(name string) (ProviderProfile, error) {
	profile, ok := providerProfiles[name]
	if !ok {
		return ProviderProfile{}, fmt.Errorf("unknown provider %q (expected groq, deepseek, or local)", name)
	}
	return profile, nil
}
func validateLocalSnapshotModel(model string) error {
	if !filepath.IsAbs(model) || !strings.Contains(model, "/models--") {
		return fmt.Errorf("local model must be an absolute pinned Hugging Face snapshot path")
	}
	marker := "/snapshots/"
	index := strings.LastIndex(model, marker)
	if index < 0 {
		return fmt.Errorf("local model must include an immutable /snapshots/<revision> path")
	}
	revision := model[index+len(marker):]
	if len(revision) != 40 {
		return fmt.Errorf("local model snapshot revision must be a 40-character commit")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		return fmt.Errorf("local model snapshot revision is not hexadecimal")
	}
	if filepath.Clean(model) != model {
		return fmt.Errorf("local model snapshot path must be clean")
	}
	return nil
}
func validateLocalEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse local endpoint: %w", err)
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.Path != "/v1/chat/completions" ||
		!(strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()) {
		return fmt.Errorf("local endpoint must be an HTTP loopback /v1/chat/completions URL")
	}
	return nil
}

func (p ProviderProfile) Price(usage Usage, startedAt time.Time) Price {
	if p.Name == "local" {
		return Price{Tier: "local"}
	}
	promptTokens := max(0, usage.PromptTokens)
	cachedTokens := min(max(0, usage.CachedPromptTokens), promptTokens)
	uncachedTokens := max(0, usage.UncachedPromptTokens)
	if cachedTokens+uncachedTokens != promptTokens {
		uncachedTokens = promptTokens - cachedTokens
	}

	if p.Name == "groq" {
		return Price{
			Tier:          "standard",
			InputCostUSD:  float64(cachedTokens)*0.037/1_000_000 + float64(uncachedTokens)*0.075/1_000_000,
			OutputCostUSD: float64(max(0, usage.CompletionTokens)) * 0.30 / 1_000_000,
		}
	}

	utc := startedAt.UTC()
	weekday := utc.Weekday()
	peak := weekday >= time.Monday && weekday <= time.Friday &&
		((utc.Hour() >= 1 && utc.Hour() < 4) || (utc.Hour() >= 6 && utc.Hour() < 10))
	if peak {
		return Price{
			Tier:          "peak",
			InputCostUSD:  float64(cachedTokens)*0.006/1_000_000 + float64(uncachedTokens)*0.30/1_000_000,
			OutputCostUSD: float64(max(0, usage.CompletionTokens)) * 1.20 / 1_000_000,
		}
	}
	return Price{
		Tier:          "off-peak",
		InputCostUSD:  float64(cachedTokens)*0.003/1_000_000 + float64(uncachedTokens)*0.15/1_000_000,
		OutputCostUSD: float64(max(0, usage.CompletionTokens)) * 0.60 / 1_000_000,
	}
}

type Price struct {
	Tier          string
	InputCostUSD  float64
	OutputCostUSD float64
}
