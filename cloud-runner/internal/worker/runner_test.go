package worker

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

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
