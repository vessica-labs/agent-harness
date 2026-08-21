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

func TestNonLinearInputAnswerCreatesOneLinearComment(t *testing.T) {
	commentCreates := 0
	var commentBody string
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
			_, _ = io.WriteString(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"source","url":"https://linear.test/AGE-1"}}}}`)
		case strings.Contains(envelope.Query, "HarnessIssue"):
			_, _ = io.WriteString(w, `{"data":{"issue":{"id":"source","comments":{"nodes":[]},"children":{"nodes":[]}}}}`)
		case strings.Contains(envelope.Query, "HarnessCommentCreate"):
			commentCreates++
			commentBody, _ = envelope.Variables["body"].(string)
			_, _ = io.WriteString(w, `{"data":{"commentCreate":{"success":true,"comment":{"id":"answer-comment"}}}}`)
		default:
			t.Fatalf("unexpected Linear query: %s", envelope.Query)
		}
	}))
	defer linearHost.Close()

	ctx := context.Background()
	memory := store.NewMemory()
	repository, _ := memory.PutRepository(ctx, model.Repository{ID: "repo", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	claimed, _ := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "source",
		IssueKey: "AGE-1", IssueTitle: "Feature", ReceivedAt: time.Now().UTC()})
	request := model.InputRequest{ID: "input-1", RunID: claimed.Run.ID, Stage: "product", Summary: "Choose",
		Questions: []model.InputQuestion{{ID: "mode", Prompt: "Which mode?", Options: []model.InputOption{{ID: "guided", Label: "Guided"}}}}}
	response := model.InputResponse{Channel: "slack", ActorName: "Taylor",
		Answers: []model.InputAnswer{{QuestionID: "slack_reply", Text: "Use the existing flow."}}}
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.linear = func(token string) *linearapi.Client { return linearapi.NewWithEndpoint(token, linearHost.URL) }
	credential, _ := json.Marshal(linearapi.OAuthCredential{AccessToken: "linear-token", ExpiresAt: time.Now().Add(time.Hour)})
	if err := server.putCredential(ctx, "linear_oauth", credential); err != nil {
		t.Fatal(err)
	}
	if err := server.syncLinearInputAnswered(ctx, request, response); err != nil {
		t.Fatal(err)
	}
	if err := server.syncLinearInputAnswered(ctx, request, response); err != nil {
		t.Fatal(err)
	}
	if commentCreates != 1 || !strings.Contains(commentBody, "Input answered via slack") ||
		!strings.Contains(commentBody, "Use the existing flow") {
		t.Fatalf("unexpected answer projection: creates=%d body=%q", commentCreates, commentBody)
	}
	linearRequest := request
	linearRequest.ID = "input-2"
	if err := server.syncLinearInputAnswered(ctx, linearRequest, model.InputResponse{Channel: "linear",
		Answers: []model.InputAnswer{{QuestionID: "linear_reply", Text: "Already in Linear."}}}); err != nil {
		t.Fatal(err)
	}
	if commentCreates != 1 {
		t.Fatalf("Linear-origin answer was duplicated into a new comment: creates=%d", commentCreates)
	}
}

func TestControlPlaneInputAnswerUsesWebUILabelAndSelectedOption(t *testing.T) {
	request := model.InputRequest{Questions: []model.InputQuestion{{ID: "mode", Prompt: "Which mode?",
		Options: []model.InputOption{{ID: "guided", Label: "Guided"}}}}}
	response := model.InputResponse{Channel: "control_plane", Answers: []model.InputAnswer{{QuestionID: "mode", OptionID: "guided"}}}
	body := renderLinearInputAnswer("marker", request, response)
	if !strings.Contains(body, "Input answered via web UI") || !strings.Contains(body, "Selected: **Guided**") {
		t.Fatalf("unexpected web UI answer: %q", body)
	}
}
