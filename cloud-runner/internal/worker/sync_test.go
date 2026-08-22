package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTicketProgressRequestReplaysTicketIdentities(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"tickets": []map[string]any{{"key": "AGE-5-Q01", "status": "running"}}}
	stateBody, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), stateBody, 0o600); err != nil {
		t.Fatal(err)
	}
	plan := []ticket{{Key: "AGE-5-Q01", Type: "bug", Title: "Repair production path", Objective: "Connect the worker", FailureEvidence: "No consumer exists."}}
	planBody, _ := json.Marshal(plan)
	if err := os.WriteFile(filepath.Join(runDir, "artifacts", "ticket-plan.json"), planBody, 0o600); err != nil {
		t.Fatal(err)
	}

	request, err := ticketProgressRequest(runDir, "coder")
	if err != nil {
		t.Fatal(err)
	}
	tickets, ok := request["tickets"].([]ticket)
	if !ok || len(tickets) != 1 || tickets[0].FailureEvidence == "" {
		t.Fatalf("ticket identity payload missing from progress sync: %+v", request)
	}
	progress, ok := request["ticket_progress"].([]map[string]any)
	if !ok || len(progress) != 1 || progress[0]["status"] != "running" {
		t.Fatalf("ticket progress missing from atomic sync: %+v", request)
	}
}
