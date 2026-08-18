# Agent Harness

Agent Harness is a lean, editable coding workflow for Codex. A repository owns its context documents, role definitions, deterministic pipeline YAML, architecture lints, and durable run journals. The plugin bootstraps and operates that workflow locally; the Go cloud runner applies the same workflow to labeled Linear issues in isolated Railway Sandboxes.

## Repository layout

- `harness-templates/base` — canonical `.harness` and `.agents` bootstrap files.
- `plugins/agent-harness` — Codex setup, run, and inspection skills plus deterministic helpers.
- `cloud-runner` — the `agent-harness` control-plane, worker, cloud CLI, and local read-only monitor.
- `tests` — plugin, template, pipeline, and architecture-lint verification.

The repository-owned `.harness/pipeline.yaml` remains the workflow authority. The cloud service does not hard-code product, architecture, coding, lint, QA, documentation, or PR behavior.

## Develop

```text
python3 -m unittest discover -s tests -v
cd cloud-runner
make verify
```

See [cloud-runner/README.md](cloud-runner/README.md) for local control-plane setup, Railway deployment, credentials, repository registration, event monitoring, and recovery.

## Safety model

- Provider credentials are encrypted in Postgres and never enter Codex subprocesses.
- GitHub installation tokens are repository-scoped and minted just in time.
- Each Linear source issue has one permanent cloud claim and one resumable run ID.
- Sandboxes are disposable; Postgres journals and pushed integration branches are recovery authorities.
- Pull requests are created as drafts and are never merged automatically.
