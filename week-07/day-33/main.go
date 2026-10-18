package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed web/* data/crm.json corpus/*
var assets embed.FS

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) > 0 && args[0] == "mcp" {
		f := flag.NewFlagSet("mcp", flag.ContinueOnError)
		f.SetOutput(os.Stderr)
		userID := f.String("user", "", "bound CRM user")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *userID == "" {
			return errors.New("usage: day-33 mcp -user USER_ID")
		}
		raw, err := assets.ReadFile("data/crm.json")
		if err != nil {
			return errors.New("CRM data unavailable")
		}
		var data crm
		if json.Unmarshal(raw, &data) != nil {
			return errors.New("CRM data unavailable")
		}
		return runMCP(*userID, os.Stdin, os.Stdout, data)
	}
	f := flag.NewFlagSet("day-33", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	addr := f.String("addr", "127.0.0.1:8033", "HTTP listen address")
	model := f.String("model", defaultModel, "DeepSeek model")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return serve(*addr, *model)
}
func serve(addr, model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model must not be empty")
	}
	raw, err := assets.ReadFile("data/crm.json")
	if err != nil {
		return errors.New("CRM data unavailable")
	}
	var data crm
	if err = json.Unmarshal(raw, &data); err != nil {
		return errors.New("CRM data unavailable")
	}
	docs, meta, err := loadCorpus()
	if err != nil {
		return errors.New("support corpus unavailable")
	}
	s := &server{model: model, crm: data, docs: docs, manifest: meta, http: &http.Client{Timeout: 40 * time.Second}, sessions: newConversationStore()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/tickets", s.tickets)
	mux.HandleFunc("GET /api/ticket", s.ticketDetails)
	mux.HandleFunc("POST /api/chat", s.chat)
	web, err := fs.Sub(assets, "web")
	if err != nil {
		return errors.New("web assets unavailable")
	}
	mux.Handle("/", http.FileServer(http.FS(web)))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method == http.MethodPost && (!originAllowed(r) || !remoteLoopback(r.RemoteAddr)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen failed: %w", err)
	}
	log.Printf("iRon support listening at http://%s (synthetic CRM)", ln.Addr())
	return (&http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second}).Serve(ln)
}
func remoteLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (s *server) bootstrap(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"users": s.crm.Users, "model": s.model, "synthetic": true})
}
func (s *server) tickets(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("user_id")
	if _, ok := s.userByID(id); !ok {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	out := make([]ticket, 0)
	for _, t := range s.crm.Tickets {
		if t.UserID == id {
			out = append(out, t)
		}
	}
	writeJSON(w, map[string]any{"tickets": out})
}
func (s *server) ticketDetails(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("X-Session-ID")
	if !validSessionID(sid) {
		http.Error(w, "Invalid session", http.StatusBadRequest)
		return
	}
	uid, tid := r.URL.Query().Get("user_id"), r.URL.Query().Get("ticket_id")
	u, ok := s.userByID(uid)
	if !ok {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	t, ok := s.ticketFor(uid, tid)
	if !ok {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}
	key := conversationKey{sid, uid, tid}
	if err := s.seedHistory(key, t.History); err != nil {
		http.Error(w, "Conversation capacity reached", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"user": u, "ticket": t, "messages": s.sessions.get(key)})
}
func (s *server) seedHistory(key conversationKey, history []message) error {
	if len(history) > maxHistoryMessages {
		return errors.New("conversation history limit reached")
	}
	for _, m := range history {
		if len([]rune(m.Content)) > maxMessageRunes {
			return errors.New("conversation message limit exceeded")
		}
	}
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	if _, exists := s.sessions.values[key]; !exists {
		if len(s.sessions.values) >= maxSessions {
			return errors.New("conversation capacity reached")
		}
		s.sessions.values[key] = append([]message(nil), history...)
	}
	return nil
}
func sendEvent(w http.ResponseWriter, v any) error {
	if err := emit(w, v); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	sid := r.Header.Get("X-Session-ID")
	if !validSessionID(sid) {
		http.Error(w, "Invalid session", http.StatusBadRequest)
		return
	}
	var in struct {
		UserID   string `json:"user_id"`
		TicketID string `json:"ticket_id"`
		Message  string `json:"message"`
	}
	if readJSON(w, r, &in) != nil || len([]rune(strings.TrimSpace(in.Message))) == 0 || len([]rune(in.Message)) > maxMessageRunes {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	_, ok := s.userByID(in.UserID)
	if !ok {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	t, ok := s.ticketFor(in.UserID, in.TicketID)
	if !ok {
		http.Error(w, "Ticket not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	send := func(v any) bool { return sendEvent(w, v) == nil }
	key := conversationKey{sid, in.UserID, in.TicketID}
	if !s.sessions.begin(key) {
		http.Error(w, "Conversation already processing", http.StatusConflict)
		return
	}
	defer s.sessions.end(key)
	if err := s.seedHistory(key, t.History); err != nil {
		send(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	history := s.sessions.get(key)
	if len(history)+2 > maxHistoryMessages {
		send(map[string]string{"type": "error", "error": "Лимит истории достигнут. Начните новую сессию."})
		return
	}
	if !send(map[string]string{"type": "stage", "stage": "mcp"}) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	gotUser, gotTicket, err := s.mcpContext(ctx, in.UserID, in.TicketID)
	if err != nil {
		send(map[string]string{"type": "error", "error": "Не удалось получить контекст CRM. Попробуйте позже."})
		return
	}
	obs := []observation{{"Пользователь", gotUser.Name}, {"Версия приложения", gotUser.AppVersion}, {"Windows", gotUser.Windows}, {"Окружение", gotUser.Environment}, {"Статус заявки", gotTicket.Status}}
	for k, v := range gotTicket.Config {
		obs = append(obs, observation{"Конфигурация · " + k, v})
	}
	for i, v := range gotTicket.Checks {
		obs = append(obs, observation{fmt.Sprintf("Проверка %d", i+1), v})
	}
	if !send(map[string]string{"type": "stage", "stage": "retrieval"}) {
		return
	}
	query := in.Message
	for i := len(history) - 1; i >= 0 && i >= len(history)-4; i-- {
		query += " " + history[i].Content
	}
	sources := s.retrieve(query)
	if len(sources) == 0 {
		send(map[string]string{"type": "error", "error": "В локальной документации нет подходящего подтверждения. Уточните вопрос."})
		return
	}
	if !send(map[string]string{"type": "stage", "stage": "deepseek"}) {
		return
	}
	payload, err := s.complete(ctx, in.Message, history, gotUser, gotTicket, sources, obs)
	if err != nil {
		safe := "Не удалось получить полный ответ от модели."
		if err.Error() == "provider unavailable" {
			safe = "Модель недоступна: проверьте DEEPSEEK_API_KEY."
		}
		send(map[string]string{"type": "error", "error": safe})
		return
	}
	byID := make(map[string]source, len(sources))
	for _, src := range sources {
		byID[src.ID] = src
	}
	citations := make([]source, 0, len(payload.SourceIDs))
	for _, id := range payload.SourceIDs {
		src, ok := byID[id]
		if !ok {
			send(map[string]string{"type": "error", "error": "Модель вернула неподтверждённую ссылку."})
			return
		}
		citations = append(citations, src)
	}
	if r.Context().Err() != nil {
		return
	}
	if len(citations) == 0 {
		send(map[string]string{"type": "error", "error": "Модель не вернула документальное подтверждение."})
		return
	}
	userMessage := message{Role: "user", Content: in.Message}
	assistantMessage := message{Role: "assistant", Content: payload.Answer, Sources: citations, Observations: obs}
	if err = s.sessions.appendTurn(key, userMessage, assistantMessage); err != nil {
		send(map[string]string{"type": "error", "error": "Лимит истории достигнут. Начните новую сессию."})
		return
	}
	if !send(map[string]any{"type": "answer", "answer": payload.Answer, "sources": citations, "observations": obs}) {
		s.rollbackTurn(key, in.Message)
	}
}
func (s *server) rollbackTurn(key conversationKey, text string) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	v := s.sessions.values[key]
	if len(v) >= 2 && v[len(v)-2].Role == "user" && v[len(v)-2].Content == text && v[len(v)-1].Role == "assistant" {
		s.sessions.values[key] = v[:len(v)-2]
	}
}
