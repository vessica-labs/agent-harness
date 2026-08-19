package worker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

Work directly in the supplied repository. Do not edit pipeline state or provider credentials. Write the exact JSON output contract to the required result file. %s`,
		stage.ID, string(role), repo, runDir, strings.Join(inputs, "\n"), resultPath,
		extra+fmt.Sprintf(" In this Railway sandbox, every Playwright invocation must explicitly use at most %d workers (for example: npm run test:e2e -- --workers=%d). HARNESS_PLAYWRIGHT_WORKERS contains this limit.", r.config.PlaywrightWorkers, r.config.PlaywrightWorkers))
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
	var stderr bytes.Buffer
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = logFile.Close()
		return err
	}
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		_, _ = logFile.Write(append(line, '\n'))
		if usage, ok := parseCodexUsage(line, r.config.CodexModel); ok {
			if err := r.event(context.WithoutCancel(ctx), "codex.usage", "info", "Codex turn usage recorded", stage.ID, usage); err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				_ = logFile.Close()
				return fmt.Errorf("record Codex usage: %w", err)
			}
		}
		if activity, ok := parseCodexActivity(line, repo); ok {
			payload := map[string]any{"action": activity.Action, "item_id": activity.ItemID}
			if ticketKey != "" {
				payload["ticket_key"] = ticketKey
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
	closeErr := logFile.Close()
	if runErr != nil {
		return fmt.Errorf("Codex %s failed: %w: %s", stage.ID, runErr, tail(stderr.String(), 3000))
	}
	if scanErr != nil {
		return fmt.Errorf("read Codex %s event stream: %w", stage.ID, scanErr)
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
