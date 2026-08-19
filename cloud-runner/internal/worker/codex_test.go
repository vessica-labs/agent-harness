package worker

import (
	"strings"
	"testing"
)

func TestCodexErrorMessageReadsStructuredTurnFailure(t *testing.T) {
	line := []byte(`{"type":"turn.failed","error":{"message":"model is not supported"}}`)
	if got := codexErrorMessage(line); got != "model is not supported" {
		t.Fatalf("unexpected Codex error: %q", got)
	}
}

func TestCodexFailureDetailFallsBackToStructuredError(t *testing.T) {
	if got := codexFailureDetail("", "model is not supported"); got != "model is not supported" {
		t.Fatalf("unexpected failure detail: %q", got)
	}
	got := codexFailureDetail("transport failed", "model is not supported")
	if !strings.Contains(got, "transport failed") || !strings.Contains(got, "model is not supported") {
		t.Fatalf("combined failure detail lost context: %q", got)
	}
}
