# Agent Harness architecture

How this repository is built, in one map. Contributor conventions and commands live in
[`AGENTS.md`](../AGENTS.md); the end-user manual is
[`docs/AGENT_HARNESS_USER_GUIDE.md`](AGENT_HARNESS_USER_GUIDE.md).

## 1. What the product is

A lean issue → draft-PR coding workflow for the Codex CLI. Two halves:

1. **Repository-owned harness** (`.harness/` + `.agents/` copied from `harness-templates/base`): context docs, agent role prompts, deterministic `pipeline.yaml`, arch-lint rules, and a durable per-run journal under `.harness/runs/<run_id>/`.
2. **Optional Railway cloud runner** (`cloud-runner/`, one Go binary): runs as the native Linear app actor **Vessica**, accepts issues delegated to that agent, and executes *the repository's own* pipeline in isolated Railway sandboxes.

Invariant to respect when building on top: **`.harness/pipeline.yaml` is the workflow authority.** Neither the Go worker nor the plugin hard-codes the product/arch/coder/lint/qa/pr stage set. Adding a stage = editing YAML + adding an `.agents/*.md` role, not editing Go.

## 2. Component map

```
plugins/agent-harness/         Codex plugin: skills, JSON schemas, default pipeline, harnessctl.py
harness-templates/base/        canonical files copied into a target repo (.harness/, .agents/)
cloud-runner/                  Go: server | worker | cloud/railway/ui CLI
tests/                         python: plugin package, harnessctl, arch-lint
.agents/plugins/marketplace.json  Codex marketplace metadata
```

`cloud-runner/internal/`:

| package | role |
| --- | --- |
| `cli` | `server`, `worker`, `cloud …`, `railway …`, `ui` subcommands; onboarding wizards (`cloud auth codex/github/linear/notion`), GitHub App manifest flow, Linear OAuth |
| `server` (1.1k lines) | HTTP control plane, webhooks, management API, SSE, internal worker API, Linear/Notion projection (`sync.go`), human input (`input.go`), team auth (`team.go`) |
| `store` | `Store` interface + Postgres impl + in-memory impl (tests), SQL migrations |
| `scheduler` | claim runs, lease Codex auth slots, create sandboxes, heartbeat, recover, destroy |
| `sandbox` | Railway CLI wrapper (create/heartbeat/destroy, detached worker bootstrap) |
| `worker` (1k-line `runner.go`) | in-sandbox pipeline executor: checkout, journal restore, stages, Codex, worktrees, PR |
| `providers/{linearapi,notionapi,githubapp}` | thin API clients |
| `secure` | AES-256-GCM credential envelope, capability/token minting |
| `ui` | embedded localhost dashboard proxying the control plane |
| `cli/profile.go` | named local control-plane profiles, keychain-backed sessions, and nearest-repository `cloud.profile` selection |
| `events` | broker used to wake SSE listeners |

`cloud-runner/scripts/harnessctl.py` is byte-identical to `plugins/agent-harness/scripts/harnessctl.py` — `make verify` enforces this with `cmp`. Any change must be made in both.

## 3. Runtime topology

```
Linear AgentSession webhook ──> control-plane service (Railway) ──> Railway Postgres
                        │
                        ├── ticket sandbox 1 ── Codex CLI
                        ├── ticket sandbox 2 ── Codex CLI
                        └── ticket sandbox 3 ── Codex CLI   (MaxActiveRuns default 3)
```

One root Linear issue delegated to Vessica → one durable run; duplicate deliveries dedupe to the same run via `webhook_deliveries` + `source_claims`. The native `AgentSession` ID is stored in run metadata, and semantic Agent Activities keep Linear's agent UI current. Ordinary Issue webhooks update dependency gates but cannot dispatch runs. Completion → **draft** GitHub PR, never merged automatically. Sandboxes are disposable; recovery comes from Postgres journals + pushed branches.

Linear may attach a generated session-thread `commentId` to an Agent-picker delegation. The parser distinguishes comment/mention invocation by `sourceCommentId`, then fetches the issue and requires its live delegate to be the configured app actor before claiming a run. Native AgentSession activity includes pipeline stages plus redacted Codex command and file-edit actions; start/completion events share a logical key so each repository action is projected once.

## 4. Control plane (`internal/server`)

Routes (`server.go`):

```
GET  /healthz /readyz
POST /webhooks/linear        signature + timestamp verified; AgentSessionEvent delegation dispatches asynchronously
POST /webhooks/github        PR merged/closed → pr.merged events
GET  /join                   invitation redemption page
POST /auth/v1/initialize | /auth/v1/invitations/redeem | /auth/v1/token
/v1/…                        management API, session-authenticated (role-scoped)
/internal/v1/…               worker API, run-scoped capability
```

Internal worker API surface: append events, journal upload/download, heartbeat, Codex auth return, just-in-time GitHub installation token, worker-binary download (with SHA-256 header), external sync.

**Event ingestion is the state machine.** Workers only emit events; the server projects them:

| event | projection |
| --- | --- |
| `stage.started` / `stage.completed` | stage row, run → `running` |
| `human_input.requested` | creates `input_requests` row, stage → `waiting_for_input`, Linear → `Needs Input` |
| `ticket.started/completed/failed` | ticket rows |
| `pr.created` / `pr.merged` | delivery branch + URL; Linear parent → For Review / Done |
| `run.completed` / `run.paused` / `run.failed` | run state; stopped execution remains Linear In Progress unless an open input request exists |
| Codex usage events | token counts + estimated cost on the run |

`/v1/events` is SSE with durable replay (`after`, `run_id`, `Last-Event-ID`), 20s keepalives that re-validate the session and emit `auth_revoked` when it is gone. Event protocol constant: `agent-harness.events/v1`.

**External projection is idempotent by marker or provider identity** (`sync.go`): every Linear comment / Notion page is upserted by an HTML marker (`<!-- agent-harness:run:<id> -->`, `:child:`, `:ticket:`, `:summary:`, `:activity:`, `:input:`, `notion-artifact:`); native Linear Agent Activities use the AgentSession ID plus a durable logical key. All projections are tracked in `external_sync` with `pending`/`synced`/`failed`. `reconcileRunProjections` can force-replay everything. Linear workflow states are resolved/created once per team (`EnsureLifecycleStates`: Todo, InProgress, NeedsInput, ForReview, Done).

**Human input** (`input.go`) is deliberately constrained: only `product` and `arch`, one round each, 1–3 questions, each with 2–3 options, exactly one marked recommended, free-text allowed. Each request is delivered to the dashboard Inbox, an issue question thread, and the native AgentSession as an `elicitation`. Answers arrive from the Inbox, a reply in the exact issue thread, or the AgentSession chat's `prompted` webhook (`FindInputRequestByDelivery`); the first accepted answer wins (`ResolveInputRequest` returns `ErrConflict` for the rest) and queues a resumed run.

## 5. Persistence (`internal/store`)

Migrations: `001_initial.sql` (repositories, webhook_deliveries, source_claims, runs, stages, tickets, external_sync, events, artifacts, credentials, auth_slots, idempotency_keys), `003_team_access.sql` (installation_state, members, invitations, member_sessions with refresh-reuse detection index, auth_audit_log), `004_human_input.sql` (input_requests, input_responses, input_deliveries), `005_linear_agent.sql` (per-repository Linear app-actor name, default `Vessica`).

Notable: `events(run_id, run_seq)` gives per-run ordering plus a global sequence for SSE; `ClaimNextRun(owner, maxActive, leaseDuration)` is the concurrency gate; `LeaseAuthSlots` / `ReleaseAuthSlot` / `QuarantineAuthSlot` manage Codex credentials. `store.Store` is a single interface with a full in-memory implementation, so most server/scheduler tests run without Postgres — keep both implementations in sync when extending it.

## 6. Scheduler (`internal/scheduler`)

Defaults: `MaxActiveRuns 3`, run lease 15m, auth lease 24h, poll 2s, heartbeat 5m, startup timeout 90s, Codex model `gpt-5.6-sol`, Playwright workers 2.

Loop: `ClaimNextRun` → for each claim, `launch()`:
1. emit infrastructure events;
2. lease exactly one Codex auth slot for the top-level source-issue run;
3. decrypt slots only for sandbox handoff;
4. mint run-scoped capability;
5. build sandbox env (run/repo IDs, feature request, source issue, human input, the run's Codex session, model, Playwright cap);
6. pick toolchain or repo-specific checkpoint (`HARNESS_SANDBOX_CHECKPOINT`, `HARNESS_REPOSITORY_CHECKPOINTS`);
7. create Railway sandbox, start worker detached, record sandbox identity.

Recovery: reconcile running runs, heartbeat sandbox + lease, detect startup timeout, requeue when the sandbox is lost, quarantine auth slots lost before return, destroy terminal sandboxes after a grace period and awaiting-input sandboxes after 15s.

## 7. Sandbox bootstrap (`internal/sandbox/railway.go`)

Writes a 0600 env file, `railway sandbox create --json` (optionally with a checkpoint), waits for `RUNNING`, then runs a detached `set -euo pipefail` bootstrap that downloads `/internal/v1/runs/<id>/worker-binary`, verifies the `X-Agent-Harness-Worker-SHA256` digest, caches it in `/opt/agent-harness/bin`, and `exec`s `agent-harness worker`. Transient network failures are retried (recent commit `6d105c9`).

## 8. Worker (`internal/worker`)

`runner.go Run()`: emit `worker.starting` → prepare filesystem → get just-in-time GitHub token → checkout (with public fallback) → load `.harness/pipeline.yaml` → `restoreOrInitialize` (download journal tarball, restore branch) → register stages → `run.started` → execute stages in order (skipping already-completed ones, refusing to run when dependencies are unmet) → `run.completed`.

- **Retries**: up to 3 attempts per stage; repair/input/policy errors and cancellation are not retried; each retry resets the stage to `pending` and checkpoints. A ticket-parallel attempt integrates and checkpoints successful siblings before returning a sibling failure, so the next attempt skips durable ticket results.
- **Repair loops** (`repair.go`) honor `max_reentries` from YAML (default: qa → coder through qa, 2) and recover a validated QA `requeue` result from a blocked checkpoint before starting a new QA invocation.
- **Ticket parallelism** (`runTicketStage`): read `artifacts/ticket-plan.json`, compute dependency waves (`harnessctl.py waves`), and create one detached git worktree per ready ticket with a copy of the journal. The worker invokes one top-level Codex coordinator for the wave with `multi_agent` explicitly enabled. That coordinator delegates every ticket to one native coder subagent, bounded by `stage.parallelism`; subagents write the existing per-ticket result contracts and commits in their assigned worktrees. The deterministic worker then validates and cherry-picks successful commits into the run worktree in ticket-key order, materializes results into the main journal, removes worktrees, pushes the branch, and checkpoints even when a sibling failed. If an operator repaired the isolated run branch before resuming, an agent result may reference that already-ancestor commit; the worker adopts it idempotently instead of attempting an empty cherry-pick.
- **Hooks**: `before` / `after` / `on_failure` per stage, executed via `harnessctl.py run-hook` with a fixed env (`HARNESS_RUN_ID`, `HARNESS_ISSUE_KEY`, `HARNESS_STAGE`, `HARNESS_ARTIFACT_DIR`, `HARNESS_WORKTREE`), default 300s timeout. Arch-lint is a hook, not Go logic (`reconcile.go` + `.harness/scripts/arch-lint.py`).
- **PR finalize** (`finalizePullRequest`): worktree must be clean; if the branch was already published it cuts `…-pr-<sha>` delivery branch; push; require title+body from the PR agent result; `gh pr view` else `gh pr create --draft`; write canonical PR JSON back into the result; checkpoint + emit `pr.created`.
- **Checkpointing**: run dir tar.gz uploaded to the control plane before external writes and at pause/terminal points — this is what makes a destroyed sandbox resumable. Before a failed ticket worktree is removed, its result contract, blocker, and failed journal state are copied into the root run journal and synchronized to the child ticket; recovery can therefore show the actual blocker while still retrying that unfinished ticket.

`codex.go`: `codex exec --json --model <model> --dangerously-bypass-approvals-and-sandbox --ignore-user-config -C <repo> --output-last-message <file> -` with a **sanitized env** and one isolated `CODEX_HOME` per source-issue run. Coder-wave coordinators additionally pass `--enable multi_agent` and receive all ready assignments, worktrees, per-ticket result paths, and the YAML concurrency limit in one prompt. Codex never sees Railway/Linear/Notion/encryption/management credentials. The release checkpoint pins Node.js 24, pnpm 11, Playwright, and system Chromium. JSONL output is parsed for usage/activity → control-plane events; a 1s terminal grace timer plus forced stdout/process close prevents hangs from lingering descendants (commit `8d13405`); falls back to `--output-last-message` if the result file is missing.

`pipeline.go` validation: `version: 1`, non-empty `run_root` and stages, unique ids, parallelism ≥ 1, mode ∈ {`single`, `ticket_parallel`}, dependencies must be previously-declared stages, repair loops must satisfy `to < from`, `through ≥ to`, `through ≤ from`, `max_reentries ≥ 1`.

## 9. Repository-owned contract

`harness-templates/base/`:
- `.agents/{product,architect,coder,lint,qa,docs,pr}.md` — each is mission + inputs + work method + boundaries + an **exact JSON output contract** (e.g. product returns `prd_markdown`, `tickets[]` with `owned_paths`/`depends_on`/`focused_checks`, `coverage[]`, or the smaller `needs_input` form).
- `.harness/{AGENTS,ARCHITECTURE,DESIGN,SECURITY,TESTING,DEPLOY}.md`, `arch-lint-rules.json`, `adrs/`, `runs/`, `worktrees/`, `scripts/arch-lint.py`.

`plugins/agent-harness/`: `pipelines/default.yaml` (the 6-stage DAG + repair loop), `schemas/{config,pipeline,state,product-output,architect-output}.schema.json`, `skills/{setup-harness,run-harness,inspect-harness,onboard-cloud-runner}`, `scripts/harnessctl.py`.

One plugin/CLI installation may address many independent control planes. A target repository's non-secret `.harness/config.yaml` can select a named local profile with `cloud.profile`; the profile stores the URL locally and its device session in the OS keychain or protected fallback. Provider credentials remain installation-wide inside each control plane's encrypted Postgres, so repositories that require distinct Linear or Notion workspaces use distinct Railway control planes and profiles rather than sharing one provider credential set.

`harnessctl.py` is the deterministic orchestration surface used by both the Go worker and local Codex runs: `validate-config`, `validate-pipeline`, `resolve-stages`, `waves`, `validate-agent-output`, `materialize-source`, `materialize-generated-inputs`, `materialize-result`, `init-run`, `checkpoint`, `set-stage`, `render-comment`, `list-runs`, `markers`, `run-hook`, `release-lease`. Journal state lives in `state.json`; leases prevent two orchestrators from touching one issue.

## 10. Security model

AES-256-GCM credentials in Postgres (`HARNESS_CREDENTIAL_KEY`), never written to repos. Codex subprocesses get a sanitized env only. GitHub installation tokens minted just in time, 1h TTL. Worker endpoints use a run-scoped short-lived capability; management endpoints use role-scoped team tokens (access 15m, refresh 30d rotating, reuse of a rotated refresh token revokes the device session). Invitations single-use, 1h default. PRs are draft-only.

Key env vars: `DATABASE_URL`, `HARNESS_MANAGEMENT_TOKEN`, `HARNESS_CREDENTIAL_KEY`, `HARNESS_PUBLIC_URL`, `HARNESS_RAILWAY_PROJECT`, `HARNESS_RAILWAY_ENVIRONMENT`, `HARNESS_SANDBOX_CHECKPOINT`, `HARNESS_REPOSITORY_CHECKPOINTS`, `RAILWAY_API_TOKEN`, `HARNESS_CODEX_MODEL`, `HARNESS_PLAYWRIGHT_WORKERS`.

## 11. Extension points (where to build)

| you want to… | change |
| --- | --- |
| add/reorder a pipeline stage | `.harness/pipeline.yaml` in the target repo (+ `plugins/.../pipelines/default.yaml` for new repos) and a new `.agents/<role>.md` |
| change agent behavior/output | the role `.md` + matching `schemas/*-output.schema.json` + `validate-agent-output` in `harnessctl.py` |
| add a deterministic check | a stage hook (`before`/`after`/`on_failure`) running a repo script — no Go change |
| new durable state / API | `model` type → `store.Store` + **both** memory and Postgres impls + a new numbered migration → server route |
| new worker→plane signal | emit an event in `worker`, project it in `server`'s event ingestion, and extend the SSE consumers/UI |
| new provider integration | `internal/providers/<x>api` + marker-based upsert + `external_sync` logical keys in `server/sync.go` |
| change concurrency | `scheduler` config (`MaxActiveRuns`) and the number of independent Codex auth slots control top-level source-issue runs; stage `parallelism` controls native coder subagents inside one run |

Gotchas: keep the two `harnessctl.py` copies identical; the arch-lint rules and Python tests (`tests/test_arch_lint.py`, `tests/test_plugin_package.py`) enforce plugin packaging and architecture boundaries, so new files often need to be declared; the `go` directive in `cloud-runner/go.mod` pins the toolchain CI installs — a local Go must be at least that version.
