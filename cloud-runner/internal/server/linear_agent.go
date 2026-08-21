package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

// processLinearAgentSession turns a native Linear issue delegation into a
// durable harness run after the webhook response has already been returned.
func (s *Server) processLinearAgentSession(parsed linear.ParsedWebhook) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	token, err := s.linearAccessToken(ctx)
	if err != nil {
		s.logger.Error("process Linear agent session", "agent_session_id", parsed.AgentSessionID, "error", err)
		return
	}
	client := s.linear(token)
	// A native thought is the first provider call so Linear does not mark the
	// newly delegated AgentSession as unresponsive while routing is resolved.
	ackCtx, ackCancel := context.WithTimeout(ctx, 8*time.Second)
	acknowledgement, ackErr := client.CreateAgentActivity(ackCtx, parsed.AgentSessionID,
		map[string]any{"type": "thought", "body": "Vessica accepted this issue and is preparing the repository workflow."})
	ackCancel()
	if ackErr != nil {
		s.logger.Error("acknowledge Linear agent session", "agent_session_id", parsed.AgentSessionID, "error", ackErr)
	}
	issue, err := client.IssueContext(ctx, parsed.Delivery.IssueID)
	if err != nil {
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID, "I could not load the delegated Linear issue.", err)
		return
	}
	if issue.Team == nil || issue.Team.ID == "" {
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID, "This issue does not resolve to an accessible Linear team.", errors.New("issue team is missing"))
		return
	}
	parsed.Delivery.IssueID, parsed.Delivery.IssueKey = issue.ID, issue.Identifier
	parsed.Delivery.IssueURL, parsed.Delivery.IssueTitle = issue.URL, issue.Title
	parsed.Delivery.TeamID = issue.Team.ID
	if issue.Project != nil {
		parsed.Delivery.ProjectID = issue.Project.ID
	}
	parsed.Delivery.FeatureRequest = strings.TrimSpace(issue.Title + "\n\n" + issue.Description)
	parsed.Delivery.Dependencies = linear.DependencyIssueKeys(issue.Description)

	repository, err := s.store.FindLinearRepository(ctx, parsed.Delivery.WorkspaceID,
		parsed.Delivery.TeamID, parsed.Delivery.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		_, _ = s.store.RecordIgnoredLinearDelivery(ctx, parsed.Delivery, "", "repository_not_registered")
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID,
			"No Agent Harness repository is registered for this issue's team and project.", err)
		return
	}
	if err != nil {
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID, "I could not resolve the registered repository.", err)
		return
	}
	if issue.Parent != nil {
		_, _ = s.store.RecordIgnoredLinearDelivery(ctx, parsed.Delivery, repository.ID, "child_issue")
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID,
			"Delegate the root issue to Vessica; generated child tickets cannot start a separate harness run.", errors.New("delegated issue is a child"))
		return
	}
	if issue.ArchivedAt != nil || issue.CanceledAt != nil {
		_, _ = s.store.RecordIgnoredLinearDelivery(ctx, parsed.Delivery, repository.ID, "inactive_issue")
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID, "Archived or canceled issues cannot start a harness run.", errors.New("delegated issue is inactive"))
		return
	}
	if issue.Delegate == nil || issue.Delegate.ID != parsed.AppUserID {
		_, _ = s.store.RecordIgnoredLinearDelivery(ctx, parsed.Delivery, repository.ID, "agent_not_delegated")
		return
	}
	if repository.LinearAgentName != "" && !strings.EqualFold(issue.Delegate.Name, repository.LinearAgentName) {
		_, _ = s.store.RecordIgnoredLinearDelivery(ctx, parsed.Delivery, repository.ID, "wrong_agent")
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID,
			fmt.Sprintf("This repository is configured for the %s Linear agent.", repository.LinearAgentName), errors.New("delegated agent name does not match repository"))
		return
	}

	contextValue := map[string]any{
		"provider": "linear", "id": issue.ID, "key": issue.Identifier, "url": issue.URL,
		"title": issue.Title, "description": issue.Description, "comments": issue.Comments.Nodes,
		"attachments": issue.Attachments.Nodes, "dependencies": parsed.Delivery.Dependencies,
		"agent_session": map[string]any{"id": parsed.AgentSessionID, "app_user_id": parsed.AppUserID,
			"prompt_context": parsed.PromptContext},
	}
	if len(parsed.Delivery.Dependencies) > 0 {
		// Keep the run unclaimable while dependency lookups happen, but create
		// it first so Vessica can acknowledge the AgentSession immediately.
		parsed.Delivery.QueueReason = "dependencies_checking"
	}
	parsed.Delivery.SourceContext, _ = json.Marshal(contextValue)
	result, err := s.store.AcceptLinearDelivery(ctx, repository, parsed.Delivery)
	if err != nil {
		s.reportLinearAgentSessionError(ctx, client, parsed.AgentSessionID, "I could not queue this issue for Agent Harness.", err)
		return
	}
	if result.Run == nil {
		return
	}
	if acknowledgement.ID != "" {
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: result.Run.ID, LogicalKey: "agent:run:queued",
			Provider: "linear-agent", State: "synced", Marker: "agent-session:" + parsed.AgentSessionID + ":agent:run:queued",
			ExternalID: acknowledgement.ID, ExternalURL: result.Run.SourceIssueURL})
	}
	if result.Duplicate {
		_, _ = s.appendEvent(ctx, model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
			Type: "webhook.duplicate", Level: "info", Message: "Repeated Linear delegation resolved to the existing run"})
	}
	if !result.Duplicate && len(parsed.Delivery.Dependencies) > 0 {
		checking := model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
			Type: "run.dependencies_waiting", Level: "info", Message: "Vessica accepted the issue and is checking its Linear dependencies"}
		checking.Payload, _ = json.Marshal(map[string]any{"dependencies": parsed.Delivery.Dependencies,
			"queue_reason": "dependencies_checking"})
		_, _ = s.appendEvent(ctx, checking)
		if err := s.syncLinearLifecycleEvent(ctx, result.Run.ID, checking); err != nil {
			s.logger.Error("acknowledge delegated Linear issue", "run_id", result.Run.ID, "error", err)
		}
		pending, dependencyErr := s.pendingLinearDependencies(ctx, client, parsed.Delivery.Dependencies)
		reason := ""
		if dependencyErr != nil {
			reason = "dependencies_check_failed: " + strings.Join(parsed.Delivery.Dependencies, ", ")
		} else if len(pending) > 0 {
			reason = "dependencies_pending: " + strings.Join(pending, ", ")
		}
		if err := s.store.RequeueRun(ctx, result.Run.ID, reason); err != nil {
			s.logger.Error("update Linear dependency gate", "run_id", result.Run.ID, "error", err)
			return
		}
		result.Run.QueueReason = reason
		if reason == "" {
			released := model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
				Type: "run.dependencies_satisfied", Level: "info", Message: "Linear dependencies are Done; pipeline released"}
			_, _ = s.appendEvent(ctx, released)
			if err := s.syncLinearLifecycleEvent(ctx, result.Run.ID, released); err != nil {
				s.logger.Error("release delegated Linear issue", "run_id", result.Run.ID, "error", err)
			}
		}
	}
	if result.Run.State == "queued" && result.Run.CurrentStage == "" {
		event := model.Event{RunID: result.Run.ID, SourceIssueID: result.Run.SourceIssueID,
			Type: "run.queued", Level: "info", Message: "Vessica accepted the delegated Linear issue"}
		if strings.HasPrefix(result.Run.QueueReason, "dependencies_") {
			event.Type, event.Message = "run.dependencies_waiting", "Run is waiting for Linear dependencies to reach Done"
			event.Payload, _ = json.Marshal(map[string]any{"dependencies": parsed.Delivery.Dependencies,
				"queue_reason": result.Run.QueueReason})
			if !result.Duplicate && len(parsed.Delivery.Dependencies) == 0 {
				_, _ = s.appendEvent(ctx, event)
			}
		}
		if len(parsed.Delivery.Dependencies) == 0 || result.Duplicate {
			if err := s.syncLinearLifecycleEvent(ctx, result.Run.ID, event); err != nil {
				s.logger.Error("synchronize delegated Linear issue", "run_id", result.Run.ID, "error", err)
			}
		}
	}
	s.broker.Notify()
}

func (s *Server) reportLinearAgentSessionError(ctx context.Context, client *linearapi.Client, sessionID, message string, cause error) {
	s.logger.Error("Linear agent session", "agent_session_id", sessionID, "error", cause)
	if client == nil || sessionID == "" {
		return
	}
	activityCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := client.CreateAgentActivity(activityCtx, sessionID, map[string]any{"type": "error", "body": message}); err != nil {
		s.logger.Error("report Linear agent session error", "agent_session_id", sessionID, "error", err)
	}
}

func linearAgentSessionID(run model.Run) string {
	var metadata struct {
		AgentSession struct {
			ID string `json:"id"`
		} `json:"agent_session"`
	}
	if json.Unmarshal(run.Metadata, &metadata) != nil {
		return ""
	}
	return metadata.AgentSession.ID
}
