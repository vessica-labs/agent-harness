package worker

import (
	"encoding/json"
	"testing"
)

func TestApplyArchitectureConstraintsMergesPathsAndDependencies(t *testing.T) {
	product := []byte(`{"agent":"product","status":"ready","tickets":[{"key":"T01","owned_paths":["a"],"depends_on":[]},{"key":"T02","owned_paths":["b"],"depends_on":["T01"]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T02","required_owned_paths":["package-lock.json","b"],"additional_dependencies":["T01"]}]}`)
	updated, plan, changed, err := applyArchitectureConstraints(product, architecture)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var output struct {
		Tickets []ticket `json:"tickets"`
	}
	if err := json.Unmarshal(updated, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.Tickets[1].OwnedPaths; len(got) != 2 || got[1] != "package-lock.json" {
		t.Fatalf("owned paths not merged: %v", got)
	}
	var ticketPlan []ticket
	if err := json.Unmarshal(plan, &ticketPlan); err != nil || len(ticketPlan) != 2 {
		t.Fatalf("ticket plan invalid: %v %v", ticketPlan, err)
	}
}

func TestApplyArchitectureConstraintsRejectsUnknownTicket(t *testing.T) {
	product := []byte(`{"tickets":[{"key":"T01","owned_paths":[],"depends_on":[]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T99"}]}`)
	if _, _, _, err := applyArchitectureConstraints(product, architecture); err == nil {
		t.Fatal("unknown architectural ticket constraint accepted")
	}
}
