package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func TestLaunchLeasesOneCodexSlotForOneTopLevelRun(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "owner", GitHubRepo: "repo", BaseBranch: "main", LinearWorkspaceID: "workspace", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "issue", IssueKey: "ISS-1", IssueTitle: "Issue"}); err != nil {
		t.Fatal(err)
	}
	run, err := memory.ClaimNextRun(ctx, "owner", 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secure.NewBox("12345678901234567890123456789012")
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Seal([]byte(`{"tokens":{"access_token":"test"}}`), secure.Purpose("codex", "codex-01"))
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.PutAuthSlot(ctx, model.AuthSlot{ID: "codex-01", State: "available", Ciphertext: ciphertext}); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	scheduler := New(memory, provider, box, events.NewBroker(), Config{Owner: "owner", ControlPlaneURL: "https://control.example", Checkpoint: "checkpoint"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.launch(ctx, run)
	if len(provider.created) != 1 {
		t.Fatalf("one available Codex slot should launch the run, sandbox creates = %d", len(provider.created))
	}
	encoded := provider.created[0].Variables["HARNESS_CODEX_SESSIONS_B64"]
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []map[string]any
	if err := json.Unmarshal(body, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0]["id"] != "codex-01" {
		t.Fatalf("worker must receive exactly one top-level Codex session: %+v", sessions)
	}
	if _, exists := provider.created[0].Variables["HARNESS_CODEX_PARALLEL_SAFE"]; exists {
		t.Fatal("obsolete per-process sharing flag was passed to the worker")
	}
}

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
