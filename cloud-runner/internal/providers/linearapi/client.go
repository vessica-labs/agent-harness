package linearapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	token    string
	endpoint string
	http     *http.Client
}

type Issue struct {
	ID          string        `json:"id"`
	Identifier  string        `json:"identifier"`
	Title       string        `json:"title"`
	URL         string        `json:"url"`
	Description string        `json:"description"`
	State       WorkflowState `json:"state"`
	Team        *struct {
		ID string `json:"id"`
	} `json:"team,omitempty"`
	Project *struct {
		ID string `json:"id"`
	} `json:"project,omitempty"`
	Parent *struct {
		ID string `json:"id"`
	} `json:"parent,omitempty"`
	Delegate *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"delegate,omitempty"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	CanceledAt *time.Time `json:"canceledAt,omitempty"`
	Comments   struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
	Attachments struct {
		Nodes []Attachment `json:"nodes"`
	} `json:"attachments"`
	Children struct {
		Nodes []Issue `json:"nodes"`
	} `json:"children"`
}

type WorkflowState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Position float64 `json:"position"`
}

type LifecycleStates struct {
	Todo       WorkflowState
	InProgress WorkflowState
	NeedsInput WorkflowState
	ForReview  WorkflowState
	Done       WorkflowState
}

type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	User      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"user,omitempty"`
	Parent *struct {
		ID string `json:"id"`
	} `json:"parent,omitempty"`
	Issue *struct {
		ID string `json:"id"`
	} `json:"issue,omitempty"`
	Children struct {
		Nodes []Comment `json:"nodes"`
	} `json:"children,omitempty"`
}

type Attachment struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type RegistrationContext struct {
	Workspace Workspace `json:"workspace"`
	Agent     User      `json:"agent"`
	Teams     []Team    `json:"teams"`
	Projects  []Project `json:"projects"`
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AgentActivity struct {
	ID string `json:"id"`
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

type Project struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	TeamIDs []string `json:"team_ids"`
}

type IssueLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Ticket struct {
	Key                      string             `json:"key"`
	Type                     string             `json:"type,omitempty"`
	Title                    string             `json:"title"`
	Objective                string             `json:"objective"`
	SourceAcceptanceCriteria []string           `json:"source_acceptance_criteria,omitempty"`
	AcceptanceCriteria       []string           `json:"acceptance_criteria"`
	OwnedPaths               []string           `json:"owned_paths"`
	DependsOn                []string           `json:"depends_on"`
	FocusedChecks            []string           `json:"focused_checks,omitempty"`
	Verification             TicketVerification `json:"verification,omitempty"`
	CommitMessage            string             `json:"commit_message,omitempty"`
	Complexity               string             `json:"complexity,omitempty"`
	FailureEvidence          string             `json:"failure_evidence,omitempty"`
	ArchitectureConstraints  []string           `json:"architecture_constraints,omitempty"`
}

type TicketVerification struct {
	IterationChecks []string       `json:"iteration_checks"`
	TicketGate      []string       `json:"ticket_gate"`
	PipelineGates   []PipelineGate `json:"pipeline_gates"`
}

type PipelineGate struct {
	Stage   string `json:"stage"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

func New(token string) *Client {
	return &Client{token: token, endpoint: "https://api.linear.app/graphql", http: &http.Client{Timeout: 20 * time.Second}}
}

func NewWithEndpoint(token, endpoint string) *Client {
	return &Client{token: token, endpoint: endpoint, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) RegistrationContext(ctx context.Context) (RegistrationContext, error) {
	var result struct {
		Viewer       User      `json:"viewer"`
		Organization Workspace `json:"organization"`
		Teams        struct {
			Nodes []Team `json:"nodes"`
		} `json:"teams"`
		Projects struct {
			Nodes []struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Teams struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"nodes"`
		} `json:"projects"`
	}
	query := `query HarnessRegistrationContext{viewer{id name} organization{id name} teams{nodes{id name key}} projects{nodes{id name teams{nodes{id}}}}}`
	if err := c.graphql(ctx, query, map[string]any{}, &result); err != nil {
		return RegistrationContext{}, err
	}
	context := RegistrationContext{Workspace: result.Organization, Agent: result.Viewer, Teams: result.Teams.Nodes, Projects: make([]Project, 0, len(result.Projects.Nodes))}
	for _, project := range result.Projects.Nodes {
		value := Project{ID: project.ID, Name: project.Name, TeamIDs: make([]string, 0, len(project.Teams.Nodes))}
		for _, team := range project.Teams.Nodes {
			value.TeamIDs = append(value.TeamIDs, team.ID)
		}
		context.Projects = append(context.Projects, value)
	}
	return context, nil
}

func (c *Client) ValidateAgentIdentity(ctx context.Context, expectedName string) (User, error) {
	var result struct {
		Viewer User `json:"viewer"`
	}
	if err := c.graphql(ctx, `query HarnessAgentIdentity{viewer{id name}}`, map[string]any{}, &result); err != nil {
		return User{}, err
	}
	if result.Viewer.ID == "" || result.Viewer.Name == "" {
		return User{}, errors.New("Linear OAuth token does not resolve to an app user")
	}
	if expectedName != "" && !strings.EqualFold(strings.TrimSpace(result.Viewer.Name), strings.TrimSpace(expectedName)) {
		return result.Viewer, fmt.Errorf("Linear app actor is %q, expected %q", result.Viewer.Name, expectedName)
	}
	return result.Viewer, nil
}

func (c *Client) ValidateRegistration(ctx context.Context, workspaceID, teamID, projectID string) error {
	var result struct {
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
		Project *struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	query := `query HarnessRegistration($team:String!){organization{id} team(id:$team){id}}`
	variables := map[string]any{"team": teamID}
	if projectID != "" {
		query = `query HarnessRegistration($team:String!,$project:String!){organization{id} team(id:$team){id} project(id:$project){id}}`
		variables["project"] = projectID
	}
	if err := c.graphql(ctx, query, variables, &result); err != nil {
		return err
	}
	if result.Organization.ID != workspaceID || result.Team.ID != teamID || (projectID != "" && (result.Project == nil || result.Project.ID != projectID)) {
		return errors.New("Linear workspace, team, or project does not match the OAuth installation")
	}
	return nil
}

func (c *Client) Issue(ctx context.Context, id string) (Issue, error) {
	var result struct {
		Issue Issue `json:"issue"`
	}
	err := c.graphql(ctx, `query HarnessIssue($id:String!){issue(id:$id){id identifier title url description archivedAt canceledAt team{id} project{id} parent{id} delegate{id name} state{id name type position} comments{nodes{id body createdAt user{id name}}} children{nodes{id identifier title url description state{id name type position}}}}}`, map[string]any{"id": id}, &result)
	return result.Issue, err
}

func (c *Client) IssueContext(ctx context.Context, id string) (Issue, error) {
	var result struct {
		Issue Issue `json:"issue"`
	}
	err := c.graphql(ctx, `query HarnessIssueContext($id:String!){issue(id:$id){id identifier title url description archivedAt canceledAt team{id} project{id} parent{id} delegate{id name} comments{nodes{id body createdAt user{id name} children{nodes{id body createdAt user{id name}}}}} attachments{nodes{id title url}}}}`, map[string]any{"id": id}, &result)
	return result.Issue, err
}

func (c *Client) Comment(ctx context.Context, id string) (Comment, error) {
	var result struct {
		Comment Comment `json:"comment"`
	}
	err := c.graphql(ctx, `query HarnessComment($id:String!){comment(id:$id){id body createdAt parent{id} issue{id} user{id name}}}`, map[string]any{"id": id}, &result)
	return result.Comment, err
}

func (c *Client) workflowStates(ctx context.Context, teamID string) ([]WorkflowState, error) {
	var result struct {
		WorkflowStates struct {
			Nodes []WorkflowState `json:"nodes"`
		} `json:"workflowStates"`
	}
	query := `query HarnessWorkflowStates($teamId:ID!){workflowStates(filter:{team:{id:{eq:$teamId}}}){nodes{id name type position}}}`
	if err := c.graphql(ctx, query, map[string]any{"teamId": teamID}, &result); err != nil {
		return nil, err
	}
	return result.WorkflowStates.Nodes, nil
}

func resolveLifecycleStates(nodes []WorkflowState) LifecycleStates {
	choose := func(kind string, preferred ...string) WorkflowState {
		var candidates []WorkflowState
		for _, state := range nodes {
			if strings.EqualFold(state.Type, kind) {
				candidates = append(candidates, state)
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Position < candidates[j].Position })
		for _, name := range preferred {
			for _, state := range candidates {
				if strings.EqualFold(strings.TrimSpace(state.Name), name) {
					return state
				}
			}
		}
		if len(candidates) > 0 {
			return candidates[0]
		}
		return WorkflowState{}
	}
	states := LifecycleStates{
		Todo: choose("unstarted", "Todo", "To Do"), InProgress: choose("started", "In Progress", "Doing"),
		NeedsInput: choose("started", "Needs Input"),
		ForReview:  choose("started", "For Review", "In Review", "Review"),
		Done:       choose("completed", "Done", "Completed"),
	}
	if states.NeedsInput.ID != "" && !strings.EqualFold(strings.TrimSpace(states.NeedsInput.Name), "Needs Input") {
		states.NeedsInput = WorkflowState{}
	}
	if states.ForReview.ID != "" && !strings.EqualFold(strings.TrimSpace(states.ForReview.Name), "For Review") &&
		!strings.EqualFold(strings.TrimSpace(states.ForReview.Name), "In Review") && !strings.EqualFold(strings.TrimSpace(states.ForReview.Name), "Review") {
		states.ForReview = WorkflowState{}
	}
	return states
}

func validateLifecycleStates(states LifecycleStates) (LifecycleStates, error) {
	if states.Todo.ID == "" || states.InProgress.ID == "" || states.NeedsInput.ID == "" || states.ForReview.ID == "" || states.Done.ID == "" {
		return LifecycleStates{}, errors.New("Linear team must have Todo, In Progress, Needs Input, For Review, and Done workflow states")
	}
	return states, nil
}

// LifecycleStates resolves the repository team's own workflow instead of
// assuming that status names or IDs are shared across Linear workspaces.
func (c *Client) LifecycleStates(ctx context.Context, teamID string) (LifecycleStates, error) {
	nodes, err := c.workflowStates(ctx, teamID)
	if err != nil {
		return LifecycleStates{}, err
	}
	return validateLifecycleStates(resolveLifecycleStates(nodes))
}

// EnsureLifecycleStates idempotently installs the two custom states managed by
// Agent Harness. Linear's standard Todo, In Progress, and Done states remain
// workspace-owned and are only validated here.
func (c *Client) EnsureLifecycleStates(ctx context.Context, teamID string) (LifecycleStates, error) {
	nodes, err := c.workflowStates(ctx, teamID)
	if err != nil {
		return LifecycleStates{}, err
	}
	states := resolveLifecycleStates(nodes)
	created := false
	maxStartedPosition := states.InProgress.Position
	for _, state := range nodes {
		if strings.EqualFold(state.Type, "started") && state.Position > maxStartedPosition {
			maxStartedPosition = state.Position
		}
	}
	if states.NeedsInput.ID == "" {
		position := maxStartedPosition + 1
		if states.ForReview.ID != "" && states.ForReview.Position > states.InProgress.Position {
			position = (states.InProgress.Position + states.ForReview.Position) / 2
		}
		if _, err := c.createWorkflowState(ctx, teamID, "Needs Input", "#F2C94C",
			"Agent Harness is waiting for additional user input before work can continue.", position); err != nil {
			return LifecycleStates{}, fmt.Errorf("create Linear workflow state %q: %w", "Needs Input", err)
		}
		created = true
		if position > maxStartedPosition {
			maxStartedPosition = position
		}
	}
	if states.ForReview.ID == "" {
		if _, err := c.createWorkflowState(ctx, teamID, "For Review", "#5E6AD2",
			"Agent Harness has opened a pull request that is ready for human review.", maxStartedPosition+1); err != nil {
			return LifecycleStates{}, fmt.Errorf("create Linear workflow state %q: %w", "For Review", err)
		}
		created = true
	}
	if created {
		nodes, err = c.workflowStates(ctx, teamID)
		if err != nil {
			return LifecycleStates{}, err
		}
	}
	return validateLifecycleStates(resolveLifecycleStates(nodes))
}

func (c *Client) createWorkflowState(ctx context.Context, teamID, name, color, description string, position float64) (WorkflowState, error) {
	if teamID == "" || name == "" {
		return WorkflowState{}, errors.New("Linear team and workflow-state name are required")
	}
	var result struct {
		WorkflowStateCreate struct {
			Success       bool          `json:"success"`
			WorkflowState WorkflowState `json:"workflowState"`
		} `json:"workflowStateCreate"`
	}
	query := `mutation HarnessWorkflowStateCreate($input:WorkflowStateCreateInput!){workflowStateCreate(input:$input){success workflowState{id name type position}}}`
	input := map[string]any{"teamId": teamID, "name": name, "type": "started", "color": color,
		"description": description, "position": position}
	if err := c.graphql(ctx, query, map[string]any{"input": input}, &result); err != nil {
		return WorkflowState{}, err
	}
	if !result.WorkflowStateCreate.Success || result.WorkflowStateCreate.WorkflowState.ID == "" {
		return WorkflowState{}, errors.New("Linear workflow-state creation did not succeed")
	}
	return result.WorkflowStateCreate.WorkflowState, nil
}

func (c *Client) SetIssueState(ctx context.Context, issueID string, state WorkflowState) (Issue, error) {
	if issueID == "" || state.ID == "" {
		return Issue{}, errors.New("Linear issue and workflow state are required")
	}
	var result struct {
		IssueUpdate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	query := `mutation HarnessIssueState($id:String!,$stateId:String!){issueUpdate(id:$id,input:{stateId:$stateId}){success issue{id identifier url state{id name type position}}}}`
	if err := c.graphql(ctx, query, map[string]any{"id": issueID, "stateId": state.ID}, &result); err != nil {
		return Issue{}, err
	}
	if !result.IssueUpdate.Success {
		return Issue{}, errors.New("Linear issue state update did not succeed")
	}
	return result.IssueUpdate.Issue, nil
}

// CreateDelegatedIssue creates an issue delegated to the authenticated Linear
// app actor. Linear creates the AgentSession whose webhook enters the harness.
func (c *Client) CreateDelegatedIssue(ctx context.Context, teamID, projectID, agentName, title, description string) (Issue, error) {
	if teamID == "" || strings.TrimSpace(title) == "" {
		return Issue{}, errors.New("Linear team and issue title are required")
	}
	agent, err := c.ValidateAgentIdentity(ctx, agentName)
	if err != nil {
		return Issue{}, err
	}
	variables := map[string]any{"teamId": teamID, "delegateId": agent.ID,
		"title": strings.TrimSpace(title), "description": strings.TrimSpace(description)}
	mutation := `mutation HarnessDelegatedIssueCreate($teamId:String!,$delegateId:String!,$title:String!,$description:String!){issueCreate(input:{teamId:$teamId,delegateId:$delegateId,title:$title,description:$description}){success issue{id identifier title url description delegate{id name}}}}`
	if projectID != "" {
		variables["projectId"] = projectID
		mutation = `mutation HarnessDelegatedIssueCreate($teamId:String!,$projectId:String!,$delegateId:String!,$title:String!,$description:String!){issueCreate(input:{teamId:$teamId,projectId:$projectId,delegateId:$delegateId,title:$title,description:$description}){success issue{id identifier title url description delegate{id name}}}}`
	}
	var result struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.graphql(ctx, mutation, variables, &result); err != nil {
		return Issue{}, err
	}
	if !result.IssueCreate.Success || result.IssueCreate.Issue.ID == "" {
		return Issue{}, errors.New("Linear issue creation did not succeed")
	}
	return result.IssueCreate.Issue, nil
}

func (c *Client) CreateAgentActivity(ctx context.Context, agentSessionID string, content map[string]any) (AgentActivity, error) {
	if agentSessionID == "" || content["type"] == nil {
		return AgentActivity{}, errors.New("Linear agent session and activity type are required")
	}
	var result struct {
		AgentActivityCreate struct {
			Success       bool          `json:"success"`
			AgentActivity AgentActivity `json:"agentActivity"`
		} `json:"agentActivityCreate"`
	}
	query := `mutation HarnessAgentActivityCreate($input:AgentActivityCreateInput!){agentActivityCreate(input:$input){success agentActivity{id}}}`
	input := map[string]any{"agentSessionId": agentSessionID, "content": content}
	if err := c.graphql(ctx, query, map[string]any{"input": input}, &result); err != nil {
		return AgentActivity{}, err
	}
	if !result.AgentActivityCreate.Success || result.AgentActivityCreate.AgentActivity.ID == "" {
		return AgentActivity{}, errors.New("Linear agent activity creation did not succeed")
	}
	return result.AgentActivityCreate.AgentActivity, nil
}

func (c *Client) UpdateAgentSessionLinks(ctx context.Context, agentSessionID string, links []map[string]string) error {
	if agentSessionID == "" {
		return errors.New("Linear agent session is required")
	}
	var result struct {
		AgentSessionUpdate struct {
			Success bool `json:"success"`
		} `json:"agentSessionUpdate"`
	}
	query := `mutation HarnessAgentSessionLinks($id:String!,$input:AgentSessionUpdateInput!){agentSessionUpdate(id:$id,input:$input){success}}`
	if err := c.graphql(ctx, query, map[string]any{"id": agentSessionID, "input": map[string]any{"externalUrls": links}}, &result); err != nil {
		return err
	}
	if !result.AgentSessionUpdate.Success {
		return errors.New("Linear agent session link update did not succeed")
	}
	return nil
}

// ArchiveIssue resolves either an issue UUID or identifier and archives the
// exact issue. It is intentionally separate from normal pipeline operations.
func (c *Client) ArchiveIssue(ctx context.Context, id string) (Issue, error) {
	issue, err := c.Issue(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	if issue.ID == "" {
		return Issue{}, fmt.Errorf("Linear issue %q was not found", id)
	}
	var result struct {
		IssueArchive struct {
			Success bool `json:"success"`
		} `json:"issueArchive"`
	}
	if err := c.graphql(ctx, `mutation HarnessIssueArchive($id:String!){issueArchive(id:$id){success}}`, map[string]any{"id": issue.ID}, &result); err != nil {
		return Issue{}, err
	}
	if !result.IssueArchive.Success {
		return Issue{}, errors.New("Linear issue archive did not succeed")
	}
	return issue, nil
}

func (c *Client) UpsertComment(ctx context.Context, issueID, marker, body string) (Comment, error) {
	issue, err := c.Issue(ctx, issueID)
	if err != nil {
		return Comment{}, err
	}
	for _, comment := range issue.Comments.Nodes {
		if strings.Contains(comment.Body, marker) {
			var result struct {
				CommentUpdate struct {
					Success bool    `json:"success"`
					Comment Comment `json:"comment"`
				} `json:"commentUpdate"`
			}
			err := c.graphql(ctx, `mutation HarnessCommentUpdate($id:String!,$body:String!){commentUpdate(id:$id,input:{body:$body}){success comment{id body}}}`, map[string]any{"id": comment.ID, "body": body}, &result)
			if err != nil {
				return Comment{}, err
			}
			if !result.CommentUpdate.Success || result.CommentUpdate.Comment.ID == "" {
				return Comment{}, errors.New("Linear comment update did not succeed")
			}
			return result.CommentUpdate.Comment, nil
		}
	}
	var result struct {
		CommentCreate struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentCreate"`
	}
	err = c.graphql(ctx, `mutation HarnessCommentCreate($issueId:String!,$body:String!){commentCreate(input:{issueId:$issueId,body:$body}){success comment{id body}}}`, map[string]any{"issueId": issueID, "body": body}, &result)
	if err != nil {
		return Comment{}, err
	}
	if !result.CommentCreate.Success || result.CommentCreate.Comment.ID == "" {
		return Comment{}, errors.New("Linear comment creation did not succeed")
	}
	return result.CommentCreate.Comment, nil
}

func (c *Client) UpsertChild(ctx context.Context, parentID, teamID, existingID, marker string, ticket Ticket, state WorkflowState) (Issue, error) {
	description := ticketDescription(marker, ticket)
	if existingID != "" {
		return c.updateChild(ctx, existingID, ticket.Title, description, state)
	}
	parent, err := c.Issue(ctx, parentID)
	if err != nil {
		return Issue{}, err
	}
	for _, child := range parent.Children.Nodes {
		if strings.Contains(child.Description, marker) {
			return c.updateChild(ctx, child.ID, ticket.Title, description, state)
		}
	}
	var result struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	err = c.graphql(ctx, `mutation HarnessIssueCreate($teamId:String!,$parentId:String!,$title:String!,$description:String!,$stateId:String!){issueCreate(input:{teamId:$teamId,parentId:$parentId,title:$title,description:$description,stateId:$stateId}){success issue{id identifier title url description state{id name type position}}}}`, map[string]any{"teamId": teamID, "parentId": parentID, "title": ticket.Title, "description": description, "stateId": state.ID}, &result)
	if err != nil {
		return Issue{}, err
	}
	if !result.IssueCreate.Success || result.IssueCreate.Issue.ID == "" {
		return Issue{}, errors.New("Linear child issue creation did not succeed")
	}
	return result.IssueCreate.Issue, nil
}

func (c *Client) updateChild(ctx context.Context, id, title, description string, state WorkflowState) (Issue, error) {
	var result struct {
		IssueUpdate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	err := c.graphql(ctx, `mutation HarnessIssueUpdate($id:String!,$title:String!,$description:String!,$stateId:String!){issueUpdate(id:$id,input:{title:$title,description:$description,stateId:$stateId}){success issue{id identifier title url description state{id name type position}}}}`, map[string]any{"id": id, "title": title, "description": description, "stateId": state.ID}, &result)
	if err != nil {
		return Issue{}, err
	}
	if !result.IssueUpdate.Success || result.IssueUpdate.Issue.ID == "" {
		return Issue{}, errors.New("Linear child issue update did not succeed")
	}
	return result.IssueUpdate.Issue, nil
}

func ticketDescription(marker string, ticket Ticket) string {
	lines := []string{marker, "", ticket.Objective}
	if len(ticket.SourceAcceptanceCriteria) > 0 {
		lines = append(lines, "", "## Source acceptance criteria")
		for _, value := range ticket.SourceAcceptanceCriteria {
			lines = append(lines, "- `"+value+"`")
		}
	}
	lines = append(lines, "", "## Acceptance criteria")
	for _, value := range ticket.AcceptanceCriteria {
		lines = append(lines, "- "+value)
	}
	if ticket.FailureEvidence != "" {
		lines = append(lines, "", "## Failure evidence", ticket.FailureEvidence)
	}
	lines = append(lines, "", "## Harness execution", "- Logical key: `"+ticket.Key+"`", "- Depends on: "+empty(strings.Join(ticket.DependsOn, ", "), "None"), "- Owned paths: "+empty(strings.Join(ticket.OwnedPaths, ", "), "None"))
	if ticket.Type != "" {
		lines = append(lines, "- Type: "+ticket.Type)
	}
	if ticket.Complexity != "" {
		lines = append(lines, "- Complexity: "+ticket.Complexity)
	}
	return strings.Join(lines, "\n") + "\n"
}

func empty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, output any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Linear API returned %d", response.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return errors.New("Linear API: " + envelope.Errors[0].Message)
	}
	return json.Unmarshal(envelope.Data, output)
}
