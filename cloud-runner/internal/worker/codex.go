package worker

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
	"strings"
	"time"
)

const codexTerminalExitGrace = time.Second

func (r *Runner) runCodex(ctx context.Context, repo string, stage Stage, ticketKey, resultPath string, extra string) error {
	codexHome := r.codexHome
	var leased runtimeCodexSession
	if !r.config.CodexParallelSafe {
		select {
		case leased = <-r.codexPool:
			codexHome = leased.home
			defer func() { r.codexPool <- leased }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	role, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(stage.Agent)))
	if err != nil {
		return err
	}
	runDir := runDirectory(repo, r.pipeline, r.config.RunID)
	var inputs []string
	for _, input := range stage.Inputs {
		file := replaceTicket(input.File, ticketKey)
		inputs = append(inputs, "- "+filepath.Join(runDir, filepath.FromSlash(file)))
	}
	prompt := fmt.Sprintf(`You are the Agent Harness %s stage. Follow this role definition exactly:

%s

Execution context:
- Repository worktree: %s
- Run journal: %s
- Declared inputs:
%s
- Required result JSON file: %s

Work directly in the supplied repository. Do not edit pipeline state or provider credentials. Write the exact JSON output contract to the required result file. Human input policy: only the product and arch stages may return status needs_input, at most once per stage. Every other stage must use the available context and continue to a terminal result; it may never ask a question or wait for a user. %s`,
		stage.ID, string(role), repo, runDir, strings.Join(inputs, "\n"), resultPath,
		extra+fmt.Sprintf(" In this Railway sandbox, Playwright and Chromium are preinstalled. Repository tests must still declare their Playwright package dependency in the appropriate package manifest and lockfile. Configure Playwright to use PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH when it is set. Every Playwright invocation must explicitly use at most %d workers (for example: npm run test:e2e -- --workers=%d). HARNESS_PLAYWRIGHT_WORKERS contains this limit.", r.config.PlaywrightWorkers, r.config.PlaywrightWorkers))
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		return err
	}
	if err := os.Remove(resultPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale agent result: %w", err)
	}
	lastMessage := filepath.Join(runDir, "logs", "codex-"+safeName(stage.ID+"-"+ticketKey)+"-last.txt")
	logPath := filepath.Join(runDir, "logs", "codex-"+safeName(stage.ID+"-"+ticketKey)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, r.config.CodexBinary, "exec", "--json", "--model", r.config.CodexModel,
		"--dangerously-bypass-approvals-and-sandbox", "--ignore-user-config", "-C", repo,
		"--output-last-message", lastMessage, "-")
	command.Env = sanitizedEnvironment(codexHome)
	command.Stdin = strings.NewReader(prompt)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	stderrPath := strings.TrimSuffix(logPath, ".jsonl") + "-stderr.txt"
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stderrFile.Close()
		_ = logFile.Close()
		return err
	}
	// Use a file rather than an os/exec-managed pipe so a leaked descendant
	// cannot keep command.Wait blocked after the Codex process exits.
	command.Stderr = stderrFile
	if err := command.Start(); err != nil {
		_ = stderrFile.Close()
		_ = logFile.Close()
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	activityStartedAt := map[string]time.Time{}
	lastCodexError := ""
	terminalCompleted := false
	terminalForced := make(chan struct{}, 1)
	var terminalTimer *time.Timer
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		_, _ = logFile.Write(append(line, '\n'))
		if message := codexErrorMessage(line); message != "" {
			lastCodexError = message
		}
		if usage, ok := parseCodexUsage(line, r.config.CodexModel); ok {
			terminalCompleted = true
			if terminalTimer == nil {
				terminalTimer = time.AfterFunc(codexTerminalExitGrace, func() {
					select {
					case terminalForced <- struct{}{}:
					default:
					}
					_ = stdout.Close()
					if command.Process != nil {
						_ = command.Process.Kill()
					}
				})
			}
			if err := r.event(context.WithoutCancel(ctx), "codex.usage", "info", "Codex turn usage recorded", stage.ID, usage); err != nil {
				_ = command.Process.Kill()
				_ = stdout.Close()
				_ = command.Wait()
				if terminalTimer != nil {
					terminalTimer.Stop()
				}
				_ = stderrFile.Close()
				_ = logFile.Close()
				return fmt.Errorf("record Codex usage: %w", err)
			}
		}
		if activity, ok := parseCodexActivity(line, repo); ok {
			payload := map[string]any{"action": activity.Action, "item_id": activity.ItemID}
			now := time.Now()
			if strings.HasSuffix(activity.Type, ".started") {
				activityStartedAt[activity.ItemID] = now
			} else if startedAt, exists := activityStartedAt[activity.ItemID]; exists {
				payload["duration_ms"] = max(now.Sub(startedAt).Milliseconds(), 0)
				delete(activityStartedAt, activity.ItemID)
			}
			if ticketKey != "" {
				payload["ticket_key"] = ticketKey
			}
			if activity.Command != "" {
				payload["command"] = activity.Command
			}
			if len(activity.Paths) > 0 {
				payload["paths"] = activity.Paths
			}
			if activity.ExitCode != nil {
				payload["exit_code"] = *activity.ExitCode
			}
			_ = r.event(context.WithoutCancel(ctx), activity.Type, activity.Level, activity.Message, stage.ID, payload)
		}
	}
	scanErr := scanner.Err()
	runErr := command.Wait()
	if terminalTimer != nil {
		terminalTimer.Stop()
	}
	forcedAfterTerminal := false
	select {
	case <-terminalForced:
		forcedAfterTerminal = true
	default:
	}
	stderrCloseErr := stderrFile.Close()
	closeErr := logFile.Close()
	stderrBody, _ := os.ReadFile(stderrPath)
	if runErr != nil && !(terminalCompleted && forcedAfterTerminal) {
		return fmt.Errorf("Codex %s failed: %w: %s", stage.ID, runErr, tail(codexFailureDetail(string(stderrBody), lastCodexError), 3000))
	}
	if scanErr != nil && !(terminalCompleted && forcedAfterTerminal) {
		return fmt.Errorf("read Codex %s event stream: %w", stage.ID, scanErr)
	}
	if stderrCloseErr != nil {
		return stderrCloseErr
	}
	if closeErr != nil {
		return closeErr
	}
	if _, err := os.Stat(resultPath); errors.Is(err, os.ErrNotExist) {
		body, readErr := os.ReadFile(lastMessage)
		if readErr != nil {
			return errors.New("Codex completed without the required result JSON")
		}
		body = stripFence(bytes.TrimSpace(body))
		if err := os.WriteFile(resultPath, body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func codexErrorMessage(line []byte) string {
	var event struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &event) != nil {
		return ""
	}
	if event.Type == "turn.failed" {
		return strings.TrimSpace(event.Error.Message)
	}
	if event.Type == "error" {
		return strings.TrimSpace(event.Message)
	}
	return ""
}

func codexFailureDetail(stderr, structured string) string {
	stderr = strings.TrimSpace(stderr)
	structured = strings.TrimSpace(structured)
	if stderr == "" {
		return structured
	}
	if structured == "" || strings.Contains(stderr, structured) {
		return stderr
	}
	return stderr + "\n" + structured
}

func replaceTicket(value, ticketKey string) string {
	return strings.ReplaceAll(value, "{ticket_key}", ticketKey)
}

func stripFence(value []byte) []byte {
	text := strings.TrimSpace(string(value))
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 && strings.HasPrefix(lines[len(lines)-1], "```") {
			return []byte(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	return value
}

func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func safeName(value string) string {
	value = strings.Trim(value, "-")
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	return builder.String()
}
