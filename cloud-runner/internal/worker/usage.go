package worker

import (
	"encoding/json"
	"strings"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

func parseCodexUsage(line []byte, codexModel string) (model.Usage, bool) {
	var value struct {
		Type  string `json:"type"`
		Usage struct {
			InputTokens       int64 `json:"input_tokens"`
			CachedInputTokens int64 `json:"cached_input_tokens"`
			OutputTokens      int64 `json:"output_tokens"`
			ReasoningTokens   int64 `json:"reasoning_output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &value) != nil || value.Type != "turn.completed" {
		return model.Usage{}, false
	}
	usage := model.Usage{Model: codexModel, InputTokens: value.Usage.InputTokens,
		CachedInputTokens: value.Usage.CachedInputTokens, OutputTokens: value.Usage.OutputTokens,
		ReasoningTokens: value.Usage.ReasoningTokens}
	usage.EstimatedCostUSD = estimateAPICost(usage)
	return usage, true
}

func estimateAPICost(usage model.Usage) float64 {
	type rates struct{ input, cached, output float64 }
	prices := map[string]rates{
		"gpt-5.3-codex": {1.75, 0.175, 14},
		"gpt-5.2-codex": {1.75, 0.175, 14},
		"gpt-5.6-sol":   {5, 0.5, 30},
	}
	rate, ok := prices[strings.ToLower(usage.Model)]
	if !ok {
		return 0
	}
	uncached := usage.InputTokens - usage.CachedInputTokens
	if uncached < 0 {
		uncached = 0
	}
	inputMultiplier, outputMultiplier := 1.0, 1.0
	if strings.EqualFold(usage.Model, "gpt-5.6-sol") && usage.InputTokens > 272000 {
		inputMultiplier, outputMultiplier = 2, 1.5
	}
	return (float64(uncached)*rate.input*inputMultiplier +
		float64(usage.CachedInputTokens)*rate.cached*inputMultiplier +
		float64(usage.OutputTokens)*rate.output*outputMultiplier) / 1_000_000
}
