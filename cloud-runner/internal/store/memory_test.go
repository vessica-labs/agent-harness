package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func repository(t *testing.T, m *Memory) model.Repository {
	t.Helper()
	value, err := m.PutRepository(context.Background(), model.Repository{Name: "repo", GitHubOwner: "v", GitHubRepo: "r", LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestExactlyOneRunUnderConcurrentDeliveries(t *testing.T) {
	ctx := context.Background()
	memory := NewMemory()
	repo := repository(t, memory)
	var wait sync.WaitGroup
	ids := make(chan string, 100)
	for i := 0; i < 100; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			result, err := memory.AcceptLinearDelivery(ctx, repo, model.LinearDelivery{DeliveryID: fmt.Sprintf("d-%d", i), IssueID: "issue-one", IssueKey: "ENG-1", IssueTitle: "One", ReceivedAt: time.Now()})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- result.Run.ID
		}(i)
	}
	wait.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("created %d runs", len(unique))
	}
	runs, _ := memory.ListRuns(ctx, model.RunFilter{})
	if len(runs) != 1 {
		t.Fatalf("stored %d runs", len(runs))
	}
}

func TestConcurrencyLimitAndEventOrder(t *testing.T) {
	ctx := context.Background()
	memory := NewMemory()
	repo := repository(t, memory)
	for i := 0; i < 4; i++ {
		_, err := memory.AcceptLinearDelivery(ctx, repo, model.LinearDelivery{DeliveryID: fmt.Sprintf("d-%d", i), IssueID: fmt.Sprintf("i-%d", i), IssueKey: fmt.Sprintf("ENG-%d", i), IssueTitle: "x", ReceivedAt: time.Now().Add(time.Duration(i) * time.Millisecond)})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := memory.ClaimNextRun(ctx, "owner", 3, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := memory.ClaimNextRun(ctx, "owner", 3, time.Minute); err != ErrNoRunnableRun {
		t.Fatalf("got %v", err)
	}
	runs, _ := memory.ListRuns(ctx, model.RunFilter{State: "queued"})
	if len(runs) != 1 || runs[0].QueueReason != "concurrency_limit" {
		t.Fatalf("queued run has no visible concurrency reason: %+v", runs)
	}
	events, _ := memory.ListEvents(ctx, model.EventFilter{})
	for i := 1; i < len(events); i++ {
		if events[i].GlobalSeq <= events[i-1].GlobalSeq {
			t.Fatal("events not ordered")
		}
	}
}

func TestAuthSlotExclusiveLease(t *testing.T) {
	ctx := context.Background()
	memory := NewMemory()
	if err := memory.PutAuthSlot(ctx, model.AuthSlot{ID: "slot", State: "available", Ciphertext: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	slots, err := memory.LeaseAuthSlots(ctx, "run-1", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.LeaseAuthSlots(ctx, "run-2", 1, time.Minute); err != ErrNoAuthSlot {
		t.Fatalf("got %v", err)
	}
	if err := memory.ReleaseAuthSlot(ctx, slots[0].ID, "run-1", []byte("updated"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.LeaseAuthSlots(ctx, "run-2", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSlotBatchLeaseIsAtomic(t *testing.T) {
	ctx := context.Background()
	memory := NewMemory()
	for _, id := range []string{"slot-a", "slot-b"} {
		if err := memory.PutAuthSlot(ctx, model.AuthSlot{ID: id, State: "available", Ciphertext: []byte(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := memory.LeaseAuthSlots(ctx, "run-too-large", 3, time.Minute); err != ErrNoAuthSlot {
		t.Fatalf("got %v", err)
	}
	values, err := memory.LeaseAuthSlots(ctx, "run-ok", 2, time.Minute)
	if err != nil || len(values) != 2 {
		t.Fatalf("atomic batch was not left available: %v %+v", err, values)
	}
	if err := memory.PutAuthSlot(ctx, model.AuthSlot{ID: "slot-a", State: "available", Ciphertext: []byte("replacement")}); err != ErrConflict {
		t.Fatalf("leased credential was overwritten: %v", err)
	}
}
