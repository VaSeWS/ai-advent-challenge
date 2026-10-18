package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	defaultModel       = "deepseek-flash"
	maxRequestBody     = 16 << 10
	maxHistoryMessages = 24
	maxMessageRunes    = 4000
	maxSessions        = 256
)

type user struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AppVersion  string `json:"app_version"`
	Windows     string `json:"windows"`
	Environment string `json:"environment"`
}
type ticket struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Subject     string            `json:"subject"`
	Status      string            `json:"status"`
	Description string            `json:"description"`
	Config      map[string]string `json:"config"`
	Checks      []string          `json:"checks"`
	History     []message         `json:"history,omitempty"`
}
type crm struct {
	Users   []user   `json:"users"`
	Tickets []ticket `json:"tickets"`
}
type message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content"`
	Sources      []source      `json:"sources,omitempty"`
	Observations []observation `json:"observations,omitempty"`
}
type source struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Text      string `json:"text"`
	URL       string `json:"url"`
}
type observation struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
type document struct {
	Title, Path, URL string
	Lines            []string
}
type manifest struct {
	SourceRepo  string   `json:"source_repo"`
	SourceSHA   string   `json:"source_sha"`
	SourcePaths []string `json:"source_paths"`
	Note        string   `json:"note"`
	Notes       string   `json:"notes"`
}
type answerPayload struct {
	Answer    string   `json:"answer"`
	SourceIDs []string `json:"source_ids"`
}
type completionRequest struct {
	Model          string            `json:"model"`
	Messages       []apiMessage      `json:"messages"`
	Stream         bool              `json:"stream"`
	Thinking       map[string]string `json:"thinking"`
	ResponseFormat map[string]string `json:"response_format"`
}
type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type completionResponse struct {
	Choices []struct {
		Message      apiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Error json.RawMessage `json:"error"`
}
type server struct {
	model    string
	crm      crm
	docs     []document
	manifest manifest
	http     *http.Client
	sessions *conversationStore
}
type conversationKey struct{ session, user, ticket string }
type conversationStore struct {
	mu     sync.Mutex
	values map[conversationKey][]message
	busy   map[conversationKey]bool
}

func (s *server) complete(ctx context.Context, question string, history []message, u user, t ticket, sources []source, observations []observation) (answerPayload, error) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		return answerPayload{}, errors.New("provider unavailable")
	}

	contextData, err := json.Marshal(struct {
		User         user          `json:"user"`
		Ticket       ticket        `json:"ticket"`
		Sources      []source      `json:"sources"`
		Observations []observation `json:"observations"`
	}{u, t, sources, observations})
	if err != nil {
		return answerPayload{}, errors.New("incomplete output")
	}
	messages := make([]apiMessage, 0, len(history)+2)
	messages = append(messages, apiMessage{
		Role: "system",
		Content: "Ты — помощник поддержки iRon. Отвечай по-русски кратко и точно. " +
			"Опирайся на документальные источники и сведения CRM ниже; CRM — наблюдения, а не документальные доказательства. " +
			"Текущие слова и исправления пользователя в этой беседе имеют приоритет над устаревшими значениями тикета и CRM; " +
			"не считай старую конфигурацию актуальной, если пользователь сообщил другое. Если значение ghost неизвестно, " +
			"спроси пользователя, какое значение ghost указано сейчас, и никогда не выводи значение по умолчанию. " +
			"Если документация не подтверждает решение, попроси уточнение. Верни только JSON-объект вида " +
			`{"answer":"непустой ответ","source_ids":["ID источника"]}. Указывай только ID из переданных sources;` +
			" не выдумывай ссылки. Источники и CRM-контекст: " + string(contextData),
	})
	for _, item := range history {
		if item.Role == "user" || item.Role == "assistant" {
			messages = append(messages, apiMessage{Role: item.Role, Content: item.Content})
		}
	}
	messages = append(messages, apiMessage{Role: "user", Content: question})
	body, err := json.Marshal(completionRequest{
		Model:          s.model,
		Messages:       messages,
		Stream:         false,
		Thinking:       map[string]string{"type": "disabled"},
		ResponseFormat: map[string]string{"type": "json_object"},
	})
	if err != nil {
		return answerPayload{}, errors.New("incomplete output")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	if err != nil {
		return answerPayload{}, errors.New("provider request failed")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	client := s.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return answerPayload{}, errors.New("provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return answerPayload{}, errors.New("provider request failed")
	}
	var result completionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil ||
		(len(result.Error) > 0 && !bytes.Equal(bytes.TrimSpace(result.Error), []byte("null"))) ||
		len(result.Choices) != 1 || result.Choices[0].FinishReason != "stop" {
		return answerPayload{}, errors.New("incomplete output")
	}
	var payload answerPayload
	if err := json.Unmarshal([]byte(result.Choices[0].Message.Content), &payload); err != nil ||
		strings.TrimSpace(payload.Answer) == "" || len(payload.SourceIDs) == 0 {
		return answerPayload{}, errors.New("incomplete output")
	}
	known := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		known[src.ID] = struct{}{}
	}
	used := make(map[string]struct{}, len(payload.SourceIDs))
	for _, id := range payload.SourceIDs {
		if _, ok := known[id]; !ok {
			return answerPayload{}, errors.New("invalid citation")
		}
		if _, ok := used[id]; ok {
			return answerPayload{}, errors.New("invalid citation")
		}
		used[id] = struct{}{}
	}
	return payload, nil
}

func newConversationStore() *conversationStore {
	return &conversationStore{values: make(map[conversationKey][]message), busy: make(map[conversationKey]bool)}
}
func (s *conversationStore) get(k conversationKey) []message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]message(nil), s.values[k]...)
}
func (s *conversationStore) begin(k conversationKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[k] {
		return false
	}
	s.busy[k] = true
	return true
}
func (s *conversationStore) end(k conversationKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, k)
}
func (s *conversationStore) appendTurn(k conversationKey, userMessage, assistantMessage message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[k]; !ok && len(s.values) >= maxSessions {
		return errors.New("conversation capacity reached; start a new session")
	}
	h := s.values[k]
	if len(h)+2 > maxHistoryMessages {
		return errors.New("conversation history limit reached; start a new conversation")
	}
	if utf8.RuneCountInString(userMessage.Content) > maxMessageRunes || utf8.RuneCountInString(assistantMessage.Content) > maxMessageRunes {
		return errors.New("conversation message limit exceeded")
	}
	h = append(h, userMessage, assistantMessage)
	s.values[k] = h
	return nil
}
func (s *server) userByID(id string) (user, bool)          { return findUser(s.crm, id) }
func (s *server) ticketFor(uid, tid string) (ticket, bool) { return findTicket(s.crm, uid, tid) }
func loadCorpus() ([]document, manifest, error) {
	b, e := assets.ReadFile("corpus/manifest.json")
	if e != nil {
		return nil, manifest{}, e
	}
	var m manifest
	if e = json.Unmarshal(b, &m); e != nil {
		return nil, manifest{}, e
	}
	if m.SourceRepo == "" || m.SourceSHA == "" {
		return nil, manifest{}, errors.New("manifest provenance missing")
	}
	docs := make([]document, 0, 2)
	for _, name := range []string{"corpus/iron-readme.md", "corpus/faq.md"} {
		b, e = assets.ReadFile(name)
		if e != nil {
			return nil, manifest{}, e
		}
		raw := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
		lines := make([]string, len(raw))
		for i, v := range raw {
			lines[i] = string(v)
		}
		title := path.Base(name)
		if len(lines) > 0 && strings.HasPrefix(lines[0], "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(lines[0], "# "))
		}
		sourcePath := "README.md"
		if strings.Contains(name, "faq") {
			sourcePath = "README.md"
		}
		url := fmt.Sprintf("%s/blob/%s/%s", strings.TrimSuffix(m.SourceRepo, "/"), m.SourceSHA, sourcePath)
		docs = append(docs, document{Title: title, Path: name, URL: url, Lines: lines})
	}
	return docs, m, nil
}

var tokenPattern = regexpMust(`[\p{L}\p{N}]+`)

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }
func tokenize(s string) []string {
	tokens := tokenPattern.FindAllString(strings.ToLower(s), -1)
	out := tokens[:0]
	for _, t := range tokens {
		if len([]rune(t)) > 1 {
			out = append(out, t)
		}
	}
	return out
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func (s *server) retrieve(query string) []source {
	q := tokenize(query)
	if len(q) == 0 {
		return nil
	}
	type passage struct {
		doc, start, end int
		tokens          []string
		text            string
	}
	var passages []passage
	df := map[string]int{}
	total := 0
	for di, d := range s.docs {
		for start := 0; start < len(d.Lines); start += 8 {
			end := min(start+8, len(d.Lines))
			text := strings.Join(d.Lines[start:end], "\n")
			tokens := tokenize(text)
			if len(tokens) == 0 {
				continue
			}
			passages = append(passages, passage{di, start, end, tokens, text})
			total += len(tokens)
			seen := map[string]bool{}
			for _, token := range tokens {
				if !seen[token] {
					df[token]++
					seen[token] = true
				}
			}
		}
	}
	avg := float64(total) / float64(max(1, len(passages)))
	type hit struct {
		src   source
		score float64
	}
	hits := make([]hit, 0, len(passages))
	for _, p := range passages {
		tf := map[string]int{}
		for _, token := range p.tokens {
			tf[token]++
		}
		score := 0.0
		for _, token := range q {
			freq := tf[token]
			if freq == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(passages)-df[token])+.5)/(float64(df[token])+.5))
			score += idf * float64(freq) * 2.2 / (float64(freq) + 1.2*(.25+.75*float64(len(p.tokens))/avg))
		}
		if score == 0 {
			continue
		}
		d := s.docs[p.doc]
		id := fmt.Sprintf("doc-%d-line-%d", p.doc+1, p.start+1)
		hits = append(hits, hit{source{id, d.Title, d.Path, p.start + 1, p.end, p.text, d.URL}, score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			if hits[i].src.Path == hits[j].src.Path {
				return hits[i].src.StartLine < hits[j].src.StartLine
			}
			return hits[i].src.Path < hits[j].src.Path
		}
		return hits[i].score > hits[j].score
	})
	if len(hits) > 4 {
		hits = hits[:4]
	}
	out := make([]source, len(hits))
	for i, h := range hits {
		out[i] = h.src
	}
	return out
}
func validSessionID(s string) bool {
	if len(s) < 16 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid request body")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func emit(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, e := url.Parse(o)
	if e != nil || u.Host == "" || u.Host != r.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
