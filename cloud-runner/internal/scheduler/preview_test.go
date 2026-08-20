package scheduler

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
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type fakeProvider struct {
	heartbeats int
	destroyed  []string
}

func (f *fakeProvider) Create(context.Context, sandbox.CreateSpec) (sandbox.Instance, error) {
	return sandbox.Instance{ID: "sandbox-1", State: "running"}, nil
}
func (f *fakeProvider) StartWorker(context.Context, string) (string, error) { return "session", nil }
func (f *fakeProvider) Heartbeat(context.Context, string) error {
	f.heartbeats++
	return nil
}
func (f *fakeProvider) Status(context.Context, string) (sandbox.Instance, error) {
	return sandbox.Instance{ID: "sandbox-1", State: "running"}, nil
}
func (f *fakeProvider) Destroy(_ context.Context, id string) error {
	f.destroyed = append(f.destroyed, id)
	return nil
}
func (f *fakeProvider) Forward(context.Context, string, int) (string, func(), error) {
	return "http://127.0.0.1:39999", func() {}, nil
}

func seedCompletedRun(t *testing.T, memory *store.Memory) model.Run {
	t.Helper()
	ctx := context.Background()
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
	run, err = memory.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func newTestScheduler(memory *store.Memory, provider sandbox.Provider) *Scheduler {
	return New(memory, provider, nil, events.NewBroker(), Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPublishedPreviewRetainsSandboxAndHeartbeats(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	future := time.Now().Add(30 * time.Minute)
	if err := memory.SetPreview(ctx, run.ID, "published", "https://previews.example.com/previews/"+run.ID+"/?cap=x", 3000, &future); err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	scheduler := newTestScheduler(memory, provider)
	current, _ := memory.GetRun(ctx, run.ID)
	if !scheduler.previewAlive(ctx, current) {
		t.Fatal("sandbox with a live preview must be retained")
	}
	if provider.heartbeats == 0 {
		t.Fatal("retained preview sandbox should be heartbeated")
	}
	scheduler.cleanupTerminal(ctx)
	if len(provider.destroyed) != 0 {
		t.Fatal("sandbox with a live preview must not be destroyed")
	}
}

func TestExpiredPreviewIsTornDownAndSandboxDestroyed(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	past := time.Now().Add(-time.Minute)
	if err := memory.SetPreview(ctx, run.ID, "published", "https://previews.example.com/previews/"+run.ID+"/?cap=x", 3000, &past); err != nil {
		t.Fatal(err)
	}
	// Ensure the run is outside the one-minute grace window.
	time.Sleep(2 * time.Millisecond)
	provider := &fakeProvider{}
	scheduler := newTestScheduler(memory, provider)
	manager := preview.NewManager(memory, provider, preview.NewBroker(time.Hour),
		"https://previews.example.com", time.Hour, 4*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	scheduler.SetPreviewManager(manager)
	current, _ := memory.GetRun(ctx, run.ID)
	if scheduler.previewAlive(ctx, current) {
		t.Fatal("expired preview must not keep the sandbox alive")
	}
	stored, _ := memory.GetRun(ctx, run.ID)
	if stored.PreviewState != "expired" {
		t.Fatalf("preview should be expired, got %q", stored.PreviewState)
	}
}

func TestCompletedRunWithoutPreviewStillCleansUp(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	provider := &fakeProvider{}
	scheduler := newTestScheduler(memory, provider)
	current, _ := memory.GetRun(ctx, run.ID)
	if scheduler.previewAlive(ctx, current) {
		t.Fatal("run without preview state must not retain its sandbox")
	}
}
