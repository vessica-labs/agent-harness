package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRailwayUpgradeCommandsUseBaseImageGitHubCLI(t *testing.T) {
	commands := railwayUpgradeCommands("v0.1.0-rc.28")
	if len(commands) == 0 {
		t.Fatal("upgrade commands were empty")
	}
	packageFields := strings.Fields(commands[0])
	for _, field := range packageFields {
		if field == "gh" {
			t.Fatal("upgrade package list must not reinstall the base image GitHub CLI")
		}
	}
	for _, required := range []string{"file", "patch", "python3-pip", "python3-venv", "unzip", "zip"} {
		found := false
		for _, field := range packageFields {
			found = found || field == required
		}
		if !found {
			t.Fatalf("worker checkpoint package baseline omitted %q", required)
		}
	}
	if !strings.Contains(strings.Join(commands, "\n"), "/v0.1.0-rc.28/") {
		t.Fatal("upgrade commands did not use the requested release version")
	}
	joined := strings.Join(commands, "\n")
	for _, required := range []string{"setup_24.x", "pnpm@11.21.0", "@playwright/test@1.62.1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("worker checkpoint omitted %q", required)
		}
	}
}

func TestRailwayUpgradeFinishesWithToolchainSmokeAndResolvedManifest(t *testing.T) {
	commands := railwayUpgradeCommands("v0.1.0-rc.28")
	last := commands[len(commands)-1]
	if output, err := exec.Command("bash", "-n", "-c", last).CombinedOutput(); err != nil {
		t.Fatalf("final checkpoint smoke is not valid shell: %v\n%s", err, output)
	}
	for _, command := range []string{
		"agent-harness", "chromium", "codex", "curl", "file", "gh", "git", "jq", "make", "node",
		"npm", "patch", "pip3", "playwright", "pnpm", "python3", "railway", "rg", "unzip", "zip",
	} {
		if !strings.Contains(last, command+" --") && !strings.Contains(last, "'"+command+"'") && !strings.Contains(last, " "+command+" ") {
			t.Fatalf("final checkpoint smoke omitted %q: %s", command, last)
		}
	}
	for _, required := range []string{
		"python3 -m pip --version", "python3 -m venv --help", "shutil.which", "'schema':2",
		"'binaries':paths", "/opt/agent-harness/runtime-manifest.json", "jq -e",
	} {
		if !strings.Contains(last, required) {
			t.Fatalf("final checkpoint smoke or manifest omitted %q", required)
		}
	}
}
