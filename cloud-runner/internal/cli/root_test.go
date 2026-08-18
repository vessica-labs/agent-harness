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
