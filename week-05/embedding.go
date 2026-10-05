package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	defaultOllamaEndpoint = "http://localhost:11434"
	defaultOllamaModel    = "nomic-embed-text"
	nomicDimensions       = 768
	ollamaBatchSize       = 16
	ollamaMaxResponseSize = 16 << 20
)

// EmbeddingFingerprint identifies both the embedding model weights and the
// preparation applied to documents and queries.
type EmbeddingFingerprint struct {
	Model       string
	Digest      string
	Preparation string
	Dimensions  int
}

// OllamaClient calls the local Ollama embedding API.
type OllamaClient struct {
	Endpoint   string
	Model      string
	HTTP       *http.Client
	Dimensions int
}

// Fingerprint reports the exact installed model digest and configured vector
// dimensions. It does not download or resolve model aliases.
func (c OllamaClient) Fingerprint(ctx context.Context) (EmbeddingFingerprint, error) {
	endpoint, model := c.settings()
	requestURL := endpoint + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return EmbeddingFingerprint{}, fmt.Errorf("create Ollama model metadata request: %w", err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return EmbeddingFingerprint{}, fmt.Errorf("request Ollama model metadata: %w", err)
	}
	defer resp.Body.Close()
	body, err := readBoundedBody(resp.Body, ollamaMaxResponseSize)
	if err != nil {
		return EmbeddingFingerprint{}, fmt.Errorf("read Ollama model metadata: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return EmbeddingFingerprint{}, fmt.Errorf("Ollama model metadata returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := decodeJSONResponse(body, &result); err != nil {
		return EmbeddingFingerprint{}, fmt.Errorf("decode Ollama model metadata: %w", err)
	}
	for _, installed := range result.Models {
		if installed.Name != model {
			continue
		}
		if strings.TrimSpace(installed.Digest) == "" {
			return EmbeddingFingerprint{}, fmt.Errorf("Ollama model %q has no digest", model)
		}
		return EmbeddingFingerprint{
			Model:       installed.Name,
			Digest:      installed.Digest,
			Preparation: embeddingPreparation(installed.Name),
			Dimensions:  c.dimensions(),
		}, nil
	}
	return EmbeddingFingerprint{}, fmt.Errorf("Ollama model %q is not installed under that exact name", model)
}

// EmbedDocuments embeds texts in request order using the document retrieval
// prefix required by nomic-embed-text.
func (c OllamaClient) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	prepared := make([]string, len(texts))
	for i, text := range texts {
		prepared[i] = prepareEmbeddingInput(c.model(), text, true)
	}
	return c.embed(ctx, prepared)
}

// EmbedQuery embeds one query using the query retrieval prefix required by
// nomic-embed-text.
func (c OllamaClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := c.embed(ctx, []string{prepareEmbeddingInput(c.model(), text, false)})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (c OllamaClient) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	vectors := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += ollamaBatchSize {
		end := start + ollamaBatchSize
		if end > len(inputs) {
			end = len(inputs)
		}
		batch, err := c.embedBatch(ctx, inputs[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed input batch starting at %d: %w", start, err)
		}
		vectors = append(vectors, batch...)
	}
	return vectors, nil
}

func (c OllamaClient) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	endpoint, model := c.settings()
	payload := struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Truncate bool     `json:"truncate"`
	}{Model: model, Input: inputs, Truncate: false}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Ollama embedding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/api/embed", bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("create Ollama embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Ollama embeddings: %w", err)
	}
	defer resp.Body.Close()
	body, err := readBoundedBody(resp.Body, ollamaMaxResponseSize)
	if err != nil {
		return nil, fmt.Errorf("read Ollama embedding response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama embedding request returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := decodeJSONResponse(body, &result); err != nil {
		return nil, fmt.Errorf("decode Ollama embedding response: %w", err)
	}
	if err := validateEmbeddingVectors(result.Embeddings, len(inputs), c.dimensions()); err != nil {
		return nil, err
	}
	return result.Embeddings, nil
}

// CompatibleFingerprint rejects any model, weight, preparation, or dimensions
// change so callers can stop before querying an incompatible index.
func CompatibleFingerprint(stored, current EmbeddingFingerprint) error {
	if stored == current {
		return nil
	}
	var changes []string
	if stored.Model != current.Model {
		changes = append(changes, "model")
	}
	if stored.Digest != current.Digest {
		changes = append(changes, "model digest")
	}
	if stored.Preparation != current.Preparation {
		changes = append(changes, "input preparation")
	}
	if stored.Dimensions != current.Dimensions {
		changes = append(changes, "dimensions")
	}
	return fmt.Errorf("embedding fingerprint is incompatible (%s changed); rebuild the index before search", strings.Join(changes, ", "))
}

func validateEmbeddingVectors(vectors [][]float32, expectedCount, dimensions int) error {
	if len(vectors) != expectedCount {
		return fmt.Errorf("Ollama returned %d embeddings for %d inputs", len(vectors), expectedCount)
	}
	for i, vector := range vectors {
		if len(vector) != dimensions {
			return fmt.Errorf("Ollama embedding %d has %d dimensions; expected %d", i, len(vector), dimensions)
		}
		var normSquared float64
		for j, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("Ollama embedding %d contains a non-finite value at dimension %d", i, j)
			}
			normSquared += float64(value) * float64(value)
		}
		if normSquared == 0 {
			return fmt.Errorf("Ollama embedding %d is a zero vector", i)
		}
	}
	return nil
}

func (c OllamaClient) settings() (string, string) {
	endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if endpoint == "" {
		endpoint = defaultOllamaEndpoint
	}
	return endpoint, c.model()
}

func (c OllamaClient) model() string {
	model := strings.TrimSpace(c.Model)
	if model == "" {
		model = defaultOllamaModel
	}
	if tag := strings.LastIndexByte(model, ':'); tag <= strings.LastIndexByte(model, '/') {
		model += ":latest"
	}
	return model
}

func (c OllamaClient) dimensions() int {
	if c.Dimensions > 0 {
		return c.Dimensions
	}
	return nomicDimensions
}

func (c OllamaClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func embeddingPreparation(model string) string {
	if isNomicEmbeddingModel(model) {
		return "search_document:/search_query: v1"
	}
	return "raw-text-v1"
}

func prepareEmbeddingInput(model, text string, document bool) string {
	if !isNomicEmbeddingModel(model) {
		return text
	}
	if document {
		return "search_document: " + text
	}
	return "search_query: " + text
}

func isNomicEmbeddingModel(model string) bool {
	tag := strings.LastIndexByte(model, ':')
	return tag > strings.LastIndexByte(model, '/') && model[:tag] == defaultOllamaModel
}

func readBoundedBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	return data, nil
}

func decodeOneJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
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
