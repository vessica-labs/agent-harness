CREATE TABLE IF NOT EXISTS installation_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    owner_member_id text NOT NULL,
    initialized_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS members (
    id text PRIMARY KEY,
    display_name text NOT NULL,
    role text NOT NULL CHECK (role IN ('viewer','operator','admin','owner')),
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz
);

CREATE TABLE IF NOT EXISTS invitations (
    id text PRIMARY KEY,
    role text NOT NULL CHECK (role IN ('viewer','operator','admin')),
    created_by text NOT NULL REFERENCES members(id),
    label text NOT NULL DEFAULT '',
    secret_hash bytea NOT NULL UNIQUE,
    max_uses integer NOT NULL DEFAULT 1 CHECK (max_uses > 0),
    use_count integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    consumed_at timestamptz
);

CREATE TABLE IF NOT EXISTS member_sessions (
    id text PRIMARY KEY,
    member_id text NOT NULL REFERENCES members(id),
    device_name text NOT NULL,
    access_token_hash bytea NOT NULL UNIQUE,
    refresh_token_hash bytea NOT NULL UNIQUE,
    previous_refresh_token_hash bytea,
    access_expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    revoked_at timestamptz,
    revoked_reason text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS member_sessions_previous_refresh_idx
    ON member_sessions(previous_refresh_token_hash) WHERE previous_refresh_token_hash IS NOT NULL;

CREATE TABLE IF NOT EXISTS auth_audit_log (
    id bigserial PRIMARY KEY,
    member_id text,
    session_id text,
    actor_id text,
    action text NOT NULL,
    target_id text NOT NULL DEFAULT '',
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_audit_created_idx ON auth_audit_log(created_at DESC);
