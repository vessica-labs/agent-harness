package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkerBootstrapUsesDigestCacheAndReportsBootstrapFailures(t *testing.T) {
	script := workerBootstrap("agent-harness")
	for _, expected := range []string{"worker-binary", "HARNESS_WORKER_CACHE_HIT=1", "run.failed", "exec \"$worker\" worker"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("bootstrap missing %q:\n%s", expected, script)
		}
	}
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap syntax: %v: %s", err, output)
	}
}

func TestRailwayStartWorkerWaitsUntilSandboxIsRunning(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, "status-count")
	binary := filepath.Join(directory, "railway")
	script := `#!/bin/sh
if [ "$1 $2" = "sandbox list" ]; then
  count=0
  if [ -f "$HARNESS_TEST_STATE" ]; then count=$(cat "$HARNESS_TEST_STATE"); fi
  count=$((count + 1))
  printf '%s' "$count" > "$HARNESS_TEST_STATE"
  status=CREATING
  if [ "$count" -ge 2 ]; then status=RUNNING; fi
  printf '[{"id":"sandbox-123","status":"%s"}]\n' "$status"
  exit 0
fi
if [ "$1 $2" = "sandbox exec" ]; then
  count=$(cat "$HARNESS_TEST_STATE")
  if [ "$count" -lt 2 ]; then exit 42; fi
  printf 'session-123\n'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_TEST_STATE", state)
	provider := RailwayCLI{Binary: binary, Project: "project", Environment: "production", APIToken: "token",
		ReadyTimeout: time.Second, ReadyPollInterval: time.Millisecond, Timeout: time.Second}
	session, err := provider.StartWorker(context.Background(), "sandbox-123")
	if err != nil {
		t.Fatal(err)
	}
	if session != "session-123" {
		t.Fatalf("unexpected session %q", session)
	}
	value, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(string(value))
	if err != nil || count < 2 {
		t.Fatalf("worker started before a RUNNING status: count=%q err=%v", value, err)
	}
}

func TestRailwayStartWorkerStopsOnTerminalSandboxState(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "railway")
	script := `#!/bin/sh
if [ "$1 $2" = "sandbox list" ]; then
  printf '[{"id":"sandbox-123","status":"DESTROYED"}]\n'
  exit 0
fi
exit 42
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := RailwayCLI{Binary: binary, Project: "project", Environment: "production", APIToken: "token",
		ReadyTimeout: time.Second, ReadyPollInterval: time.Millisecond, Timeout: time.Second}
	_, err := provider.StartWorker(context.Background(), "sandbox-123")
	if err == nil || !strings.Contains(err.Error(), "terminal state DESTROYED") {
		t.Fatalf("expected terminal-state failure, got %v", err)
	}
}

func TestRailwayDestroyUsesDocumentedIDFlag(t *testing.T) {
	directory := t.TempDir()
	arguments := filepath.Join(directory, "arguments")
	binary := filepath.Join(directory, "railway")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$HARNESS_TEST_ARGUMENTS\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_TEST_ARGUMENTS", arguments)
	provider := RailwayCLI{Binary: binary, Project: "project", Environment: "production", APIToken: "token"}
	if err := provider.Destroy(context.Background(), "sandbox-123"); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	command := string(value)
	if !strings.Contains(command, "sandbox destroy") || !strings.Contains(command, "--id sandbox-123") {
		t.Fatalf("unexpected Railway command: %s", command)
	}
}
