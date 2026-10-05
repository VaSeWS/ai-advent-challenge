package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	providerGroq            = "groq"
	providerDeepSeek        = "deepseek"
	defaultGroqEndpoint     = "https://api.groq.com/openai/v1/chat/completions"
	defaultDeepSeekEndpoint = "https://api.deepseek.com/chat/completions"
	groqMaxResponseSize     = 4 << 20
)

func defaultProviderEndpoint(provider string) string {
	switch provider {
	case "", providerGroq:
		return defaultGroqEndpoint
	case providerDeepSeek:
		return defaultDeepSeekEndpoint
	default:
		return ""
	}
}

func providerModels(provider string) (string, string) {
	if provider == providerDeepSeek {
		return "deepseek-flash", "deepseek-flash"
	}
	return defaultMainModel, defaultAuxModel
}

func providerAPIKeyEnv(provider string) (string, error) {
	switch provider {
	case "", providerGroq:
		return "GROQ_API_KEY", nil
	case providerDeepSeek:
		return "DEEPSEEK_API_KEY", nil
	default:
		return "", fmt.Errorf("unknown provider %q (want groq or deepseek)", provider)
	}
}

func providerAPIKey(provider string) (string, error) {
	name, err := providerAPIKeyEnv(provider)
	if err != nil {
		return "", err
	}
	return os.Getenv(name), nil
}

func newProviderClient(provider, endpoint, mainModel, auxModel string, temperature float64, maxTokens int) (GroqClient, GenerationSettings, error) {
	if provider == "" {
		provider = providerGroq
	}
	if defaultProviderEndpoint(provider) == "" {
		return GroqClient{}, GenerationSettings{}, fmt.Errorf("unknown provider %q (want groq or deepseek)", provider)
	}
	if endpoint == "" {
		endpoint = defaultProviderEndpoint(provider)
	}
	defaultMain, defaultAux := providerModels(provider)
	if mainModel == "" {
		mainModel = defaultMain
	}
	if auxModel == "" {
		auxModel = defaultAux
	}
	client := GroqClient{
		Provider: provider, Endpoint: endpoint,
		MainModel: mainModel, AuxModel: auxModel,
		Temperature: temperature, MaxTokens: maxTokens,
	}
	settings := GenerationSettings{
		Provider: provider, Endpoint: endpoint, MainModel: mainModel, AuxModel: auxModel,
		Temperature: temperature, MaxTokens: maxTokens,
	}
	return client, settings, nil
}

// LLMMessage is one role/content message for a chat-completions request.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GroqClient is the minimal configurable client for the selected chat provider.
// Groq requests use strict JSON Schema; DeepSeek requests use JSON Output and
// validate the decoded content against the same schema locally.
type GroqClient struct {
	Provider    string
	Endpoint    string
	APIKey      string
	MainModel   string
	AuxModel    string
	HTTP        *http.Client
	Temperature float64
	MaxTokens   int
}

// Complete asks the selected provider for a structured response matching schema
// and decodes the single response into out. API keys are never included in
// returned errors.
func (c GroqClient) Complete(ctx context.Context, model string, messages []LLMMessage, schemaName string, schema any, out any) error {
	provider := c.Provider
	if provider == "" {
		provider = providerGroq
	}
	apiKey := strings.TrimSpace(c.APIKey)
	if apiKey == "" {
		var err error
		apiKey, err = providerAPIKey(provider)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		envName, err := providerAPIKeyEnv(provider)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s is required for %s requests", envName, provider)
	}
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("%s model must be explicitly selected", provider)
	}
	if strings.TrimSpace(schemaName) == "" || out == nil {
		return errors.New("structured response schema name and destination are required")
	}
	resolvedSchema, err := resolveGroqSchema(schema)
	if err != nil {
		return fmt.Errorf("invalid response JSON Schema: %w", err)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if endpoint == "" {
		endpoint = defaultProviderEndpoint(provider)
	}
	if endpoint == "" {
		return fmt.Errorf("unknown provider %q (want groq or deepseek)", provider)
	}

	var payload any
	switch provider {
	case providerGroq:
		request := struct {
			Model          string       `json:"model"`
			Messages       []LLMMessage `json:"messages"`
			Temperature    float64      `json:"temperature"`
			MaxTokens      int          `json:"max_tokens"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
					Schema any    `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}{Model: model, Messages: messages, Temperature: c.Temperature, MaxTokens: c.MaxTokens}
		request.ResponseFormat.Type = "json_schema"
		request.ResponseFormat.JSONSchema.Name = schemaName
		request.ResponseFormat.JSONSchema.Strict = true
		request.ResponseFormat.JSONSchema.Schema = schema
		payload = request
	case providerDeepSeek:
		schemaJSON, err := json.Marshal(schema)
		if err != nil {
			return fmt.Errorf("encode response JSON Schema: %w", err)
		}
		instructions := append([]LLMMessage(nil), messages...)
		instructions = append([]LLMMessage{{Role: "system", Content: fmt.Sprintf(
			"Return only valid JSON matching this JSON Schema named %q. Do not include markdown or extra properties. Schema: %s",
			schemaName, schemaJSON,
		)}}, instructions...)
		request := struct {
			Model       string       `json:"model"`
			Messages    []LLMMessage `json:"messages"`
			Temperature float64      `json:"temperature"`
			MaxTokens   int          `json:"max_tokens"`
			Thinking    struct {
				Type string `json:"type"`
			} `json:"thinking"`
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}{Model: model, Messages: instructions, Temperature: c.Temperature, MaxTokens: c.MaxTokens}
		request.Thinking.Type = "disabled"
		request.ResponseFormat.Type = "json_object"
		payload = request
	default:
		return fmt.Errorf("unknown provider %q (want groq or deepseek)", provider)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", provider, err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create %s request: %w", provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", provider, err)
	}
	defer resp.Body.Close()
	body, err := readBoundedBody(resp.Body, groqMaxResponseSize)
	if err != nil {
		return fmt.Errorf("read %s response: %w", provider, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s request returned HTTP %d", provider, resp.StatusCode)
	}
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := decodeJSONResponse(body, &response); err != nil {
		return fmt.Errorf("decode %s response: %w", provider, err)
	}
	if response.Error != nil {
		return fmt.Errorf("%s returned an API error", provider)
	}
	if len(response.Choices) != 1 {
		return fmt.Errorf("%s returned %d choices; expected exactly one", provider, len(response.Choices))
	}
	choice := response.Choices[0]
	if choice.FinishReason != "stop" {
		return fmt.Errorf("%s response did not finish normally (finish_reason %q)", provider, choice.FinishReason)
	}
	if choice.Message.Refusal != "" {
		return fmt.Errorf("%s refused the structured-output request", provider)
	}
	var content string
	if err := json.Unmarshal(choice.Message.Content, &content); err != nil {
		return fmt.Errorf("%s response content is not a JSON string", provider)
	}
	if err := validateJSONSchema(content, resolvedSchema); err != nil {
		return fmt.Errorf("%s structured output does not match schema: %w", provider, err)
	}
	if err := decodeOneJSON([]byte(content), out); err != nil {
		return fmt.Errorf("decode %s structured output: %w", provider, err)
	}
	return nil
}

func (c GroqClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 90 * time.Second}
}

func decodeJSONResponse(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains trailing JSON data")
		}
		return fmt.Errorf("response has invalid trailing data: %w", err)
	}
	return nil
}

// resolveGroqSchema verifies Groq's strict-object constraints locally and
// compiles the schema for validating the returned structured content.
func resolveGroqSchema(schema any) (*jsonschema.Resolved, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("encode response schema: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(schemaJSON, &root); err != nil {
		return nil, fmt.Errorf("decode response schema: %w", err)
	}
	if root == nil {
		return nil, errors.New("schema must be a non-empty JSON object")
	}
	var rootType string
	if err := json.Unmarshal(root["type"], &rootType); err != nil || rootType != "object" {
		return nil, errors.New("schema root type must be object")
	}
	if err := validateStrictSchemaNode(schemaJSON, "$"); err != nil {
		return nil, err
	}
	var parsedSchema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &parsedSchema); err != nil {
		return nil, fmt.Errorf("decode response schema: %w", err)
	}
	resolved, err := parsedSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve response schema: %w", err)
	}
	return resolved, nil
}

func validateStrictSchemaNode(raw []byte, path string) error {
	var node map[string]json.RawMessage
	if err := json.Unmarshal(raw, &node); err != nil {
		return fmt.Errorf("schema at %s must be an object: %w", path, err)
	}
	if node == nil {
		return fmt.Errorf("schema at %s must be a non-empty object", path)
	}
	var typeName string
	if typeRaw, exists := node["type"]; exists {
		_ = json.Unmarshal(typeRaw, &typeName)
	}
	if typeName == "object" {
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(node["properties"], &properties); err != nil || properties == nil {
			return fmt.Errorf("object schema at %s must declare properties", path)
		}
		var required []string
		if err := json.Unmarshal(node["required"], &required); err != nil {
			return fmt.Errorf("object schema at %s must declare required properties", path)
		}
		var additionalProperties bool
		if err := json.Unmarshal(node["additionalProperties"], &additionalProperties); err != nil || additionalProperties {
			return fmt.Errorf("object schema at %s must set additionalProperties to false", path)
		}
		requiredSet := make(map[string]struct{}, len(required))
		for _, key := range required {
			requiredSet[key] = struct{}{}
		}
		if len(requiredSet) != len(properties) {
			return fmt.Errorf("object schema at %s must require every declared property", path)
		}
		for key, property := range properties {
			if _, exists := requiredSet[key]; !exists {
				return fmt.Errorf("object schema at %s does not require property %q", path, key)
			}
			if err := validateStrictSchemaNode(property, path+"."+key); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"items", "not", "if", "then", "else"} {
		if child, exists := node[key]; exists {
			if err := validateStrictSchemaNode(child, path+"."+key); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"anyOf", "allOf", "oneOf"} {
		if children, exists := node[key]; exists {
			var schemas []json.RawMessage
			if err := json.Unmarshal(children, &schemas); err != nil {
				return fmt.Errorf("schema keyword %s at %s must be an array", key, path)
			}
			for i, child := range schemas {
				if err := validateStrictSchemaNode(child, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if definitions, exists := node[key]; exists {
			var schemas map[string]json.RawMessage
			if err := json.Unmarshal(definitions, &schemas); err != nil || schemas == nil {
				return fmt.Errorf("schema keyword %s at %s must be an object", key, path)
			}
			for name, child := range schemas {
				if err := validateStrictSchemaNode(child, path+"."+key+"."+name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateJSONSchema(content string, schema *jsonschema.Resolved) error {
	var value any
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("structured output contains trailing JSON data")
		}
		return fmt.Errorf("structured output has invalid trailing data: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return fmt.Errorf("validate response schema: %w", err)
	}
	return nil
}
