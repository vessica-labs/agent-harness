package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RailwayCLI struct {
	Binary            string
	Project           string
	Environment       string
	APIToken          string
	WorkerPath        string
	Timeout           time.Duration
	ReadyTimeout      time.Duration
	ReadyPollInterval time.Duration
}

func (r RailwayCLI) Create(ctx context.Context, spec CreateSpec) (Instance, error) {
	path, err := writeEnvFile(spec.Variables)
	if err != nil {
		return Instance{}, err
	}
	defer os.Remove(path)
	args := []string{"sandbox", "create", "--json", "--project", r.Project, "--environment", r.Environment,
		"--env-file", path, "--idle-timeout-minutes", strconv.Itoa(spec.IdleTimeoutMinutes)}
	if spec.Checkpoint != "" {
		args = append(args, "--checkpoint", spec.Checkpoint)
	}
	output, err := r.run(ctx, args...)
	if err != nil {
		return Instance{}, err
	}
	id := findJSONString(output, "id", "sandboxId", "sandbox_id")
	if id == "" {
		return Instance{}, fmt.Errorf("Railway sandbox create returned no id")
	}
	return Instance{ID: id, State: coalesce(findJSONString(output, "state", "status"), "running")}, nil
}

func (r RailwayCLI) StartWorker(ctx context.Context, id string) (string, error) {
	if err := r.waitForRunning(ctx, id); err != nil {
		return "", err
	}
	worker := r.WorkerPath
	if worker == "" {
		worker = "agent-harness"
	}
	bootstrap := workerBootstrap(worker)
	output, err := r.run(ctx, "sandbox", "exec", "--project", r.Project, "--environment", r.Environment,
		"--id", id, "--detach", "--", "bash", "-lc", bootstrap)
	if err != nil {
		return "", err
	}
	lines := strings.Fields(strings.TrimSpace(string(output)))
	if len(lines) == 0 {
		return "", errors.New("Railway sandbox did not return a durable session")
	}
	return lines[len(lines)-1], nil
}

func workerBootstrap(fallback string) string {
	return strings.Join([]string{
		"set -euo pipefail",
		"export HARNESS_BOOTSTRAP_STARTED_AT_MS=$(date +%s%3N)",
		"report_bootstrap_failure() { code=$?; if test $code -ne 0; then curl -fsS -X POST -H \"Authorization: Bearer $HARNESS_RUN_CAPABILITY\" -H 'Content-Type: application/json' \"$HARNESS_CONTROL_URL/internal/v1/runs/$HARNESS_RUN_ID/events\" --data '{\"type\":\"run.failed\",\"level\":\"error\",\"message\":\"Sandbox worker bootstrap failed\",\"payload\":{\"phase\":\"worker_bootstrap\"}}' >/dev/null 2>&1 || true; fi; exit $code; }",
		"trap report_bootstrap_failure EXIT",
		"install -d -m 0755 /opt/agent-harness/bin",
		"worker=/opt/agent-harness/bin/agent-harness",
		"worker_url=\"$HARNESS_CONTROL_URL/internal/v1/runs/$HARNESS_RUN_ID/worker-binary\"",
		"worker_digest=$(curl -fsSI --retry 5 --retry-delay 1 --retry-all-errors --connect-timeout 10 -H \"Authorization: Bearer $HARNESS_RUN_CAPABILITY\" \"$worker_url\" | awk -F': ' 'tolower($1)==\"x-agent-harness-worker-sha256\" {gsub(/\\r/,\"\",$2); print $2}')",
		"export HARNESS_WORKER_DOWNLOAD_STARTED_AT_MS=$(date +%s%3N)",
		"if test -n \"$worker_digest\"; then if test -x \"$worker\" && test \"$(cat \"$worker.sha256\" 2>/dev/null || true)\" = \"$worker_digest\"; then export HARNESS_WORKER_CACHE_HIT=1; else export HARNESS_WORKER_CACHE_HIT=0; tmp=$(mktemp /tmp/agent-harness-worker.XXXXXX); curl -fsSL --retry 5 --retry-delay 1 --retry-all-errors --connect-timeout 10 -H \"Authorization: Bearer $HARNESS_RUN_CAPABILITY\" \"$worker_url\" -o \"$tmp\"; echo \"$worker_digest  $tmp\" | sha256sum -c -; install -m 0755 \"$tmp\" \"$worker\"; printf '%s\\n' \"$worker_digest\" >\"$worker.sha256\"; rm -f \"$tmp\"; fi; else export HARNESS_WORKER_CACHE_HIT=0; worker=" + shellQuote(fallback) + "; fi",
		"export HARNESS_WORKER_DOWNLOADED_AT_MS=$(date +%s%3N)",
		"trap - EXIT",
		"exec \"$worker\" worker",
	}, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func (r RailwayCLI) waitForRunning(ctx context.Context, id string) error {
	timeout := r.ReadyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	poll := r.ReadyPollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	lastState := "unknown"
	var lastErr error
	for {
		instance, err := r.Status(readyCtx, id)
		if err == nil {
			lastState = strings.ToUpper(strings.TrimSpace(instance.State))
			if lastState == "RUNNING" {
				return nil
			}
			switch lastState {
			case "CRASHED", "DESTROYED", "FAILED", "REMOVED", "STOPPED":
				return fmt.Errorf("Railway sandbox %s entered terminal state %s before worker start", id, lastState)
			}
		} else {
			lastErr = err
		}
		timer := time.NewTimer(poll)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			if lastErr != nil {
				return fmt.Errorf("wait for Railway sandbox %s to run after state %s: %w (last status error: %v)", id, lastState, readyCtx.Err(), lastErr)
			}
			return fmt.Errorf("wait for Railway sandbox %s to run after state %s: %w", id, lastState, readyCtx.Err())
		case <-timer.C:
		}
	}
}

func (r RailwayCLI) Heartbeat(ctx context.Context, id string) error {
	_, err := r.run(ctx, "sandbox", "exec", "--project", r.Project, "--environment", r.Environment,
		"--id", id, "--timeout", "30", "--", "true")
	return err
}

func (r RailwayCLI) Status(ctx context.Context, id string) (Instance, error) {
	output, err := r.run(ctx, "sandbox", "list", "--json", "--project", r.Project, "--environment", r.Environment)
	if err != nil {
		return Instance{}, err
	}
	var raw any
	if err := json.Unmarshal(output, &raw); err != nil {
		return Instance{}, fmt.Errorf("decode Railway sandbox list: %w", err)
	}
	if found, ok := findObjectByID(raw, id); ok {
		return Instance{ID: id, State: coalesce(mapString(found, "state", "status"), "unknown")}, nil
	}
	return Instance{}, errors.New("sandbox not found")
}

func (r RailwayCLI) Destroy(ctx context.Context, id string) error {
	_, err := r.run(ctx, "sandbox", "destroy", "--project", r.Project, "--environment", r.Environment, "--id", id)
	return err
}

func (r RailwayCLI) run(ctx context.Context, args ...string) ([]byte, error) {
	binary := r.Binary
	if binary == "" {
		binary = "railway"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(callCtx, binary, args...)
	command.Env = append(os.Environ(), "RAILWAY_API_TOKEN="+r.APIToken,
		"RAILWAY_CALLER=agent-harness-control-plane", "RAILWAY_AGENT_SESSION=agent-harness-control-plane")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 1200 {
			message = message[:1200]
		}
		return nil, fmt.Errorf("railway %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, message)
	}
	return stdout.Bytes(), nil
}

func writeEnvFile(values map[string]string) (string, error) {
	directory := os.TempDir()
	file, err := os.CreateTemp(directory, "agent-harness-sandbox-*.env")
	if err != nil {
		return "", err
	}
	path := filepath.Clean(file.Name())
	cleanup := true
	defer func() {
		file.Close()
		if cleanup {
			os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	writer := bufio.NewWriter(file)
	for _, key := range keys {
		value := values[key]
		if strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("sandbox variable %q contains a newline", key)
		}
		if _, err := fmt.Fprintf(writer, "%s=%s\n", key, value); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	cleanup = false
	return path, nil
}

func findJSONString(data []byte, keys ...string) string {
	var raw any
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	return walkString(raw, keys...)
}

func walkString(raw any, keys ...string) string {
	switch value := raw.(type) {
	case map[string]any:
		if result := mapString(value, keys...); result != "" {
			return result
		}
		for _, child := range value {
			if result := walkString(child, keys...); result != "" {
				return result
			}
		}
	case []any:
		for _, child := range value {
			if result := walkString(child, keys...); result != "" {
				return result
			}
		}
	}
	return ""
}

func findObjectByID(raw any, id string) (map[string]any, bool) {
	switch value := raw.(type) {
	case map[string]any:
		if mapString(value, "id", "sandboxId", "sandbox_id") == id {
			return value, true
		}
		for _, child := range value {
			if result, ok := findObjectByID(child, id); ok {
				return result, true
			}
		}
	case []any:
		for _, child := range value {
			if result, ok := findObjectByID(child, id); ok {
				return result, true
			}
		}
	}
	return nil, false
}

func mapString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := value[key]; ok && raw != nil {
			return fmt.Sprint(raw)
		}
	}
	return ""
}

func coalesce(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
