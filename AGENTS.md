# Agent Harness Agent Guide

- Scope: this repository only. Files under `harness-templates/` and `plugins/agent-harness/assets/` are payloads shipped into *other* repositories and are not this repository's own contract.

## Project Purpose

Agent Harness is a lean, editable issue-to-pull-request coding workflow for Codex. Each target repository owns its context documents, agent definitions, deterministic pipeline YAML, architecture rules, and durable run journal. The optional Railway cloud runner watches labeled Linear issues and executes that repository-owned workflow in isolated sandboxes.

## Repository Map

| Path | Responsibility |
| --- | --- |
| `cloud-runner/` | One Go binary: `server` control plane, `worker` sandbox executor, and the `cloud`/`railway`/`ui` management CLI |
| `plugins/agent-harness/` | Codex plugin: skills, JSON schemas, default pipeline, and `scripts/harnessctl.py` |
| `harness-templates/base/` | Canonical `.harness/` and `.agents/` files copied into a target repository |
| `tests/` | Python verification for the plugin package, `harnessctl.py`, and architecture lint |
| `docs/` | Architecture map and the end-user guide |

## Sources of Truth

| Topic | Source |
| --- | --- |
| Architecture | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Product, setup, and operations | [`docs/AGENT_HARNESS_USER_GUIDE.md`](docs/AGENT_HARNESS_USER_GUIDE.md) |
| Cloud runner operation | [`cloud-runner/README.md`](cloud-runner/README.md) |
| Workflow definition for a run | the target repository's checked-in `.harness/pipeline.yaml` |
| CI contract | [`.github/workflows/cloud-runner.yml`](.github/workflows/cloud-runner.yml) |

## Essential Commands

| Task | Command |
| --- | --- |
| Test (Python) | `python3 -m unittest discover -s tests -v` |
| Test (Go) | `cd cloud-runner && make test` |
| Lint and verify | `cd cloud-runner && make verify` |
| Build | `cd cloud-runner && make build` |

## Non-Negotiable Rules

- `.harness/pipeline.yaml` is the workflow authority. Never hard-code a stage set, stage order, or concurrency value in Go or in a skill.
- `cloud-runner/scripts/harnessctl.py` and `plugins/agent-harness/scripts/harnessctl.py` must stay byte-identical; `make verify` enforces it.
- Extending `store.Store` requires updating the Postgres implementation, the in-memory implementation, and a new numbered migration.
- Codex subprocesses receive a sanitized environment only. Never pass Railway, Linear, Notion, encryption, or management credentials to an agent process, and never write credentials into a repository.
- Pull requests are created as drafts and are never merged automatically.

## Change Workflow

1. Read the linked sources of truth for the area you are changing.
2. Make the smallest change that satisfies the requirement, following the conventions of the package you are editing.
3. Run both test suites and `make verify` before handing off.
4. Update `docs/ARCHITECTURE.md` when you add a component, an event type, a table, or an extension point.

## Definition of Done

- `python3 -m unittest discover -s tests` passes.
- `cd cloud-runner && make verify` passes (gofmt, `go vet`, `go test -race`, build, and the `harnessctl.py` equality check).
- Documentation affected by the change is updated in the same change.

## Escalation Conditions

- Credential handling, encryption, or token-scope changes.
- Database migrations that are destructive or not backward compatible.
- Changes to the default pipeline, agent output contracts, or anything that alters existing target repositories.
- Any behavior that would merge a pull request or mutate a provider outside the marker-based, idempotent projection model.
