package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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
	timeout := profile.RequestTimeout
	if timeout == 0 {
		timeout = defaultRemoteRequestTimeout
	}
	httpClient := &http.Client{Timeout: timeout}
	if profile.Name == "local" {
		httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	if profile.Name == "groq" && os.Getenv("GROQ_IPV4") == "1" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{}
		transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		}
		httpClient.Transport = transport
	}
	return &OpenAICompatibleClient{profile: profile, key: key, http: httpClient}
}

func (c *OpenAICompatibleClient) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	messages := req.Messages
	if c.profile.Name == "local" {
		var err error
		messages, err = localProviderMessages(messages)
		if err != nil {
			return Completion{}, err
		}
	}
	body := map[string]any{
		"model":                  c.profile.Model,
		"messages":               messages,
		c.profile.MaxTokensField: req.MaxTokens,
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
		if req.ToolChoice == "" {
			body["tool_choice"] = "auto"
		} else {
			body["tool_choice"] = req.ToolChoice
		}
	}
	if c.profile.ReasoningEffort != "" {
		body["reasoning_effort"] = c.profile.ReasoningEffort
	}
	if c.profile.ThinkingDisabled {
		body["thinking"] = map[string]string{"type": "disabled"}
	}
	if c.profile.Name == "local" && req.Kind == "memory" {
		body["enable_thinking"] = false
		body["response_format"] = localMemoryResponseFormat
	} else if c.profile.ThinkingEnabled {
		body["enable_thinking"] = true
	}
	if c.profile.Name == "deepseek" && req.Kind == "memory" {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if c.profile.Name == "local" && c.profile.ThinkingEnabled && req.Kind == "main" && c.profile.ThinkingBudget > 0 && req.MaxTokens > c.profile.ThinkingBudget {
		body["thinking_budget"] = c.profile.ThinkingBudget
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return Completion{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.profile.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Completion{}, err
	}
	if c.profile.Name != "local" {
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
	if c.profile.Name == "local" && choice.FinishReason == "length" {
		return Completion{}, fmt.Errorf("local completion reached its finite token limit (finish_reason %q)", choice.FinishReason)
	}
	return Completion{
		Content:      choice.Message.Content,
		ToolCalls:    choice.Message.ToolCalls,
		Usage:        decoded.Usage.normalized(),
		FinishReason: choice.FinishReason,
	}, nil
}
func localProviderMessages(messages []CompletionMessage) ([]CompletionMessage, error) {
	firstNonSystem := 0
	var system strings.Builder
	for firstNonSystem < len(messages) && messages[firstNonSystem].Role == "system" {
		if system.Len() > 0 {
			system.WriteString("\n\n")
		}
		system.WriteString(messages[firstNonSystem].Content)
		firstNonSystem++
	}
	for _, message := range messages[firstNonSystem:] {
		if message.Role == "system" {
			return nil, fmt.Errorf("local provider requires system instructions before conversation messages")
		}
	}
	if firstNonSystem <= 1 {
		return messages, nil
	}
	normalized := make([]CompletionMessage, 1, len(messages)-firstNonSystem+1)
	normalized[0] = CompletionMessage{Role: "system", Content: system.String()}
	normalized = append(normalized, messages[firstNonSystem:]...)
	return normalized, nil
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
