package worker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitCommitAlreadyIntegrated(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Agent Harness Test"},
		{"config", "user.email", "agent-harness@example.test"},
	} {
		if _, err := runCommand(ctx, repo, nil, orchestratorGit, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "main.txt"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "add", "main.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "commit", "-m", "main"); err != nil {
		t.Fatal(err)
	}
	mainCommit, err := runCommand(ctx, repo, nil, orchestratorGit, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if integrated, err := gitCommitAlreadyIntegrated(ctx, repo, string(mainCommit)); err != nil || !integrated {
		t.Fatalf("HEAD commit not recognized as integrated: integrated=%v err=%v", integrated, err)
	}

	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "checkout", "--orphan", "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "rm", "-rf", "."); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "add", "other.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(ctx, repo, nil, orchestratorGit, "commit", "-m", "other"); err != nil {
		t.Fatal(err)
	}
	if integrated, err := gitCommitAlreadyIntegrated(ctx, repo, string(mainCommit)); err != nil || integrated {
		t.Fatalf("unrelated commit recognized as integrated: integrated=%v err=%v", integrated, err)
	}
}

func TestTicketWaves(t *testing.T) {
	waves, err := ticketWaves([]ticket{
		{Key: "T03", DependsOn: []string{"T01"}},
		{Key: "T02", DependsOn: []string{"T01"}},
		{Key: "T01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 2 || waves[0][0].Key != "T01" || waves[1][0].Key != "T02" || waves[1][1].Key != "T03" {
		t.Fatalf("unexpected waves: %+v", waves)
	}
	if _, err := ticketWaves([]ticket{
		{Key: "A", DependsOn: []string{"B"}},
		{Key: "B", DependsOn: []string{"A"}},
	}); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestIntegrationBranchFitsRailwaySandboxPushPolicy(t *testing.T) {
	runner := &Runner{config: Config{RunID: "run_3682e6dea7c890803e3b", IssueKey: "AGE-5"}}
	if got, want := runner.branchName(), "sandbox/agent-harness-age-5-a7c890803e3b"; got != want {
		t.Fatalf("branchName() = %q, want %q", got, want)
	}
	runner.deliveryBranch = "sandbox/agent-harness-age-5-a7c890803e3b-pr-0123456789ab"
	if got := runner.branchName(); got != runner.deliveryBranch {
		t.Fatalf("delivery branchName() = %q, want %q", got, runner.deliveryBranch)
	}
	if got := runner.baseRunBranchName(); got != "sandbox/agent-harness-age-5-a7c890803e3b" {
		t.Fatalf("baseRunBranchName() = %q", got)
	}
}

func TestTicketWavesResumeCompletedTickets(t *testing.T) {
	waves, err := ticketWavesWithDone([]ticket{
		{Key: "T01"},
		{Key: "T02", DependsOn: []string{"T01"}},
		{Key: "T03", DependsOn: []string{"T02"}},
	}, map[string]bool{"T01": true, "T02": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(waves) != 1 || len(waves[0]) != 1 || waves[0][0].Key != "T03" {
		t.Fatalf("unexpected resumed waves: %+v", waves)
	}
}

func TestFailedTicketEvidenceIsPreservedForRecovery(t *testing.T) {
	runDir := t.TempDir()
	stage := Stage{Result: FileContract{File: "agent-output/coder/{ticket_key}.json"}}
	result := []byte(`{"agent":"coder","status":"blocked","commit":null,"blocker":{"reason":"owned path missing","path":"package.json"}}`)
	path, err := preserveTicketResult(runDir, stage, "AGE-1-T05", result)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(result) {
		t.Fatalf("preserved result = %s, want %s", stored, result)
	}
	if got := ticketBlockerText(map[string]any{"reason": "owned path missing", "path": "package.json"}); got != `{"path":"package.json","reason":"owned path missing"}` {
		t.Fatalf("structured blocker = %q", got)
	}
}

func TestPipelineValidatesRepairLoops(t *testing.T) {
	root := t.TempDir()
	valid := `version: 1
name: repair
run_root: .harness/runs/{run_id}
stages:
  - id: coder
    agent: .agents/coder.md
    mode: ticket_parallel
    parallelism: 1
  - id: lint
    agent: .agents/lint.md
    needs: [coder]
    mode: single
    parallelism: 1
  - id: qa
    agent: .agents/qa.md
    needs: [lint]
    mode: single
    parallelism: 1
repair_loops:
  - from: qa
    to: coder
    through: qa
    max_reentries: 2
`
	path := filepath.Join(root, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := loadPipeline(path)
	if err != nil || len(pipeline.RepairLoops) != 1 {
		t.Fatalf("valid repair loop rejected: %v", err)
	}
	invalid := filepath.Join(root, "invalid.yaml")
	if err := os.WriteFile(invalid, []byte(valid[:len(valid)-2]+"unknown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPipeline(invalid); err == nil {
		t.Fatal("invalid repair loop accepted")
	}
}

func TestRecoverBlockedQARepairRequest(t *testing.T) {
	runDir := t.TempDir()
	resultPath := filepath.Join(runDir, "agent-output", "qa.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(`{"stages":{"qa":{"status":"blocked"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := `{"agent":"qa","status":"requeue","new_tickets":[{"key":"AGE-29-Q01"}]}`
	if err := os.WriteFile(resultPath, []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{runDir: runDir}
	stage := Stage{ID: "qa", Result: FileContract{File: "agent-output/qa.json"}}
	recovered, err := runner.recoverRepairRequest(stage)
	if err != nil || recovered == nil || recovered.resultPath != resultPath {
		t.Fatalf("blocked QA repair was not recovered: request=%+v error=%v", recovered, err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(`{"stages":{"qa":{"status":"pending"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := runner.recoverRepairRequest(stage); err != nil || recovered != nil {
		t.Fatalf("pending QA incorrectly reused stale repair: request=%+v error=%v", recovered, err)
	}
}

func TestJournalRoundTripAndTraversalRejection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "state.json"), []byte(`{"run_id":"run_test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "journal.tar.gz")
	if err := archiveDirectory(source, archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := extractDirectory(archive, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "state.json")); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(root, "unsafe.tar.gz")
	file, _ := os.Create(unsafe)
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	_ = file.Close()
	if err := extractDirectory(unsafe, filepath.Join(root, "target")); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestOnlyProductAndArchitectureCanRequestInputOnce(t *testing.T) {
	root := t.TempDir()
	result := filepath.Join(root, "result.json")
	body := `{"status":"needs_input","input_request":{"summary":"Choose","questions":[{"id":"choice","prompt":"Which?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"}],"allow_free_text":true,"required":true}]}}`
	if err := os.WriteFile(result, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{config: Config{RunID: "run_test"}}
	var signal *inputRequestSignal
	if err := runner.detectInputRequest(Stage{ID: "product"}, result); !errors.As(err, &signal) {
		t.Fatalf("product request was not signaled: %v", err)
	}
	var policy *inputPolicyError
	if err := runner.detectInputRequest(Stage{ID: "coder"}, result); !errors.As(err, &policy) {
		t.Fatalf("coder request was not rejected by policy: %v", err)
	}
	runner.config.HumanInput = []byte(`[{"request":{"stage":"product"},"responses":[{"answers":[]}]}]`)
	if err := runner.detectInputRequest(Stage{ID: "product"}, result); !errors.As(err, &policy) {
		t.Fatalf("second product round was not rejected: %v", err)
	}
	if err := runner.detectInputRequest(Stage{ID: "arch"}, result); !errors.As(err, &signal) {
		t.Fatalf("product response incorrectly exhausted architect round: %v", err)
	}
}
