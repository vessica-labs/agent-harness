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
}
