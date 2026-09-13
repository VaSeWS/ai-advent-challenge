package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Completer interface {
	Complete(context.Context, CompletionRequest) (Completion, error)
}

type OpenAICompatibleClient struct {
	profile ProviderProfile
	key     string
	http    *http.Client
}

func NewOpenAICompatibleClient(profile ProviderProfile, key string) *OpenAICompatibleClient {
	return &OpenAICompatibleClient{profile: profile, key: key, http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *OpenAICompatibleClient) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	body := map[string]any{
		"model":                  c.profile.Model,
		"messages":               req.Messages,
		c.profile.MaxTokensField: req.MaxTokens,
	}
	if c.profile.ReasoningEffort != "" {
		body["reasoning_effort"] = c.profile.ReasoningEffort
	}
	if c.profile.ThinkingDisabled {
		body["thinking"] = map[string]string{"type": "disabled"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.profile.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	if c.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.key)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(httpReq)
	if err != nil {
		return Completion{}, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return Completion{}, err
	}
	if response.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("completion request failed: %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}

	var decoded completionAPIResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return Completion{}, fmt.Errorf("decode completion response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Completion{}, fmt.Errorf("completion response contains no choices")
	}

	choice := decoded.Choices[0]
	return Completion{
		Content:      choice.Message.Content,
		Usage:        decoded.Usage.normalized(),
		FinishReason: choice.FinishReason,
	}, nil
}

type completionAPIResponse struct {
	Choices []struct {
		Message      CompletionMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage completionAPIUsage `json:"usage"`
}

type completionAPIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptDetails    *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens  *int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens *int `json:"prompt_cache_miss_tokens"`
}

func (u completionAPIUsage) normalized() Usage {
	usage := Usage{
		PromptTokens:     max(0, u.PromptTokens),
		CompletionTokens: max(0, u.CompletionTokens),
		TotalTokens:      max(0, u.TotalTokens),
	}

	switch {
	case u.PromptCacheHitTokens != nil && u.PromptCacheMissTokens != nil:
		usage.CachedPromptTokens = *u.PromptCacheHitTokens
		usage.UncachedPromptTokens = *u.PromptCacheMissTokens
	case u.PromptDetails != nil:
		usage.CachedPromptTokens = u.PromptDetails.CachedTokens
	}

	usage.CachedPromptTokens = min(max(0, usage.CachedPromptTokens), usage.PromptTokens)
	usage.UncachedPromptTokens = max(0, usage.UncachedPromptTokens)
	if usage.CachedPromptTokens+usage.UncachedPromptTokens != usage.PromptTokens {
		usage.UncachedPromptTokens = usage.PromptTokens - usage.CachedPromptTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	return usage
}
