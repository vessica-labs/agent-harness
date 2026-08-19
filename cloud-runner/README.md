# Agent Harness Cloud Runner

The cloud runner is one Go binary with three roles:

- `agent-harness server` runs the Railway control plane.
- `agent-harness worker` runs one source-ticket pipeline in an isolated Railway sandbox.
- `agent-harness cloud`, `agent-harness railway`, and `agent-harness ui` operate the service locally.

Postgres owns claims, leases, events, encrypted credentials, synchronization identities, and compressed run journals. Sandboxes are disposable; recovery recreates `.harness/runs/<run-id>` and checks out the last pushed integration branch.

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
agent-harness cloud profile set --url http://127.0.0.1:8080 --token "$HARNESS_MANAGEMENT_TOKEN"
agent-harness ui
```

## Railway setup

1. Enable Railway Sandboxes through Priority Boarding.
2. Create one single-replica control-plane service and one Postgres service.
3. Publish a tagged release, then create the versioned worker checkpoint with `agent-harness railway upgrade --project <id> --version vX.Y.Z`.
4. Run `agent-harness railway init` to install sealed service configuration and a local profile.
5. Add independent Codex login slots and GitHub, Linear, and Notion service credentials with `agent-harness cloud auth`.
6. Register each repository with `agent-harness cloud repo add`.
7. Deploy with `agent-harness railway deploy` and wait for terminal Railway success plus `/healthz` and `/readyz`.

`railway init` first checks that the target environment can list Sandboxes. It stops with an enablement instruction when the feature is unavailable. The Railway CLI and Codex CLI versions are pinned in the image and checkpoint builder.

## Service authentication

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

`LINEAR_EXPIRES_AT` may be seconds from now or an RFC3339 timestamp. Rotated access and refresh tokens are re-encrypted atomically. Configure the OAuth application with `actor=app`, the required read/write/create scopes, and the control-plane `/webhooks/linear` URL.

For the guided path, run `agent-harness cloud auth linear manifest --url https://<control-plane>` to open Linear's pre-filled application manifest, then run `agent-harness cloud auth linear --client-id ... --client-secret ... --webhook-secret ...`. The second command opens the app-actor OAuth consent flow on a loopback callback and stores the rotating token set directly in the control plane.

Create the least-privilege GitHub App with `agent-harness cloud auth github --manifest-owner <organization>`. The local manifest callback requests only Metadata read, Contents write, and Pull Requests write, then encrypts the generated private key without writing it to disk. Install that app only on repositories the runner should operate and use the resulting installation ID during repository registration.

## Repository and run commands

```text
agent-harness cloud repo add --name example --github-owner owner --github-repo repo \
  --github-installation 123 --linear-workspace org --linear-team team \
  --trigger-label agent-harness --notion-parent page --base-branch main
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

The issue commands use the control plane's encrypted Linear app credential; provider tokens never enter the local process. `issue create` applies the repository's configured trigger label so Linear's signed webhook remains the only run-claim path. `issue archive` refuses to archive a source issue or canonical child already mapped to a durable run.

The UI binds only to `127.0.0.1`. Its backend injects the bearer token into proxied REST and SSE calls; browser JavaScript never receives the credential.

Selecting a run filters the SSE feed to that run; “Show all runs” reconnects to the global feed. Each run reports execution duration, explicit Codex model, model calls, input/cached-input/output/reasoning token counts, and an estimated API-equivalent cost. The estimate uses the checked-in pricing table for the selected model; ChatGPT-based Codex authentication may be billed through a plan rather than as API token charges.

Cloud workers cap Playwright at two workers by default. This preserves browser parallelism without allowing a repository's CPU-visible default to exhaust the sandbox process/thread budget. Repositories should read `HARNESS_PLAYWRIGHT_WORKERS` in Playwright configuration or pass it as `--workers`; every cloud agent is also instructed to apply the cap explicitly.

Linear child issues start in the team's Todo workflow state, move to In Progress when their coder wave is claimed, and move to Done after their commit is integrated. The parent moves to In Progress when the ticket plan is published and to Done only after all durable child tickets complete. Workflow IDs are discovered from the configured team rather than hard-coded.

The public surface is deliberately small: signed Linear webhook intake and health checks. Management and SSE endpoints require the generated bearer token. Worker endpoints require a short-lived capability scoped to one run. The localhost UI proxies that token server-side and never stores it in browser JavaScript.

## Required service variables

| Variable | Purpose |
|---|---|
| `DATABASE_URL` | Railway Postgres connection |
| `HARNESS_MANAGEMENT_TOKEN` | Management API bearer token |
| `HARNESS_CREDENTIAL_KEY` | AES-256-GCM key for provider and Codex credentials |
| `HARNESS_PUBLIC_URL` | Public control-plane origin |
| `HARNESS_RAILWAY_PROJECT` | Sandbox project ID |
| `HARNESS_RAILWAY_ENVIRONMENT` | Sandbox environment ID or name |
| `HARNESS_SANDBOX_CHECKPOINT` | Versioned worker checkpoint |
| `RAILWAY_API_TOKEN` | Workspace-scoped token used only by the control plane |
| `HARNESS_CODEX_MODEL` | Explicit worker model; defaults to `gpt-5.3-codex` |
| `HARNESS_PLAYWRIGHT_WORKERS` | Maximum browser-test workers per sandbox; defaults to `2` |

Provider credentials are encrypted in Postgres and never written to a repository. GitHub installation tokens are minted just in time and given only to controlled Git/CLI subprocesses. Linear and Notion credentials remain in the control plane.
