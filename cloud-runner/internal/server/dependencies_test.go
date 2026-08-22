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

func TestLinearDependencyWaiterReleasesOnlyAfterAllDependenciesAreDone(t *testing.T) {
	done := map[string]bool{"AGE-22": false, "AGE-23": true}
	linearHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_, _ = io.WriteString(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"dependent","url":"https://linear.test/AGE-24"}}}}`)
		case strings.Contains(envelope.Query, "HarnessCommentCreate"):
			_, _ = io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1"}}}}`)
		case strings.Contains(envelope.Query, "HarnessIssue"):
			key, _ := envelope.Variables["id"].(string)
			stateType := "started"
			if done[key] {
				stateType = "completed"
			}
			_, _ = io.WriteString(w, `{"data":{"issue":{"id":"`+key+`","identifier":"`+key+`","state":{"id":"state","name":"State","type":"`+stateType+`"},"comments":{"nodes":[]},"children":{"nodes":[]}}}}`)
		default:
			t.Fatalf("unexpected Linear query: %s", envelope.Query)
		}
	}))
	defer linearHost.Close()

	ctx := context.Background()
	memory := store.NewMemory()
	repository, _ := memory.PutRepository(ctx, model.Repository{ID: "repo", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	metadata, _ := json.Marshal(map[string]any{"dependencies": []string{"AGE-22", "AGE-23"}})
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "dependent",
		IssueKey: "AGE-24", IssueTitle: "Dependent", SourceContext: metadata,
		QueueReason: "dependencies_pending: AGE-22", ReceivedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.linear = func(token string) *linearapi.Client { return linearapi.NewWithEndpoint(token, linearHost.URL) }
	credential, _ := json.Marshal(linearapi.OAuthCredential{AccessToken: "linear-token", ExpiresAt: time.Now().Add(time.Hour)})
	if err := server.putCredential(ctx, "linear_oauth", credential); err != nil {
		t.Fatal(err)
	}
	client := linearapi.NewWithEndpoint("linear-token", linearHost.URL)
	if err := server.releaseLinearDependencyWaiters(ctx, repository, client, "AGE-23"); err != nil {
		t.Fatal(err)
	}
	waiting, _ := memory.GetRun(ctx, claimed.Run.ID)
	if waiting.QueueReason != "dependencies_pending: AGE-22" {
		t.Fatalf("run released before every dependency was Done: %+v", waiting)
	}
	done["AGE-22"] = true
	if err := server.releaseLinearDependencyWaiters(ctx, repository, client, "AGE-22"); err != nil {
		t.Fatal(err)
	}
	released, _ := memory.GetRun(ctx, claimed.Run.ID)
	if released.QueueReason != "" {
		t.Fatalf("run remained blocked after dependencies completed: %+v", released)
	}
	if _, err := memory.ClaimNextRun(ctx, "owner", 1, time.Minute); err != nil {
		t.Fatalf("released run was not claimable: %v", err)
	}
}

func TestRunDependencyKeysRepairsLegacyLinkSlugMetadata(t *testing.T) {
	metadata, _ := json.Marshal(map[string]any{"dependencies": []string{"VES-9", "MVP-410"}})
	run := model.Run{
		FeatureRequest: "Depends on [VES-9](https://linear.app/vessica/issue/VES-9/mvp-410-verify-users)",
		Metadata:       metadata,
	}
	dependencies := runDependencyKeys(run)
	if strings.Join(dependencies, ",") != "VES-9" {
		t.Fatalf("legacy metadata was not repaired from source text: %v", dependencies)
	}
}
