ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_state_check;
ALTER TABLE runs ADD CONSTRAINT runs_state_check
    CHECK (state IN ('queued','running','awaiting_input','paused','completed','cancelled'));

CREATE TABLE IF NOT EXISTS input_requests (
    id text PRIMARY KEY,
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    stage text NOT NULL CHECK (stage IN ('product','arch')),
    round integer NOT NULL DEFAULT 1 CHECK (round = 1),
    status text NOT NULL CHECK (status IN ('open','answered','cancelled')),
    summary text NOT NULL,
    questions jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    answered_at timestamptz,
    UNIQUE (run_id, stage, round)
);
CREATE INDEX IF NOT EXISTS input_requests_status_idx ON input_requests(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS input_responses (
    id text PRIMARY KEY,
    request_id text NOT NULL REFERENCES input_requests(id) ON DELETE CASCADE,
    run_id text NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    channel text NOT NULL,
    actor_id text NOT NULL DEFAULT '',
    actor_name text NOT NULL DEFAULT '',
    external_id text NOT NULL DEFAULT '',
    answers jsonb NOT NULL,
    accepted boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS input_responses_external_idx
    ON input_responses(request_id, channel, external_id) WHERE external_id <> '';

CREATE TABLE IF NOT EXISTS input_deliveries (
    request_id text NOT NULL REFERENCES input_requests(id) ON DELETE CASCADE,
    provider text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending','delivered','failed')),
    external_id text NOT NULL DEFAULT '',
    external_url text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, provider)
);

INSERT INTO schema_migrations(version) VALUES (4) ON CONFLICT DO NOTHING;
