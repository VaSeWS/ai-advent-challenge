package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRequestGuardRejectsUntrustedHosts(t *testing.T) {
	listener := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8035}
	routed := false
	handler := requestGuard(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		routed = true
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, host := range []string{
		"attacker.example:8035",
		"user@localhost:8035",
		"127.0.0.1:8036",
		"[127.0.0.1]:8035",
		"localhost:invalid",
	} {
		t.Run(host, func(t *testing.T) {
			routed = false
			r := httptest.NewRequest(http.MethodPost, "/api/config", nil)
			r.Host = host
			r.Header.Set("Origin", "http://"+host)
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
			}
			if routed {
				t.Fatal("untrusted host reached the route")
			}
		})
	}
}

func TestRequestGuardAllowsLoopbackHosts(t *testing.T) {
	listener := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8035}
	handler := requestGuard(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, host := range []string{"localhost:8035", "127.0.0.1:8035"} {
		t.Run(host, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://"+host+"/api/config", nil)
			r.Host = host
			r.Header.Set("Origin", "http://"+host)
			r.RemoteAddr = "127.0.0.1:1234"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d, body %s", w.Code, http.StatusNoContent, w.Body)
			}
		})
	}
}

func TestManualCreationPersistsSnapshotsAndRejectsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &store{path: path, state: State{Config: defaultConfig(), Tournaments: []Tournament{}}}
	c := defaultConfig()
	body, err := json.Marshal(map[string]any{"config": c, "starts_at": []string{"2026-10-19T20:00:00+03:00"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/tournaments", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.createTournaments(w, r)
		return w
	}
	if w := request(); w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body %s", w.Code, w.Body)
	}
	if w := request(); w.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want %d", w.Code, http.StatusConflict)
	}
	c.Template.Name = "Edited after creation"
	c.Template.Rounds[0].Name = "Edited round"
	c.Template.Rewards[0].Amount = 999
	c.Schedule.RegistrationOpenOffsetSeconds = -172800
	c.Schedule.RegistrationCloseOffsetSeconds = -7200
	update, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(update))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.putConfig(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("config update status = %d, body %s", w.Code, w.Body)
	}
	loaded := &store{path: path}
	if err = loaded.load(); err != nil {
		t.Fatal(err)
	}
	if len(loaded.state.Tournaments) != 1 {
		t.Fatalf("loaded tournaments = %d, want 1", len(loaded.state.Tournaments))
	}
	if got := loaded.state.Tournaments[0].TemplateSnapshot.Name; got != "Осенний турнир знаний" {
		t.Fatalf("snapshot name changed: %q", got)
	}
	if got := loaded.state.Tournaments[0].TemplateSnapshot.Rounds[0].Name; got != "Разминка" {
		t.Fatalf("snapshot round name changed: %q", got)
	}
	if got := loaded.state.Tournaments[0].TemplateSnapshot.Rewards[0].Amount; got != 100 {
		t.Fatalf("snapshot reward amount changed: %d", got)
	}
	if got := loaded.state.Tournaments[0].ScheduleSnapshot.RegistrationOpenOffsetSeconds; got != -86400 {
		t.Fatalf("snapshot registration open offset changed: %d", got)
	}
	if got := loaded.state.Tournaments[0].ScheduleSnapshot.RegistrationCloseOffsetSeconds; got != -3600 {
		t.Fatalf("snapshot registration close offset changed: %d", got)
	}
	if got := loaded.state.Config.Template.Rounds[0].Name; got != "Edited round" {
		t.Fatalf("editable config round name = %q", got)
	}
	if got := loaded.state.Config.Template.Rewards[0].Amount; got != 999 {
		t.Fatalf("editable config reward amount = %d", got)
	}
	if got := loaded.state.Config.Schedule.RegistrationOpenOffsetSeconds; got != -172800 {
		t.Fatalf("editable config registration open offset = %d", got)
	}
	if got := loaded.state.Config.Schedule.RegistrationCloseOffsetSeconds; got != -7200 {
		t.Fatalf("editable config registration close offset = %d", got)
	}
	if got := loaded.state.Config.Template.Name; got != "Edited after creation" {
		t.Fatalf("editable config name = %q", got)
	}
}

func TestManualCreationPersistsEmptyRewardsArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &store{path: path, state: State{Config: defaultConfig(), Tournaments: []Tournament{}}}
	c := defaultConfig()
	c.Template.Rewards = []Reward{}
	body, err := json.Marshal(map[string]any{"config": c, "starts_at": []string{"2026-10-19T20:00:00+03:00"}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/tournaments", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.createTournaments(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("create status = %d, body %s", w.Code, w.Body)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"rewards":[]`)) {
		t.Fatalf("create response did not preserve empty rewards array: %s", w.Body)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(`"rewards":null`)) {
		t.Fatalf("create response encoded empty rewards as null: %s", w.Body)
	}
	var response State
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if len(response.Tournaments) != 1 {
		t.Fatalf("response tournaments = %d, want 1", len(response.Tournaments))
	}
	if response.Config.Template.Rewards == nil || len(response.Config.Template.Rewards) != 0 {
		t.Fatalf("response config rewards = %#v, want non-nil empty slice", response.Config.Template.Rewards)
	}
	if response.Config.Template.Name != c.Template.Name {
		t.Fatalf("response config template name = %q, want %q", response.Config.Template.Name, c.Template.Name)
	}
	if response.Tournaments[0].TemplateSnapshot.Rounds[0].Name != c.Template.Rounds[0].Name {
		t.Fatalf("response snapshot round name = %q, want %q", response.Tournaments[0].TemplateSnapshot.Rounds[0].Name, c.Template.Rounds[0].Name)
	}
	if response.Tournaments[0].TemplateSnapshot.Rewards == nil || len(response.Tournaments[0].TemplateSnapshot.Rewards) != 0 {
		t.Fatalf("response snapshot rewards = %#v, want non-nil empty slice", response.Tournaments[0].TemplateSnapshot.Rewards)
	}

	loaded := &store{path: path}
	if err := loaded.load(); err != nil {
		t.Fatalf("load persisted state: %v", err)
	}
	if loaded.state.Config.Template.Rewards == nil || len(loaded.state.Config.Template.Rewards) != 0 {
		t.Fatalf("loaded config rewards = %#v, want non-nil empty slice", loaded.state.Config.Template.Rewards)
	}
	if loaded.state.Config.Template.Name != c.Template.Name {
		t.Fatalf("loaded config template name = %q, want %q", loaded.state.Config.Template.Name, c.Template.Name)
	}
	if len(loaded.state.Tournaments) != 1 {
		t.Fatalf("loaded tournaments = %d, want 1", len(loaded.state.Tournaments))
	}
	snapshot := loaded.state.Tournaments[0].TemplateSnapshot
	if snapshot.Rounds[0].Name != c.Template.Rounds[0].Name {
		t.Fatalf("loaded snapshot round name = %q, want %q", snapshot.Rounds[0].Name, c.Template.Rounds[0].Name)
	}
	if snapshot.Rewards == nil || len(snapshot.Rewards) != 0 {
		t.Fatalf("loaded snapshot rewards = %#v, want non-nil empty slice", snapshot.Rewards)
	}
}
