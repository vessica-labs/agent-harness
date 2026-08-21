ALTER TABLE repositories
    ADD COLUMN IF NOT EXISTS linear_agent_name text NOT NULL DEFAULT 'Vessica';
