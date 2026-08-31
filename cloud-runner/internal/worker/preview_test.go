package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchPreviewReturnsWithoutWaitingForLongLivedCommand(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(workspace, "preview.log")
	started := time.Now()
	process, err := launchPreview(workspace, "sleep 30", 4173, logPath)
	if err != nil {
		t.Fatalf("launch preview: %v", err)
	}
	t.Cleanup(func() {
		_ = process.Kill()
		_, _ = process.Wait()
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("launch waited for long-lived preview: %s", elapsed)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("preview log was not created: %v", err)
	}
}
