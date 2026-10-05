package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompatibleFingerprintRejectsChangedDigest(t *testing.T) {
	stored := EmbeddingFingerprint{
		Model:       "nomic-embed-text:latest",
		Digest:      "sha256:old",
		Preparation: "search_document:/search_query: v1",
		Dimensions:  768,
	}
	current := stored
	current.Digest = "sha256:new"
	if err := CompatibleFingerprint(stored, current); err == nil {
		t.Fatal("CompatibleFingerprint() accepted a changed digest")
	}
	if err := CompatibleFingerprint(stored, stored); err != nil {
		t.Fatalf("CompatibleFingerprint() for identical fingerprints = %v", err)
	}
}

func TestCompatibleFingerprintRejectsInstalledTagChange(t *testing.T) {
	stored := EmbeddingFingerprint{
		Model:       "nomic-embed-text:latest",
		Digest:      "sha256:model",
		Preparation: "search_document:/search_query: v1",
		Dimensions:  768,
	}
	current := stored
	current.Model = "nomic-embed-text"
	if err := CompatibleFingerprint(stored, current); err == nil {
		t.Fatal("CompatibleFingerprint() accepted a model name without the installed tag")
	}
}

func TestValidateEmbeddingVectorsBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		vectors       [][]float32
		expectedCount int
		dimensions    int
		wantError     bool
	}{
		{name: "valid boundary dimensions", vectors: [][]float32{{math.SmallestNonzeroFloat32, 1}}, expectedCount: 1, dimensions: 2},
		{name: "valid batch", vectors: [][]float32{{1, 0}, {0, -1}}, expectedCount: 2, dimensions: 2},
		{name: "missing vector", vectors: [][]float32{{1, 0}}, expectedCount: 2, dimensions: 2, wantError: true},
		{name: "extra vector", vectors: [][]float32{{1, 0}, {0, 1}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "short vector", vectors: [][]float32{{1}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "long vector", vectors: [][]float32{{1, 0, 0}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "zero vector", vectors: [][]float32{{0, 0}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "nan", vectors: [][]float32{{float32(math.NaN()), 1}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "positive infinity", vectors: [][]float32{{float32(math.Inf(1)), 1}}, expectedCount: 1, dimensions: 2, wantError: true},
		{name: "negative infinity", vectors: [][]float32{{1, float32(math.Inf(-1))}}, expectedCount: 1, dimensions: 2, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateEmbeddingVectors(test.vectors, test.expectedCount, test.dimensions)
			if test.wantError != (err != nil) {
				t.Fatalf("validateEmbeddingVectors() error presence = %t, want %t", err != nil, test.wantError)
			}
		})
	}
}

func TestOllamaEmbedRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embeddings":[[1,0]]}`))
	}))
	defer server.Close()

	client := OllamaClient{Endpoint: server.URL, Model: "nomic-embed-text", Dimensions: 2}
	_, err := client.EmbedDocuments(context.Background(), []string{"one", "two"})
	if err == nil {
		t.Fatal("EmbedDocuments() accepted a malformed vector response")
	}
}
