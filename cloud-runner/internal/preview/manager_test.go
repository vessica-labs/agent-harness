package preview

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

type fakeForwarder struct {
	forwards int
	stopped  int
	fail     bool
}

func (f *fakeForwarder) Forward(_ context.Context, _ string, _ int) (string, func(), error) {
	if f.fail {
		return "", nil, context.DeadlineExceeded
	}
	f.forwards++
	return "http://127.0.0.1:39999", func() { f.stopped++ }, nil
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
	if err := memory.SetPreview(ctx, run.ID, "ready", "", 3000, nil); err != nil {
		t.Fatal(err)
	}
	run, err = memory.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func newTestManager(memory *store.Memory, forwarder *fakeForwarder) *Manager {
	return NewManager(memory, forwarder, NewBroker(time.Hour), "https://previews.example.com",
		time.Hour, 4*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPublishMintsCapabilityURLAndPersistsState(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	forwarder := &fakeForwarder{}
	manager := newTestManager(memory, forwarder)
	url, err := manager.Publish(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "https://previews.example.com/previews/"+run.ID+"/?cap=") {
		t.Fatalf("unexpected preview URL %q", url)
	}
	stored, err := memory.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PreviewState != "published" || stored.PreviewURL != url || stored.PreviewExpiresAt == nil {
		t.Fatalf("preview state not persisted: %+v", stored)
	}
	if !manager.Broker.Registered(run.ID) || forwarder.forwards != 1 {
		t.Fatal("forward should be registered exactly once")
	}
}

func TestPublishSerializesConcurrentTriggers(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	forwarder := &fakeForwarder{}
	manager := newTestManager(memory, forwarder)
	urls := make(chan string, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			url, err := manager.Publish(ctx, run)
			urls <- url
			errs <- err
		}()
	}
	group.Wait()
	close(urls)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var published string
	for url := range urls {
		if published == "" {
			published = url
		} else if url != published {
			t.Fatalf("concurrent triggers returned different capabilities: %q != %q", url, published)
		}
	}
	if forwarder.forwards != 1 {
		t.Fatalf("concurrent triggers created %d forwards, want 1", forwarder.forwards)
	}
}

func TestRecordFailureDoesNotOverwritePublishedPreview(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	manager := newTestManager(memory, &fakeForwarder{})
	url, err := manager.Publish(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := manager.RecordFailure(ctx, run.ID, run.PreviewPort)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("a late failure must not overwrite a published preview")
	}
	stored, err := memory.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PreviewState != "published" || stored.PreviewURL != url {
		t.Fatalf("published preview was overwritten: %+v", stored)
	}
}

func TestPublishRequiresSandboxAndPort(t *testing.T) {
	manager := newTestManager(store.NewMemory(), &fakeForwarder{})
	if _, err := manager.Publish(context.Background(), model.Run{ID: "run"}); err == nil {
		t.Fatal("publish without sandbox/port must fail")
	}
}

func TestPublishRejectsLoopbackPublicOrigin(t *testing.T) {
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	manager := NewManager(memory, &fakeForwarder{}, NewBroker(time.Hour), "http://127.0.0.1:8080",
		time.Hour, 4*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := manager.Publish(context.Background(), run); err == nil {
		t.Fatal("loopback public origin must be rejected")
	}
}

func TestExpireStopsForwardAndMarksRun(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	forwarder := &fakeForwarder{}
	manager := newTestManager(memory, forwarder)
	if _, err := manager.Publish(ctx, run); err != nil {
		t.Fatal(err)
	}
	run, _ = memory.GetRun(ctx, run.ID)
	manager.Expire(ctx, run)
	if manager.Broker.Registered(run.ID) || forwarder.stopped != 1 {
		t.Fatal("expire should remove the target and stop the forward")
	}
	stored, _ := memory.GetRun(ctx, run.ID)
	if stored.PreviewState != "expired" || stored.PreviewURL != "" {
		t.Fatalf("expired state not persisted: %+v", stored)
	}
}

func TestRestoreRebuildsUnexpiredPreviewsAndExpiresStaleOnes(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	run := seedCompletedRun(t, memory)
	forwarder := &fakeForwarder{}
	manager := newTestManager(memory, forwarder)
	url, err := manager.Publish(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a restart with a fresh broker/manager sharing the same store.
	restarted := newTestManager(memory, forwarder)
	restarted.Restore(ctx)
	if !restarted.Broker.Registered(run.ID) {
		t.Fatal("restore should re-register the unexpired preview")
	}
	token := strings.SplitN(url, "cap=", 2)[1]
	if runID, ok := restarted.Broker.touch(token); !ok || runID != run.ID {
		t.Fatal("restored capability should still authorize the run")
	}
	// Now expire it durably and restore again.
	past := time.Now().Add(-time.Minute)
	if err := memory.SetPreview(ctx, run.ID, "published", url, 3000, &past); err != nil {
		t.Fatal(err)
	}
	final := newTestManager(memory, forwarder)
	final.Restore(ctx)
	if final.Broker.Registered(run.ID) {
		t.Fatal("expired preview must not be restored")
	}
	stored, _ := memory.GetRun(ctx, run.ID)
	if stored.PreviewState != "expired" {
		t.Fatalf("stale preview should be marked expired, got %q", stored.PreviewState)
	}
}
