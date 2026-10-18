package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestProposalValidatesNestedConfigPresence(t *testing.T) {
	configJSON, err := json.Marshal(defaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	configText := string(configJSON)

	tests := []struct {
		name       string
		config     string
		wantConfig bool
		wantError  bool
	}{
		{
			name:      "missing schedule enabled",
			config:    strings.Replace(configText, `,"enabled":true`, ``, 1),
			wantError: true,
		},
		{
			name:      "missing registration offset",
			config:    strings.Replace(configText, `"registration_open_offset_seconds":-86400,`, ``, 1),
			wantError: true,
		},
		{
			name: "explicit false and zero",
			config: strings.Replace(
				strings.Replace(
					strings.Replace(configText, `"enabled":true`, `"enabled":false`, 1),
					`"registration_open_offset_seconds":-86400`,
					`"registration_open_offset_seconds":0`, 1),
				`"registration_close_offset_seconds":-3600`,
				`"registration_close_offset_seconds":0`, 1),
			wantConfig: true,
		},
		{
			name:       "clarification with null config",
			config:     `null`,
			wantConfig: false,
		},
		{
			name:      "unknown nested schedule field",
			config:    strings.Replace(configText, `"enabled":true`, `"enabled":true,"unexpected":1`, 1),
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("provider request method = %q, want POST", r.Method)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("provider authorization = %q, want configured bearer token", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + mustJSONString(t, `{"message":"Готово","config":`+tt.config+`,"changes":[]}`) + `}}]}`))
			}))
			defer server.Close()

			settings := providerConfig{endpoint: server.URL, model: "test-model"}
			proposal, err := requestProposal(context.Background(), server.Client(), settings, "test-key", chatRequest{
				Config:   defaultConfig(),
				Revision: 7,
				Messages: []chatMessage{{Role: "user", Content: "Уточни конфигурацию"}},
			})
			if tt.wantError {
				if err == nil {
					t.Fatal("requestProposal accepted a structurally invalid model configuration")
				}
				return
			}
			if err != nil {
				t.Fatalf("requestProposal returned error: %v", err)
			}
			if got := proposal.Config != nil; got != tt.wantConfig {
				t.Fatalf("proposal config present = %t, want %t", got, tt.wantConfig)
			}
			if tt.wantConfig {
				if err := validateConfig(*proposal.Config); err != nil {
					t.Fatalf("explicit false and zero configuration failed validation: %v", err)
				}
				if proposal.Config.Schedule.Enabled {
					t.Error("explicit false schedule.enabled was not preserved")
				}
				if proposal.Config.Schedule.RegistrationOpenOffsetSeconds != 0 {
					t.Errorf("explicit zero registration offset = %d, want 0", proposal.Config.Schedule.RegistrationOpenOffsetSeconds)
				}
			}
		})
	}
}

func TestRequestProposalRejectsTrailingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"message\":\"Вопрос\",\"config\":null,\"changes\":[]} {}"}}]}`))
	}))
	defer server.Close()

	_, err := requestProposal(context.Background(), server.Client(), providerConfig{endpoint: server.URL, model: "test-model"}, "test-key", chatRequest{
		Config: defaultConfig(), Messages: []chatMessage{{Role: "user", Content: "Вопрос"}},
	})
	if err == nil {
		t.Fatal("requestProposal accepted trailing JSON after the proposal")
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
