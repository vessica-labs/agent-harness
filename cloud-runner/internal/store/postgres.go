package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Postgres struct {
	pool *pgxpool.Pool
}

func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Postgres) Close()                         { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := p.pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

const repositoryColumns = `id, name, github_owner, github_repo, github_installation_id,
base_branch, linear_workspace_id, linear_team_id, linear_project_id, trigger_label,
notion_parent_page_id, enabled, created_at, updated_at`

type rowScanner interface {
	Scan(...any) error
}

func scanRepository(row rowScanner) (model.Repository, error) {
	var value model.Repository
	err := row.Scan(&value.ID, &value.Name, &value.GitHubOwner, &value.GitHubRepo,
		&value.GitHubInstallation, &value.BaseBranch, &value.LinearWorkspaceID,
		&value.LinearTeamID, &value.LinearProjectID, &value.TriggerLabel,
		&value.NotionParentID, &value.Enabled, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (p *Postgres) PutRepository(ctx context.Context, repo model.Repository) (model.Repository, error) {
	if repo.ID == "" {
		repo.ID = newID("repo")
	}
	if repo.TriggerLabel == "" {
		repo.TriggerLabel = "agent-harness"
	}
	if repo.BaseBranch == "" {
		repo.BaseBranch = "main"
	}
	row := p.pool.QueryRow(ctx, `INSERT INTO repositories (
id, name, github_owner, github_repo, github_installation_id, base_branch,
linear_workspace_id, linear_team_id, linear_project_id, trigger_label,
notion_parent_page_id, enabled) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, github_owner=EXCLUDED.github_owner,
github_repo=EXCLUDED.github_repo, github_installation_id=EXCLUDED.github_installation_id,
base_branch=EXCLUDED.base_branch, linear_workspace_id=EXCLUDED.linear_workspace_id,
linear_team_id=EXCLUDED.linear_team_id, linear_project_id=EXCLUDED.linear_project_id,
trigger_label=EXCLUDED.trigger_label, notion_parent_page_id=EXCLUDED.notion_parent_page_id,
enabled=EXCLUDED.enabled, updated_at=now()
RETURNING `+repositoryColumns, repo.ID, repo.Name, repo.GitHubOwner, repo.GitHubRepo,
		repo.GitHubInstallation, repo.BaseBranch, repo.LinearWorkspaceID, repo.LinearTeamID,
		repo.LinearProjectID, repo.TriggerLabel, repo.NotionParentID, repo.Enabled)
	return scanRepository(row)
}

func (p *Postgres) ListRepositories(ctx context.Context) ([]model.Repository, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+repositoryColumns+` FROM repositories ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Repository, 0)
	for rows.Next() {
		value, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) GetRepository(ctx context.Context, id string) (model.Repository, error) {
	value, err := scanRepository(p.pool.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM repositories WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) DisableRepository(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE repositories SET enabled=false,updated_at=now() WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) FindLinearRepository(ctx context.Context, workspaceID, teamID, projectID string) (model.Repository, error) {
	value, err := scanRepository(p.pool.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM repositories
WHERE enabled=true AND linear_workspace_id=$1 AND linear_team_id=$2
AND (linear_project_id='' OR linear_project_id=$3) ORDER BY (linear_project_id=$3) DESC LIMIT 1`, workspaceID, teamID, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

const runColumns = `id, repository_id, provider, source_issue_id, source_issue_key,
source_issue_url, source_issue_title, feature_request, state, current_stage, queue_reason,
attempt, sandbox_id, sandbox_session, auth_slot_id, lease_owner, lease_expires_at,
heartbeat_at, branch, pull_request_url, preview_state, preview_url, preview_port, preview_expires_at,
error, metadata, codex_model, codex_calls,
input_tokens, cached_input_tokens, output_tokens, reasoning_output_tokens, estimated_api_cost_usd,
started_at, created_at, updated_at, completed_at`

func scanRun(row rowScanner) (model.Run, error) {
	var value model.Run
	err := row.Scan(&value.ID, &value.RepositoryID, &value.Provider, &value.SourceIssueID,
		&value.SourceIssueKey, &value.SourceIssueURL, &value.SourceIssueTitle,
		&value.FeatureRequest, &value.State, &value.CurrentStage, &value.QueueReason,
		&value.Attempt, &value.SandboxID, &value.SandboxSession, &value.AuthSlotID,
		&value.LeaseOwner, &value.LeaseExpiresAt, &value.HeartbeatAt, &value.Branch,
		&value.PullRequestURL, &value.PreviewState, &value.PreviewURL, &value.PreviewPort,
		&value.PreviewExpiresAt, &value.Error, &value.Metadata, &value.CodexModel, &value.CodexCalls,
		&value.InputTokens, &value.CachedInputTokens, &value.OutputTokens, &value.ReasoningTokens,
		&value.EstimatedCostUSD, &value.StartedAt, &value.CreatedAt,
		&value.UpdatedAt, &value.CompletedAt)
	value.DeriveDuration(time.Now().UTC())
	return value, err
}

func (p *Postgres) AddRunUsage(ctx context.Context, runID string, usage model.Usage) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET
codex_model=CASE WHEN codex_model='' THEN $2 WHEN codex_model=$2 THEN codex_model
  WHEN position(','||$2||',' in ','||codex_model||',')>0 THEN codex_model ELSE codex_model||','||$2 END,
codex_calls=codex_calls+1,input_tokens=input_tokens+$3,cached_input_tokens=cached_input_tokens+$4,
output_tokens=output_tokens+$5,reasoning_output_tokens=reasoning_output_tokens+$6,
estimated_api_cost_usd=estimated_api_cost_usd+$7,updated_at=now() WHERE id=$1`, runID, usage.Model,
		usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.EstimatedCostUSD)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

const inputRequestColumns = `id,run_id,stage,round,status,summary,questions,created_at,updated_at,answered_at`
const qualifiedInputRequestColumns = `r.id,r.run_id,r.stage,r.round,r.status,r.summary,r.questions,r.created_at,r.updated_at,r.answered_at`

func scanInputRequest(row rowScanner) (model.InputRequest, error) {
	var value model.InputRequest
	var questions []byte
	err := row.Scan(&value.ID, &value.RunID, &value.Stage, &value.Round, &value.Status,
		&value.Summary, &questions, &value.CreatedAt, &value.UpdatedAt, &value.AnsweredAt)
	if err == nil {
		err = json.Unmarshal(questions, &value.Questions)
	}
	return value, err
}

func (p *Postgres) CreateInputRequest(ctx context.Context, value model.InputRequest) (model.InputRequest, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return value, err
	}
	defer tx.Rollback(ctx)
	if value.ID == "" {
		value.ID = newID("input")
	}
	questions, _ := json.Marshal(value.Questions)
	value, err = scanInputRequest(tx.QueryRow(ctx, `INSERT INTO input_requests(id,run_id,stage,round,status,summary,questions)
VALUES($1,$2,$3,$4,'open',$5,$6) ON CONFLICT(run_id,stage,round) DO NOTHING RETURNING `+inputRequestColumns,
		value.ID, value.RunID, value.Stage, value.Round, value.Summary, questions))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrConflict
	}
	if err != nil {
		return value, err
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET state='awaiting_input',current_stage=$2,queue_reason='human_input',
error='',lease_owner='',lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND state='running'`, value.RunID, value.Stage)
	if err != nil {
		return value, err
	}
	if tag.RowsAffected() == 0 {
		return value, ErrNotFound
	}
	return value, tx.Commit(ctx)
}

func (p *Postgres) GetInputRequest(ctx context.Context, id string) (model.InputRequest, error) {
	value, err := scanInputRequest(p.pool.QueryRow(ctx, `SELECT `+inputRequestColumns+` FROM input_requests WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) ListInputRequests(ctx context.Context, filter model.InputRequestFilter) ([]model.InputRequest, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT `+inputRequestColumns+` FROM input_requests
WHERE ($1='' OR run_id=$1) AND ($2='' OR status=$2) ORDER BY updated_at DESC LIMIT $3`, filter.RunID, filter.Status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]model.InputRequest, 0)
	for rows.Next() {
		value, err := scanInputRequest(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func scanInputResponse(row rowScanner) (model.InputResponse, error) {
	var value model.InputResponse
	var answers []byte
	err := row.Scan(&value.ID, &value.RequestID, &value.RunID, &value.Channel, &value.ActorID,
		&value.ActorName, &value.ExternalID, &answers, &value.Accepted, &value.CreatedAt)
	if err == nil {
		err = json.Unmarshal(answers, &value.Answers)
	}
	return value, err
}

func (p *Postgres) ListInputResponses(ctx context.Context, requestID string) ([]model.InputResponse, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,request_id,run_id,channel,actor_id,actor_name,external_id,answers,accepted,created_at
FROM input_responses WHERE request_id=$1 ORDER BY created_at`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]model.InputResponse, 0)
	for rows.Next() {
		value, err := scanInputResponse(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (p *Postgres) ResolveInputRequest(ctx context.Context, id string, response model.InputResponse) (model.InputRequest, model.InputResponse, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.InputRequest{}, response, err
	}
	defer tx.Rollback(ctx)
	request, err := scanInputRequest(tx.QueryRow(ctx, `SELECT `+inputRequestColumns+` FROM input_requests WHERE id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return request, response, ErrNotFound
	}
	if err != nil {
		return request, response, err
	}
	if request.Status != "open" {
		return request, response, ErrConflict
	}
	if response.ID == "" {
		response.ID = newID("response")
	}
	answers, _ := json.Marshal(response.Answers)
	response, err = scanInputResponse(tx.QueryRow(ctx, `INSERT INTO input_responses(id,request_id,run_id,channel,actor_id,actor_name,external_id,answers,accepted)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,true) ON CONFLICT DO NOTHING
RETURNING id,request_id,run_id,channel,actor_id,actor_name,external_id,answers,accepted,created_at`, response.ID,
		request.ID, request.RunID, response.Channel, response.ActorID, response.ActorName, response.ExternalID, answers))
	if errors.Is(err, pgx.ErrNoRows) {
		return request, response, ErrConflict
	}
	if err != nil {
		return request, response, err
	}
	request, err = scanInputRequest(tx.QueryRow(ctx, `UPDATE input_requests SET status='answered',answered_at=now(),updated_at=now()
WHERE id=$1 RETURNING `+inputRequestColumns, id))
	if err != nil {
		return request, response, err
	}
	tag, err := tx.Exec(ctx, `UPDATE runs SET state='queued',queue_reason='human_input_answered',error='',
lease_owner='',lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND state='awaiting_input'`, request.RunID)
	if err != nil {
		return request, response, err
	}
	if tag.RowsAffected() == 0 {
		return request, response, ErrConflict
	}
	return request, response, tx.Commit(ctx)
}

func (p *Postgres) PutInputDelivery(ctx context.Context, value model.InputDelivery) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO input_deliveries(request_id,provider,state,external_id,external_url,error)
VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(request_id,provider) DO UPDATE SET state=EXCLUDED.state,
external_id=EXCLUDED.external_id,external_url=EXCLUDED.external_url,error=EXCLUDED.error,updated_at=now()`,
		value.RequestID, value.Provider, value.State, value.ExternalID, value.ExternalURL, value.Error)
	return err
}

func (p *Postgres) ListInputDeliveries(ctx context.Context, requestID string) ([]model.InputDelivery, error) {
	rows, err := p.pool.Query(ctx, `SELECT request_id,provider,state,external_id,external_url,error,updated_at
FROM input_deliveries WHERE request_id=$1 ORDER BY provider`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]model.InputDelivery, 0)
	for rows.Next() {
		var value model.InputDelivery
		if err := rows.Scan(&value.RequestID, &value.Provider, &value.State, &value.ExternalID,
			&value.ExternalURL, &value.Error, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (p *Postgres) FindInputRequestByDelivery(ctx context.Context, provider, externalID string) (model.InputRequest, error) {
	value, err := scanInputRequest(p.pool.QueryRow(ctx, `SELECT `+qualifiedInputRequestColumns+` FROM input_requests r
JOIN input_deliveries d ON d.request_id=r.id WHERE d.provider=$1 AND d.external_id=$2`, provider, externalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) PutStage(ctx context.Context, value model.StageState) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO stages(run_id,stage,state,attempt,details,started_at,completed_at)
VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id,stage) DO UPDATE SET
state=EXCLUDED.state,attempt=EXCLUDED.attempt,details=EXCLUDED.details,
started_at=COALESCE(stages.started_at,EXCLUDED.started_at),completed_at=EXCLUDED.completed_at,updated_at=now()`,
		value.RunID, value.Stage, value.State, value.Attempt, jsonOrEmpty(value.Details), value.StartedAt, value.CompletedAt)
	return err
}

func (p *Postgres) ListStages(ctx context.Context, runID string) ([]model.StageState, error) {
	rows, err := p.pool.Query(ctx, `SELECT run_id,stage,state,attempt,details,started_at,completed_at,updated_at
FROM stages WHERE run_id=$1 ORDER BY updated_at,stage`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.StageState, 0)
	for rows.Next() {
		var value model.StageState
		if err := rows.Scan(&value.RunID, &value.Stage, &value.State, &value.Attempt, &value.Details,
			&value.StartedAt, &value.CompletedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) PutTicket(ctx context.Context, value model.TicketState) error {
	dependencies, _ := json.Marshal(value.Dependencies)
	_, err := p.pool.Exec(ctx, `INSERT INTO tickets(run_id,logical_key,provider_issue_id,provider_issue_key,state,owner,commit_sha,dependencies,metadata)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(run_id,logical_key) DO UPDATE SET
provider_issue_id=CASE WHEN EXCLUDED.provider_issue_id='' THEN tickets.provider_issue_id ELSE EXCLUDED.provider_issue_id END,
provider_issue_key=CASE WHEN EXCLUDED.provider_issue_key='' THEN tickets.provider_issue_key ELSE EXCLUDED.provider_issue_key END,
state=EXCLUDED.state,owner=EXCLUDED.owner,commit_sha=CASE WHEN EXCLUDED.commit_sha='' THEN tickets.commit_sha ELSE EXCLUDED.commit_sha END,
dependencies=EXCLUDED.dependencies,metadata=EXCLUDED.metadata,updated_at=now()`, value.RunID, value.LogicalKey,
		value.ProviderIssueID, value.ProviderIssueKey, value.State, value.Owner, value.CommitSHA, dependencies, jsonOrEmpty(value.Metadata))
	return err
}

func (p *Postgres) ListTickets(ctx context.Context, runID string) ([]model.TicketState, error) {
	rows, err := p.pool.Query(ctx, `SELECT run_id,logical_key,provider_issue_id,provider_issue_key,state,owner,commit_sha,dependencies,metadata,updated_at
FROM tickets WHERE run_id=$1 ORDER BY logical_key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.TicketState, 0)
	for rows.Next() {
		var value model.TicketState
		var dependencies []byte
		if err := rows.Scan(&value.RunID, &value.LogicalKey, &value.ProviderIssueID, &value.ProviderIssueKey,
			&value.State, &value.Owner, &value.CommitSHA, &dependencies, &value.Metadata, &value.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(dependencies, &value.Dependencies)
		result = append(result, value)
	}
	return result, rows.Err()
}

func jsonOrEmpty(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func (p *Postgres) AcceptLinearDelivery(ctx context.Context, repo model.Repository, delivery model.LinearDelivery) (model.DeliveryResult, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return model.DeliveryResult{}, err
	}
	defer tx.Rollback(ctx)

	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO webhook_deliveries(provider, delivery_id, event_type, action,
payload_sha256, raw_payload, received_at, repository_id) VALUES ('linear',$1,$2,$3,$4,$5,$6,$7)
ON CONFLICT DO NOTHING RETURNING delivery_id`, delivery.DeliveryID, delivery.EventType,
		delivery.Action, delivery.PayloadSHA256, delivery.RawPayload, delivery.ReceivedAt, repo.ID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		var runID, reason string
		var accepted bool
		if err := tx.QueryRow(ctx, `SELECT COALESCE(run_id,''), accepted, reason FROM webhook_deliveries
WHERE provider='linear' AND delivery_id=$1`, delivery.DeliveryID).Scan(&runID, &accepted, &reason); err != nil {
			return model.DeliveryResult{}, err
		}
		result := model.DeliveryResult{Duplicate: true, Ignored: !accepted, Reason: reason}
		if runID != "" {
			run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, runID))
			if err != nil {
				return result, err
			}
			result.Run = &run
		}
		if err := tx.Commit(ctx); err != nil {
			return result, err
		}
		return result, nil
	}
	if err != nil {
		return model.DeliveryResult{}, err
	}

	runID := newID("run")
	var claimedRunID string
	err = tx.QueryRow(ctx, `INSERT INTO source_claims(provider, source_issue_id, repository_id, run_id)
VALUES ('linear',$1,$2,$3) ON CONFLICT DO NOTHING RETURNING run_id`, delivery.IssueID, repo.ID, runID).Scan(&claimedRunID)
	created := true
	if errors.Is(err, pgx.ErrNoRows) {
		created = false
		if err := tx.QueryRow(ctx, `SELECT run_id FROM source_claims WHERE provider='linear' AND source_issue_id=$1`, delivery.IssueID).Scan(&claimedRunID); err != nil {
			return model.DeliveryResult{}, err
		}
	} else if err != nil {
		return model.DeliveryResult{}, err
	}

	if created {
		_, err = tx.Exec(ctx, `INSERT INTO runs(id, repository_id, provider, source_issue_id,
source_issue_key, source_issue_url, source_issue_title, feature_request, metadata, state, queue_reason)
VALUES ($1,$2,'linear',$3,$4,$5,$6,$7,$8,'queued',$9)`, claimedRunID, repo.ID, delivery.IssueID,
			delivery.IssueKey, delivery.IssueURL, delivery.IssueTitle, delivery.FeatureRequest,
			jsonOrEmpty(delivery.SourceContext), delivery.QueueReason)
		if err != nil {
			return model.DeliveryResult{}, err
		}
		if _, err := appendEventTx(ctx, tx, model.Event{RunID: claimedRunID,
			SourceIssueID: delivery.IssueID, Type: "run.queued", Level: "info",
			Message: "Linear issue claimed and queued"}); err != nil {
			return model.DeliveryResult{}, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE webhook_deliveries SET run_id=$2, accepted=true,
reason=$3 WHERE provider='linear' AND delivery_id=$1`, delivery.DeliveryID, claimedRunID,
		map[bool]string{true: "claimed", false: "already_claimed"}[created])
	if err != nil {
		return model.DeliveryResult{}, err
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, claimedRunID))
	if err != nil {
		return model.DeliveryResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.DeliveryResult{}, err
	}
	return model.DeliveryResult{Run: &run, Duplicate: !created}, nil
}

func (p *Postgres) RecordIgnoredLinearDelivery(ctx context.Context, delivery model.LinearDelivery, repositoryID, reason string) (bool, error) {
	tag, err := p.pool.Exec(ctx, `INSERT INTO webhook_deliveries(provider,delivery_id,event_type,action,
payload_sha256,raw_payload,received_at,repository_id,accepted,reason)
VALUES('linear',$1,$2,$3,$4,$5,$6,NULLIF($7,''),false,$8) ON CONFLICT DO NOTHING`,
		delivery.DeliveryID, delivery.EventType, delivery.Action, delivery.PayloadSHA256, delivery.RawPayload, delivery.ReceivedAt, repositoryID, reason)
	return tag.RowsAffected() == 0, err
}

func (p *Postgres) ClaimNextRun(ctx context.Context, owner string, maxActive int, lease time.Duration) (model.Run, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return model.Run{}, err
	}
	defer tx.Rollback(ctx)
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runs WHERE state='running' AND lease_expires_at > now()`).Scan(&active); err != nil {
		return model.Run{}, err
	}
	if active >= maxActive {
		if _, err := tx.Exec(ctx, `UPDATE runs SET queue_reason='concurrency_limit',updated_at=now()
WHERE state='queued' AND queue_reason !~ '^dependencies_'`); err != nil {
			return model.Run{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return model.Run{}, err
		}
		return model.Run{}, ErrNoRunnableRun
	}
	run, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs
WHERE (state='queued' AND queue_reason !~ '^dependencies_')
   OR (state='running' AND lease_expires_at < now())
ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Run{}, ErrNoRunnableRun
	}
	if err != nil {
		return model.Run{}, err
	}
	if err := tx.QueryRow(ctx, `UPDATE runs SET state='running', attempt=attempt+1,
lease_owner=$2, lease_expires_at=now()+$3::interval, heartbeat_at=now(), queue_reason='',
started_at=COALESCE(started_at,now()),updated_at=now() WHERE id=$1 RETURNING `+runColumns, run.ID, owner, interval(lease)).Scan(
		&run.ID, &run.RepositoryID, &run.Provider, &run.SourceIssueID, &run.SourceIssueKey,
		&run.SourceIssueURL, &run.SourceIssueTitle, &run.FeatureRequest, &run.State,
		&run.CurrentStage, &run.QueueReason, &run.Attempt, &run.SandboxID,
		&run.SandboxSession, &run.AuthSlotID, &run.LeaseOwner, &run.LeaseExpiresAt,
		&run.HeartbeatAt, &run.Branch, &run.PullRequestURL, &run.Error, &run.Metadata,
		&run.CodexModel, &run.CodexCalls, &run.InputTokens, &run.CachedInputTokens,
		&run.OutputTokens, &run.ReasoningTokens, &run.EstimatedCostUSD,
		&run.StartedAt, &run.CreatedAt, &run.UpdatedAt, &run.CompletedAt); err != nil {
		return model.Run{}, err
	}
	if _, err := appendEventTx(ctx, tx, model.Event{RunID: run.ID, SourceIssueID: run.SourceIssueID,
		Type: "run.claimed", Level: "info", Message: "Run leased by dispatcher"}); err != nil {
		return model.Run{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Run{}, err
	}
	return run, nil
}

func interval(value time.Duration) string { return fmt.Sprintf("%f seconds", value.Seconds()) }

func (p *Postgres) GetRun(ctx context.Context, id string) (model.Run, error) {
	value, err := scanRun(p.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) ListRuns(ctx context.Context, filter model.RunFilter) ([]model.Run, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `SELECT `+runColumns+` FROM runs
WHERE ($1='' OR state=$1) AND ($2='' OR repository_id=$2)
AND ($3::timestamptz='0001-01-01T00:00:00Z'::timestamptz OR updated_at>$3)
ORDER BY created_at DESC LIMIT $4`, filter.State, filter.RepositoryID, filter.After, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Run, 0)
	for rows.Next() {
		value, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) SetRunState(ctx context.Context, id, state, stage, message string) error {
	completed := "NULL"
	if state == "completed" || state == "cancelled" {
		completed = "now()"
	}
	command := `UPDATE runs SET state=$2, current_stage=$3, error=$4,
updated_at=now(), completed_at=` + completed + ` WHERE id=$1
AND (state NOT IN ('completed','cancelled') OR state=$2)`
	tag, err := p.pool.Exec(ctx, command, id, state, stage, message)
	if err == nil && tag.RowsAffected() == 0 {
		if _, getErr := p.GetRun(ctx, id); getErr != nil {
			return getErr
		}
	}
	return err
}

func (p *Postgres) RequeueRun(ctx context.Context, id, reason string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET state='queued',queue_reason=$2,lease_owner='',
lease_expires_at=NULL,updated_at=now() WHERE id=$1 AND state NOT IN ('completed','cancelled')`, id, reason)
	if err == nil && tag.RowsAffected() == 0 {
		if _, getErr := p.GetRun(ctx, id); getErr != nil {
			return getErr
		}
	}
	return err
}

func (p *Postgres) SetSandbox(ctx context.Context, id, sandboxID, session string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET sandbox_id=$2, sandbox_session=$3, updated_at=now() WHERE id=$1`, id, sandboxID, session)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) SetAuthSlot(ctx context.Context, id, slotID string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET auth_slot_id=$2, updated_at=now() WHERE id=$1`, id, slotID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) SetDelivery(ctx context.Context, id, branch, pullRequestURL string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET branch=$2,pull_request_url=$3,updated_at=now() WHERE id=$1`, id, branch, pullRequestURL)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) SetPreview(ctx context.Context, id, state, url string, port int, expiresAt *time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET preview_state=$2, preview_url=$3, preview_port=$4,
preview_expires_at=$5, updated_at=now() WHERE id=$1`, id, state, url, port, expiresAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) Heartbeat(ctx context.Context, id, owner string, lease time.Duration) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET heartbeat_at=now(), lease_expires_at=now()+$3::interval,
updated_at=now() WHERE id=$1 AND lease_owner=$2 AND state='running'`, id, owner, interval(lease))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) UpdateRunInput(ctx context.Context, id, featureRequest string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET feature_request=$2, error='', updated_at=now()
WHERE id=$1 AND state='paused'`, id, featureRequest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 0 {
		return nil
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrConflict
}

func (p *Postgres) ResumeRun(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE runs SET state='queued', queue_reason='', error='',
lease_owner='', lease_expires_at=NULL, updated_at=now() WHERE id=$1 AND state='paused'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) CancelRun(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE runs SET state='cancelled', lease_owner='',
lease_expires_at=NULL, completed_at=now(), updated_at=now() WHERE id=$1
AND state NOT IN ('completed','cancelled')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE input_requests SET status='cancelled',updated_at=now()
WHERE run_id=$1 AND status='open'`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appendEventTx(ctx context.Context, tx pgx.Tx, event model.Event) (model.Event, error) {
	if event.Level == "" {
		event.Level = "info"
	}
	if len(event.Payload) == 0 {
		event.Payload = []byte(`{}`)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM runs WHERE id=$1 FOR UPDATE`, event.RunID); err != nil {
		return event, err
	}
	row := tx.QueryRow(ctx, `INSERT INTO events(run_id, run_seq, source_issue_id, sandbox_id,
stage, type, level, message, payload) SELECT $1, COALESCE(max(run_seq),0)+1,
$2,$3,$4,$5,$6,$7,$8 FROM events WHERE run_id=$1
RETURNING global_seq, run_seq, created_at`, event.RunID, event.SourceIssueID, event.SandboxID,
		event.Stage, event.Type, event.Level, event.Message, event.Payload)
	if err := row.Scan(&event.GlobalSeq, &event.RunSeq, &event.CreatedAt); err != nil {
		return event, err
	}
	event.ID = event.GlobalSeq
	event.Protocol = model.EventProtocol
	return event, nil
}

func (p *Postgres) AppendEvent(ctx context.Context, event model.Event) (model.Event, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return event, err
	}
	defer tx.Rollback(ctx)
	event, err = appendEventTx(ctx, tx, event)
	if err != nil {
		return event, err
	}
	return event, tx.Commit(ctx)
}

func (p *Postgres) ListEvents(ctx context.Context, filter model.EventFilter) ([]model.Event, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := p.pool.Query(ctx, `SELECT global_seq, run_seq, run_id, source_issue_id,
sandbox_id, stage, type, level, message, payload, created_at FROM events
WHERE global_seq>$1 AND ($2='' OR run_id=$2) ORDER BY global_seq LIMIT $3`, filter.After, filter.RunID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Event, 0)
	for rows.Next() {
		var value model.Event
		if err := rows.Scan(&value.GlobalSeq, &value.RunSeq, &value.RunID, &value.SourceIssueID,
			&value.SandboxID, &value.Stage, &value.Type, &value.Level, &value.Message,
			&value.Payload, &value.CreatedAt); err != nil {
			return nil, err
		}
		value.ID = value.GlobalSeq
		value.Protocol = model.EventProtocol
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) PutArtifact(ctx context.Context, artifact model.Artifact) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO artifacts(run_id,path,media_type,sha256,size,content)
VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(run_id,path) DO UPDATE SET media_type=EXCLUDED.media_type,
sha256=EXCLUDED.sha256,size=EXCLUDED.size,content=EXCLUDED.content,updated_at=now()`,
		artifact.RunID, artifact.Path, artifact.MediaType, artifact.SHA256, artifact.Size, artifact.Content)
	return err
}

func scanArtifact(row rowScanner, includeContent bool) (model.Artifact, error) {
	var value model.Artifact
	var err error
	if includeContent {
		err = row.Scan(&value.RunID, &value.Path, &value.MediaType, &value.SHA256,
			&value.Size, &value.Content, &value.CreatedAt, &value.UpdatedAt)
	} else {
		err = row.Scan(&value.RunID, &value.Path, &value.MediaType, &value.SHA256,
			&value.Size, &value.CreatedAt, &value.UpdatedAt)
	}
	return value, err
}

func (p *Postgres) GetArtifact(ctx context.Context, runID, path string) (model.Artifact, error) {
	value, err := scanArtifact(p.pool.QueryRow(ctx, `SELECT run_id,path,media_type,sha256,size,content,
created_at,updated_at FROM artifacts WHERE run_id=$1 AND path=$2`, runID, path), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) ListArtifacts(ctx context.Context, runID string) ([]model.Artifact, error) {
	rows, err := p.pool.Query(ctx, `SELECT run_id,path,media_type,sha256,size,created_at,updated_at
FROM artifacts WHERE run_id=$1 ORDER BY path`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Artifact, 0)
	for rows.Next() {
		value, err := scanArtifact(rows, false)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) GetExternalSync(ctx context.Context, runID, logicalKey, provider string) (model.ExternalSync, error) {
	var value model.ExternalSync
	err := p.pool.QueryRow(ctx, `SELECT run_id,logical_key,provider,state,marker,external_id,
external_url,error,updated_at FROM external_sync WHERE run_id=$1 AND logical_key=$2 AND provider=$3`,
		runID, logicalKey, provider).Scan(&value.RunID, &value.LogicalKey, &value.Provider,
		&value.State, &value.Marker, &value.ExternalID, &value.ExternalURL, &value.Error, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) ListExternalSyncs(ctx context.Context, runID string) ([]model.ExternalSync, error) {
	rows, err := p.pool.Query(ctx, `SELECT run_id,logical_key,provider,state,marker,external_id,
external_url,error,updated_at FROM external_sync WHERE run_id=$1 ORDER BY provider,logical_key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.ExternalSync, 0)
	for rows.Next() {
		var value model.ExternalSync
		if err := rows.Scan(&value.RunID, &value.LogicalKey, &value.Provider, &value.State,
			&value.Marker, &value.ExternalID, &value.ExternalURL, &value.Error, &value.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) PutExternalSync(ctx context.Context, value model.ExternalSync) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO external_sync(run_id,logical_key,provider,state,marker,
external_id,external_url,error) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(run_id,logical_key,provider) DO UPDATE SET state=EXCLUDED.state,marker=EXCLUDED.marker,
external_id=EXCLUDED.external_id,external_url=EXCLUDED.external_url,error=EXCLUDED.error,updated_at=now()`,
		value.RunID, value.LogicalKey, value.Provider, value.State, value.Marker,
		value.ExternalID, value.ExternalURL, value.Error)
	return err
}

func (p *Postgres) PutCredential(ctx context.Context, credential model.Credential) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO credentials(name,ciphertext) VALUES($1,$2)
ON CONFLICT(name) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,updated_at=now()`, credential.Name, credential.Ciphertext)
	return err
}

func (p *Postgres) GetCredential(ctx context.Context, name string) (model.Credential, error) {
	var value model.Credential
	err := p.pool.QueryRow(ctx, `SELECT name,ciphertext,updated_at FROM credentials WHERE name=$1`, name).Scan(&value.Name, &value.Ciphertext, &value.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, ErrNotFound
	}
	return value, err
}

func (p *Postgres) PutAuthSlot(ctx context.Context, slot model.AuthSlot) error {
	tag, err := p.pool.Exec(ctx, `INSERT INTO auth_slots(id,state,ciphertext,last_error) VALUES($1,$2,$3,$4)
ON CONFLICT(id) DO UPDATE SET state=EXCLUDED.state,ciphertext=EXCLUDED.ciphertext,
lease_run_id='',lease_expires_at=NULL,last_error=EXCLUDED.last_error,updated_at=now()
WHERE auth_slots.state<>'leased'`,
		slot.ID, slot.State, slot.Ciphertext, slot.LastError)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return err
}

func (p *Postgres) ListAuthSlots(ctx context.Context) ([]model.AuthSlot, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,state,ciphertext,lease_run_id,lease_expires_at,last_error,updated_at
FROM auth_slots ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.AuthSlot, 0)
	for rows.Next() {
		var value model.AuthSlot
		if err := rows.Scan(&value.ID, &value.State, &value.Ciphertext, &value.LeaseRunID,
			&value.LeaseExpiresAt, &value.LastError, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.Ciphertext = nil
		result = append(result, value)
	}
	return result, rows.Err()
}

func (p *Postgres) LeaseAuthSlots(ctx context.Context, runID string, count int, lease time.Duration) ([]model.AuthSlot, error) {
	if count < 1 {
		return nil, ErrNoAuthSlot
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,state,ciphertext,lease_run_id,lease_expires_at,last_error,updated_at
FROM auth_slots WHERE state='available' OR (state='leased' AND lease_expires_at<now())
ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT $1`, count)
	if err != nil {
		return nil, err
	}
	result := make([]model.AuthSlot, 0)
	for rows.Next() {
		var slot model.AuthSlot
		if err := rows.Scan(&slot.ID, &slot.State, &slot.Ciphertext, &slot.LeaseRunID,
			&slot.LeaseExpiresAt, &slot.LastError, &slot.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, slot)
	}
	rows.Close()
	if len(result) < count {
		return nil, ErrNoAuthSlot
	}
	for index := range result {
		slot := &result[index]
		err = tx.QueryRow(ctx, `UPDATE auth_slots SET state='leased',lease_run_id=$2,
lease_expires_at=now()+$3::interval,updated_at=now() WHERE id=$1
RETURNING id,state,ciphertext,lease_run_id,lease_expires_at,last_error,updated_at`, slot.ID, runID, interval(lease)).Scan(
			&slot.ID, &slot.State, &slot.Ciphertext, &slot.LeaseRunID, &slot.LeaseExpiresAt,
			&slot.LastError, &slot.UpdatedAt)
		if err != nil {
			return nil, err
		}
	}
	return result, tx.Commit(ctx)
}

func (p *Postgres) ReleaseAuthSlot(ctx context.Context, id, runID string, ciphertext []byte, lastError string) error {
	state := "available"
	if lastError != "" {
		state = "quarantined"
	}
	tag, err := p.pool.Exec(ctx, `UPDATE auth_slots SET state=$3,ciphertext=$4,lease_run_id='',
lease_expires_at=NULL,last_error=$5,updated_at=now() WHERE id=$1 AND lease_run_id=$2`,
		id, runID, state, ciphertext, lastError)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) QuarantineAuthSlot(ctx context.Context, id, runID, reason string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE auth_slots SET state='quarantined',lease_run_id='',
lease_expires_at=NULL,last_error=$3,updated_at=now() WHERE id=$1 AND lease_run_id=$2`, id, runID, reason)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
