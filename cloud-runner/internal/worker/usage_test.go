package worker

import (
	"math"
	"testing"
)

func TestParseCodexUsageAndEstimateCost(t *testing.T) {
	usage, ok := parseCodexUsage([]byte(`{"type":"turn.completed","usage":{"input_tokens":100000,"cached_input_tokens":40000,"output_tokens":10000,"reasoning_output_tokens":2500}}`), "gpt-5.3-codex")
	if !ok {
		t.Fatal("turn usage was not recognized")
	}
	want := (60000*1.75 + 40000*0.175 + 10000*14.0) / 1_000_000
	if usage.InputTokens != 100000 || usage.ReasoningTokens != 2500 || math.Abs(usage.EstimatedCostUSD-want) > 0.0000001 {
		t.Fatalf("usage=%+v want cost=%f", usage, want)
	}
	if _, ok := parseCodexUsage([]byte(`{"type":"item.completed"}`), "gpt-5.3-codex"); ok {
		t.Fatal("non-turn event was treated as usage")
	}
}
