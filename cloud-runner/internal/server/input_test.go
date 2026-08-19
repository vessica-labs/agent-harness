package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func TestInputResponseAPIAnswersAndRequeuesRun(t *testing.T) {
	host, memory := teamTestServer(t)
	defer host.Close()
	owner := initializeOwner(t, host)
	ctx := context.Background()
	repository, err := memory.PutRepository(ctx, model.Repository{ID: "repo-input", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery-input", IssueID: "issue-input",
		IssueKey: "AGE-22", IssueTitle: "Question", ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SetRunState(ctx, claimed.Run.ID, "running", "product", ""); err != nil {
		t.Fatal(err)
	}
	request, err := memory.CreateInputRequest(ctx, model.InputRequest{RunID: claimed.Run.ID, Stage: "product", Round: 1,
		Summary: "Choose a mode", Questions: []model.InputQuestion{{ID: "mode", Prompt: "Which mode?", AllowFreeText: true,
			Required: true, Options: []model.InputOption{{ID: "guided", Label: "Guided", Recommended: true}, {ID: "automatic", Label: "Automatic"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"answers": []map[string]string{{"question_id": "mode", "option_id": "guided"}}}
	if status := callTeam(t, host.Client(), http.MethodPost, host.URL+"/v1/input-requests/"+request.ID+"/responses", owner.Tokens.AccessToken, input, nil); status != http.StatusAccepted {
		t.Fatalf("answer status %d", status)
	}
	stored, _ := memory.GetInputRequest(ctx, request.ID)
	run, _ := memory.GetRun(ctx, claimed.Run.ID)
	if stored.Status != "answered" || run.State != "queued" || run.QueueReason != "human_input_answered" {
		t.Fatalf("input response did not resume run: request=%+v run=%+v", stored, run)
	}
}

func TestInputRequestEventPolicyAllowsOnlyProductAndArchitecture(t *testing.T) {
	payload := []byte(`{"summary":"Choose","questions":[{"id":"choice","prompt":"Which?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"}],"allow_free_text":true}]}`)
	if _, err := decodeInputRequestEvent("run", "product", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeInputRequestEvent("run", "coder", payload); err == nil {
		t.Fatal("coder human-input request was accepted")
	}
}
