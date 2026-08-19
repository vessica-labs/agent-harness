package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/notionapi"
)

type syncRequest struct {
	Stage          string             `json:"stage"`
	ParentComment  string             `json:"parent_comment"`
	Summary        string             `json:"summary,omitempty"`
	Tickets        []linearapi.Ticket `json:"tickets,omitempty"`
	TicketProgress []ticketProgress   `json:"ticket_progress,omitempty"`
	Artifacts      []syncArtifact     `json:"artifacts,omitempty"`
}

type ticketProgress struct {
	Key         string   `json:"key"`
	State       string   `json:"status"`
	Owner       string   `json:"owner"`
	Commit      string   `json:"commit"`
	DependsOn   []string `json:"depends_on"`
	ProviderID  string   `json:"provider_id,omitempty"`
	ProviderKey string   `json:"provider_key,omitempty"`
	ProviderURL string   `json:"provider_url,omitempty"`
}

type syncArtifact struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}
type syncResponse struct {
	CommentID string                      `json:"comment_id,omitempty"`
	Tickets   map[string]externalIdentity `json:"tickets,omitempty"`
	Artifacts map[string]externalIdentity `json:"artifacts,omitempty"`
}
type externalIdentity struct {
	ID  string `json:"id"`
	Key string `json:"key,omitempty"`
	URL string `json:"url,omitempty"`
}

func (s *Server) internalSync(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input syncRequest
	if err := decodeJSON(w, r, 8<<20, &input); err != nil {
		return
	}
	result, err := s.synchronize(r.Context(), runID, input)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) synchronize(ctx context.Context, runID string, input syncRequest) (syncResponse, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return syncResponse{}, err
	}
	repository, err := s.store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return syncResponse{}, err
	}
	linearToken, err := s.linearAccessToken(ctx)
	if err != nil {
		return syncResponse{}, err
	}
	linearClient := linearapi.New(linearToken)
	lifecycle, err := linearClient.LifecycleStates(ctx, repository.LinearTeamID)
	if err != nil {
		return syncResponse{}, fmt.Errorf("resolve Linear workflow states: %w", err)
	}
	result := syncResponse{Tickets: map[string]externalIdentity{}, Artifacts: map[string]externalIdentity{}}
	if input.ParentComment != "" {
		marker := "<!-- agent-harness:run:" + runID + " -->"
		comment, err := linearClient.UpsertComment(ctx, run.SourceIssueID, marker, input.ParentComment)
		if err != nil {
			return result, s.recordSyncFailure(ctx, runID, "parent-comment", "linear", marker, err)
		}
		result.CommentID = comment.ID
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: "parent-comment", Provider: "linear", State: "synced", Marker: marker, ExternalID: comment.ID})
	}
	for _, ticket := range input.Tickets {
		marker := fmt.Sprintf("<!-- agent-harness:child:%s:%s -->", runID, ticket.Key)
		previous, _ := s.store.GetExternalSync(ctx, runID, "ticket:"+ticket.Key, "linear")
		child, err := linearClient.UpsertChild(ctx, run.SourceIssueID, repository.LinearTeamID, previous.ExternalID, marker, ticket)
		if err != nil {
			return result, s.recordSyncFailure(ctx, runID, "ticket:"+ticket.Key, "linear", marker, err)
		}
		result.Tickets[ticket.Key] = externalIdentity{ID: child.ID, Key: child.Identifier, URL: child.URL}
		state := "planned"
		if previousTicket, ok := ticketByKey(ctx, s, runID, ticket.Key); ok {
			state = previousTicket.State
		}
		_ = s.store.PutTicket(ctx, model.TicketState{RunID: runID, LogicalKey: ticket.Key,
			ProviderIssueID: child.ID, ProviderIssueKey: child.Identifier, State: state, Dependencies: ticket.DependsOn})
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: "ticket:" + ticket.Key, Provider: "linear", State: "synced", Marker: marker, ExternalID: child.ID, ExternalURL: child.URL})
		target := workflowStateForTicket(state, lifecycle)
		if err := s.setLinearIssueState(ctx, linearClient, runID, "ticket-state:"+ticket.Key, child.ID, target, false); err != nil {
			return result, err
		}
		progressMarker := fmt.Sprintf("<!-- agent-harness:ticket:%s:%s -->", runID, ticket.Key)
		progress := progressMarker + "\n\n## Agent Harness ticket `" + ticket.Key + "`\n\n- Run: `" + runID + "`\n- Status: planned\n- Depends on: " + emptyJoin(ticket.DependsOn) + "\n"
		_, _ = linearClient.UpsertComment(ctx, child.ID, progressMarker, progress)
	}
	if len(input.Tickets) > 0 {
		if err := s.setLinearIssueState(ctx, linearClient, runID, "parent-state", run.SourceIssueID, lifecycle.InProgress, false); err != nil {
			return result, err
		}
	}
	for _, progress := range input.TicketProgress {
		synced, err := s.store.GetExternalSync(ctx, runID, "ticket:"+progress.Key, "linear")
		if err != nil || synced.ExternalID == "" {
			return result, s.recordSyncFailure(ctx, runID, "ticket-progress:"+progress.Key, "linear", progress.Key, errors.New("ticket child identity is missing"))
		}
		marker := fmt.Sprintf("<!-- agent-harness:ticket:%s:%s -->", runID, progress.Key)
		body := marker + "\n\n## Agent Harness ticket `" + progress.Key + "`\n\n- Run: `" + runID + "`\n- Status: " + progress.State +
			"\n- Depends on: " + emptyJoin(progress.DependsOn) + "\n- Owner: " + empty(progress.Owner, "Unclaimed") + "\n- Commit: " + empty(progress.Commit, "Pending") + "\n"
		comment, err := linearClient.UpsertComment(ctx, synced.ExternalID, marker, body)
		if err != nil {
			return result, s.recordSyncFailure(ctx, runID, "ticket-progress:"+progress.Key, "linear", marker, err)
		}
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: "ticket-progress:" + progress.Key,
			Provider: "linear", State: "synced", Marker: marker, ExternalID: comment.ID})
		if err := s.setLinearIssueState(ctx, linearClient, runID, "ticket-state:"+progress.Key,
			synced.ExternalID, workflowStateForTicket(progress.State, lifecycle), false); err != nil {
			return result, err
		}
	}
	if input.Summary != "" {
		marker := "<!-- agent-harness:summary:" + runID + " -->"
		comment, err := linearClient.UpsertComment(ctx, run.SourceIssueID, marker, input.Summary)
		if err != nil {
			return result, s.recordSyncFailure(ctx, runID, "summary", "linear", marker, err)
		}
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: "summary", Provider: "linear", State: "synced", Marker: marker, ExternalID: comment.ID})
		if allTicketsCompleted(ctx, s, runID) {
			if err := s.setLinearIssueState(ctx, linearClient, runID, "parent-state", run.SourceIssueID, lifecycle.Done, false); err != nil {
				return result, err
			}
		}
	}
	if len(input.Artifacts) > 0 {
		if repository.NotionParentID == "" {
			return result, errors.New("repository has no Notion parent page")
		}
		notionToken, err := s.credential(ctx, "notion")
		if err != nil {
			return result, errors.New("Notion service credential is not configured")
		}
		notionClient := notionapi.New(string(notionToken))
		hubMarker := "<!-- agent-harness:notion-hub:linear:" + strings.ToLower(run.SourceIssueKey) + " -->"
		hubSync, _ := s.store.GetExternalSync(ctx, runID, "notion-hub", "notion")
		hubTitle := run.SourceIssueKey + " — Agent Harness"
		hub, err := notionClient.UpsertPage(ctx, repository.NotionParentID, hubSync.ExternalID, hubTitle, hubMarker+"\n\n# "+hubTitle+"\n\nRun `"+runID+"`")
		if err != nil {
			return result, s.recordSyncFailure(ctx, runID, "notion-hub", "notion", hubMarker, err)
		}
		_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: "notion-hub", Provider: "notion", State: "synced", Marker: hubMarker, ExternalID: hub.ID, ExternalURL: hub.URL})
		for _, artifact := range input.Artifacts {
			logical := "artifact:" + artifact.Key
			marker := "<!-- agent-harness:notion-artifact:" + runID + ":" + artifact.Key + " -->"
			previous, _ := s.store.GetExternalSync(ctx, runID, logical, "notion")
			title := run.SourceIssueKey + " — " + artifact.Title
			page, err := notionClient.UpsertPage(ctx, hub.ID, previous.ExternalID, title, marker+"\n\n"+artifact.Markdown)
			if err != nil {
				return result, s.recordSyncFailure(ctx, runID, logical, "notion", marker, err)
			}
			result.Artifacts[artifact.Key] = externalIdentity{ID: page.ID, URL: page.URL}
			_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: logical, Provider: "notion", State: "synced", Marker: marker, ExternalID: page.ID, ExternalURL: page.URL})
		}
	}
	return result, nil
}

func ticketByKey(ctx context.Context, s *Server, runID, key string) (model.TicketState, bool) {
	values, err := s.store.ListTickets(ctx, runID)
	if err != nil {
		return model.TicketState{}, false
	}
	for _, value := range values {
		if value.LogicalKey == key {
			return value, true
		}
	}
	return model.TicketState{}, false
}

func workflowStateForTicket(state string, lifecycle linearapi.LifecycleStates) linearapi.WorkflowState {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running", "in_progress", "in progress", "claimed":
		return lifecycle.InProgress
	case "completed", "done":
		return lifecycle.Done
	default:
		return lifecycle.Todo
	}
}

func allTicketsCompleted(ctx context.Context, s *Server, runID string) bool {
	values, err := s.store.ListTickets(ctx, runID)
	if err != nil || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if value.State != "completed" {
			return false
		}
	}
	return true
}

func (s *Server) setLinearIssueState(ctx context.Context, client *linearapi.Client, runID, logicalKey, issueID string, state linearapi.WorkflowState, force bool) error {
	marker := "workflow-state:" + state.ID
	if previous, err := s.store.GetExternalSync(ctx, runID, logicalKey, "linear"); !force && err == nil && previous.State == "synced" && previous.Marker == marker {
		return nil
	}
	issue, err := client.SetIssueState(ctx, issueID, state)
	if err != nil {
		return s.recordSyncFailure(ctx, runID, logicalKey, "linear", marker, err)
	}
	return s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: logicalKey, Provider: "linear",
		State: "synced", Marker: marker, ExternalID: issueID, ExternalURL: issue.URL})
}

func (s *Server) reconcileRunProjections(ctx context.Context, runID string) (map[string]any, error) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	repository, err := s.store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return nil, err
	}
	linearToken, err := s.linearAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	linearClient := linearapi.New(linearToken)
	lifecycle, err := linearClient.LifecycleStates(ctx, repository.LinearTeamID)
	if err != nil {
		return nil, fmt.Errorf("resolve Linear workflow states: %w", err)
	}
	tickets, err := s.store.ListTickets(ctx, runID)
	if err != nil {
		return nil, err
	}
	linearUpdated := 0
	for _, ticket := range tickets {
		if ticket.ProviderIssueID == "" {
			continue
		}
		if err := s.setLinearIssueState(ctx, linearClient, runID, "ticket-state:"+ticket.LogicalKey,
			ticket.ProviderIssueID, workflowStateForTicket(ticket.State, lifecycle), true); err != nil {
			return nil, err
		}
		linearUpdated++
	}
	parentState := lifecycle.Todo
	if len(tickets) > 0 {
		parentState = lifecycle.InProgress
	}
	if run.State == "completed" && allTicketsCompleted(ctx, s, runID) {
		parentState = lifecycle.Done
	}
	if err := s.setLinearIssueState(ctx, linearClient, runID, "parent-state", run.SourceIssueID, parentState, true); err != nil {
		return nil, err
	}
	linearUpdated++

	notionRestored := 0
	if raw, credentialErr := s.credential(ctx, "notion"); credentialErr == nil {
		notionClient := notionapi.New(string(raw))
		syncs, listErr := s.store.ListExternalSyncs(ctx, runID)
		if listErr != nil {
			return nil, listErr
		}
		for _, projection := range syncs {
			if projection.Provider != "notion" || projection.ExternalID == "" ||
				(projection.LogicalKey != "notion-hub" && !strings.HasPrefix(projection.LogicalKey, "artifact:")) {
				continue
			}
			page, restoreErr := notionClient.RestorePage(ctx, projection.ExternalID)
			if restoreErr != nil {
				return nil, s.recordSyncFailure(ctx, runID, projection.LogicalKey, "notion", projection.Marker, restoreErr)
			}
			url := page.URL
			if url == "" {
				url = projection.ExternalURL
			}
			_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: projection.LogicalKey,
				Provider: "notion", State: "synced", Marker: projection.Marker, ExternalID: projection.ExternalID, ExternalURL: url})
			notionRestored++
		}
	}
	return map[string]any{"ok": true, "linear_issues_updated": linearUpdated, "notion_pages_restored": notionRestored}, nil
}

func (s *Server) linearAccessToken(ctx context.Context) (string, error) {
	s.linearMu.Lock()
	defer s.linearMu.Unlock()
	raw, err := s.credential(ctx, "linear_oauth")
	if err != nil {
		return "", errors.New("Linear service credential is not configured")
	}
	var credential linearapi.OAuthCredential
	if err := json.Unmarshal(raw, &credential); err != nil || credential.AccessToken == "" {
		return "", errors.New("stored Linear credential is invalid")
	}
	if credential.NeedsRefresh(time.Now().UTC()) {
		credential, err = linearapi.RefreshOAuth(ctx, nil, "", credential, time.Now().UTC())
		if err != nil {
			return "", fmt.Errorf("refresh Linear OAuth token: %w", err)
		}
		updated, _ := json.Marshal(credential)
		if err := s.putCredential(ctx, "linear_oauth", updated); err != nil {
			return "", fmt.Errorf("persist rotated Linear OAuth token: %w", err)
		}
	}
	return credential.AccessToken, nil
}

func (s *Server) recordSyncFailure(ctx context.Context, runID, key, provider, marker string, cause error) error {
	_ = s.store.PutExternalSync(ctx, model.ExternalSync{RunID: runID, LogicalKey: key, Provider: provider, State: "pending", Marker: marker, Error: cause.Error()})
	return cause
}

func emptyJoin(values []string) string {
	if len(values) == 0 {
		return "None"
	}
	return strings.Join(values, ", ")
}

func empty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
