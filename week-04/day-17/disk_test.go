package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadPublishesAndPreservesFileOnFailure(t *testing.T) {
	for _, publishFails := range []bool{false, true} {
		t.Run(fmt.Sprint("publishFails=", publishFails), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "week-04", "day-17", "video", "example.mp4")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("video bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			var calls []string
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path != "/transfer" && r.Header.Get("Authorization") != "OAuth test-token" {
					t.Error("missing OAuth authorization on Disk API request")
				}
				switch {
				case r.Method == "PUT" && r.URL.Path == "/resources":
					w.WriteHeader(http.StatusConflict) // folder already exists
				case r.Method == "GET" && r.URL.Path == "/resources/upload":
					fmt.Fprintf(w, `{"href":%q}`, server.URL+"/transfer")
				case r.Method == "PUT" && r.URL.Path == "/transfer":
					if r.ContentLength != int64(len("video bytes")) {
						t.Error("uploaded length mismatch")
					}
					w.WriteHeader(http.StatusCreated)
				case r.Method == "PUT" && r.URL.Path == "/resources/publish":
					if publishFails {
						w.WriteHeader(http.StatusForbidden)
					}
				case r.Method == "GET" && r.URL.Path == "/resources":
					fmt.Fprint(w, `{"public_url":"https://disk.yandex.ru/i/example"}`)
				default:
					t.Errorf("unexpected API call: %s %s", r.Method, r.URL)
				}
			}))
			defer server.Close()
			disk := &diskAPI{baseURL: server.URL + "/resources", token: "test-token", client: server.Client(), root: root}
			got, err := disk.upload(context.Background(), uploadInput{File: path})
			if publishFails {
				if err == nil || !strings.Contains(err.Error(), "publish") {
					t.Fatalf("expected publish error, got %v", err)
				}
			} else if err != nil || got.Path != "/ai-advent-challenge/week-4/example.mp4" || got.PublicURL != "https://disk.yandex.ru/i/example" {
				t.Fatalf("unexpected upload result: %+v, %v", got, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("recording lost after upload: %v", err)
			}
			if len(calls) < 4 || calls[0] != "PUT /resources" || calls[2] != "GET /resources/upload" || calls[3] != "PUT /transfer" {
				t.Fatalf("incorrect upload sequence: %v", calls)
			}
		})
	}
}
