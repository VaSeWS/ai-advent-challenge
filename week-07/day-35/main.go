package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/* schemas/* examples/*
var assets embed.FS

const maxRequestBytes = 1 << 20

type store struct {
	mu    sync.Mutex
	path  string
	state State
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	f := flag.NewFlagSet("day-35", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	addr := f.String("addr", "127.0.0.1:8035", "HTTP listen address")
	data := f.String("data", "data/state.json", "state JSON file")
	provider := f.String("provider", "groq", "AI provider: groq or deepseek")
	model := f.String("model", "", "provider model")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *provider != "groq" && *provider != "deepseek" {
		return errors.New("provider must be groq or deepseek")
	}
	if *model == "" {
		if *provider == "groq" {
			*model = "openai/gpt-oss-120b"
		} else {
			*model = "deepseek-chat"
		}
	}
	return serve(*addr, *data, *provider, *model)
}
func serve(addr, path, provider, model string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("server must bind to a loopback IP address")
	}
	s := &store{path: path}
	if err = s.load(); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/tournaments", s.createTournaments)
	mux.HandleFunc("GET /api/provider", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, providerInfo(provider, model)) })
	mux.HandleFunc("POST /api/chat", newChatHandler(provider, model))
	mux.HandleFunc("GET /schemas/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(r.PathValue("name"), ".json")
		if r.PathValue("name") != name+".json" || (name != "template" && name != "schedule" && name != "tournament") {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, assets, "schemas/"+name+".json")
	})
	web, err := fs.Sub(assets, "web")
	if err != nil {
		return fmt.Errorf("web assets: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(web)))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen failed: %w", err)
	}
	handler := requestGuard(ln.Addr(), mux)
	log.Printf("Tournament admin listening at http://%s", ln.Addr())
	return (&http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 95 * time.Second, IdleTimeout: 60 * time.Second}).Serve(ln)
}
func requestGuard(listenerAddr net.Addr, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !validRequestHost(r.Host, listenerAddr) {
			writeError(w, http.StatusForbidden, "loopback host required")
			return
		}
		if r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodDelete {
			if !sameOrigin(r) || !remoteLoopback(r.RemoteAddr) {
				writeError(w, http.StatusForbidden, "same-origin loopback request required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func validRequestHost(host string, listenerAddr net.Addr) bool {
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "80")
	}
	hostName, port, err := net.SplitHostPort(host)
	if err != nil || host != net.JoinHostPort(hostName, port) {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return false
	}
	listener, ok := listenerAddr.(*net.TCPAddr)
	if !ok || listener.Port != portNumber {
		return false
	}
	if strings.EqualFold(hostName, "localhost") {
		return true
	}
	hostIP := net.ParseIP(hostName)
	return hostIP != nil && listener.IP != nil && hostIP.IsLoopback() && hostIP.Equal(listener.IP)
}
func (s *store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = State{Config: defaultConfig(), Tournaments: []Tournament{}}
		return validateConfig(s.state.Config)
	}
	if err != nil {
		return err
	}
	var st State
	if err = decodeStrict(raw, &st); err != nil {
		return fmt.Errorf("invalid state JSON: %w", err)
	}
	if err = validateConfig(st.Config); err != nil {
		return fmt.Errorf("invalid persisted config: %w", err)
	}
	if st.Tournaments == nil {
		st.Tournaments = []Tournament{}
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, t := range st.Tournaments {
		if err = validateTournament(t); err != nil {
			return fmt.Errorf("invalid persisted tournament %s: %w", t.ID, err)
		}
		if t.ID == "" || ids[t.ID] || t.Status != "scheduled" || t.TemplateID != t.TemplateSnapshot.ID || t.ScheduleID != t.ScheduleSnapshot.ID || t.ScheduleSnapshot.TemplateID != t.TemplateSnapshot.ID || !t.ScheduleSnapshot.Enabled {
			return errors.New("invalid persisted tournament snapshot")
		}
		ids[t.ID] = true
		cfg := Config{Template: t.TemplateSnapshot, Schedule: t.ScheduleSnapshot}
		if err = validateConfig(cfg); err != nil {
			return fmt.Errorf("invalid persisted tournament %s: %w", t.ID, err)
		}
		occ, e := selectedOccurrence(t.ScheduleSnapshot, t.StartsAt)
		if e != nil || occ.StartsAt != t.StartsAt || occ.RegistrationOpensAt != t.RegistrationOpensAt || occ.RegistrationClosesAt != t.RegistrationClosesAt {
			return fmt.Errorf("invalid persisted tournament occurrence %s", t.ID)
		}
		start, e := time.Parse(time.RFC3339, t.StartsAt)
		if e != nil {
			return fmt.Errorf("invalid persisted tournament start: %w", e)
		}
		key := t.ScheduleID + "\x00" + start.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			return errors.New("duplicate persisted tournament occurrence")
		}
		seen[key] = true
	}
	s.state = st
	return nil
}
func (s *store) persist(next State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(name)
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	ok = true
	if dir, e := os.Open(filepath.Dir(s.path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func (s *store) getState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, s.state)
}
func (s *store) putConfig(w http.ResponseWriter, r *http.Request) {
	var c Config
	if !decodeRequest(w, r, &c) {
		return
	}
	if err := validateConfig(c); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state
	next.Config = c
	if err := s.persist(next); err != nil {
		writeError(w, 500, "could not persist state: "+err.Error())
		return
	}
	s.state = next
	writeJSON(w, s.state)
}
func (s *store) preview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config Config `json:"config"`
		Count  int    `json:"count"`
	}
	if !decodeRequest(w, r, &req) {
		return
	}
	if err := validateConfig(req.Config); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	items, err := scheduleOccurrences(req.Config.Schedule, req.Count)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"occurrences": items})
}
func (s *store) createTournaments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config   Config   `json:"config"`
		StartsAt []string `json:"starts_at"`
	}
	if !decodeRequest(w, r, &req) {
		return
	}
	if err := validateConfig(req.Config); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if !req.Config.Schedule.Enabled {
		writeError(w, 400, "schedule is disabled")
		return
	}
	if len(req.StartsAt) == 0 || len(req.StartsAt) > 50 {
		writeError(w, 400, "select between 1 and 50 occurrences")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := State{Config: req.Config, Tournaments: append([]Tournament(nil), s.state.Tournaments...)}
	seen := map[string]bool{}
	for _, t := range next.Tournaments {
		parsed, e := time.Parse(time.RFC3339, t.StartsAt)
		if e == nil {
			seen[t.ScheduleID+"\x00"+parsed.UTC().Format(time.RFC3339Nano)] = true
		}
	}
	for _, value := range req.StartsAt {
		occ, err := selectedOccurrence(req.Config.Schedule, value)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		start, _ := time.Parse(time.RFC3339, occ.StartsAt)
		key := req.Config.Schedule.ID + "\x00" + start.UTC().Format(time.RFC3339Nano)
		if seen[key] {
			writeError(w, 409, "tournament occurrence already exists")
			return
		}
		seen[key] = true
		id, err := newID()
		if err != nil {
			writeError(w, 500, "could not generate tournament id")
			return
		}
		template := cloneTemplate(req.Config.Template)
		schedule := req.Config.Schedule
		next.Tournaments = append(next.Tournaments, Tournament{ID: id, TemplateID: template.ID, ScheduleID: schedule.ID, StartsAt: occ.StartsAt, RegistrationOpensAt: occ.RegistrationOpensAt, RegistrationClosesAt: occ.RegistrationClosesAt, Status: "scheduled", TemplateSnapshot: template, ScheduleSnapshot: schedule})
	}
	if err := s.persist(next); err != nil {
		writeError(w, 500, "could not persist state: "+err.Error())
		return
	}
	s.state = next
	writeJSON(w, s.state)
}
func cloneTemplate(t Template) Template {
	t.Rounds = slices.Clone(t.Rounds)
	t.Rewards = slices.Clone(t.Rewards)
	return t
}
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}
func decodeRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writeError(w, 415, "Content-Type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 413, "request body too large or unreadable")
		return false
	}
	if err = decodeStrict(b, dst); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.User != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(u.Host, r.Host) && u.Scheme == scheme
}
func remoteLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]string{"error": message})
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
