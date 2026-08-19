package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	githubwebhook "github.com/vessica-labs/agent-harness/cloud-runner/internal/github"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/githubapp"
)

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := readLimited(w, r, s.config.MaxRequestBytes)
	if err != nil {
		return
	}
	raw, err := s.credential(r.Context(), "github_app")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitHub App credential is not configured"))
		return
	}
	var credential githubapp.Credentials
	if json.Unmarshal(raw, &credential) != nil || credential.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("GitHub App webhook secret is not configured"))
		return
	}
	if err := githubwebhook.Verify(r.Header, body, credential.WebhookSecret); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	merged, eligible, err := githubwebhook.ParsePullRequestMerged(r.Header, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !eligible {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "reason": "not_merged_pull_request"})
		return
	}
	repositories, err := s.store.ListRepositories(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var repository model.Repository
	for _, candidate := range repositories {
		if candidate.Enabled && strings.EqualFold(candidate.GitHubOwner, merged.Owner) && strings.EqualFold(candidate.GitHubRepo, merged.Repository) {
			repository = candidate
			break
		}
	}
	if repository.ID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "reason": "repository_not_registered"})
		return
	}
	runs, err := s.store.ListRuns(r.Context(), model.RunFilter{RepositoryID: repository.ID, Limit: 500})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var run model.Run
	for _, candidate := range runs {
		if sameURL(candidate.PullRequestURL, merged.PullRequest) {
			run = candidate
			break
		}
	}
	if run.ID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "reason": "pull_request_not_managed"})
		return
	}
	payload, _ := json.Marshal(map[string]any{"url": merged.PullRequest, "delivery_id": merged.DeliveryID})
	event := model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, Stage: "pr", Type: "pr.merged",
		Level: "info", Message: "Pull request merged", Payload: payload}
	duplicate := pullRequestMerged(r.Context(), s, run.ID)
	if !duplicate {
		event, err = s.appendEvent(r.Context(), event)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.syncLinearLifecycleEvent(r.Context(), run.ID, event); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("synchronize merged pull request: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "run_id": run.ID, "duplicate": duplicate})
}

func sameURL(left, right string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(left), "/"), strings.TrimRight(strings.TrimSpace(right), "/"))
}
