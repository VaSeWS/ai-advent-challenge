package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPersistentSubmitCallsProviderAndCommitsBothLanes(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	provider := newCompletionRecorder(t, `{"status":"insufficient_context","answer":"Not enough evidence.","citations":[]}`)
	t.Setenv("DEEPSEEK_API_KEY", "synthetic-persistent-key")
	generation := GenerationSettings{
		Provider: providerDeepSeek, Endpoint: provider.URL,
		MainModel: "persistent-main", AuxModel: "persistent-aux", Temperature: 0.35, MaxTokens: 96,
	}
	lanes := [2]LaneSettings{
		{Mode: "no-rag", Strategy: strategyFixed},
		{Mode: "no-rag", Strategy: strategyStructural},
	}
	session, err := store.NewSession(ctx, lanes, generation, RetrievalSettings{CandidateK: 7, ContextK: 2, Threshold: 0.3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Store: store, Groq: GroqClient{Provider: providerDeepSeek, Endpoint: provider.URL, HTTP: provider.server.Client()}}
	app, err := newChatApp(ctx, func() {}, store, engine, 3, session, lanes, false, generation)
	if err != nil {
		t.Fatal(err)
	}
	app.input.SetValue("persistent question")

	saved := submitAndComplete(t, app)
	if saved.err != nil {
		t.Fatalf("submitted turn failed to persist: %v", saved.err)
	}
	if app.fatalErr != nil {
		t.Fatalf("persistent submit failed: %v", app.fatalErr)
	}
	if got := len(provider.requests()); got != 2 {
		t.Fatalf("provider received %d lane requests, want 2", got)
	}
	for i, request := range provider.requests() {
		if request.Model != generation.MainModel {
			t.Errorf("lane %d requested model %q, want %q", i+1, request.Model, generation.MainModel)
		}
		if !request.hasMessage("persistent question") {
			t.Errorf("lane %d provider request omitted the submitted question", i+1)
		}
		if request.Authorization != "Bearer synthetic-persistent-key" {
			t.Errorf("lane %d authorization = %q, want selected synthetic credential", i+1, request.Authorization)
		}
	}
	turns, err := store.LoadTurns(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Question != "persistent question" {
		t.Fatalf("persisted turns = %#v, want one committed submitted turn", turns)
	}
	for lane := range turns[0].Lanes {
		if turns[0].Lanes[lane].Generation != generation {
			t.Errorf("persisted lane %d generation = %#v, want %#v", lane+1, turns[0].Lanes[lane].Generation, generation)
		}
		history, err := store.LoadHistory(ctx, session.ID, lane)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 || history[0].Content != "persistent question" || history[1].Content != "Not enough evidence." {
			t.Errorf("persisted lane %d history = %#v, want submitted question and provider answer", lane+1, history)
		}
	}
}

func TestRawTurnsOnlyResumeCreatesCleanSessionWithSavedConfiguration(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	generation := GenerationSettings{
		Provider: providerDeepSeek, Endpoint: "https://deepseek.example.test/custom",
		MainModel: "saved-main", AuxModel: "saved-aux", Temperature: 0.47, MaxTokens: 321,
	}
	retrieval := RetrievalSettings{CandidateK: 17, ContextK: 5, Threshold: 0.42, Calibrated: true, Calibration: "saved calibration"}
	lanes := [2]LaneSettings{
		{Mode: "grounded", Strategy: strategyFixed, TaskMemory: true},
		{Mode: "grounded", Strategy: strategyStructural},
	}
	saved, err := store.NewSession(ctx, lanes, generation, retrieval, 8)
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (&ChatService{Engine: &Engine{Store: store}}).PrepareTurn(ctx, saved.ID, "old conversation question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&ChatService{Engine: &Engine{Store: store}}).CommitTurn(ctx, prep, chatTestResults(prep, [2]TaskState{})); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveChatResumeConfig(
		saved,
		map[string]bool{"raw-turns": true},
		GenerationSettings{Provider: providerGroq, MainModel: defaultMainModel, AuxModel: defaultAuxModel, Temperature: 0, MaxTokens: 2048},
		RetrievalSettings{CandidateK: 10, ContextK: 3, Threshold: 0.60, Calibrated: true, Calibration: "week-05/calibration.json"},
		3, "grounded-memory", strategyStructural,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RawTurns != 3 {
		t.Errorf("resolved raw-turns = %d, want explicit value 3", resolved.RawTurns)
	}
	if resolved.Generation != generation {
		t.Errorf("raw-turns-only resume changed saved generation: got %#v, want %#v", resolved.Generation, generation)
	}
	if resolved.Retrieval != retrieval {
		t.Errorf("raw-turns-only resume changed saved retrieval: got %#v, want %#v", resolved.Retrieval, retrieval)
	}
	if resolved.Lanes != lanes {
		t.Errorf("raw-turns-only resume changed saved lanes: got %#v, want %#v", resolved.Lanes, lanes)
	}
	created, err := store.NewSession(ctx, resolved.Lanes, resolved.Generation, resolved.Retrieval, resolved.RawTurns)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == saved.ID || created.RawTurns != 3 {
		t.Fatalf("clean session = %#v, want distinct session with raw-turns 3", created)
	}
	newTurns, err := store.LoadTurns(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(newTurns) != 0 {
		t.Fatalf("new session inherited %d old turns, want clean history", len(newTurns))
	}
	oldTurns, err := store.LoadTurns(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldTurns) != 1 || oldTurns[0].Question != "old conversation question" {
		t.Fatalf("original saved history = %#v, want its original turn untouched", oldTurns)
	}
	original, err := store.LoadSession(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if original.RawTurns != 8 || original.Generation != generation || original.Retrieval != retrieval || original.Lanes != lanes {
		t.Fatalf("original session settings changed: %#v", original)
	}
}

func TestOpenSessionConfigDrivesFollowingIsolatedExperiment(t *testing.T) {
	ctx := context.Background()
	store := chatTestStore(t)
	provider := newCompletionRecorder(t, `{"status":"insufficient_context","answer":"Not enough evidence.","citations":[]}`)
	oldProvider := newCompletionRecorder(t, `{"status":"insufficient_context","answer":"Not enough evidence.","citations":[]}`)
	t.Setenv("DEEPSEEK_API_KEY", "synthetic-opened-key")
	generation := GenerationSettings{
		Provider: providerDeepSeek, Endpoint: provider.URL,
		MainModel: "opened-main", AuxModel: "opened-aux", Temperature: 0.6, MaxTokens: 77,
	}
	retrieval := RetrievalSettings{CandidateK: 4, ContextK: 2, Threshold: 0.1, Calibrated: false, Calibration: "opened retrieval"}
	lanes := [2]LaneSettings{
		{Mode: "grounded", Strategy: strategyFixed},
		{Mode: "grounded", Strategy: strategyStructural},
	}
	saved, err := store.NewSession(ctx, lanes, generation, retrieval, 4)
	if err != nil {
		t.Fatal(err)
	}
	service := &ChatService{Engine: &Engine{Store: store}}
	prep, err := service.PrepareTurn(ctx, saved.ID, "old saved question")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitTurn(ctx, prep, chatTestResults(prep, [2]TaskState{})); err != nil {
		t.Fatal(err)
	}

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"test-embedding:latest","digest":"test-digest"}]}`))
		case "/api/embed":
			_, _ = w.Write([]byte(`{"embeddings":[[1,0]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollamaServer.Close()
	fingerprint := EmbeddingFingerprint{Model: "test-embedding:latest", Digest: "test-digest", Preparation: embeddingPreparation("test-embedding"), Dimensions: 2}
	if _, err := store.BuildIndex(ctx, testStoreSnapshot(), ChunkConfig{Size: 2}, fingerprint, testEmbeddings); err != nil {
		t.Fatal(err)
	}

	engine := &Engine{
		Store:     store,
		Ollama:    OllamaClient{Endpoint: ollamaServer.URL, Model: "test-embedding", Dimensions: 2, HTTP: ollamaServer.Client()},
		Groq:      GroqClient{Provider: providerGroq, Endpoint: oldProvider.URL, APIKey: "synthetic-old-key", HTTP: oldProvider.server.Client()},
		Retrieval: RetrievalSettings{CandidateK: 1, ContextK: 1, Threshold: 1},
	}
	app, err := newChatApp(ctx, func() {}, store, engine, 6, ChatSession{}, lanes, false, GenerationSettings{
		Provider: providerGroq, Endpoint: oldProvider.URL, MainModel: "old-main", AuxModel: "old-aux", Temperature: 0, MaxTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.command("/open " + saved.ID)
	if app.session.ID != saved.ID || app.experiment {
		t.Fatalf("opened session state = session %q, experiment %t; want saved session active", app.session.ID, app.experiment)
	}
	if app.engine.Retrieval != retrieval || app.rawTurns != saved.RawTurns {
		t.Fatalf("opened retrieval/raw-turns = %#v/%d, want %#v/%d", app.engine.Retrieval, app.rawTurns, retrieval, saved.RawTurns)
	}
	if oldCalls := len(oldProvider.requests()); oldCalls != 0 {
		t.Fatalf("old provider received %d calls while opening session", oldCalls)
	}
	for range 3 {
		_, _ = app.Update(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	}
	if !app.experiment || app.pair != "baseline-rag" {
		t.Fatalf("Ctrl+P after open selected pair %q, experiment=%t; want isolated baseline-rag", app.pair, app.experiment)
	}
	app.input.SetValue("new experiment question")
	savedExperiment := submitAndComplete(t, app)
	if savedExperiment.err != nil {
		t.Fatalf("isolated experiment failed to save: %v", savedExperiment.err)
	}
	if len(provider.requests()) != 2 {
		t.Fatalf("opened provider received %d requests, want both experiment lanes", len(provider.requests()))
	}
	for i, request := range provider.requests() {
		if request.Model != generation.MainModel {
			t.Errorf("experiment lane %d requested model %q, want opened model %q", i+1, request.Model, generation.MainModel)
		}
		if request.Authorization != "Bearer synthetic-opened-key" {
			t.Errorf("experiment lane %d used authorization %q, want opened provider key", i+1, request.Authorization)
		}
		if request.hasMessage("old saved question") {
			t.Errorf("isolated experiment lane %d received prior session history", i+1)
		}
		if !request.hasMessage("new experiment question") {
			t.Errorf("experiment lane %d omitted its submitted question", i+1)
		}
	}
	if len(oldProvider.requests()) != 0 {
		t.Fatalf("old provider received %d calls after opening DeepSeek session", len(oldProvider.requests()))
	}
	run, err := store.LoadRun(ctx, savedExperiment.runID)
	if err != nil {
		t.Fatal(err)
	}
	for lane := range run.Lanes {
		if run.Lanes[lane].Generation != generation {
			t.Errorf("persisted experiment lane %d generation = %#v, want opened settings %#v", lane+1, run.Lanes[lane].Generation, generation)
		}
	}
	originalTurns, err := store.LoadTurns(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(originalTurns) != 1 || originalTurns[0].Question != "old saved question" {
		t.Fatalf("opening and experimenting changed saved-session history: %#v", originalTurns)
	}
}

type completionRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
	Authorization string `json:"-"`
}

func (r completionRequest) hasMessage(needle string) bool {
	for _, message := range r.Messages {
		if strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

type completionRecorder struct {
	URL      string
	server   *httptest.Server
	mu       sync.Mutex
	captured []completionRequest
}

func newCompletionRecorder(t *testing.T, content string) *completionRecorder {
	t.Helper()
	recorder := &completionRecorder{}
	encodedContent, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		request.Authorization = r.Header.Get("Authorization")
		recorder.mu.Lock()
		recorder.captured = append(recorder.captured, request)
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":` + string(encodedContent) + `}}]}`))
	}))
	t.Cleanup(recorder.server.Close)
	recorder.URL = recorder.server.URL
	return recorder
}

func (r *completionRecorder) requests() []completionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]completionRequest(nil), r.captured...)
}

func submitAndComplete(t *testing.T, app *chatApp) chatPairSavedMessage {
	t.Helper()
	_, command := app.submit()
	if command == nil {
		t.Fatal("submitting a nonempty question returned no command")
	}
	batchValue := command()
	batch, ok := batchValue.(tea.BatchMsg)
	if !ok {
		t.Fatalf("submit command result = %T, want lane command batch", batchValue)
	}
	var saved chatPairSavedMessage
	for _, laneCommand := range batch {
		laneValue := laneCommand()
		message, ok := laneValue.(chatLaneMessage)
		if !ok {
			t.Fatalf("lane command result = %T, want chat lane result", laneValue)
		}
		if message.err != nil {
			t.Fatalf("lane completion failed: %v", message.err)
		}
		_, persist := app.Update(message)
		if persist == nil {
			continue
		}
		persistedValue := persist()
		savedMessage, ok := persistedValue.(chatPairSavedMessage)
		if !ok {
			t.Fatalf("pair persistence result = %T, want saved-pair result", persistedValue)
		}
		saved = savedMessage
		app.Update(savedMessage)
	}
	if saved.runID == "" && saved.turn.ID == "" {
		t.Fatal("lane completions did not produce a persisted pair")
	}
	return saved
}
