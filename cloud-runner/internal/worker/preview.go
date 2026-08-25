package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type previewConfig struct {
	Command     string `json:"command"`
	Port        int    `json:"port"`
	Healthcheck string `json:"healthcheck"`
}

// previewFromConfig reads the optional preview section of the repository's
// .harness/config.yaml through harnessctl. A nil result means the repository
// does not opt into previews.
func (r *Runner) previewFromConfig(ctx context.Context) (*previewConfig, error) {
	output, err := r.harness(ctx, r.repo, "validate-config", filepath.Join(r.repo, ".harness", "config.yaml"))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Config struct {
			Preview *previewConfig `json:"preview"`
		} `json:"config"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return nil, fmt.Errorf("decode harness config: %w", err)
	}
	return parsed.Config.Preview, nil
}

// startPreview launches the repository's preview command detached from the
// worker process so the application keeps serving after the worker exits,
// waits for the healthcheck, and reports the port to the control plane.
// Preview startup is best effort: failures are reported but never fail a run
// that already produced its draft pull request.
func (r *Runner) startPreview(ctx context.Context) {
	config, err := r.previewFromConfig(ctx)
	if err != nil {
		_ = r.event(ctx, "preview.failed", "warning", "Could not read preview configuration", "", map[string]any{"error": err.Error()})
		return
	}
	if config == nil {
		return
	}
	_ = r.event(ctx, "preview.starting", "info", "Preview application is starting inside the sandbox", "", map[string]any{
		"port": config.Port,
	})
	logPath := filepath.Join(r.config.Workspace, ".harness-preview.log")
	launch := fmt.Sprintf("cd %s && PORT=%d setsid nohup bash -c %s >>%s 2>&1 & disown",
		shellQuoteWorker(r.repo), config.Port, shellQuoteWorker(config.Command), shellQuoteWorker(logPath))
	if _, err := runCommand(ctx, r.repo, sanitizedEnvironment(""), "bash", "-lc", launch); err != nil {
		_ = r.event(ctx, "preview.failed", "warning", "Preview command failed to launch", "", map[string]any{"error": err.Error()})
		return
	}
	healthcheck := config.Healthcheck
	if healthcheck == "" {
		healthcheck = "/"
	}
	if err := waitForPreview(ctx, fmt.Sprintf("http://127.0.0.1:%d%s", config.Port, healthcheck), 2*time.Minute); err != nil {
		_ = r.event(ctx, "preview.failed", "warning", "Preview application did not become healthy", "", map[string]any{"error": err.Error(), "port": config.Port})
		return
	}
	_ = r.event(ctx, "preview.ready", "info", "Preview application is serving inside the sandbox", "", map[string]any{
		"port": config.Port, "healthcheck": healthcheck,
	})
}

func waitForPreview(ctx context.Context, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("healthcheck returned HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("preview did not answer within %s: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func shellQuoteWorker(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
