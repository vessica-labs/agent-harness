package server

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/preview"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/sandbox"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type expiringPreviewStore struct {
	store.Store
}

func (s *expiringPreviewStore) SetPreview(ctx context.Context, runID, state, previewURL string, port int, expiresAt *time.Time) error {
	if err := s.Store.SetPreview(ctx, runID, state, previewURL, port, expiresAt); err != nil {
		return err
	}
	if state == "published" {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type previewForwarder struct{}

func (previewForwarder) Forward(context.Context, string, int) (string, func(), error) {
	return "http://127.0.0.1:39999", func() {}, nil
}

var _ sandbox.Forwarder = previewForwarder{}

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

func TestPreviewPublicationFailureUsesFreshReconciliationContext(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "owner", GitHubRepo: "repo", LinearWorkspaceID: "workspace", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "issue", IssueKey: "ISS-1", IssueTitle: "Issue"}); err != nil {
		t.Fatal(err)
	}
	run, err := memory.ClaimNextRun(ctx, "owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SetSandbox(ctx, run.ID, "sandbox-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := memory.SetRunState(ctx, run.ID, "completed", "pr", ""); err != nil {
		t.Fatal(err)
	}
	if err := memory.SetPreview(ctx, run.ID, "ready", "", 3000, nil); err != nil {
		t.Fatal(err)
	}

	values := &expiringPreviewStore{Store: memory}
	key, err := secure.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	box, err := secure.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Config{}, values, box, events.NewBroker(), logger)
	manager := preview.NewManager(values, previewForwarder{}, preview.NewBroker(time.Hour), "https://previews.example.com", time.Hour, 4*time.Hour, logger)
	server.SetPreviewManager(manager)

	server.publishPreviewWithTimeout(run.ID, 10*time.Millisecond)
	stored, err := memory.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PreviewState != "failed" || stored.PreviewURL != "" {
		t.Fatalf("expired publication context was reused during reconciliation: %+v", stored)
	}
	if manager.Broker.Registered(run.ID) {
		t.Fatal("failed preview retained a broker target")
	}
}
