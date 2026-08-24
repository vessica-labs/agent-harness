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

func TestEnsureWorkspaceDependencyOwnershipAddsManifestAndLockfile(t *testing.T) {
	repo := t.TempDir()
	writeFile := func(path, body string) {
		t.Helper()
		path = filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("packages/contracts/package.json", `{"name":"@vessica/contracts"}`)
	writeFile("packages/db/package.json", `{"name":"@vessica/db","dependencies":{}}`)
	writeFile("pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	plan := []ticket{{
		Key: "VES-14-T02", OwnedPaths: []string{"packages/db/src/schema.ts"},
		ArchitectureConstraints: []string{"Depend on @vessica/contracts and reuse its schemas."},
	}}
	updated, changed, err := ensureWorkspaceDependencyOwnership(repo, plan)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := updated[0].OwnedPaths
	if len(got) != 3 || got[1] != "packages/db/package.json" || got[2] != "pnpm-lock.yaml" {
		t.Fatalf("package ownership not reconciled: %v", got)
	}
}

func TestEnsureWorkspaceDependencyOwnershipLeavesDeclaredAndNegativeDependenciesAlone(t *testing.T) {
	repo := t.TempDir()
	writeFile := func(path, body string) {
		t.Helper()
		path = filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("packages/contracts/package.json", `{"name":"@vessica/contracts"}`)
	writeFile("packages/db/package.json", `{"name":"@vessica/db","dependencies":{"@vessica/contracts":"workspace:*"}}`)
	writeFile("packages/other/package.json", `{"name":"@vessica/other","dependencies":{}}`)
	plan := []ticket{{
		Key: "T01", OwnedPaths: []string{"packages/db/src/schema.ts"},
		ArchitectureConstraints: []string{"Depend on @vessica/contracts."},
	}, {
		Key: "T02", OwnedPaths: []string{"packages/other/src/other.ts"},
		ArchitectureConstraints: []string{"Do not depend on @vessica/contracts."},
	}}
	updated, changed, err := ensureWorkspaceDependencyOwnership(repo, plan)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v plan=%+v", changed, err, updated)
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

func TestRefreshTicketContextsForQARepairPlan(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "agent-output"), 0o700); err != nil {
		t.Fatal(err)
	}
	product := `{
  "prd_markdown":"## Requirements\n\n- R1: Create reminders.\n\n## Acceptance Criteria\n\n### AC-1: Durable creation\n\n- Given a request\n- When it is replayed\n- Then one reminder exists",
  "tickets":[{"key":"VES-10-T01","title":"Base","objective":"Build base","acceptance_criteria":["AC-1"],"owned_paths":["base"],"depends_on":[]}],
  "coverage":[{"requirement":"R1","tickets":["VES-10-T01"]}]
}`
	architecture := `{"status":"ready","ticket_constraints":[],"applicable_adrs":["ADR-001"]}`
	if err := os.WriteFile(filepath.Join(runDir, "agent-output", "product.json"), []byte(product), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "agent-output", "arch.json"), []byte(architecture), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := []ticket{
		{Key: "VES-10-T01", Title: "Base", Objective: "Build base", AcceptanceCriteria: []string{"AC-1"}, OwnedPaths: []string{"base"}},
		{Key: "VES-10-Q01", Title: "Repair", Objective: "Cover the gap", SourceAcceptanceCriteria: []string{"AC-1"}, AcceptanceCriteria: []string{"Playwright proves durable creation"}, OwnedPaths: []string{"e2e"}},
	}
	runner := &Runner{runDir: runDir}
	if err := runner.refreshTicketContextsForPlan(plan); err != nil {
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
	if len(packets) != 2 || packets[0].Key != "VES-10-Q01" {
		t.Fatalf("repair context missing: %+v", packets)
	}
	if packets[0].AcceptanceExcerpts["AC-1"] == "" {
		t.Fatalf("repair source acceptance excerpt missing: %+v", packets[0].AcceptanceExcerpts)
	}
	if len(packets[0].ApplicableADRs) != 1 || packets[0].ApplicableADRs[0] != "ADR-001" {
		t.Fatalf("repair applicable ADRs missing: %+v", packets[0].ApplicableADRs)
	}
}

func TestRefreshTicketContextsAfterRestoreRepairsMissingQAInputs(t *testing.T) {
	runDir := t.TempDir()
	for _, directory := range []string{"agent-output", "artifacts"} {
		if err := os.MkdirAll(filepath.Join(runDir, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	product := `{"prd_markdown":"## Acceptance Criteria\n\n### AC-8: Telemetry\n\n- Then bounded metrics exist","tickets":[],"coverage":[]}`
	architecture := `{"status":"ready","ticket_constraints":[],"applicable_adrs":[]}`
	plan := `[{
  "key":"VES-10-Q02","title":"Telemetry","objective":"Repair telemetry","source_acceptance_criteria":["AC-8"],
  "acceptance_criteria":["Metrics are emitted"],"owned_paths":["worker"],"depends_on":[]
}]`
	state := `{"stages":{"arch":{"status":"completed"}}}`
	for path, body := range map[string]string{
		"agent-output/product.json":  product,
		"agent-output/arch.json":     architecture,
		"artifacts/ticket-plan.json": plan,
		"state.json":                 state,
	} {
		if err := os.WriteFile(filepath.Join(runDir, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &Runner{runDir: runDir}
	if err := runner.refreshTicketContextsAfterRestore(); err != nil {
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
	if len(packets) != 1 || packets[0].Key != "VES-10-Q02" || packets[0].AcceptanceExcerpts["AC-8"] == "" {
		t.Fatalf("restored QA repair context missing: %+v", packets)
	}
}

func TestApplyArchitectureConstraintsRejectsUnknownTicket(t *testing.T) {
	product := []byte(`{"tickets":[{"key":"T01","owned_paths":[],"depends_on":[]}]}`)
	architecture := []byte(`{"status":"ready","ticket_constraints":[{"ticket_key":"T99"}]}`)
	if _, _, _, err := applyArchitectureConstraints(product, architecture); err == nil {
		t.Fatal("unknown architectural ticket constraint accepted")
	}
}
