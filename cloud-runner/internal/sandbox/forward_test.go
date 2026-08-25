package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureRailwaySSHIdentityGeneratesRuntimeKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	home := t.TempDir()
	helper := filepath.Join(t.TempDir(), "ssh-keygen")
	script := `#!/bin/sh
for last do :; done
printf private > "$last"
printf public > "$last.pub"
`
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := ensureRailwaySSHIdentity(context.Background(), home, helper); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(home, ".ssh", "id_ed25519")
	info, err := os.Stat(identity)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("identity permissions = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(identity))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("SSH directory permissions = %o, want 700", got)
	}
}

func TestEnsureRailwaySSHIdentityKeepsExistingKey(t *testing.T) {
	home := t.TempDir()
	identity := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(identity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureRailwaySSHIdentity(context.Background(), home, filepath.Join(home, "missing-keygen")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(identity)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("existing identity was replaced: %q", content)
	}
}

func TestEnsureRailwaySSHIdentityReportsGenerationFailure(t *testing.T) {
	err := ensureRailwaySSHIdentity(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "missing-keygen"))
	if err == nil || !strings.Contains(err.Error(), "generate Railway sandbox SSH identity") {
		t.Fatalf("error = %v, want generation context", err)
	}
}
