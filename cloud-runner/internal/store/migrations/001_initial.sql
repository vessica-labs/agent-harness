CREATE TABLE IF NOT EXISTS schema_migrations (
    version bigint PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS repositories (
    id text PRIMARY KEY,
    name text NOT NULL,
    github_owner text NOT NULL,
    github_repo text NOT NULL,
    github_installation_id bigint NOT NULL DEFAULT 0,
    base_branch text NOT NULL DEFAULT 'main',
    linear_workspace_id text NOT NULL,
    linear_team_id text NOT NULL,
    linear_project_id text NOT NULL DEFAULT '',
    trigger_label text NOT NULL DEFAULT 'agent-harness',
    notion_parent_page_id text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (linear_workspace_id, linear_team_id, linear_project_id)
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    provider text NOT NULL,
    delivery_id text NOT NULL,
    event_type text NOT NULL,
    action text NOT NULL,
    payload_sha256 text NOT NULL,
    raw_payload bytea NOT NULL,
    received_at timestamptz NOT NULL,
    repository_id text REFERENCES repositories(id),
    run_id text,
    accepted boolean NOT NULL DEFAULT false,
    reason text NOT NULL DEFAULT '',
    PRIMARY KEY (provider, delivery_id)
);

CREATE TABLE IF NOT EXISTS source_claims (
    provider text NOT NULL,
    source_issue_id text NOT NULL,
    repository_id text NOT NULL REFERENCES repositories(id),
    run_id text NOT NULL UNIQUE,
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, source_issue_id)
);

CREATE TABLE IF NOT EXISTS runs (
    id text PRIMARY KEY,
    repository_id text NOT NULL REFERENCES repositories(id),
    provider text NOT NULL,
    source_issue_id text NOT NULL,
    source_issue_key text NOT NULL,
    source_issue_url text NOT NULL DEFAULT '',
    source_issue_title text NOT NULL,
    feature_request text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('queued','running','paused','completed','cancelled')),
    current_stage text NOT NULL DEFAULT '',
    queue_reason text NOT NULL DEFAULT '',
    attempt integer NOT NULL DEFAULT 0,
    sandbox_id text NOT NULL DEFAULT '',
    sandbox_session text NOT NULL DEFAULT '',
    auth_slot_id text NOT NULL DEFAULT '',
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    heartbeat_at timestamptz,
    branch text NOT NULL DEFAULT '',
    pull_request_url text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (provider, source_issue_id)
);

ALTER TABLE webhook_deliveries
    DROP CONSTRAINT IF EXISTS webhook_deliveries_run_id_fkey;
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id);

CREATE TABLE IF NOT EXISTS stages (
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    stage text NOT NULL,
    state text NOT NULL,
    attempt integer NOT NULL DEFAULT 0,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, stage)
);

CREATE TABLE IF NOT EXISTS tickets (
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    logical_key text NOT NULL,
    provider_issue_id text NOT NULL DEFAULT '',
    provider_issue_key text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    owner text NOT NULL DEFAULT '',
    commit_sha text NOT NULL DEFAULT '',
    dependencies jsonb NOT NULL DEFAULT '[]'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, logical_key)
);

CREATE TABLE IF NOT EXISTS external_sync (
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    logical_key text NOT NULL,
    provider text NOT NULL,
    state text NOT NULL,
    marker text NOT NULL,
    external_id text NOT NULL DEFAULT '',
    external_url text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, logical_key, provider)
);

CREATE TABLE IF NOT EXISTS events (
    global_seq bigserial PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    run_seq bigint NOT NULL,
    source_issue_id text NOT NULL DEFAULT '',
    sandbox_id text NOT NULL DEFAULT '',
    stage text NOT NULL DEFAULT '',
    type text NOT NULL,
    level text NOT NULL DEFAULT 'info',
    message text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, run_seq)
);
CREATE INDEX IF NOT EXISTS events_run_seq_idx ON events(run_id, run_seq);

CREATE TABLE IF NOT EXISTS artifacts (
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    path text NOT NULL,
    media_type text NOT NULL,
    sha256 text NOT NULL,
    size bigint NOT NULL,
    content bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, path)
);

CREATE TABLE IF NOT EXISTS credentials (
    name text PRIMARY KEY,
    ciphertext bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_slots (
    id text PRIMARY KEY,
    state text NOT NULL CHECK (state IN ('available','leased','quarantined')),
    ciphertext bytea NOT NULL,
    lease_run_id text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    scope text NOT NULL,
    key text NOT NULL,
    response jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);

INSERT INTO schema_migrations(version) VALUES (1) ON CONFLICT DO NOTHING;

