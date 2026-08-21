package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
)

func (s *Server) pendingLinearDependencies(ctx context.Context, client *linearapi.Client, dependencies []string) ([]string, error) {
	if len(dependencies) == 0 {
		return nil, nil
	}
	if client == nil {
		return append([]string(nil), dependencies...), errors.New("Linear service credential is not configured")
	}
	pending := make([]string, 0, len(dependencies))
	for _, key := range dependencies {
		issue, err := client.Issue(ctx, key)
		if err != nil {
			return append(pending, key), err
		}
		if !strings.EqualFold(issue.State.Type, "completed") {
			pending = append(pending, key)
		}
	}
	return pending, nil
}

func runDependencyKeys(run model.Run) []string {
	var metadata struct {
		Dependencies []string `json:"dependencies"`
	}
	if json.Unmarshal(run.Metadata, &metadata) == nil && len(metadata.Dependencies) > 0 {
		return metadata.Dependencies
	}
	return linear.DependencyIssueKeys(run.FeatureRequest)
}

func (s *Server) releaseLinearDependencyWaiters(ctx context.Context, repository model.Repository, client *linearapi.Client, changedIssueKey string) error {
	changedIssueKey = strings.ToUpper(strings.TrimSpace(changedIssueKey))
	if changedIssueKey == "" || client == nil {
		return nil
	}
	runs, err := s.store.ListRuns(ctx, model.RunFilter{State: "queued", RepositoryID: repository.ID, Limit: 500})
	if err != nil {
		return err
	}
	var combined error
	for _, run := range runs {
		if !strings.HasPrefix(run.QueueReason, "dependencies_") {
			continue
		}
		dependencies := runDependencyKeys(run)
		matches := false
		for _, dependency := range dependencies {
			matches = matches || strings.EqualFold(dependency, changedIssueKey)
		}
		matches = matches || strings.EqualFold(run.SourceIssueKey, changedIssueKey)
		if !matches {
			continue
		}
		pending, checkErr := s.pendingLinearDependencies(ctx, client, dependencies)
		if checkErr != nil {
			combined = errors.Join(combined, checkErr)
			continue
		}
		if len(pending) > 0 {
			if err := s.store.RequeueRun(ctx, run.ID, "dependencies_pending: "+strings.Join(pending, ", ")); err != nil {
				combined = errors.Join(combined, err)
			}
			continue
		}
		if err := s.store.RequeueRun(ctx, run.ID, ""); err != nil {
			combined = errors.Join(combined, err)
			continue
		}
		event := model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID, Type: "run.dependencies_satisfied",
			Level: "info", Message: "All Linear dependencies are Done; run released to the pipeline"}
		event.Payload, _ = json.Marshal(map[string]any{"dependencies": dependencies})
		_, _ = s.appendEvent(ctx, event)
		if err := s.syncLinearLifecycleEvent(ctx, run.ID, event); err != nil {
			combined = errors.Join(combined, err)
		}
		s.broker.Notify()
	}
	return combined
}
