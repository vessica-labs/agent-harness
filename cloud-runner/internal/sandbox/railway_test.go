package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRailwayDestroyUsesDocumentedIDFlag(t *testing.T) {
	directory := t.TempDir()
	arguments := filepath.Join(directory, "arguments")
	binary := filepath.Join(directory, "railway")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$HARNESS_TEST_ARGUMENTS\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_TEST_ARGUMENTS", arguments)
	provider := RailwayCLI{Binary: binary, Project: "project", Environment: "production", APIToken: "token"}
	if err := provider.Destroy(context.Background(), "sandbox-123"); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	command := string(value)
	if !strings.Contains(command, "sandbox destroy") || !strings.Contains(command, "--id sandbox-123") {
		t.Fatalf("unexpected Railway command: %s", command)
	}
}
