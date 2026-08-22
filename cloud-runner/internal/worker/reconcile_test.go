package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyArchitectureConstraintsMergesPathsAndDependencies(t *testing.T) {
	product := []byte(`{"agent":"product","status":"ready","tickets":[{"key":"T01","owned_paths":["a"],"depends_on":[],"focused_checks":["test a"]},{"key":"T02","owned_paths":["b"],"depends_on":["T01"],"focused_checks":["test b"]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T02","constraints":["use shared schema"],"required_owned_paths":["package-lock.json","b"],"additional_dependencies":["T01"],"required_focused_checks":["test integration"]}]}`)
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
	if got := output.Tickets[1].FocusedChecks; len(got) != 2 || got[1] != "test integration" {
		t.Fatalf("focused checks not merged: %v", got)
	}
	if got := output.Tickets[1].ArchitectureConstraints; len(got) != 1 || got[0] != "use shared schema" {
		t.Fatalf("architecture constraints not merged: %v", got)
	}
	var ticketPlan []ticket
	if err := json.Unmarshal(plan, &ticketPlan); err != nil || len(ticketPlan) != 2 {
		t.Fatalf("ticket plan invalid: %v %v", ticketPlan, err)
	}
}

func TestApplyArchitectureConstraintsMergesTieredVerification(t *testing.T) {
	product := []byte(`{"agent":"product","status":"ready","tickets":[{"key":"T01","owned_paths":["a"],"depends_on":[],"verification":{"iteration_checks":["test unit"],"ticket_gate":["test package"],"pipeline_gates":[{"stage":"lint","command":"lint all","reason":"static analysis"}]}}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T01","constraints":[],"required_owned_paths":[],"additional_dependencies":[],"required_iteration_checks":["test adapter"],"required_ticket_gates":["test integration"],"required_pipeline_gates":[{"stage":"lint","command":"lint all","reason":"duplicate"},{"stage":"qa","command":"test e2e","reason":"browser boundary"}]}]}`)
	updated, _, changed, err := applyArchitectureConstraints(product, architecture)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var output struct {
		Tickets []ticket `json:"tickets"`
	}
	if err := json.Unmarshal(updated, &output); err != nil {
		t.Fatal(err)
	}
	verification := output.Tickets[0].Verification
	if verification == nil {
		t.Fatal("tiered verification was removed")
	}
	if got := verification.IterationChecks; len(got) != 2 || got[1] != "test adapter" {
		t.Fatalf("iteration checks not merged: %v", got)
	}
	if got := verification.TicketGate; len(got) != 2 || got[1] != "test integration" {
		t.Fatalf("ticket gates not merged: %v", got)
	}
	if got := verification.PipelineGates; len(got) != 2 || got[1].Stage != "qa" || got[1].Command != "test e2e" {
		t.Fatalf("pipeline gates not merged and deduplicated: %+v", got)
	}
}

func TestApplyArchitectureConstraintsPreservesLegacyChecksWhenIntroducingTiers(t *testing.T) {
	product := []byte(`{"tickets":[{"key":"T01","owned_paths":["a"],"depends_on":[],"focused_checks":["test legacy"]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T01","constraints":[],"required_owned_paths":[],"additional_dependencies":[],"required_iteration_checks":["test unit"],"required_ticket_gates":["test package"],"required_pipeline_gates":[]}]}`)
	updated, _, _, err := applyArchitectureConstraints(product, architecture)
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Tickets []ticket `json:"tickets"`
	}
	if err := json.Unmarshal(updated, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.Tickets[0].Verification.TicketGate; len(got) != 2 || got[0] != "test legacy" || got[1] != "test package" {
		t.Fatalf("legacy focused checks were not preserved in ticket gate: %v", got)
	}
}

func TestContextExtractionKeepsOnlyRelevantRequirementsAndAcceptance(t *testing.T) {
	markdown := `# PRD: Example

## Requirements

- R1: First requirement
- R2: Second requirement

## Acceptance Criteria

### AC-1: First outcome

- Given a state
- When an action occurs
- Then the first result appears

### AC-2: Second outcome

- Then the second result appears

## Constraints and Dependencies

- Keep it small`
	requirements := taggedMarkdownLines(markdown, "R")
	if requirements["R1"] != "First requirement" || requirements["R2"] != "Second requirement" {
		t.Fatalf("unexpected requirements: %v", requirements)
	}
	acceptance := acceptanceCriterionBlocks(markdown)
	if _, ok := acceptance["AC-1"]; !ok || acceptance["AC-2"] == "" {
		t.Fatalf("unexpected acceptance blocks: %v", acceptance)
	}
	if got := markdownSection(markdown, "## Constraints and Dependencies"); got != "- Keep it small" {
		t.Fatalf("unexpected shared context: %q", got)
	}
}

func TestMaterializeTicketContextsBuildsOneCompactPacketPerTicket(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	product := []byte(`{
  "prd_markdown":"# PRD: Example\n\n## Non-Goals\n\n- NG1: No export\n\n## Requirements\n\n- R1: Use the real adapter\n- R2: Add an unrelated screen\n\n## Acceptance Criteria\n\n### AC-1: Adapter works\n\n- Then the adapter responds\n\n### AC-2: Screen works\n\n- Then the screen appears\n\n## Constraints and Dependencies\n\n- Keep credentials server-side",
  "tickets":[
    {"key":"T01","title":"Adapter","objective":"Wire adapter","acceptance_criteria":["AC-1"],"owned_paths":["adapter"],"depends_on":[],"focused_checks":["go test ./adapter"],"architecture_constraints":["reuse shared schema"]},
    {"key":"T02","title":"Screen","objective":"Add screen","acceptance_criteria":["AC-2"],"owned_paths":["web"],"depends_on":[],"focused_checks":["npm test"]}
  ],
  "coverage":[{"requirement":"R1","tickets":["T01"]},{"requirement":"R2","tickets":["T02"]}]
}`)
	architecture := []byte(`{"status":"ready","applicable_adrs":["ADR-shared-schema.md"],"ticket_constraints":[]}`)
	runner := &Runner{runDir: runDir}
	if err := runner.materializeTicketContexts(product, architecture); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(runDir, "artifacts", "ticket-contexts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var packets []ticketContextPacket
	if err := json.Unmarshal(body, &packets); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 2 || packets[0].RequirementExcerpts["R1"] != "Use the real adapter" {
		t.Fatalf("unexpected packets: %+v", packets)
	}
	if _, leaked := packets[0].RequirementExcerpts["R2"]; leaked {
		t.Fatalf("unrelated requirement leaked into T01 packet: %+v", packets[0])
	}
	if packets[0].AcceptanceExcerpts["AC-1"] == "" || len(packets[0].ApplicableADRs) != 1 {
		t.Fatalf("relevant context missing: %+v", packets[0])
	}
}

func TestApplyArchitectureConstraintsRejectsUnknownTicket(t *testing.T) {
	product := []byte(`{"tickets":[{"key":"T01","owned_paths":[],"depends_on":[]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T99"}]}`)
	if _, _, _, err := applyArchitectureConstraints(product, architecture); err == nil {
		t.Fatal("unknown architectural ticket constraint accepted")
	}
}
