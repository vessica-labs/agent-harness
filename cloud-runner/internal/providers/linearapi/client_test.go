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
	issue, err := client.UpsertChild(context.Background(), "parent", "team", "existing-child-id", "marker", Ticket{Key: "T01", Title: "Ticket"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || issue.Identifier != "AGE-6" {
		t.Fatalf("requests=%d issue=%+v", requests, issue)
	}
}
