package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func TestLinearParentLifecycleFollowsRunAndMergeEvents(t *testing.T) {
	var transitions []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(envelope.Query, "HarnessWorkflowStates"):
			_, _ = io.WriteString(w, `{"data":{"workflowStates":{"nodes":[{"id":"todo","name":"Todo","type":"unstarted","position":1},{"id":"progress","name":"In Progress","type":"started","position":2},{"id":"input","name":"Needs Input","type":"started","position":3},{"id":"review","name":"For Review","type":"started","position":4},{"id":"done","name":"Done","type":"completed","position":5}]}}}`)
		case strings.Contains(envelope.Query, "HarnessIssueState"):
			state := envelope.Variables["stateId"].(string)
			transitions = append(transitions, state)
			_, _ = io.WriteString(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"source","url":"https://linear.test/AGE-1"}}}}`)
		case strings.Contains(envelope.Query, "HarnessIssue"):
			_, _ = io.WriteString(w, `{"data":{"issue":{"id":"source","comments":{"nodes":[]},"children":{"nodes":[]}}}}`)
		case strings.Contains(envelope.Query, "HarnessCommentCreate"):
			_, _ = io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1"}}}}`)
		default:
			t.Fatalf("unexpected Linear query: %s", envelope.Query)
		}
	}))
	defer host.Close()

	ctx := context.Background()
	memory := store.NewMemory()
	repository, _ := memory.PutRepository(ctx, model.Repository{ID: "repo", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "source",
		IssueKey: "AGE-1", IssueURL: "https://linear.test/AGE-1", IssueTitle: "Feature", ReceivedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.linear = func(token string) *linearapi.Client { return linearapi.NewWithEndpoint(token, host.URL) }
	credential, _ := json.Marshal(linearapi.OAuthCredential{AccessToken: "linear-token", ExpiresAt: time.Now().Add(time.Hour)})
	if err := server.putCredential(ctx, "linear_oauth", credential); err != nil {
		t.Fatal(err)
	}

	events := []model.Event{
		{Type: "run.queued", Message: "claimed"},
		{Type: "stage.started", Stage: "product", Message: "started"},
		{Type: "stage.completed", Stage: "product", Message: "completed"},
		{Type: "run.completed", Message: "complete"},
		{Type: "pr.merged", Stage: "pr", Message: "merged"},
	}
	for _, event := range events {
		if err := server.syncLinearLifecycleEvent(ctx, claimed.Run.ID, event); err != nil {
			t.Fatalf("sync %s: %v", event.Type, err)
		}
	}
	if strings.Join(transitions, ",") != "todo,progress,review,done" {
		t.Fatalf("unexpected lifecycle transitions: %v", transitions)
	}
	for _, key := range []string{"activity:run:queued", "activity:stage:product:started", "activity:stage:product:completed", "activity:run:completed", "activity:pr:merged"} {
		projection, err := memory.GetExternalSync(ctx, claimed.Run.ID, key, "linear")
		if err != nil || projection.State != "synced" {
			t.Fatalf("activity %s was not durably synchronized: %+v %v", key, projection, err)
		}
	}
}
