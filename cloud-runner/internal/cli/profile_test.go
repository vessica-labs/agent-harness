package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryProfileNameUsesNearestHarnessConfig(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "worktrees", "ticket")
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "version: 1\ncloud:\n  profile: vessica-cli\n"
	if err := os.WriteFile(filepath.Join(root, ".harness", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := repositoryProfileName(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != "vessica-cli" {
		t.Fatalf("repositoryProfileName() = %q, want vessica-cli", got)
	}
}

func TestRequestedProfileNamePrecedence(t *testing.T) {
	t.Setenv("AGENT_HARNESS_PROFILE", "from-environment")
	got, err := requestedProfileName("explicit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "explicit" {
		t.Fatalf("explicit profile = %q", got)
	}
	got, err = requestedProfileName("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-environment" {
		t.Fatalf("environment profile = %q", got)
	}
}

func TestCloudCommandProfile(t *testing.T) {
	profile, args, err := cloudCommandProfile([]string{"--profile", "marketing", "repo", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if profile != "marketing" || len(args) != 2 || args[0] != "repo" || args[1] != "list" {
		t.Fatalf("profile=%q args=%v", profile, args)
	}
}
