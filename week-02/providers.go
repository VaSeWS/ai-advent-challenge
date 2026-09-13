package main

import (
	"fmt"
	"time"
)

type ProviderProfile struct {
	Name, Endpoint, Model, KeyEnv, MaxTokensField, CounterEncoding string
	ContextWindow, MainMax, AuxiliaryMax                           int
	ReasoningEffort                                                string
	ThinkingDisabled                                               bool
}

type Price struct {
	Tier          string
	InputCostUSD  float64
	OutputCostUSD float64
}

var providerProfiles = map[string]ProviderProfile{
	"groq": {
		Name: "groq", Endpoint: "https://api.groq.com/openai/v1/chat/completions", Model: "openai/gpt-oss-20b", KeyEnv: "GROQ_API_KEY",
		ContextWindow: 131072, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_completion_tokens", ReasoningEffort: "low", CounterEncoding: "o200k_harmony",
	},
	"deepseek": {
		Name: "deepseek", Endpoint: "https://api.deepseek.com/chat/completions", Model: "deepseek-flash", KeyEnv: "DEEPSEEK_API_KEY",
		ContextWindow: 1000000, MainMax: 2048, AuxiliaryMax: 512, MaxTokensField: "max_tokens", ReasoningEffort: "none", ThinkingDisabled: true, CounterEncoding: "deepseek-v4-estimate",
	},
	"local": {
		Name: "local", Endpoint: "http://localhost:11434/v1/chat/completions", Model: "llama3-ctx2k", KeyEnv: "",
		ContextWindow: 2048, MainMax: 512, AuxiliaryMax: 256, MaxTokensField: "max_tokens", CounterEncoding: "cl100k_base",
	},
}

func providerProfile(name string) (ProviderProfile, error) {
	profile, ok := providerProfiles[name]
	if !ok {
		return ProviderProfile{}, fmt.Errorf("unknown provider %q (expected groq, deepseek or local)", name)
	}
	return profile, nil
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
