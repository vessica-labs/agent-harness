package worker

import (
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
		stage.ID, string(role), repo, runDir, strings.Join(inputs, "\n"), resultPath, extra)
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
	command := exec.CommandContext(ctx, r.config.CodexBinary, "exec", "--json",
		"--dangerously-bypass-approvals-and-sandbox", "--ignore-user-config", "-C", repo,
		"--output-last-message", lastMessage, "-")
	command.Env = sanitizedEnvironment(codexHome)
	command.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command.Stdout, command.Stderr = logFile, &stderr
	runErr := command.Run()
	closeErr := logFile.Close()
	if runErr != nil {
		return fmt.Errorf("Codex %s failed: %w: %s", stage.ID, runErr, tail(stderr.String(), 3000))
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
