package server

import (
	"testing"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func TestPreviewPublicationRequiresCompletedRunAndReadyPort(t *testing.T) {
	ready := model.Run{State: "completed", PreviewState: "ready", PreviewPort: 4173, SandboxID: "sandbox-1"}
	if !previewPublishable(ready) {
		t.Fatal("completed run with a ready preview was not publishable")
	}

	for name, run := range map[string]model.Run{
		"running":         {State: "running", PreviewState: "ready", PreviewPort: 4173, SandboxID: "sandbox-1"},
		"preview pending": {State: "completed", PreviewState: "pending", PreviewPort: 4173, SandboxID: "sandbox-1"},
		"missing port":    {State: "completed", PreviewState: "ready", SandboxID: "sandbox-1"},
		"missing sandbox": {State: "completed", PreviewState: "ready", PreviewPort: 4173},
	} {
		t.Run(name, func(t *testing.T) {
			if previewPublishable(run) {
				t.Fatalf("run should not be publishable: %+v", run)
			}
		})
	}
}
