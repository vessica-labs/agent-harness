package cli

import "testing"

func TestVersionStringUsesBuildVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	Version = "v0.1.0-rc.2"
	if got, want := versionString(), "agent-harness v0.1.0-rc.2"; got != want {
		t.Fatalf("versionString() = %q, want %q", got, want)
	}
}

func TestEnvStringMap(t *testing.T) {
	t.Setenv("HARNESS_TEST_MAP", `{"repo-1":"checkpoint-1"}`)
	if got := envStringMap("HARNESS_TEST_MAP")["repo-1"]; got != "checkpoint-1" {
		t.Fatalf("checkpoint=%q", got)
	}
	t.Setenv("HARNESS_TEST_MAP", "not-json")
	if got := envStringMap("HARNESS_TEST_MAP"); len(got) != 0 {
		t.Fatalf("invalid map=%v", got)
	}
}
