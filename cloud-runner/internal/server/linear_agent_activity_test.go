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
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func TestLinearAgentActivityProjectsCodexActionsAndFullQuestion(t *testing.T) {
	run := model.Run{ID: "run-1"}
	commandPayload := json.RawMessage(`{"item_id":"cmd-1","command":"npm test","exit_code":0,"duration_ms":1250}`)
	startedKey, started, ok := linearAgentActivity(run, model.Event{Type: "codex.command.started", Payload: commandPayload})
	if !ok || startedKey != "agent:codex:command:cmd-1" || started["action"] != "Run command" || started["parameter"] != "npm test" {
		t.Fatalf("unexpected command activity: key=%q content=%#v", startedKey, started)
	}
	completedKey, completed, ok := linearAgentActivity(run, model.Event{Type: "codex.command.completed", Payload: commandPayload})
	if !ok || completedKey != startedKey || !strings.Contains(completed["result"].(string), "1.2s") {
		t.Fatalf("command completion should coalesce with its start: key=%q content=%#v", completedKey, completed)
	}
	filesKey, files, ok := linearAgentActivity(run, model.Event{Type: "codex.files.completed",
		Payload: json.RawMessage(`{"item_id":"edit-1","paths":["server.go","server_test.go"]}`)})
	if !ok || filesKey != "agent:codex:files:edit-1" || files["action"] != "Edit" ||
		!strings.Contains(files["parameter"].(string), "server_test.go") {
		t.Fatalf("unexpected file activity: key=%q content=%#v", filesKey, files)
	}

	questionPayload := json.RawMessage(`{"request_id":"input-1","summary":"Choose the rollout mode","questions":[{"id":"mode","prompt":"Which rollout?","why":"This changes risk.","options":[{"id":"gradual","label":"Gradual","description":"Start small.","recommended":true},{"id":"all","label":"All at once","description":"Move faster."}],"allow_free_text":true}]}`)
	questionKey, question, ok := linearAgentActivity(run, model.Event{Type: "human_input.requested", Stage: "product", Payload: questionPayload})
	body, _ := question["body"].(string)
	if !ok || questionKey != "agent:input:input-1" || question["type"] != "elicitation" ||
		!strings.Contains(body, "Which rollout?") || !strings.Contains(body, "Gradual (recommended)") ||
		!strings.Contains(body, "Reply in this Agent Session") {
		t.Fatalf("unexpected input activity: key=%q content=%#v", questionKey, question)
	}
	for _, eventType := range []string{"codex.command.started", "codex.command.completed", "codex.files.started", "codex.files.completed"} {
		if !shouldSyncLinearLifecycleEvent(eventType) {
			t.Fatalf("%s must be projected to the Linear Agent Session", eventType)
		}
	}
}

func TestLinearAgentSessionQuestionDeliveryAcceptsPrompt(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, _ := memory.PutRepository(ctx, model.Repository{ID: "repo", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	metadata, _ := json.Marshal(map[string]any{"agent_session": map[string]any{"id": "session-1"}})
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{DeliveryID: "delivery", IssueID: "source",
		IssueKey: "AGE-1", IssueTitle: "Feature", SourceContext: metadata, ReceivedAt: time.Now().UTC()})
	if err != nil || claimed.Run == nil {
		t.Fatalf("seed run: %+v %v", claimed, err)
	}
	if err := memory.SetRunState(ctx, claimed.Run.ID, "running", "product", ""); err != nil {
		t.Fatal(err)
	}
	request, err := memory.CreateInputRequest(ctx, model.InputRequest{ID: "input-1", RunID: claimed.Run.ID, Stage: "product",
		Summary: "Choose", Questions: []model.InputQuestion{{ID: "mode", Prompt: "Which mode?", AllowFreeText: true,
			Options: []model.InputOption{{ID: "guided", Label: "Guided", Recommended: true}, {ID: "fast", Label: "Fast"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	linearHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"agentActivityCreate":{"success":true,"agentActivity":{"id":"activity-1"}}}}`)
	}))
	defer linearHost.Close()
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	eventPayload, _ := json.Marshal(map[string]any{"request_id": request.ID, "summary": request.Summary, "questions": request.Questions})
	event := model.Event{RunID: claimed.Run.ID, Type: "human_input.requested", Stage: "product", Payload: eventPayload}
	if err := server.syncLinearAgentLifecycleActivity(ctx, linearapi.NewWithEndpoint("token", linearHost.URL), *claimed.Run, event); err != nil {
		t.Fatal(err)
	}
	delivered, err := memory.FindInputRequestByDelivery(ctx, "linear-agent", "session-1")
	if err != nil || delivered.ID != request.ID {
		t.Fatalf("question was not addressable by Agent Session: %+v %v", delivered, err)
	}
	server.processLinearAgentPrompt(linear.ParsedWebhook{AgentSessionID: "session-1", AgentActivityID: "prompt-1",
		AgentPromptBody: "Use Guided.", ActorID: "user-1", ActorName: "Taylor"})
	answered, err := memory.GetInputRequest(ctx, request.ID)
	if err != nil || answered.Status != "answered" {
		t.Fatalf("prompt did not answer request: %+v %v", answered, err)
	}
	responses, _ := memory.ListInputResponses(ctx, request.ID)
	if len(responses) != 1 || responses[0].Channel != "linear" || responses[0].ExternalID != "prompt-1" ||
		responses[0].Answers[0].Text != "Use Guided." {
		t.Fatalf("unexpected Agent Session response: %+v", responses)
	}
	resumed, _ := memory.GetRun(ctx, claimed.Run.ID)
	if resumed.State != "queued" || resumed.QueueReason != "human_input_answered" {
		t.Fatalf("run was not queued to resume: %+v", resumed)
	}
}
