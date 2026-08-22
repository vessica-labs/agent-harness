package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type repairRequest struct{ resultPath string }

func (r *repairRequest) Error() string { return "QA requested coding repair tickets" }

func (r *Runner) recoverRepairRequest(stage Stage) (*repairRequest, error) {
	if stage.ID != "qa" {
		return nil, nil
	}
	state, err := r.state()
	if err != nil {
		return nil, err
	}
	details, _ := state.Stages[stage.ID].(map[string]any)
	if details["status"] != "blocked" {
		return nil, nil
	}
	resultPath := filepath.Join(r.runDir, filepath.FromSlash(stage.Result.File))
	body, err := os.ReadFile(resultPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var output struct {
		Agent      string   `json:"agent"`
		Status     string   `json:"status"`
		NewTickets []ticket `json:"new_tickets"`
	}
	if json.Unmarshal(body, &output) != nil || output.Agent != "qa" || output.Status != "requeue" || len(output.NewTickets) == 0 {
		return nil, nil
	}
	return &repairRequest{resultPath: resultPath}, nil
}

func (r *Runner) loadRepairCounts() (map[string]int, error) {
	counts := map[string]int{}
	body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state struct {
		RepairCycles map[string]int `json:"repair_cycles"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	for stage, count := range state.RepairCycles {
		counts[stage] = count
	}
	return counts, nil
}

func (r *Runner) handleRepair(ctx context.Context, stage Stage, request *repairRequest, counts map[string]int) (int, error) {
	var loop *RepairLoop
	for index := range r.pipeline.RepairLoops {
		if r.pipeline.RepairLoops[index].From == stage.ID {
			loop = &r.pipeline.RepairLoops[index]
			break
		}
	}
	if loop == nil {
		return 0, errors.New("QA requested requeue but the pipeline declares no repair loop")
	}
	if counts[stage.ID] >= loop.MaxReentries {
		return 0, fmt.Errorf("QA repair loop exhausted after %d reentries", loop.MaxReentries)
	}
	body, err := os.ReadFile(request.resultPath)
	if err != nil {
		return 0, err
	}
	var output struct {
		NewTickets []ticket `json:"new_tickets"`
	}
	if err := json.Unmarshal(body, &output); err != nil {
		return 0, err
	}
	if len(output.NewTickets) == 0 {
		return 0, errors.New("QA requeue contained no repair tickets")
	}
	planPath := filepath.Join(r.runDir, "artifacts", "ticket-plan.json")
	planBody, err := os.ReadFile(planPath)
	if err != nil {
		return 0, err
	}
	var plan []ticket
	if err := json.Unmarshal(planBody, &plan); err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for _, item := range plan {
		known[item.Key] = true
	}
	for _, item := range output.NewTickets {
		if item.Key == "" {
			return 0, errors.New("QA repair ticket has no key")
		}
		if !known[item.Key] {
			plan = append(plan, item)
			known[item.Key] = true
		}
	}
	updated, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(planPath, append(updated, '\n'), 0o600); err != nil {
		return 0, err
	}
	target, through := -1, -1
	for index, item := range r.pipeline.Stages {
		if item.ID == loop.To {
			target = index
		}
		if item.ID == loop.Through {
			through = index
		}
	}
	if target < 0 || through < target {
		return 0, errors.New("repair loop has an invalid to/through range")
	}
	for index := target; index <= through; index++ {
		item := r.pipeline.Stages[index]
		if _, err := r.harness(ctx, r.repo, "set-stage", "--run-dir", r.runDir, "--stage", item.ID, "--status", "pending", "--details-json", `{"summary":"queued by QA repair loop"}`); err != nil {
			return 0, err
		}
	}
	counts[stage.ID]++
	stateBody, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err != nil {
		return 0, err
	}
	var state map[string]any
	if err := json.Unmarshal(stateBody, &state); err != nil {
		return 0, err
	}
	rawExisting, _ := state["tickets"].([]any)
	existingMaps := make([]map[string]any, 0, len(rawExisting))
	for _, value := range rawExisting {
		if item, ok := value.(map[string]any); ok {
			existingMaps = append(existingMaps, item)
		}
	}
	existingMaps = normalizeTicketState(existingMaps)
	existing := make([]any, 0, len(existingMaps)+len(output.NewTickets))
	existingKeys := map[string]bool{}
	for _, item := range existingMaps {
		existing = append(existing, item)
		if key, _ := item["key"].(string); key != "" {
			existingKeys[key] = true
		}
	}
	for _, item := range output.NewTickets {
		if !existingKeys[item.Key] {
			existing = append(existing, map[string]any{"key": item.Key, "depends_on": item.DependsOn, "status": "pending"})
			existingKeys[item.Key] = true
		}
	}
	patch := map[string]any{"tickets": existing, "repair_cycles": map[string]int{stage.ID: counts[stage.ID]}}
	if _, err := r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json", string(mustJSON(patch)), "--event", "qa.requeue"); err != nil {
		return 0, err
	}
	parent, err := r.harness(ctx, r.repo, "render-comment", "--run-dir", r.runDir, "--kind", "parent")
	if err != nil {
		return 0, err
	}
	if err := r.client.sync(ctx, map[string]any{"stage": stage.ID, "parent_comment": string(parent), "tickets": output.NewTickets}, &map[string]any{}); err != nil {
		return 0, err
	}
	if err := r.checkpoint(ctx); err != nil {
		return 0, err
	}
	_ = r.event(ctx, "qa.requeued", "warning", "QA created repair tickets and re-entered the coding pipeline", stage.ID, map[string]any{"cycle": counts[stage.ID]})
	return target, nil
}
