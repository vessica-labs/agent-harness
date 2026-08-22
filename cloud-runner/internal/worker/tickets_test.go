package worker

import (
	"encoding/json"
	"testing"
)

func TestQARepairTicketPreservesDiagnosticMetadata(t *testing.T) {
	input := []byte(`{
		"key":"AGE-29-Q01",
		"type":"bug",
		"title":"Repair production path",
		"objective":"Connect the worker",
		"source_acceptance_criteria":["AC-5"],
		"acceptance_criteria":["One message is processed"],
		"owned_paths":["apps/worker/src"],
		"depends_on":[],
		"failure_evidence":"The durable job has no consumer."
	}`)
	var item ticket
	if err := json.Unmarshal(input, &item); err != nil {
		t.Fatalf("decode QA repair ticket: %v", err)
	}
	output, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("encode QA repair ticket: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(output, &roundTrip); err != nil {
		t.Fatalf("decode round-tripped ticket: %v", err)
	}
	if roundTrip["failure_evidence"] == nil || roundTrip["source_acceptance_criteria"] == nil {
		t.Fatalf("diagnostic metadata was dropped: %s", output)
	}
}
