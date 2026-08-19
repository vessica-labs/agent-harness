package linearapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpsertChildUsesDurableExternalIdentity(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		if !strings.Contains(envelope.Query, "HarnessIssueUpdate") {
			t.Errorf("expected direct issue update, got %s", envelope.Query)
		}
		if envelope.Variables["id"] != "existing-child-id" {
			t.Errorf("existing id not used: %#v", envelope.Variables)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"existing-child-id","identifier":"AGE-6","url":"https://linear.test/AGE-6"}}}}`))
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	issue, err := client.UpsertChild(context.Background(), "parent", "team", "existing-child-id", "marker", Ticket{Key: "T01", Title: "Ticket"}, WorkflowState{ID: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || issue.Identifier != "AGE-6" {
		t.Fatalf("requests=%d issue=%+v", requests, issue)
	}
}

func TestCreateRootIssueResolvesConfiguredTriggerLabel(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			if !strings.Contains(envelope.Query, "HarnessIssueLabel") || envelope.Variables["name"] != "agent-harness" {
				t.Fatalf("unexpected label request: %#v", envelope)
			}
			_, _ = w.Write([]byte(`{"data":{"issueLabels":{"nodes":[{"id":"label-1","name":"agent-harness"}]}}}`))
		case 2:
			if !strings.Contains(envelope.Query, "HarnessRootIssueCreate") || envelope.Variables["projectId"] != "project-1" {
				t.Fatalf("unexpected create request: %#v", envelope)
			}
			labels, ok := envelope.Variables["labelIds"].([]any)
			if !ok || len(labels) != 1 || labels[0] != "label-1" {
				t.Fatalf("trigger label not applied: %#v", envelope.Variables["labelIds"])
			}
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"issue-12","identifier":"AGE-12","title":"Pipeline explorer","url":"https://linear.test/AGE-12"}}}}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	issue, err := client.CreateRootIssue(context.Background(), "team-1", "project-1", "agent-harness", " Pipeline explorer ", "description")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || issue.Identifier != "AGE-12" {
		t.Fatalf("requests=%d issue=%+v", requests, issue)
	}
}

func TestArchiveIssueResolvesIdentifierBeforeMutation(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if envelope.Variables["id"] != "AGE-6" {
				t.Fatalf("identifier not resolved: %#v", envelope.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"issue-6","identifier":"AGE-6","title":"duplicate"}}}`))
			return
		}
		if !strings.Contains(envelope.Query, "HarnessIssueArchive") || envelope.Variables["id"] != "issue-6" {
			t.Fatalf("unexpected archive request: %#v", envelope)
		}
		_, _ = w.Write([]byte(`{"data":{"issueArchive":{"success":true}}}`))
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	issue, err := client.ArchiveIssue(context.Background(), "AGE-6")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || issue.ID != "issue-6" {
		t.Fatalf("requests=%d issue=%+v", requests, issue)
	}
}

func TestLifecycleStatesAndIssueTransitionUseTeamWorkflow(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if !strings.Contains(envelope.Query, "HarnessWorkflowStates") || envelope.Variables["teamId"] != "team-1" {
				t.Fatalf("unexpected workflow request: %#v", envelope)
			}
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[{"id":"done","name":"Done","type":"completed","position":5},{"id":"review","name":"For Review","type":"started","position":4},{"id":"input","name":"Needs Input","type":"started","position":3},{"id":"started","name":"In Progress","type":"started","position":2},{"id":"todo","name":"Todo","type":"unstarted","position":1}]}}}`))
			return
		}
		if !strings.Contains(envelope.Query, "HarnessIssueState") || envelope.Variables["id"] != "issue-1" || envelope.Variables["stateId"] != "started" {
			t.Fatalf("unexpected state mutation: %#v", envelope)
		}
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"issue-1","identifier":"AGE-1","state":{"id":"started","name":"In Progress","type":"started"}}}}}`))
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	states, err := client.LifecycleStates(context.Background(), "team-1")
	if err != nil {
		t.Fatal(err)
	}
	issue, err := client.SetIssueState(context.Background(), "issue-1", states.InProgress)
	if err != nil {
		t.Fatal(err)
	}
	if states.Todo.ID != "todo" || states.NeedsInput.ID != "input" || states.ForReview.ID != "review" || states.Done.ID != "done" || issue.State.ID != "started" {
		t.Fatalf("states=%+v issue=%+v", states, issue)
	}
}

func TestEnsureLifecycleStatesCreatesMissingHarnessStates(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1:
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[{"id":"todo","name":"Todo","type":"unstarted","position":1},{"id":"progress","name":"In Progress","type":"started","position":2},{"id":"done","name":"Done","type":"completed","position":3}]}}}`))
		case 2, 3:
			if !strings.Contains(envelope.Query, "HarnessWorkflowStateCreate") {
				t.Fatalf("expected workflow state creation, got %s", envelope.Query)
			}
			input, ok := envelope.Variables["input"].(map[string]any)
			if !ok || input["teamId"] != "team-1" || input["type"] != "started" {
				t.Fatalf("unexpected workflow input: %#v", envelope.Variables["input"])
			}
			name, _ := input["name"].(string)
			if (requests == 2 && name != "Needs Input") || (requests == 3 && name != "For Review") {
				t.Fatalf("unexpected creation order: %q", name)
			}
			_, _ = w.Write([]byte(`{"data":{"workflowStateCreate":{"success":true,"workflowState":{"id":"created","name":"created","type":"started","position":3}}}}`))
		case 4:
			_, _ = w.Write([]byte(`{"data":{"workflowStates":{"nodes":[{"id":"todo","name":"Todo","type":"unstarted","position":1},{"id":"progress","name":"In Progress","type":"started","position":2},{"id":"input","name":"Needs Input","type":"started","position":3},{"id":"review","name":"For Review","type":"started","position":4},{"id":"done","name":"Done","type":"completed","position":5}]}}}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	states, err := client.EnsureLifecycleStates(context.Background(), "team-1")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 4 || states.NeedsInput.ID != "input" || states.ForReview.ID != "review" {
		t.Fatalf("requests=%d states=%+v", requests, states)
	}
}

func TestCreateChildIncludesTodoStateInCreationMutation(t *testing.T) {
	requests := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var envelope struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"parent","children":{"nodes":[]}}}}`))
			return
		}
		if !strings.Contains(envelope.Query, "stateId:$stateId") || envelope.Variables["stateId"] != "todo" {
			t.Fatalf("Todo was not applied atomically: %#v", envelope)
		}
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"child","identifier":"AGE-2","state":{"id":"todo"}}}}}`))
	}))
	defer host.Close()
	client := &Client{token: "token", endpoint: host.URL, http: &http.Client{Timeout: time.Second}}
	issue, err := client.UpsertChild(context.Background(), "parent", "team", "", "marker", Ticket{Key: "T01", Title: "Ticket"}, WorkflowState{ID: "todo"})
	if err != nil || issue.State.ID != "todo" || requests != 2 {
		t.Fatalf("issue=%+v requests=%d err=%v", issue, requests, err)
	}
}
