package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func (r *Runner) recordTicketPlan(ctx context.Context) error {
	body, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"))
	if err != nil {
		return err
	}
	var plan []ticket
	if err := json.Unmarshal(body, &plan); err != nil {
		return err
	}
	values := make([]map[string]any, 0, len(plan))
	for _, item := range plan {
		values = append(values, map[string]any{"key": item.Key, "depends_on": item.DependsOn, "status": "pending", "owner": "", "commit": nil})
	}
	_, err = r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json", string(mustJSON(map[string]any{"tickets": values})), "--event", "tickets.planned")
	return err
}

func (r *Runner) recordTicketCompletion(ctx context.Context, completed ticket, commit string) error {
	body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err != nil {
		return err
	}
	var state struct {
		Tickets []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return err
	}
	for _, value := range state.Tickets {
		if value["key"] == completed.Key {
			value["status"] = "completed"
			value["owner"] = r.config.LeaseOwner
			value["commit"] = commit
			delete(value, "blocker")
		}
	}
	_, err = r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"tickets": state.Tickets})), "--event", "ticket.completed",
		"--event-details-json", string(mustJSON(map[string]any{"ticket_key": completed.Key, "commit": commit})))
	return err
}

func (r *Runner) recordTicketFailure(ctx context.Context, failed ticket, blocker string) error {
	body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err != nil {
		return err
	}
	var state struct {
		Tickets []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return err
	}
	for _, value := range state.Tickets {
		if value["key"] == failed.Key {
			value["status"] = "failed"
			value["owner"] = r.config.LeaseOwner
			value["commit"] = nil
			value["blocker"] = blocker
		}
	}
	_, err = r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"tickets": state.Tickets})), "--event", "ticket.failed",
		"--event-details-json", string(mustJSON(map[string]any{"ticket_key": failed.Key, "blocker": blocker})))
	return err
}

func (r *Runner) recordTicketWaveStarted(ctx context.Context, wave []ticket) error {
	body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
	if err != nil {
		return err
	}
	var state struct {
		Tickets []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return err
	}
	ready := map[string]bool{}
	for _, item := range wave {
		ready[item.Key] = true
	}
	for _, value := range state.Tickets {
		if key, _ := value["key"].(string); ready[key] {
			value["status"] = "running"
			value["owner"] = r.config.LeaseOwner
			delete(value, "blocker")
		}
	}
	_, err = r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json",
		string(mustJSON(map[string]any{"tickets": state.Tickets})), "--event", "tickets.started")
	return err
}

func (r *Runner) syncTicketProgress(ctx context.Context, stageID string) error {
	request, err := ticketProgressRequest(r.runDir, stageID)
	if err != nil {
		return err
	}
	return r.client.sync(ctx, request, &map[string]any{})
}

func ticketProgressRequest(runDir, stageID string) (map[string]any, error) {
	body, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state struct {
		Tickets []map[string]any `json:"tickets"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	state.Tickets = normalizeTicketState(state.Tickets)
	planBody, err := os.ReadFile(filepath.Join(runDir, "artifacts", "ticket-plan.json"))
	if err != nil {
		return nil, err
	}
	var plan []ticket
	if err := json.Unmarshal(planBody, &plan); err != nil {
		return nil, err
	}
	return map[string]any{"stage": stageID, "tickets": plan, "ticket_progress": state.Tickets}, nil
}

func normalizeTicketState(values []map[string]any) []map[string]any {
	order := make([]string, 0, len(values))
	selected := map[string]map[string]any{}
	for _, value := range values {
		key, _ := value["key"].(string)
		if key == "" {
			continue
		}
		current, exists := selected[key]
		if !exists {
			order = append(order, key)
			selected[key] = value
			continue
		}
		if ticketStateRank(value["status"]) > ticketStateRank(current["status"]) {
			selected[key] = value
		}
	}
	result := make([]map[string]any, 0, len(order))
	for _, key := range order {
		result = append(result, selected[key])
	}
	return result
}

func ticketStateRank(value any) int {
	switch value {
	case "completed":
		return 4
	case "running":
		return 3
	case "failed":
		return 2
	case "pending":
		return 1
	default:
		return 0
	}
}

func (r *Runner) syncStage(ctx context.Context, stage Stage) error {
	parent, err := r.harness(ctx, r.repo, "render-comment", "--run-dir", r.runDir, "--kind", "parent")
	if err != nil {
		return err
	}
	request := map[string]any{"stage": stage.ID, "parent_comment": string(parent)}
	if stage.ID == "product" {
		body, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"))
		if err != nil {
			return err
		}
		var tickets []ticket
		if err := json.Unmarshal(body, &tickets); err != nil {
			return err
		}
		request["tickets"] = tickets
		prd, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "prd.md"))
		if err != nil {
			return err
		}
		request["artifacts"] = []map[string]string{{"key": "prd", "title": "Product Requirements", "markdown": string(prd)}}
	}
	if stage.ID == "arch" {
		adr, err := os.ReadFile(filepath.Join(r.runDir, "artifacts", "adr.md"))
		if err != nil {
			return err
		}
		request["artifacts"] = []map[string]string{{"key": "adr", "title": "Architecture Decision Record", "markdown": string(adr)}}
	}
	if stage.ID == "docs" {
		resultPath := filepath.Join(r.runDir, filepath.FromSlash(stage.Result.File))
		body, err := os.ReadFile(resultPath)
		if err != nil {
			return err
		}
		var output struct {
			ExternalDocuments []struct {
				Title    string `json:"title"`
				Markdown string `json:"markdown"`
			} `json:"external_documents"`
		}
		if err := json.Unmarshal(body, &output); err != nil {
			return err
		}
		var artifacts []map[string]string
		for _, doc := range output.ExternalDocuments {
			artifacts = append(artifacts, map[string]string{"key": strings.ToLower(safeName(doc.Title)), "title": doc.Title, "markdown": doc.Markdown})
		}
		request["artifacts"] = artifacts
	}
	if stage.ID == "coder" {
		body, err := os.ReadFile(filepath.Join(r.runDir, "state.json"))
		if err != nil {
			return err
		}
		var state struct {
			Tickets []map[string]any `json:"tickets"`
		}
		if err := json.Unmarshal(body, &state); err != nil {
			return err
		}
		request["ticket_progress"] = state.Tickets
	}
	state, err := r.state()
	if err != nil {
		return err
	}
	if state.Status == "completed" {
		summary, err := r.harness(ctx, r.repo, "render-comment", "--run-dir", r.runDir, "--kind", "summary")
		if err != nil {
			return err
		}
		request["summary"] = string(summary)
	}
	var response struct {
		CommentID string `json:"comment_id"`
		Tickets   map[string]struct {
			ID  string `json:"id"`
			Key string `json:"key"`
			URL string `json:"url"`
		} `json:"tickets"`
		Artifacts map[string]struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"artifacts"`
	}
	if err := r.client.sync(ctx, request, &response); err != nil {
		return err
	}
	patch := map[string]any{"external": map[string]any{"parent_comment_id": response.CommentID, "sync_pending": []any{}}}
	if len(response.Artifacts) > 0 {
		artifacts := map[string]any{}
		for key, value := range response.Artifacts {
			artifacts[key] = map[string]any{"notion_id": value.ID, "notion_url": value.URL, "path": "artifacts/" + key + ".md"}
		}
		patch["artifacts"] = artifacts
	}
	if len(response.Tickets) > 0 {
		body, _ := os.ReadFile(filepath.Join(r.runDir, "artifacts", "ticket-plan.json"))
		var plan []ticket
		_ = json.Unmarshal(body, &plan)
		values := make([]map[string]any, 0, len(plan))
		for _, item := range plan {
			identity := response.Tickets[item.Key]
			values = append(values, map[string]any{"key": item.Key, "depends_on": item.DependsOn, "status": "pending", "provider_id": identity.ID, "provider_key": identity.Key, "provider_url": identity.URL})
		}
		patch["tickets"] = values
	}
	if _, err := r.harness(ctx, r.repo, "checkpoint", "--run-dir", r.runDir, "--patch-json", string(mustJSON(patch)), "--event", "external.synced"); err != nil {
		return err
	}
	if stage.ID == "product" {
		updated, err := r.harness(ctx, r.repo, "render-comment", "--run-dir", r.runDir, "--kind", "parent")
		if err != nil {
			return err
		}
		return r.client.sync(ctx, map[string]any{"stage": stage.ID, "parent_comment": string(updated)}, &map[string]any{})
	}
	return nil
}
