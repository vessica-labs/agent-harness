package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func TestWorkerSessionStaleRequiresNoWorkerEvent(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "owner", GitHubRepo: "repo", LinearWorkspaceID: "workspace", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "issue", IssueKey: "ISS-1", IssueTitle: "Issue"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := memory.ClaimNextRun(ctx, "owner", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SetSandbox(ctx, run.ID, "sandbox-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AppendEvent(ctx, model.Event{RunID: run.ID, Type: "sandbox.started"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	scheduler := New(memory, nil, nil, events.NewBroker(), Config{StartupTimeout: time.Nanosecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	run.SandboxID = "sandbox-1"
	if !scheduler.workerSessionStale(ctx, run) {
		t.Fatal("expected detached session without worker event to be stale")
	}
	if _, err := memory.AppendEvent(ctx, model.Event{RunID: delivery.Run.ID, Type: "worker.starting"}); err != nil {
		t.Fatal(err)
	}
	if scheduler.workerSessionStale(ctx, run) {
		t.Fatal("worker startup event must suppress stale-session recovery")
	}
}
