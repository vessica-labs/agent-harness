package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSyncRequestAcceptsDurableTicketIdentity(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`{
		"stage":"coder",
		"parent_comment":"progress",
		"ticket_progress":[{
			"key":"AGE-5-T01",
			"status":"completed",
			"owner":"worker-1",
			"commit":"abc123",
			"blocker":"owned path missing",
			"depends_on":[],
			"provider_id":"linear-id",
			"provider_key":"AGE-9",
			"provider_url":"https://linear.app/example"
		}]
	}`))
	decoder.DisallowUnknownFields()
	var request syncRequest
	if err := decoder.Decode(&request); err != nil {
		t.Fatalf("decode worker ticket progress: %v", err)
	}
	if len(request.TicketProgress) != 1 || request.TicketProgress[0].ProviderID != "linear-id" || request.TicketProgress[0].Blocker != "owned path missing" {
		t.Fatalf("durable ticket identity was not decoded: %+v", request.TicketProgress)
	}
}

func TestSyncRequestAcceptsQARepairTicketContract(t *testing.T) {
	decoder := json.NewDecoder(strings.NewReader(`{
		"stage":"qa",
		"parent_comment":"QA requested repairs",
		"tickets":[{
			"key":"AGE-5-Q01",
			"type":"bug",
			"title":"Wire production processing",
			"objective":"Connect the production path",
			"source_acceptance_criteria":["AC-5","AC-6"],
			"acceptance_criteria":["The production path processes one message"],
			"owned_paths":["apps/worker/src"],
			"depends_on":[],
			"focused_checks":["go test ./internal/worker"],
			"verification":{
				"iteration_checks":["go test ./internal/worker -run TestMessage"],
				"ticket_gate":["go test ./internal/worker"],
				"pipeline_gates":[{"stage":"qa","command":"make test","reason":"integrated proof"}]
			},
			"commit_message":"wire production processing",
			"complexity":"l",
			"failure_evidence":"The durable job has no consumer.",
			"architecture_constraints":["Preserve idempotency"]
		}]
	}`))
	decoder.DisallowUnknownFields()
	var request syncRequest
	if err := decoder.Decode(&request); err != nil {
		t.Fatalf("decode QA repair ticket: %v", err)
	}
	if len(request.Tickets) != 1 || request.Tickets[0].Type != "bug" || request.Tickets[0].FailureEvidence == "" {
		t.Fatalf("QA repair metadata was not decoded: %+v", request.Tickets)
	}
}
