package worker

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestStartupFailureIsReportedImmediately(t *testing.T) {
	var mu sync.Mutex
	var types []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var event struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(body, &event)
		mu.Lock()
		types = append(types, event.Type)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	runner := New(Config{RunID: "run-1", IssueID: "issue-1", ControlURL: server.URL, Capability: "capability", Workspace: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := runner.Run(t.Context()); err == nil {
		t.Fatal("expected startup failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(types) < 2 || types[0] != "worker.starting" || types[len(types)-1] != "run.failed" {
		t.Fatalf("events=%v", types)
	}
}
