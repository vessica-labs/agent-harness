package cli

import (
	"strings"
	"testing"
)

func TestRailwayUpgradeCommandsUseBaseImageGitHubCLI(t *testing.T) {
	commands := railwayUpgradeCommands("v0.1.0-rc.28")
	if len(commands) == 0 {
		t.Fatal("upgrade commands were empty")
	}
	for _, field := range strings.Fields(commands[0]) {
		if field == "gh" {
			t.Fatal("upgrade package list must not reinstall the base image GitHub CLI")
		}
	}
	if !strings.Contains(strings.Join(commands, "\n"), "/v0.1.0-rc.28/") {
		t.Fatal("upgrade commands did not use the requested release version")
	}
	joined := strings.Join(commands, "\n")
	for _, required := range []string{"setup_24.x", "pnpm@11.21.0", "@playwright/test@1.62.1", `"playwright":"1.62.1"`, `"chromium":"/usr/bin/chromium"`} {
		if !strings.Contains(joined, required) {
			t.Fatalf("worker checkpoint omitted %q", required)
		}
	}
}
