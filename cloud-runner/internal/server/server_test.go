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

func TestSignedWebhookClaimsOnceAndManagementIsProtected(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemory()
	repo, err := memory.PutRepository(ctx, model.Repository{Name: "repo", GitHubOwner: "v", GitHubRepo: "r", LinearWorkspaceID: "org", LinearTeamID: "team", TriggerLabel: "agent-harness", Enabled: true})
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
	if len(runs) != 1 {
		t.Fatalf("got %d runs", len(runs))
	}
	capability, _ := box.MintCapability(runs[0].ID, time.Now().Add(time.Hour))
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
		LinearWorkspaceID: "org", LinearTeamID: "team", TriggerLabel: "agent-harness", Enabled: true})
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
	initial := make([]byte, len(": connected\n\n"))
	if _, err := io.ReadFull(response.Body, initial); err != nil || string(initial) != ": connected\n\n" {
		t.Fatalf("event stream did not send initial connection frame: %q %v", initial, err)
	}
}
