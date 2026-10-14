package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	requestBodyLimit    = 64 << 10
	maxMessages         = 32
	maxMessageBytes     = 8 << 10
	defaultOutputTokens = 1024
	maxOutputTokens     = 2048
	rateWindow          = time.Minute
	rateRequests        = 3
	maxRateClients      = 2048
	generationTimeout   = 30 * time.Minute
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens *int          `json:"max_tokens"`
}
type chatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
}
type rateEntry struct {
	started time.Time
	count   int
}
type service struct {
	cfg          config
	client       *http.Client
	mu           sync.Mutex
	jobActive    bool
	jobUncertain bool
	rates        map[string]rateEntry
}

func newService(cfg config) *service {
	return &service{cfg: cfg, client: &http.Client{Timeout: generationTimeout, CheckRedirect: rejectRedirects}, rates: make(map[string]rateEntry)}
}

func (s *service) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "service-running", "backend": "loopback MLX", "model": pinnedModelID, "revision": pinnedRevision})
}

func (s *service) limits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"model": pinnedModelID, "revision": pinnedRevision, "thinking": false,
		"backend_context_limit_tokens": contextLimit, "context_enforcement": "exact MLX tokenizer and configured KV limit; backend authoritative",
		"request_body_bytes": requestBodyLimit, "messages_max": maxMessages, "message_bytes_max": maxMessageBytes,
		"output_tokens_default": defaultOutputTokens, "output_tokens_max": maxOutputTokens,
		"simultaneous_generations": 1, "rate_limit_requests": rateRequests, "rate_limit_window_seconds": int(rateWindow.Seconds()),
		"transport": "plain HTTP; trusted private LAN only",
	})
}

func (s *service) chatCompletions(w http.ResponseWriter, r *http.Request) {
	if !bearerMatches(r.Header.Get("Authorization"), s.cfg.serviceToken) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	clientKey, ok := requestHost(r.RemoteAddr)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid remote address")
		return
	}
	if retry := s.allowRequest(clientKey, time.Now()); retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()+0.999))))
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}
	var request chatRequest
	if err := decodeJSONBody(w, r, requestBodyLimit, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid request body")
		}
		return
	}
	if err := validateChatRequest(request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	outputTokens := defaultOutputTokens
	if request.MaxTokens != nil {
		outputTokens = *request.MaxTokens
	}
	if !s.acquireJob() {
		writeError(w, http.StatusServiceUnavailable, "a generation is active or backend completion is unconfirmed")
		return
	}
	// MLX-VLM does not expose an acknowledgment when it stops a canceled GPU
	// request. Keep the bounded generation alive after a downstream disconnect
	// and drain its stream through [DONE] before releasing admission.
	backendCtx, cancel := context.WithTimeout(context.Background(), generationTimeout)
	result, err := s.generate(backendCtx, request.Messages, outputTokens)
	cancel()
	if err != nil {
		var backendError *backendFailure
		if errors.As(err, &backendError) && backendError.terminal {
			s.releaseJob()
			if r.Context().Err() == nil {
				writeError(w, backendError.status, backendError.message)
			}
			return
		}
		// Timeout, transport loss, malformed stream, or absent [DONE] cannot
		// prove that MLX stopped. Require operator reconciliation and restart.
		s.markUncertain()
		if r.Context().Err() == nil {
			writeError(w, http.StatusBadGateway, "backend completion could not be confirmed; inspect MLX and restart this service only after confirming it is idle")
		}
		return
	}
	s.releaseJob()
	if r.Context().Err() != nil {
		return
	}
	response := chatResponse{ID: result.id, Object: "chat.completion", Model: "local"}
	response.Choices = make([]struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	}, 1)
	response.Choices[0].Message = chatMessage{Role: "assistant", Content: result.content}
	response.Choices[0].FinishReason = result.finishReason
	writeJSON(w, http.StatusOK, response)
}

func validateChatRequest(request chatRequest) error {
	if request.Model != "" && request.Model != "local" {
		return errors.New("model must be omitted or the local alias")
	}
	if len(request.Messages) == 0 || len(request.Messages) > maxMessages {
		return errors.New("messages must contain 1 to 32 entries")
	}
	userMessage := false
	for _, message := range request.Messages {
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return errors.New("message role must be system, user, or assistant")
		}
		if strings.TrimSpace(message.Content) == "" || len(message.Content) > maxMessageBytes {
			return errors.New("message content must be non-empty and at most 8192 UTF-8 bytes")
		}
		if message.Role == "user" {
			userMessage = true
		}
	}
	if !userMessage {
		return errors.New("at least one user message is required")
	}
	if request.MaxTokens != nil && (*request.MaxTokens < 1 || *request.MaxTokens > maxOutputTokens) {
		return errors.New("max_tokens must be between 1 and 2048")
	}
	return nil
}

func (s *service) allowRequest(host string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.rates {
		if now.Sub(entry.started) >= rateWindow {
			delete(s.rates, key)
		}
	}
	entry, exists := s.rates[host]
	if !exists {
		if len(s.rates) >= maxRateClients {
			return rateWindow
		}
		s.rates[host] = rateEntry{started: now, count: 1}
		return 0
	}
	if entry.count >= rateRequests {
		return rateWindow - now.Sub(entry.started)
	}
	entry.count++
	s.rates[host] = entry
	return 0
}

func requestHost(remoteAddr string) (string, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "", false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false
	}
	return ip.String(), true
}

func (s *service) acquireJob() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobActive || s.jobUncertain {
		return false
	}
	s.jobActive = true
	return true
}
func (s *service) releaseJob() { s.mu.Lock(); s.jobActive = false; s.mu.Unlock() }
func (s *service) markUncertain() {
	s.mu.Lock()
	s.jobUncertain = true
	s.jobActive = true
	s.mu.Unlock()
}

type backendFailure struct {
	status   int
	message  string
	terminal bool
}

func (e *backendFailure) Error() string { return e.message }

type generationResult struct {
	content      string
	id           string
	finishReason string
}

func (s *service) generate(ctx context.Context, messages []chatMessage, maxTokens int) (generationResult, error) {
	payload := struct {
		Model          string        `json:"model"`
		Messages       []chatMessage `json:"messages"`
		MaxTokens      int           `json:"max_tokens"`
		Stream         bool          `json:"stream"`
		EnableThinking bool          `json:"enable_thinking"`
	}{s.cfg.backendModel, messages, maxTokens, true, false}
	body, err := json.Marshal(payload)
	if err != nil {
		return generationResult{}, errors.New("encode backend request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, backendURL, bytes.NewReader(body))
	if err != nil {
		return generationResult{}, errors.New("create backend request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return generationResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		status, message := http.StatusBadGateway, "backend rejected the generation request"
		if response.StatusCode == http.StatusBadRequest {
			status, message = http.StatusBadRequest, "backend context or request limit exceeded"
		}
		return generationResult{}, &backendFailure{status: status, message: message, terminal: response.StatusCode == http.StatusBadRequest}
	}
	reader := bufio.NewScanner(response.Body)
	const maxSSEEventBytes = 1 << 20
	reader.Buffer(make([]byte, 4096), maxSSEEventBytes)
	var content strings.Builder
	var result generationResult
	var eventData strings.Builder
	eventType := ""
	eventBytes := 0
	hasData := false
	finished := false
	terminalReasonSeen := false
	dispatch := func() error {
		if eventType == "error" {
			return errors.New("backend stream reported an error event")
		}
		if !hasData {
			eventType = ""
			eventBytes = 0
			return nil
		}
		data := eventData.String()
		eventData.Reset()
		eventType = ""
		eventBytes = 0
		hasData = false
		if data == "[DONE]" {
			if result.id == "" || !terminalReasonSeen ||
				(result.finishReason != "stop" && result.finishReason != "length") {
				return errors.New("backend completion ended before a valid terminal choice")
			}
			finished = true
			return nil
		}
		var chunk struct {
			ID      string          `json:"id"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index *int `json:"index"`
				Delta *struct {
					Content      string          `json:"content"`
					ToolCalls    json.RawMessage `json:"tool_calls"`
					FunctionCall json.RawMessage `json:"function_call"`
				} `json:"delta"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FinishReason *string         `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return errors.New("invalid backend stream")
		}
		if len(chunk.Error) != 0 && string(chunk.Error) != "null" {
			return errors.New("backend stream reported an error")
		}
		if chunk.ID == "" || (result.id != "" && chunk.ID != result.id) {
			return errors.New("backend completion identity missing or inconsistent")
		}
		if result.id == "" {
			result.id = chunk.ID
		}
		if terminalReasonSeen || len(chunk.Choices) != 1 ||
			chunk.Choices[0].Index == nil || *chunk.Choices[0].Index != 0 {
			return errors.New("backend stream did not contain one intended text choice")
		}
		choice := chunk.Choices[0]
		if choice.Delta == nil {
			return errors.New("backend stream did not contain a text delta")
		}
		if (len(choice.Delta.ToolCalls) != 0 && string(choice.Delta.ToolCalls) != "null") ||
			(len(choice.Delta.FunctionCall) != 0 && string(choice.Delta.FunctionCall) != "null") {
			return errors.New("backend returned a non-text completion")
		}
		if len(choice.ToolCalls) != 0 && string(choice.ToolCalls) != "null" {
			return errors.New("backend returned a non-text completion")
		}
		content.WriteString(choice.Delta.Content)
		if choice.FinishReason != nil {
			result.finishReason = *choice.FinishReason
			terminalReasonSeen = true
		}
		return nil
	}
	for reader.Scan() {
		line := strings.TrimSuffix(reader.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return generationResult{}, err
			}
			if finished {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, hasColon := strings.Cut(line, ":")
		if hasColon {
			value = strings.TrimPrefix(value, " ")
		}
		eventBytes += len(line) + 1
		if eventBytes > maxSSEEventBytes {
			return generationResult{}, errors.New("backend SSE event exceeded size limit")
		}
		switch field {
		case "event":
			eventType = value
			if eventType == "error" {
				return generationResult{}, errors.New("backend stream reported an error event")
			}
		case "data":
			if hasData {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(value)
			hasData = true
		}
	}
	if err := reader.Err(); err != nil {
		return generationResult{}, err
	}
	if eventBytes != 0 || hasData || eventType != "" {
		return generationResult{}, errors.New("backend stream ended with an incomplete SSE event")
	}
	if !finished {
		return generationResult{}, errors.New("backend stream ended before completion")
	}
	result.content = content.String()
	if result.id == "" {
		return generationResult{}, errors.New("backend completion identity missing")
	}
	if !terminalReasonSeen {
		return generationResult{}, errors.New("backend finish reason missing")
	}
	if result.finishReason != "stop" && result.finishReason != "length" {
		return generationResult{}, errors.New("backend returned an unsupported finish reason")
	}
	if result.content == "" {
		return generationResult{}, &backendFailure{status: http.StatusBadGateway, message: "backend returned an empty completion", terminal: true}
	}
	return result, nil
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message, "type": "service_error"}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func checkBackendHealth(ctx context.Context, modelPath string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/health", nil)
	if err != nil {
		return errors.New("create MLX health request")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: rejectRedirects}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("MLX is not reachable at 127.0.0.1:8080")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("MLX health check failed")
	}
	var health struct {
		Status           string `json:"status"`
		LoadedModel      string `json:"loaded_model"`
		EffectiveContext int    `json:"effective_context_limit"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&health); err != nil {
		return errors.New("MLX health response is invalid")
	}
	if health.Status != "healthy" || health.LoadedModel != modelPath || health.EffectiveContext != contextLimit {
		return fmt.Errorf("MLX must load the pinned snapshot with effective context limit %d and thinking enabled", contextLimit)
	}
	return nil
}
