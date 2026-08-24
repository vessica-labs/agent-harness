package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/events"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/secure"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func testTeamToken(t *testing.T, memory *store.Memory, box *secure.Box, role string) string {
	t.Helper()
	token := "team-access-" + role
	now := time.Now().UTC()
	member := model.Member{ID: "member-" + role, DisplayName: "Test " + role, Role: role, State: "active", CreatedAt: now}
	session := model.MemberSession{ID: "session-" + role, MemberID: member.ID, DeviceName: "test", AccessTokenHash: box.TokenDigest("access", token), RefreshTokenHash: box.TokenDigest("refresh", "refresh-"+role), AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(24 * time.Hour), CreatedAt: now}
	if err := memory.InitializeTeam(context.Background(), member, session, model.AuthAudit{Action: "test.initialized"}); err != nil {
		t.Fatal(err)
	}
	return token
}

func TestPipelineStageRegistrationPreservesDurableExecutionState(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	existing := model.StageState{
		RunID:       "run-resumed",
		Stage:       "product",
		State:       "completed",
		Attempt:     2,
		Details:     json.RawMessage(`{"summary":"validated"}`),
		StartedAt:   &started,
		CompletedAt: &completed,
	}
	if err := memory.PutStage(ctx, existing); err != nil {
		t.Fatal(err)
	}

	registered := model.StageState{
		RunID:   existing.RunID,
		Stage:   existing.Stage,
		State:   "pending",
		Details: json.RawMessage(`{"order":0}`),
	}
	got := preserveRegisteredStage(ctx, memory, registered)
	if got.State != "completed" || got.Attempt != 2 || got.StartedAt == nil || got.CompletedAt == nil ||
		string(got.Details) != string(existing.Details) {
		t.Fatalf("replayed registration downgraded stage: %+v", got)
	}

	newStage := preserveRegisteredStage(ctx, memory, model.StageState{RunID: existing.RunID, Stage: "arch", State: "pending"})
	if newStage.State != "pending" || newStage.Stage != "arch" {
		t.Fatalf("new registration was not preserved: %+v", newStage)
	}
}

func TestDelegatedAgentSessionDispatchesAndAcknowledgesNatively(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	_, err := memory.PutRepository(ctx, model.Repository{ID: "repo-1", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", LinearAgentName: "Vessica", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var activities atomic.Int32
	linearHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(envelope.Query, "HarnessIssueContext"):
			_, _ = io.WriteString(w, `{"data":{"issue":{"id":"issue-7","identifier":"ENG-7","title":"Native dispatch","url":"https://linear.test/ENG-7","description":"Implement it","team":{"id":"team"},"delegate":{"id":"app-user","name":"Vessica"},"comments":{"nodes":[]},"attachments":{"nodes":[]}}}}`)
		case strings.Contains(envelope.Query, "HarnessAgentActivityCreate"):
			activities.Add(1)
			_, _ = io.WriteString(w, `{"data":{"agentActivityCreate":{"success":true,"agentActivity":{"id":"activity-1"}}}}`)
		case strings.Contains(envelope.Query, "HarnessWorkflowStates"):
			_, _ = io.WriteString(w, `{"data":{"workflowStates":{"nodes":[{"id":"todo","name":"Todo","type":"unstarted","position":1},{"id":"started","name":"In Progress","type":"started","position":2},{"id":"input","name":"Needs Input","type":"started","position":3},{"id":"review","name":"For Review","type":"started","position":4},{"id":"done","name":"Done","type":"completed","position":5}]}}}`)
		case strings.Contains(envelope.Query, "HarnessIssueState"):
			_, _ = io.WriteString(w, `{"data":{"issueUpdate":{"success":true,"issue":{"id":"issue-7","identifier":"ENG-7","url":"https://linear.test/ENG-7"}}}}`)
		default:
			t.Errorf("unexpected Linear query: %s", envelope.Query)
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer linearHost.Close()
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{LinearWebhookSecret: "webhook-secret", MaxRequestBytes: 1 << 20, WebhookTolerance: time.Minute},
		memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.linear = func(token string) *linearapi.Client { return linearapi.NewWithEndpoint(token, linearHost.URL) }
	credential, _ := json.Marshal(linearapi.OAuthCredential{AccessToken: "linear-token"})
	if err := server.putCredential(ctx, "linear_oauth", credential); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	body := []byte(`{"action":"created","type":"AgentSessionEvent","organizationId":"org","appUserId":"app-user","agentSession":{"id":"session-7","appUserId":"app-user","issue":{"id":"issue-7","identifier":"ENG-7","title":"Native dispatch","description":"Implement it","url":"https://linear.test/ENG-7","teamId":"team"}}}`)
	req, _ := http.NewRequest(http.MethodPost, host.URL+"/webhooks/linear", bytes.NewReader(body))
	now := time.Now()
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	mac.Write(body)
	req.Header.Set(linear.HeaderDelivery, "delivery-agent-7")
	req.Header.Set(linear.HeaderEvent, "AgentSessionEvent")
	req.Header.Set(linear.HeaderTimestamp, strconv.FormatInt(now.UnixMilli(), 10))
	req.Header.Set(linear.HeaderSignature, hex.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(req)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("webhook response: %v status=%v", err, response.StatusCode)
	}
	response.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := memory.ListRuns(ctx, model.RunFilter{})
		if len(runs) == 1 && activities.Load() == 1 {
			if linearAgentSessionID(runs[0]) != "session-7" {
				t.Fatalf("agent session was not persisted: %s", runs[0].Metadata)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("delegated session was not dispatched or acknowledged; activities=%d", activities.Load())
}

func TestIssueWebhookDoesNotDispatchAndManagementIsProtected(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repo, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "v", GitHubRepo: "r", LinearWorkspaceID: "org", LinearTeamID: "team", LinearAgentName: "Vessica", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = repo
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(Config{ManagementToken: "management-secret", LinearWebhookSecret: "webhook-secret", MaxRequestBytes: 1 << 20, MaxJournalBytes: 1 << 20, WebhookTolerance: time.Minute}, memory, box, events.NewBroker(), logger)
	teamToken := testTeamToken(t, memory, box, "owner")
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	body := []byte(`{"action":"create","type":"Issue","organizationId":"org","data":{"id":"issue-1","identifier":"ENG-1","title":"Build","description":"It","url":"https://linear.app/ENG-1","team":{"id":"team"},"labels":[{"name":"agent-harness"}]}}`)
	request := func(delivery string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, host.URL+"/webhooks/linear", bytes.NewReader(body))
		now := time.Now()
		mac := hmac.New(sha256.New, []byte("webhook-secret"))
		mac.Write(body)
		req.Header.Set(linear.HeaderDelivery, delivery)
		req.Header.Set(linear.HeaderEvent, "Issue")
		req.Header.Set(linear.HeaderTimestamp, strconv.FormatInt(now.UnixMilli(), 10))
		req.Header.Set(linear.HeaderSignature, hex.EncodeToString(mac.Sum(nil)))
		return req
	}
	for _, delivery := range []string{"delivery-1", "delivery-1", "delivery-2"} {
		response, err := http.DefaultClient.Do(request(delivery))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status %d", response.StatusCode)
		}
		response.Body.Close()
	}
	runs, _ := memory.ListRuns(ctx, model.RunFilter{})
	if len(runs) != 0 {
		t.Fatalf("ordinary issue webhooks must not dispatch runs: %d", len(runs))
	}
	claimed, err := memory.AcceptLinearDelivery(ctx, repo, model.LinearDelivery{DeliveryID: "agent-session-delivery",
		EventType: "AgentSessionEvent", Action: "created", IssueID: "issue-1", IssueKey: "ENG-1",
		IssueTitle: "Build", ReceivedAt: time.Now().UTC()})
	if err != nil || claimed.Run == nil {
		t.Fatalf("seed run: %+v %v", claimed, err)
	}
	runs = []model.Run{*claimed.Run}
	capability, _ := box.MintCapability(runs[0].ID, time.Now().Add(time.Hour))
	binaryRequest, _ := http.NewRequest(http.MethodHead, host.URL+"/internal/v1/runs/"+runs[0].ID+"/worker-binary", nil)
	binaryRequest.Header.Set("Authorization", "Bearer "+capability)
	binaryResponse, err := http.DefaultClient.Do(binaryRequest)
	if err != nil || binaryResponse.StatusCode != http.StatusOK || binaryResponse.Header.Get("X-Agent-Harness-Worker-SHA256") == "" {
		t.Fatalf("worker binary endpoint failed: %v status=%v digest=%q", err, binaryResponse.StatusCode, binaryResponse.Header.Get("X-Agent-Harness-Worker-SHA256"))
	}
	binaryResponse.Body.Close()
	eventBody := bytes.NewBufferString(`{"stage":"coder","type":"ticket.completed","level":"info","message":"integrated","payload":{"ticket_key":"ENG-1-T01","commit":"abc123","depends_on":[]}}`)
	eventRequest, _ := http.NewRequest(http.MethodPost, host.URL+"/internal/v1/runs/"+runs[0].ID+"/events", eventBody)
	eventRequest.Header.Set("Authorization", "Bearer "+capability)
	eventRequest.Header.Set("Content-Type", "application/json")
	eventResponse, err := http.DefaultClient.Do(eventRequest)
	if err != nil || eventResponse.StatusCode != http.StatusCreated {
		t.Fatalf("event projection failed: %v status=%v", err, eventResponse.StatusCode)
	}
	eventResponse.Body.Close()
	tickets, _ := memory.ListTickets(ctx, runs[0].ID)
	if len(tickets) != 1 || tickets[0].CommitSHA != "abc123" || tickets[0].State != "completed" {
		t.Fatalf("ticket event was not projected: %+v", tickets)
	}
	usageBody := bytes.NewBufferString(`{"stage":"coder","type":"codex.usage","level":"info","message":"usage","payload":{"model":"gpt-5.3-codex","input_tokens":120,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"estimated_api_cost_usd":0.001}}`)
	usageRequest, _ := http.NewRequest(http.MethodPost, host.URL+"/internal/v1/runs/"+runs[0].ID+"/events", usageBody)
	usageRequest.Header.Set("Authorization", "Bearer "+capability)
	usageRequest.Header.Set("Content-Type", "application/json")
	usageResponse, err := http.DefaultClient.Do(usageRequest)
	if err != nil || usageResponse.StatusCode != http.StatusCreated {
		var responseBody []byte
		if usageResponse != nil {
			responseBody, _ = io.ReadAll(usageResponse.Body)
		}
		t.Fatalf("usage projection failed: %v status=%v body=%s", err, usageResponse.StatusCode, responseBody)
	}
	usageResponse.Body.Close()
	measured, _ := memory.GetRun(ctx, runs[0].ID)
	if measured.CodexModel != "gpt-5.3-codex" || measured.InputTokens != 120 || measured.EstimatedCostUSD != 0.001 {
		t.Fatalf("usage event was not projected: %+v", measured)
	}
	if err := memory.SetPreview(ctx, runs[0].ID, "starting", "", 4173, nil); err != nil {
		t.Fatal(err)
	}
	previewFailedBody := bytes.NewBufferString(`{"type":"preview.failed","level":"warning","message":"health check timed out","payload":{"error":"timeout"}}`)
	previewFailedRequest, _ := http.NewRequest(http.MethodPost, host.URL+"/internal/v1/runs/"+runs[0].ID+"/events", previewFailedBody)
	previewFailedRequest.Header.Set("Authorization", "Bearer "+capability)
	previewFailedRequest.Header.Set("Content-Type", "application/json")
	previewFailedResponse, err := http.DefaultClient.Do(previewFailedRequest)
	if err != nil || previewFailedResponse.StatusCode != http.StatusCreated {
		t.Fatalf("preview failure projection failed: %v status=%v", err, previewFailedResponse.StatusCode)
	}
	previewFailedResponse.Body.Close()
	failedPreview, _ := memory.GetRun(ctx, runs[0].ID)
	if failedPreview.PreviewState != "failed" || failedPreview.PreviewPort != 4173 {
		t.Fatalf("preview failure was not durably projected: %+v", failedPreview)
	}
	pausedBody := bytes.NewBufferString(`{"stage":"coder","type":"run.paused","level":"error","message":"dependency contract failed"}`)
	pausedRequest, _ := http.NewRequest(http.MethodPost, host.URL+"/internal/v1/runs/"+runs[0].ID+"/events", pausedBody)
	pausedRequest.Header.Set("Authorization", "Bearer "+capability)
	pausedRequest.Header.Set("Content-Type", "application/json")
	pausedResponse, err := http.DefaultClient.Do(pausedRequest)
	if err != nil || pausedResponse.StatusCode != http.StatusCreated {
		t.Fatalf("pause projection failed: %v status=%v", err, pausedResponse.StatusCode)
	}
	pausedResponse.Body.Close()
	paused, _ := memory.GetRun(ctx, runs[0].ID)
	stages, _ := memory.ListStages(ctx, runs[0].ID)
	if paused.State != "paused" || len(stages) != 1 || stages[0].Stage != "coder" || stages[0].State != "blocked" ||
		!strings.Contains(string(stages[0].Details), "dependency contract failed") {
		t.Fatalf("paused stage was not durably blocked: run=%+v stages=%+v", paused, stages)
	}
	response, err := http.Get(host.URL + "/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", response.StatusCode)
	}
	response.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, host.URL+"/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer "+teamToken)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authorized status %d", response.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if runs, ok := result["runs"].([]any); !ok || len(runs) != 1 {
		t.Fatalf("run list must encode as an array: %#v", result["runs"])
	}
}

func TestEmptyRunListEncodesAsArray(t *testing.T) {
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	memory := store.NewMemory()
	server := New(Config{ManagementToken: "management-secret", MaxRequestBytes: 1 << 20, MaxJournalBytes: 1 << 20},
		memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	teamToken := testTeamToken(t, memory, box, "owner")
	request := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer "+teamToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if runs, ok := result["runs"].([]any); !ok || len(runs) != 0 {
		t.Fatalf("empty run list must encode as []: %#v", result["runs"])
	}
}

func TestArchiveGuardRejectsCanonicalIssuesAndAllowsUnmappedDuplicates(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, err := memory.PutRepository(ctx, model.Repository{ID: "repo-1", Name: "repo", GitHubOwner: "v", GitHubRepo: "r",
		LinearWorkspaceID: "org", LinearTeamID: "team", LinearAgentName: "Vessica", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	delivery := model.LinearDelivery{DeliveryID: "delivery", WorkspaceID: "org", TeamID: "team", IssueID: "source-id",
		IssueKey: "AGE-5", IssueTitle: "Source", IssueURL: "https://linear.test/AGE-5", ReceivedAt: time.Now().UTC()}
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, delivery)
	if err != nil || claimed.Run == nil {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	if err := memory.PutTicket(ctx, model.TicketState{RunID: claimed.Run.ID, LogicalKey: "AGE-5-T01", ProviderIssueID: "child-id", ProviderIssueKey: "AGE-9"}); err != nil {
		t.Fatal(err)
	}
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{}, memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, issue := range []linearapi.Issue{{ID: "source-id", Identifier: "AGE-5"}, {ID: "child-id", Identifier: "AGE-9"}} {
		if err := server.ensureLinearIssueIsNotCanonical(ctx, repository.ID, issue); err == nil {
			t.Fatalf("canonical issue %s was allowed", issue.Identifier)
		}
	}
	if err := server.ensureLinearIssueIsNotCanonical(ctx, repository.ID, linearapi.Issue{ID: "duplicate-id", Identifier: "AGE-6"}); err != nil {
		t.Fatalf("unmapped duplicate rejected: %v", err)
	}
}

func TestPausedRunInputCanBeClarifiedBeforeResume(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repository, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "v", GitHubRepo: "r", LinearWorkspaceID: "org", LinearTeamID: "team", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := memory.AcceptLinearDelivery(ctx, repository, model.LinearDelivery{
		DeliveryID: "clarify-delivery", IssueID: "clarify-issue", IssueKey: "ENG-10",
		IssueTitle: "Original", FeatureRequest: "original request", ReceivedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SetRunState(ctx, claimed.Run.ID, "paused", "product", "needs scope"); err != nil {
		t.Fatal(err)
	}
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	server := New(Config{ManagementToken: "management-secret", MaxRequestBytes: 1 << 20, MaxJournalBytes: 1 << 20},
		memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	teamToken := testTeamToken(t, memory, box, "owner")
	body := bytes.NewBufferString(`{"feature_request":"public message board MVP"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/runs/"+claimed.Run.ID+"/input", body)
	request.Header.Set("Authorization", "Bearer "+teamToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("input update status %d: %s", response.Code, response.Body.String())
	}
	updated, err := memory.GetRun(ctx, claimed.Run.ID)
	if err != nil || updated.FeatureRequest != "public message board MVP" || updated.State != "paused" {
		t.Fatalf("unexpected updated run: %+v %v", updated, err)
	}
	eventValues, err := memory.ListEvents(ctx, model.EventFilter{RunID: claimed.Run.ID})
	if err != nil || len(eventValues) == 0 || eventValues[len(eventValues)-1].Type != "run.input_updated" {
		t.Fatalf("missing input event: %+v %v", eventValues, err)
	}
}

func TestEventStreamFlushesHeadersImmediately(t *testing.T) {
	key, _ := secure.GenerateKey()
	box, _ := secure.NewBox(key)
	memory := store.NewMemory()
	server := New(Config{ManagementToken: "management-secret", MaxRequestBytes: 1 << 20, MaxJournalBytes: 1 << 20},
		memory, box, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	teamToken := testTeamToken(t, memory, box, "owner")
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, host.URL+"/v1/events", nil)
	request.Header.Set("Authorization", "Bearer "+teamToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("event stream did not establish immediately: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	initialFrame := "retry: 1000\n: connected\n\n"
	initial := make([]byte, len(initialFrame))
	if _, err := io.ReadFull(response.Body, initial); err != nil || string(initial) != initialFrame {
		t.Fatalf("event stream did not send initial connection frame: %q %v", initial, err)
	}
}
