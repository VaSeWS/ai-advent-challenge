package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunLaneFiltersAtInclusiveBoundaryWithoutPadding(t *testing.T) {
	var queryVector atomic.Value
	queryVector.Store([]float32{1, 0})
	engine, store, closeServer := newTestEngine(t, &queryVector, "digest")
	defer closeServer()
	defer store.Close()
	engine.Retrieval = RetrievalSettings{CandidateK: 8, ContextK: 8, Threshold: 1, Calibrated: true, Calibration: "boundary test"}
	result, err := engine.RunLane(context.Background(), "Which tiers are covered?", LaneSettings{Mode: "filtered", Strategy: strategyFixed}, nil, TaskState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) == 0 {
		t.Fatal("expected indexed candidates")
	}
	if len(result.Context) != len(result.Candidates) {
		t.Fatalf("boundary candidates should pass inclusively, got %d of %d", len(result.Context), len(result.Candidates))
	}
	if len(result.Context) >= engine.Retrieval.ContextK {
		t.Fatalf("context was padded to K instead of retaining the available matching candidates: %d", len(result.Context))
	}
	if len(result.Context) > engine.Retrieval.ContextK {
		t.Fatalf("context exceeded K: %d", len(result.Context))
	}
}

func TestRunLaneEmptyFilteredContextSkipsMainModel(t *testing.T) {
	var queryVector atomic.Value
	queryVector.Store([]float32{0, 1})
	engine, store, closeServer := newTestEngine(t, &queryVector, "digest")
	defer closeServer()
	defer store.Close()
	engine.Retrieval = RetrievalSettings{CandidateK: 5, ContextK: 3, Threshold: 0.5, Calibrated: false, Calibration: "not calibrated"}
	result, err := engine.RunLane(context.Background(), "What does the snapshot say?", LaneSettings{Mode: "grounded", Strategy: strategyFixed}, nil, TaskState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) == 0 {
		t.Fatal("test must exercise candidates removed by filtering")
	}
	if len(result.Context) != 0 {
		t.Fatalf("filtered context not empty: %d", len(result.Context))
	}
	if result.Answer.Status != "insufficient_context" || result.Answer.Answer != insufficientAnswer || len(result.Answer.Citations) != 0 {
		t.Fatalf("unexpected deterministic refusal: %#v", result.Answer)
	}
	if got := atomic.LoadInt32(&engineTestMainCalls); got != 0 {
		t.Fatalf("main model called %d times for empty context", got)
	}
}

func TestRunLaneFingerprintMismatchStopsBeforeQueryEmbedding(t *testing.T) {
	var queryVector atomic.Value
	queryVector.Store([]float32{1, 0})
	engine, store, closeServer := newTestEngine(t, &queryVector, "changed-digest")
	defer closeServer()
	defer store.Close()
	engine.Retrieval = RetrievalSettings{CandidateK: 2, ContextK: 1, Threshold: 0, Calibrated: false}
	if _, err := engine.RunLane(context.Background(), "Search this", LaneSettings{Mode: "rag", Strategy: strategyFixed}, nil, TaskState{}); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("RunLane error = %v, want fingerprint incompatibility", err)
	}
	if got := atomic.LoadInt32(&engineTestEmbedCalls); got != 0 {
		t.Fatalf("query embeddings executed before compatibility check: %d", got)
	}
}

func TestCompareDoesNotLeakEvidenceIntoNoRAGLane(t *testing.T) {
	var queryVector atomic.Value
	queryVector.Store([]float32{1, 0})
	engine, store, closeServer := newTestEngine(t, &queryVector, "digest")
	defer closeServer()
	defer store.Close()
	engine.Retrieval = RetrievalSettings{CandidateK: 2, ContextK: 1, Threshold: 0, Calibrated: false}
	atomic.StoreInt32(&engineTestMainCalls, 0)
	captured := make(chan string, 4)
	engine.Groq.HTTP = captureGroqMessages(t, captured)
	run, err := engine.Compare(context.Background(), "What is in the corpus?", [2]LaneSettings{{Mode: "no-rag", Strategy: strategyFixed}, {Mode: "rag", Strategy: strategyFixed}})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Lanes[0].Candidates) != 0 || len(run.Lanes[0].Context) != 0 || run.Lanes[0].Build.ID != "" {
		t.Fatalf("no-rag lane performed retrieval: %#v", run.Lanes[0])
	}
	if len(run.Lanes[1].Context) == 0 {
		t.Fatal("RAG lane did not retrieve evidence")
	}
	var noRAGMessages, ragMessages string
	for range 2 {
		messages := <-captured
		if strings.Contains(messages, "Retrieved wiki evidence") {
			ragMessages = messages
		} else {
			noRAGMessages = messages
		}
	}
	if strings.Contains(noRAGMessages, "violet-ember-731") || strings.Contains(noRAGMessages, "Task state") {
		t.Fatalf("no-rag request contains other-lane state/evidence: %s", noRAGMessages)
	}
	if !strings.Contains(ragMessages, "violet-ember-731") {
		t.Fatal("RAG lane request did not include its own retrieved evidence")
	}
}
func TestNoRAGIgnoresTaskState(t *testing.T) {
	var queryVector atomic.Value
	queryVector.Store([]float32{1, 0})
	engine, store, closeServer := newTestEngine(t, &queryVector, "digest")
	defer closeServer()
	defer store.Close()
	captured := make(chan string, 1)
	engine.Groq.HTTP = captureGroqMessages(t, captured)
	state := TaskState{Goal: "private prior goal", GoalQuote: "user provided prior goal"}
	result, err := engine.RunLane(context.Background(), "Answer without retrieval", LaneSettings{Mode: "no-rag", Strategy: strategyFixed, TaskMemory: true}, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.State, TaskState{}) {
		t.Fatalf("no-rag result retained task state: %#v", result.State)
	}
	if message := <-captured; strings.Contains(message, state.Goal) || strings.Contains(message, state.GoalQuote) {
		t.Fatalf("no-rag main request received task state: %s", message)
	}
}

var engineTestMainCalls int32
var engineTestEmbedCalls int32

func newTestEngine(t *testing.T, queryVector *atomic.Value, fingerprintDigest string) (*Engine, *Store, func()) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "engine.db"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := EmbeddingFingerprint{Model: "test-embed:latest", Digest: "digest", Preparation: embeddingPreparation("test-embed:latest"), Dimensions: 2}
	snapshot := testStoreSnapshot()
	snapshot.Documents[0].Text = "violet-ember-731 identifies stored evidence for this retrieval lane."
	snapshot.Documents[0].Sections = []Section{{ID: "evidence", Path: "Evidence", Start: 0, End: len(strings.Fields(snapshot.Documents[0].Text))}}
	if _, err := store.BuildIndex(context.Background(), snapshot, ChunkConfig{Size: 20}, fingerprint, func(_ context.Context, texts []string) ([][]float32, error) {
		vectors := make([][]float32, len(texts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		return vectors, nil
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": "test-embed:latest", "digest": fingerprintDigest}}})
		case "/api/embed":
			atomic.AddInt32(&engineTestEmbedCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{queryVector.Load().([]float32)}})
		case "/chat/completions":
			var request struct {
				ResponseFormat struct {
					JSONSchema struct {
						Name string `json:"name"`
					} `json:"json_schema"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode Groq request: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			var content string
			if request.ResponseFormat.JSONSchema.Name == "lane_query_resolution" {
				content = `{"search_query":"standalone search","goal":"","goal_quote":"","clarifications":[],"constraints":[],"terms":[]}`
			} else {
				atomic.AddInt32(&engineTestMainCalls, 1)
				content = `{"status":"answer","answer":"A response based on the request.","citations":[]}`
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	engine := &Engine{Store: store, Ollama: OllamaClient{Endpoint: server.URL, Model: "test-embed:latest", Dimensions: 2}, Groq: GroqClient{Endpoint: server.URL + "/chat/completions", APIKey: "test-only", MainModel: defaultMainModel, AuxModel: defaultAuxModel}}
	atomic.StoreInt32(&engineTestMainCalls, 0)
	atomic.StoreInt32(&engineTestEmbedCalls, 0)
	return engine, store, server.Close
}

func captureGroqMessages(t *testing.T, messages chan<- string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var payload struct {
			Messages []LLMMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request payload: %v", err)
		}
		var joined strings.Builder
		for _, message := range payload.Messages {
			joined.WriteString(message.Content)
			joined.WriteByte('\n')
		}
		messages <- joined.String()
		return http.DefaultTransport.RoundTrip(request)
	})}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestValidateTaskStateRejectsDuplicateNormalizedKeys(t *testing.T) {
	state := TaskState{
		Clarifications: []StateFact{
			{Key: "Target", Value: "first", Quote: "first"},
			{Key: " target ", Value: "second", Quote: "second"},
		},
	}
	if err := validateTaskState(state); err == nil {
		t.Fatal("validateTaskState() accepted duplicate normalized keys")
	}
}

func TestValidateStateQuotesRejectsAssistantOnlyQuote(t *testing.T) {
	next := TaskState{
		Clarifications: []StateFact{{Key: "target", Value: "item", Quote: "assistant invented this"}},
	}
	history := []LLMMessage{
		{Role: "user", Content: "What is this item?"},
		{Role: "assistant", Content: "assistant invented this"},
	}
	if err := validateStateQuotes(next, TaskState{}, "Please identify it.", history); err == nil {
		t.Fatal("validateStateQuotes() accepted an assistant-only quote")
	}
}

func TestValidateStateQuotesRejectsQuoteJoinedAcrossUserMessages(t *testing.T) {
	quote := "LV\nUse HV"
	next := TaskState{Goal: "use the quoted levels", GoalQuote: quote}
	if err := validateTaskState(next); err != nil {
		t.Fatalf("test state is invalid: %v", err)
	}
	prior := TaskState{Goal: "prior goal", GoalQuote: "LV"}
	history := []LLMMessage{{Role: "user", Content: "Use HV"}}
	if err := validateStateQuotes(next, prior, "LV", history); err == nil {
		t.Fatal("validateStateQuotes() accepted a quote fabricated by joining separate user messages and prior state")
	}
}

func TestValidateStateQuotesAcceptsQuoteFromOneUserMessage(t *testing.T) {
	quote := "LV\nUse HV"
	next := TaskState{Goal: "use the quoted levels", GoalQuote: quote}
	if err := validateTaskState(next); err != nil {
		t.Fatalf("test state is invalid: %v", err)
	}
	history := []LLMMessage{{Role: "user", Content: "Please remember:\n" + quote}}
	if err := validateStateQuotes(next, TaskState{}, "What should I use?", history); err != nil {
		t.Fatalf("validateStateQuotes() rejected a quote present in one user message: %v", err)
	}
}
