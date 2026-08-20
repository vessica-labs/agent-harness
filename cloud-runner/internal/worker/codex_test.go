package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexErrorMessageReadsStructuredTurnFailure(t *testing.T) {
	line := []byte(`{"type":"turn.failed","error":{"message":"model is not supported"}}`)
	if got := codexErrorMessage(line); got != "model is not supported" {
		t.Fatalf("unexpected Codex error: %q", got)
	}
}

func TestCodexFailureDetailFallsBackToStructuredError(t *testing.T) {
	if got := codexFailureDetail("", "model is not supported"); got != "model is not supported" {
		t.Fatalf("unexpected failure detail: %q", got)
	}
	got := codexFailureDetail("transport failed", "model is not supported")
	if !strings.Contains(got, "transport failed") || !strings.Contains(got, "model is not supported") {
		t.Fatalf("combined failure detail lost context: %q", got)
	}
}

func TestRunCodexDoesNotHangAfterTerminalEventWhenDescendantKeepsStdoutOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	repo := t.TempDir()
	agentPath := filepath.Join(repo, ".agents", "product.md")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, []byte("Return the required result."), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(repo, "fake-codex")
	script := `#!/bin/sh
prompt=$(cat)
result=$(printf '%s\n' "$prompt" | sed -n 's/^- Required result JSON file: //p' | head -1)
printf '%s\n' '{"agent":"product","status":"needs_input"}' > "$result"
(sleep 10) &
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}'
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{
		config:   Config{RunID: "run_test", CodexBinary: fakeCodex, CodexModel: "gpt-5.6-sol", CodexParallelSafe: true, PlaywrightWorkers: 2},
		client:   &controlClient{baseURL: server.URL, token: "test", runID: "run_test", http: server.Client()},
		pipeline: Pipeline{RunRoot: ".harness/runs/{run_id}"},
	}
	resultPath := filepath.Join(repo, ".harness", "runs", "run_test", "agent-output", "product.json")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	if err := runner.runCodex(ctx, repo, Stage{ID: "product", Agent: ".agents/product.md"}, "", resultPath, ""); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("runCodex waited for leaked stdout after terminal event: %s", elapsed)
	}
}
