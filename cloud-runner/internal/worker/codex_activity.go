package worker

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
)

var commandSecret = regexp.MustCompile(`(?i)((?:--?|^)(?:token|secret|password|api[-_]?key|authorization)(?:=|\s+))\S+`)

type codexActivity struct {
	Type     string
	Level    string
	Message  string
	Action   string
	ItemID   string
	Paths    []string
	ExitCode *int
}

func parseCodexActivity(line []byte, repo string) (codexActivity, bool) {
	var envelope struct {
		Type string `json:"type"`
		Item struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Command  string `json:"command"`
			ExitCode *int   `json:"exit_code"`
			Changes  []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &envelope) != nil || (envelope.Type != "item.started" && envelope.Type != "item.completed") {
		return codexActivity{}, false
	}
	switch envelope.Item.Type {
	case "command_execution":
		command := safeCommandSummary(envelope.Item.Command)
		if command == "" {
			command = "repository command"
		}
		activity := codexActivity{Type: "codex.command.started", Level: "info", Message: "Running command: " + command,
			Action: "command", ItemID: envelope.Item.ID, ExitCode: envelope.Item.ExitCode}
		if envelope.Type == "item.completed" {
			activity.Type, activity.Message = "codex.command.completed", "Ran command: "+command
			if envelope.Item.ExitCode != nil && *envelope.Item.ExitCode != 0 {
				activity.Level = "warning"
				activity.Message = fmt.Sprintf("Command exited %d: %s", *envelope.Item.ExitCode, command)
			}
		}
		return activity, true
	case "file_change":
		paths := make([]string, 0, len(envelope.Item.Changes))
		for _, change := range envelope.Item.Changes {
			paths = append(paths, safeRepoPath(repo, change.Path))
		}
		if len(paths) == 0 {
			return codexActivity{}, false
		}
		verb := "Editing"
		eventType := "codex.files.started"
		if envelope.Type == "item.completed" {
			verb, eventType = "Edited", "codex.files.completed"
		}
		return codexActivity{Type: eventType, Level: "info", Message: verb + " " + strings.Join(paths, ", "),
			Action: "file_change", ItemID: envelope.Item.ID, Paths: paths}, true
	default:
		return codexActivity{}, false
	}
}

func safeCommandSummary(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"/bin/zsh -lc ", "/bin/bash -lc ", "bash -lc ", "sh -lc "} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.Trim(value, "'\"")
	value = secure.Redact(value)
	value = commandSecret.ReplaceAllString(value, "$1[REDACTED]")
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		value = string(runes[:237]) + "…"
	}
	return value
}

func safeRepoPath(repo, value string) string {
	cleaned := filepath.Clean(value)
	if relative, err := filepath.Rel(repo, cleaned); err == nil && relative != "." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		cleaned = relative
	} else if filepath.IsAbs(cleaned) {
		cleaned = filepath.Base(cleaned)
	}
	return filepath.ToSlash(cleaned)
}
