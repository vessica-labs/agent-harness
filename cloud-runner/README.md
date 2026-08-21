# Agent Harness Cloud Runner

The cloud runner is one Go binary with three roles:

- `agent-harness server` runs the Railway control plane.
- `agent-harness worker` runs one source-ticket pipeline in an isolated Railway sandbox.
- `agent-harness cloud`, `agent-harness railway`, and `agent-harness ui` operate the service locally.

Postgres owns claims, leases, events, encrypted credentials, synchronization identities, compressed run journals, and canonical human-input requests and responses. Channel delivery records are provider-neutral: the control-plane UI and Linear project the same request today, and a Slack adapter can add another delivery and response channel without changing runner state. Accepted non-Linear answers are mirrored to one idempotent Linear comment; Linear-origin replies are not duplicated. Top-level `Depends on ISSUE-123` description instructions are persisted as queue gates and keep a run unclaimable until every referenced Linear issue is completed. Sandboxes are disposable; recovery recreates `.harness/runs/<run-id>` and checks out the last pushed integration branch.

## Local verification

```text
docker compose up -d postgres
export DATABASE_URL=postgres://harness:harness@127.0.0.1:55432/agent_harness?sslmode=disable
export HARNESS_MANAGEMENT_TOKEN=<random bearer token>
export HARNESS_CREDENTIAL_KEY=<base64 encoded 32-byte key>
export HARNESS_SCHEDULER_ENABLED=false
go run ./cmd/agent-harness server
```

With the server running, set the local profile and open the read-only dashboard:

```text
agent-harness cloud profile set --name local-development --url http://127.0.0.1:8080 --token "$HARNESS_MANAGEMENT_TOKEN"
agent-harness cloud team initialize --name "Local owner" --device "Development machine"
agent-harness cloud whoami
agent-harness ui
```

The installed CLI can keep many named profiles. A target repository selects one without committing credentials:

```yaml
cloud:
  profile: vessica-cli
```

Commands run inside that repository, including nested worktrees, automatically use the selected profile. `agent-harness cloud --profile NAME ...` and `AGENT_HARNESS_PROFILE=NAME` are explicit overrides; `agent-harness cloud profile list` shows the current selection and URLs without secrets. Provider credentials are scoped to an entire control-plane installation. Deploy one Railway control plane and Postgres database per repository when Linear or Notion workspaces must be isolated.

## Railway setup

1. Enable Railway Sandboxes through Priority Boarding.
2. Create one single-replica control-plane service and one Postgres service for each independently credentialed repository.
3. Publish a tagged release, then create the versioned worker checkpoint with `agent-harness railway upgrade --project <id> --version vX.Y.Z`.
4. Run `agent-harness railway init --profile <repository-profile>` to install sealed service configuration and a local profile that matches the repository's `cloud.profile`.
5. Run `agent-harness cloud team initialize` once to exchange the bootstrap token for the first owner device session.
6. Add independent Codex login slots and GitHub, Linear, and Notion service credentials with `agent-harness cloud auth`.
7. Register each repository with `agent-harness cloud repo add`.
8. For a source-based installation, deploy with `agent-harness railway deploy` and wait for terminal Railway success plus `/healthz` and `/readyz`. The maintained production installation uses the repository-level `make release VERSION=vX.Y.Z` flow instead.

`railway init` first checks that the target environment can list Sandboxes. It stops with an enablement instruction when the feature is unavailable. The Railway CLI and Codex CLI versions are pinned in the image and checkpoint builder.

## Maintainer release

Run the release workflow from the repository root, not this directory:

```text
make release-check
make release
```

`release-check` validates a clean, rebased `main` and runs every Python and Go
check without changing GitHub or Railway. Both commands select the next RC from
the remote release tags; pass `VERSION=vX.Y.Z-rc.N` only to override that choice
or begin a new version line. `release` pushes the selected tag, waits for the
GitHub release assets and GHCR image, creates the matching
`agent-harness-worker-X.Y.Z-rc.N`, sets `HARNESS_SANDBOX_CHECKPOINT`, updates the
Railway control-plane image, waits for terminal `SUCCESS`, and checks both
health endpoints. Use `make publish`, `make checkpoint`, and
`make deploy-production` with an explicit `VERSION` to resume individual stages.

## Service authentication

### Team access

The generated `HARNESS_MANAGEMENT_TOKEN` is a bootstrap secret, not a shared daily credential. The first operator exchanges it exactly once:

```text
agent-harness cloud team initialize --name "Owner name" --device "Owner laptop"
```

Each teammate then receives a one-time magic link and claims an individual device session:

```text
agent-harness cloud team invite --role operator --label "Teammate" --expires 1h
agent-harness cloud join 'https://control-plane.example/join#invite=...'
agent-harness cloud whoami
```

Access tokens expire after 15 minutes and are refreshed by a rotating device credential stored in the OS keychain. Reusing an old refresh token revokes the entire device session. Owners and administrators can inspect and revoke access with `cloud team members`, `cloud team sessions`, `cloud team audit`, and `cloud team revoke member|session|invite <id>`. The final owner cannot be removed or demoted.

Roles are deliberately small: viewers can inspect runs and events; operators can also control runs and create test issues; administrators manage repositories, integrations, Codex slots, invitations, roles, and sessions; owners additionally anchor installation ownership and recovery.

Configure independent Codex sessions with `agent-harness cloud auth codex add --slots 3`. The command performs a three-process safety check against the first session. A safe session may serve the YAML-declared coder concurrency; otherwise the scheduler atomically leases one independent auth slot per concurrent Codex process.

The Linear credential command accepts the current OAuth token set from environment variables:

```text
LINEAR_ACCESS_TOKEN=... \
LINEAR_REFRESH_TOKEN=... \
LINEAR_CLIENT_ID=... \
LINEAR_CLIENT_SECRET=... \
LINEAR_EXPIRES_AT=86400 \
LINEAR_WEBHOOK_SECRET=... \
agent-harness cloud auth linear
```

`LINEAR_EXPIRES_AT` may be seconds from now or an RFC3339 timestamp. Rotated access and refresh tokens are re-encrypted atomically. Configure the OAuth application as the **Vessica** app actor with `actor=app`, `app:assignable` plus the required read/write/create scopes, and the control-plane `/webhooks/linear` URL.

For the guided path, run `agent-harness cloud auth linear manifest --url https://<control-plane>` to open Linear's pre-filled Vessica application manifest, then run `agent-harness cloud auth linear --client-id ... --client-secret ... --webhook-secret ...`. The manifest subscribes to AgentSessionEvent, Issue, Comment, OAuthAuthorization, and PermissionChange events. AgentSessionEvent drives native delegation; Issue remains read-only intake for dependency release, and Comment remains the human-input answer channel. The second command opens the assignable app-actor OAuth consent flow on a loopback callback and stores the rotating token set directly in the control plane.

Create the least-privilege GitHub App with `agent-harness cloud auth github --manifest-owner <organization>`. The local manifest callback requests only Metadata read, Contents write, and Pull Requests write, configures the signed `/webhooks/github` endpoint for pull-request events, then encrypts the generated private key and webhook secret without writing them to disk. Install that app only on repositories the runner should operate and use the resulting installation ID during repository registration.

## Repository and run commands

```text
agent-harness cloud repo add --name example --github-owner owner --github-repo repo \
  --github-installation 123 --linear-workspace org --linear-team team \
  --linear-agent Vessica --notion-parent page --base-branch main
agent-harness cloud repo issue create --repo <repository-id> --title "Test feature" \
  --description-file feature-request.md
agent-harness cloud repo issue archive --repo <repository-id> --issue AGE-123 --yes
agent-harness cloud runs list
agent-harness cloud runs watch --run <run-id>
agent-harness cloud runs input <run-id> --file clarified-request.md
agent-harness cloud runs reconcile <run-id>
agent-harness cloud runs export <run-id> --repo /path/to/repo
agent-harness ui
```

The issue commands use the control plane's encrypted Linear app credential; provider tokens never enter the local process. `issue create` delegates the new issue to the configured Vessica app actor so Linear creates the native AgentSession whose signed webhook is the only run-claim path. `issue archive` refuses to archive a source issue or canonical child already mapped to a durable run.

The UI binds only to `127.0.0.1`. Its backend refreshes and injects the current device access token into proxied REST and SSE calls; browser JavaScript never receives either device credential. The Team view manages invitations, roles, members, devices, and the authentication audit history.

Selecting a run filters the SSE feed to that run; “Show all runs” reconnects to the global feed. The top-level Inbox highlights every open human-input request and Run Detail renders its multiple-choice questions, recommended option, and free-text alternative. Submitting an answer atomically records the response and requeues the checkpointed run. Each run also reports execution duration, explicit Codex model, model calls, input/cached-input/output/reasoning token counts, and an estimated API-equivalent cost. The estimate uses the checked-in pricing table for the selected model; ChatGPT-based Codex authentication may be billed through a plan rather than as API token charges.

Cloud workers cap Playwright at two workers by default. This preserves browser parallelism without allowing a repository's CPU-visible default to exhaust the sandbox process/thread budget. Repositories should read `HARNESS_PLAYWRIGHT_WORKERS` in Playwright configuration or pass it as `--workers`; every cloud agent is also instructed to apply the cap explicitly.

Linear parents move to Todo when claimed and In Progress when the first pipeline stage starts. Vessica acknowledges the native AgentSession immediately, publishes semantic Agent Activities for stage progress, input waits, failures, and completion, and attaches the draft pull request URL to the session. Product and Architecture may each use one structured human-input round; the journal is uploaded, the disposable sandbox exits, the parent moves to Needs Input, and a single question-thread comment is created. A human reply to that exact thread or an Inbox answer records the same canonical response, returns the issue to In Progress, and queues a fresh sandbox that restores the journal. All other stages are forbidden from entering a human-input wait state. Child issues are created directly in Todo, move to In Progress when a coder claims them, and move to Done after their commit is integrated. A completed pipeline moves the parent to For Review; the signed GitHub merge webhook moves it to Done. Workflow IDs are discovered from the configured team rather than hard-coded.

The public surface is deliberately small: signed Linear and GitHub webhook intake, health checks, the inert join page, invitation redemption, and token rotation. Management and SSE endpoints require a short-lived member access token and enforce viewer/operator/admin/owner roles. Worker endpoints require a separate short-lived capability scoped to one run. The localhost UI proxies device authentication server-side and never stores it in browser JavaScript.

## Required service variables

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | Railway Postgres connection |
| `HARNESS_MANAGEMENT_TOKEN` | One-time first-owner bootstrap token; rejected by ordinary APIs after team initialization |
| `HARNESS_CREDENTIAL_KEY` | AES-256-GCM key for provider and Codex credentials |
| `HARNESS_PUBLIC_URL` | Public control-plane origin |
| `HARNESS_RAILWAY_PROJECT` | Sandbox project ID |
| `HARNESS_RAILWAY_ENVIRONMENT` | Sandbox environment ID or name |
| `HARNESS_SANDBOX_CHECKPOINT` | Versioned worker checkpoint |
| `HARNESS_REPOSITORY_CHECKPOINTS` | Optional JSON map of repository ID to a warmed Railway checkpoint containing a clean checkout, dependencies, and package caches but no credentials |
| `RAILWAY_API_TOKEN` | Workspace-scoped token used only by the control plane |
| `HARNESS_CODEX_MODEL` | Explicit worker model; defaults to `gpt-5.6-sol` |
| `HARNESS_PLAYWRIGHT_WORKERS` | Maximum browser-test workers per sandbox; defaults to `2` |

Sandbox startup emits `run.infrastructure.stage` events for queueing, auth-slot
lease, checkpoint restore, worker download/cache, filesystem preparation,
repository checkout, journal restore, and total time to the first pipeline
stage. The release checkpoint contains the pinned system and Codex toolchain.
At launch, the sandbox verifies the exact worker binary exposed by the current
control-plane deployment and reuses its snapshot-cached copy when the digest
matches. Repository-specific checkpoints additionally keep clean source,
installed dependencies, and package caches warm across runs.

Provider credentials are encrypted in Postgres and never written to a repository. GitHub installation tokens are minted just in time and given only to controlled Git/CLI subprocesses. Linear and Notion credentials remain in the control plane.
