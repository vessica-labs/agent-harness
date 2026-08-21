package model

import (
	"encoding/json"
	"time"
)

const EventProtocol = "agent-harness.events/v1"

type Repository struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	GitHubOwner        string    `json:"github_owner"`
	GitHubRepo         string    `json:"github_repo"`
	GitHubInstallation int64     `json:"github_installation_id,omitempty"`
	BaseBranch         string    `json:"base_branch"`
	LinearWorkspaceID  string    `json:"linear_workspace_id"`
	LinearTeamID       string    `json:"linear_team_id"`
	LinearProjectID    string    `json:"linear_project_id,omitempty"`
	TriggerLabel       string    `json:"trigger_label"`
	NotionParentID     string    `json:"notion_parent_page_id,omitempty"`
	Enabled            bool      `json:"enabled"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Run struct {
	ID                string          `json:"id"`
	RepositoryID      string          `json:"repository_id"`
	Provider          string          `json:"provider"`
	SourceIssueID     string          `json:"source_issue_id"`
	SourceIssueKey    string          `json:"source_issue_key"`
	SourceIssueURL    string          `json:"source_issue_url,omitempty"`
	SourceIssueTitle  string          `json:"source_issue_title"`
	FeatureRequest    string          `json:"feature_request,omitempty"`
	State             string          `json:"state"`
	CurrentStage      string          `json:"current_stage,omitempty"`
	QueueReason       string          `json:"queue_reason,omitempty"`
	Attempt           int             `json:"attempt"`
	SandboxID         string          `json:"sandbox_id,omitempty"`
	SandboxSession    string          `json:"sandbox_session,omitempty"`
	AuthSlotID        string          `json:"auth_slot_id,omitempty"`
	LeaseOwner        string          `json:"lease_owner,omitempty"`
	LeaseExpiresAt    *time.Time      `json:"lease_expires_at,omitempty"`
	HeartbeatAt       *time.Time      `json:"heartbeat_at,omitempty"`
	Branch            string          `json:"branch,omitempty"`
	PullRequestURL    string          `json:"pull_request_url,omitempty"`
	PreviewState      string          `json:"preview_state,omitempty"`
	PreviewURL        string          `json:"preview_url,omitempty"`
	PreviewPort       int             `json:"preview_port,omitempty"`
	PreviewExpiresAt  *time.Time      `json:"preview_expires_at,omitempty"`
	Error             string          `json:"error,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CodexModel        string          `json:"codex_model,omitempty"`
	CodexCalls        int64           `json:"codex_calls"`
	InputTokens       int64           `json:"input_tokens"`
	CachedInputTokens int64           `json:"cached_input_tokens"`
	OutputTokens      int64           `json:"output_tokens"`
	ReasoningTokens   int64           `json:"reasoning_output_tokens"`
	EstimatedCostUSD  float64         `json:"estimated_api_cost_usd"`
	DurationMS        int64           `json:"duration_ms"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
}

type Usage struct {
	Model             string  `json:"model"`
	InputTokens       int64   `json:"input_tokens"`
	CachedInputTokens int64   `json:"cached_input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	ReasoningTokens   int64   `json:"reasoning_output_tokens"`
	EstimatedCostUSD  float64 `json:"estimated_api_cost_usd"`
}

func (r *Run) DeriveDuration(now time.Time) {
	end := now
	if r.CompletedAt != nil {
		end = *r.CompletedAt
	}
	start := r.CreatedAt
	if r.StartedAt != nil {
		start = *r.StartedAt
	}
	if !start.IsZero() && end.After(start) {
		r.DurationMS = end.Sub(start).Milliseconds()
	}
}

type Event struct {
	Protocol      string          `json:"protocol"`
	ID            int64           `json:"id"`
	GlobalSeq     int64           `json:"global_seq"`
	RunSeq        int64           `json:"run_seq"`
	RunID         string          `json:"run_id"`
	SourceIssueID string          `json:"source_issue_id,omitempty"`
	SandboxID     string          `json:"sandbox_id,omitempty"`
	Stage         string          `json:"stage,omitempty"`
	Type          string          `json:"type"`
	Level         string          `json:"level"`
	Message       string          `json:"message"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

type Artifact struct {
	RunID     string    `json:"run_id"`
	Path      string    `json:"path"`
	MediaType string    `json:"media_type"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Content   []byte    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type StageState struct {
	RunID       string          `json:"run_id"`
	Stage       string          `json:"stage"`
	State       string          `json:"state"`
	Attempt     int             `json:"attempt"`
	Details     json.RawMessage `json:"details,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type TicketState struct {
	RunID            string          `json:"run_id"`
	LogicalKey       string          `json:"logical_key"`
	ProviderIssueID  string          `json:"provider_issue_id,omitempty"`
	ProviderIssueKey string          `json:"provider_issue_key,omitempty"`
	State            string          `json:"state"`
	Owner            string          `json:"owner,omitempty"`
	CommitSHA        string          `json:"commit_sha,omitempty"`
	Dependencies     []string        `json:"dependencies"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type InputOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

type InputQuestion struct {
	ID            string        `json:"id"`
	Prompt        string        `json:"prompt"`
	Why           string        `json:"why,omitempty"`
	Options       []InputOption `json:"options"`
	AllowFreeText bool          `json:"allow_free_text"`
	Required      bool          `json:"required"`
}

type InputRequest struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	Stage      string          `json:"stage"`
	Round      int             `json:"round"`
	Status     string          `json:"status"`
	Summary    string          `json:"summary"`
	Questions  []InputQuestion `json:"questions"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	AnsweredAt *time.Time      `json:"answered_at,omitempty"`
}

type InputAnswer struct {
	QuestionID string `json:"question_id"`
	OptionID   string `json:"option_id,omitempty"`
	Text       string `json:"text,omitempty"`
}

type InputResponse struct {
	ID         string        `json:"id"`
	RequestID  string        `json:"request_id"`
	RunID      string        `json:"run_id"`
	Channel    string        `json:"channel"`
	ActorID    string        `json:"actor_id,omitempty"`
	ActorName  string        `json:"actor_name,omitempty"`
	ExternalID string        `json:"external_id,omitempty"`
	Answers    []InputAnswer `json:"answers"`
	Accepted   bool          `json:"accepted"`
	CreatedAt  time.Time     `json:"created_at"`
}

type InputDelivery struct {
	RequestID   string    `json:"request_id"`
	Provider    string    `json:"provider"`
	State       string    `json:"state"`
	ExternalID  string    `json:"external_id,omitempty"`
	ExternalURL string    `json:"external_url,omitempty"`
	Error       string    `json:"error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type InputRequestFilter struct {
	RunID  string
	Status string
	Limit  int
}

type LinearDelivery struct {
	DeliveryID     string
	EventType      string
	Action         string
	IssueID        string
	IssueKey       string
	IssueURL       string
	IssueTitle     string
	FeatureRequest string
	SourceContext  json.RawMessage
	Dependencies   []string
	QueueReason    string
	WorkspaceID    string
	TeamID         string
	ProjectID      string
	PayloadSHA256  string
	RawPayload     []byte
	ReceivedAt     time.Time
}

type DeliveryResult struct {
	Run       *Run   `json:"run,omitempty"`
	Duplicate bool   `json:"duplicate"`
	Ignored   bool   `json:"ignored"`
	Reason    string `json:"reason,omitempty"`
}

type RunFilter struct {
	State        string
	RepositoryID string
	After        time.Time
	Limit        int
}

type EventFilter struct {
	After int64
	RunID string
	Limit int
}

type Credential struct {
	Name       string
	Ciphertext []byte
	UpdatedAt  time.Time
}

type AuthSlot struct {
	ID             string     `json:"id"`
	State          string     `json:"state"`
	Ciphertext     []byte     `json:"-"`
	LeaseRunID     string     `json:"lease_run_id,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type ExternalSync struct {
	RunID       string    `json:"run_id"`
	LogicalKey  string    `json:"logical_key"`
	Provider    string    `json:"provider"`
	State       string    `json:"state"`
	Marker      string    `json:"marker"`
	ExternalID  string    `json:"external_id,omitempty"`
	ExternalURL string    `json:"external_url,omitempty"`
	Error       string    `json:"error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type InstallationState struct {
	Initialized   bool       `json:"initialized"`
	OwnerMemberID string     `json:"owner_member_id,omitempty"`
	InitializedAt *time.Time `json:"initialized_at,omitempty"`
}

type Member struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

type Invitation struct {
	ID         string     `json:"id"`
	Role       string     `json:"role"`
	CreatedBy  string     `json:"created_by"`
	Label      string     `json:"label,omitempty"`
	SecretHash []byte     `json:"-"`
	MaxUses    int        `json:"max_uses"`
	UseCount   int        `json:"use_count"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
}

type MemberSession struct {
	ID                       string     `json:"id"`
	MemberID                 string     `json:"member_id"`
	DeviceName               string     `json:"device_name"`
	AccessTokenHash          []byte     `json:"-"`
	RefreshTokenHash         []byte     `json:"-"`
	PreviousRefreshTokenHash []byte     `json:"-"`
	AccessExpiresAt          time.Time  `json:"access_expires_at"`
	RefreshExpiresAt         time.Time  `json:"refresh_expires_at"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	LastSeenAt               *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt                *time.Time `json:"revoked_at,omitempty"`
	RevokedReason            string     `json:"revoked_reason,omitempty"`
}

type Principal struct {
	Member  Member        `json:"member"`
	Session MemberSession `json:"session"`
}

type SessionRefreshResult struct {
	Principal Principal
	Reused    bool
}

type AuthAudit struct {
	ID        int64           `json:"id"`
	MemberID  string          `json:"member_id,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	ActorID   string          `json:"actor_id,omitempty"`
	Action    string          `json:"action"`
	TargetID  string          `json:"target_id,omitempty"`
	Details   json.RawMessage `json:"details,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}
