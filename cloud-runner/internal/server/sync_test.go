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
	if len(request.TicketProgress) != 1 || request.TicketProgress[0].ProviderID != "linear-id" {
		t.Fatalf("durable ticket identity was not decoded: %+v", request.TicketProgress)
	}
}
