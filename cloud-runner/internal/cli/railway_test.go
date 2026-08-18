package cli

import (
	"encoding/json"
	"testing"
)

func TestFindSandboxState(t *testing.T) {
	var document any
	if err := json.Unmarshal([]byte(`[{"id":"other","status":"RUNNING"},{"id":"target","status":"CREATING"}]`), &document); err != nil {
		t.Fatal(err)
	}
	if got, want := findSandboxState(document, "target"), "CREATING"; got != want {
		t.Fatalf("findSandboxState() = %q, want %q", got, want)
	}
}

func TestFindSandboxStateIgnoresNestedUnrelatedIDs(t *testing.T) {
	var document any
	if err := json.Unmarshal([]byte(`{"sandboxes":[{"id":"target","status":"RUNNING","metadata":{"id":"other"}}]}`), &document); err != nil {
		t.Fatal(err)
	}
	if got, want := findSandboxState(document, "target"), "RUNNING"; got != want {
		t.Fatalf("findSandboxState() = %q, want %q", got, want)
	}
}
