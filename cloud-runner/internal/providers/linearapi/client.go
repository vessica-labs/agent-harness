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
	Comments    struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
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
	Done       WorkflowState
}

type Comment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

type RegistrationContext struct {
	Workspace Workspace `json:"workspace"`
	Teams     []Team    `json:"teams"`
	Projects  []Project `json:"projects"`
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
	Key                string   `json:"key"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	OwnedPaths         []string `json:"owned_paths"`
	DependsOn          []string `json:"depends_on"`
}

func New(token string) *Client {
	return &Client{token: token, endpoint: "https://api.linear.app/graphql", http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) RegistrationContext(ctx context.Context) (RegistrationContext, error) {
	var result struct {
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
	query := `query HarnessRegistrationContext{organization{id name} teams{nodes{id name key}} projects{nodes{id name teams{nodes{id}}}}}`
	if err := c.graphql(ctx, query, map[string]any{}, &result); err != nil {
		return RegistrationContext{}, err
	}
	context := RegistrationContext{Workspace: result.Organization, Teams: result.Teams.Nodes, Projects: make([]Project, 0, len(result.Projects.Nodes))}
	for _, project := range result.Projects.Nodes {
		value := Project{ID: project.ID, Name: project.Name, TeamIDs: make([]string, 0, len(project.Teams.Nodes))}
		for _, team := range project.Teams.Nodes {
			value.TeamIDs = append(value.TeamIDs, team.ID)
		}
		context.Projects = append(context.Projects, value)
	}
	return context, nil
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
	err := c.graphql(ctx, `query HarnessIssue($id:String!){issue(id:$id){id identifier title url description state{id name type position} comments{nodes{id body}} children{nodes{id identifier title url description state{id name type position}}}}}`, map[string]any{"id": id}, &result)
	return result.Issue, err
}

// LifecycleStates resolves the repository team's own workflow instead of
// assuming that status names or IDs are shared across Linear workspaces.
func (c *Client) LifecycleStates(ctx context.Context, teamID string) (LifecycleStates, error) {
	var result struct {
		WorkflowStates struct {
			Nodes []WorkflowState `json:"nodes"`
		} `json:"workflowStates"`
	}
	query := `query HarnessWorkflowStates($teamId:ID!){workflowStates(filter:{team:{id:{eq:$teamId}}}){nodes{id name type position}}}`
	if err := c.graphql(ctx, query, map[string]any{"teamId": teamID}, &result); err != nil {
		return LifecycleStates{}, err
	}
	choose := func(kind string, preferred ...string) WorkflowState {
		var candidates []WorkflowState
		for _, state := range result.WorkflowStates.Nodes {
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
		Done: choose("completed", "Done", "Completed"),
	}
	if states.Todo.ID == "" || states.InProgress.ID == "" || states.Done.ID == "" {
		return LifecycleStates{}, errors.New("Linear team must have unstarted, started, and completed workflow states")
	}
	return states, nil
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

// CreateRootIssue creates an issue that can enter the webhook-driven harness.
// The trigger label is resolved by name inside the configured team so callers
// never need to handle provider label identifiers or credentials locally.
func (c *Client) CreateRootIssue(ctx context.Context, teamID, projectID, labelName, title, description string) (Issue, error) {
	if teamID == "" || labelName == "" || strings.TrimSpace(title) == "" {
		return Issue{}, errors.New("Linear team, trigger label, and issue title are required")
	}
	var labels struct {
		IssueLabels struct {
			Nodes []IssueLabel `json:"nodes"`
		} `json:"issueLabels"`
	}
	query := `query HarnessIssueLabel($teamId:ID!,$name:String!){issueLabels(filter:{team:{id:{eq:$teamId}},name:{eq:$name}}){nodes{id name}}}`
	if err := c.graphql(ctx, query, map[string]any{"teamId": teamID, "name": labelName}, &labels); err != nil {
		return Issue{}, err
	}
	if len(labels.IssueLabels.Nodes) != 1 {
		return Issue{}, fmt.Errorf("Linear trigger label %q resolved to %d labels in the configured team", labelName, len(labels.IssueLabels.Nodes))
	}
	variables := map[string]any{"teamId": teamID, "labelIds": []string{labels.IssueLabels.Nodes[0].ID},
		"title": strings.TrimSpace(title), "description": strings.TrimSpace(description)}
	mutation := `mutation HarnessRootIssueCreate($teamId:String!,$labelIds:[String!]!,$title:String!,$description:String!){issueCreate(input:{teamId:$teamId,labelIds:$labelIds,title:$title,description:$description}){success issue{id identifier title url description}}}`
	if projectID != "" {
		variables["projectId"] = projectID
		mutation = `mutation HarnessRootIssueCreate($teamId:String!,$projectId:String!,$labelIds:[String!]!,$title:String!,$description:String!){issueCreate(input:{teamId:$teamId,projectId:$projectId,labelIds:$labelIds,title:$title,description:$description}){success issue{id identifier title url description}}}`
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
			return result.CommentUpdate.Comment, err
		}
	}
	var result struct {
		CommentCreate struct {
			Success bool    `json:"success"`
			Comment Comment `json:"comment"`
		} `json:"commentCreate"`
	}
	err = c.graphql(ctx, `mutation HarnessCommentCreate($issueId:String!,$body:String!){commentCreate(input:{issueId:$issueId,body:$body}){success comment{id body}}}`, map[string]any{"issueId": issueID, "body": body}, &result)
	return result.CommentCreate.Comment, err
}

func (c *Client) UpsertChild(ctx context.Context, parentID, teamID, existingID, marker string, ticket Ticket) (Issue, error) {
	description := ticketDescription(marker, ticket)
	if existingID != "" {
		return c.updateChild(ctx, existingID, ticket.Title, description)
	}
	parent, err := c.Issue(ctx, parentID)
	if err != nil {
		return Issue{}, err
	}
	for _, child := range parent.Children.Nodes {
		if strings.Contains(child.Description, marker) {
			return c.updateChild(ctx, child.ID, ticket.Title, description)
		}
	}
	var result struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	err = c.graphql(ctx, `mutation HarnessIssueCreate($teamId:String!,$parentId:String!,$title:String!,$description:String!){issueCreate(input:{teamId:$teamId,parentId:$parentId,title:$title,description:$description}){success issue{id identifier title url description}}}`, map[string]any{"teamId": teamID, "parentId": parentID, "title": ticket.Title, "description": description}, &result)
	return result.IssueCreate.Issue, err
}

func (c *Client) updateChild(ctx context.Context, id, title, description string) (Issue, error) {
	var result struct {
		IssueUpdate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueUpdate"`
	}
	err := c.graphql(ctx, `mutation HarnessIssueUpdate($id:String!,$title:String!,$description:String!){issueUpdate(id:$id,input:{title:$title,description:$description}){success issue{id identifier title url description}}}`, map[string]any{"id": id, "title": title, "description": description}, &result)
	return result.IssueUpdate.Issue, err
}

func ticketDescription(marker string, ticket Ticket) string {
	lines := []string{marker, "", ticket.Objective, "", "## Acceptance criteria"}
	for _, value := range ticket.AcceptanceCriteria {
		lines = append(lines, "- "+value)
	}
	lines = append(lines, "", "## Harness execution", "- Logical key: `"+ticket.Key+"`", "- Depends on: "+empty(strings.Join(ticket.DependsOn, ", "), "None"), "- Owned paths: "+empty(strings.Join(ticket.OwnedPaths, ", "), "None"))
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
