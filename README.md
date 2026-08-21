# Agent Harness

Agent Harness is a lean, editable issue-to-pull-request coding workflow for Codex. Each repository owns its context documents, agent definitions, deterministic pipeline YAML, architecture rules, and durable run journal. The optional Railway cloud runner appears in Linear as the **Vessica** agent app actor and executes the same repository-owned workflow when an issue is delegated to it.

For the complete product, setup, workflow, operations, security, recovery, and CLI documentation, see the [Agent Harness User Guide](docs/AGENT_HARNESS_USER_GUIDE.md).

## What is included

- `harness-templates/base` — canonical `.harness` and `.agents` bootstrap files.
- `plugins/agent-harness` — Codex skills for setup, cloud onboarding, execution, and inspection.
- `cloud-runner` — one Go binary for the Railway control plane, isolated workers, local management CLI, and localhost dashboard.
- `.agents/plugins/marketplace.json` — the repository marketplace used to install the Codex plugin.
- `tests` — plugin, template, pipeline, and architecture-lint verification.

The checked-in `.harness/pipeline.yaml` is always the workflow authority. The cloud runner does not hard-code the product, architecture, coding, lint, QA, documentation, or pull-request stages.

## Quick start with Codex

### 1. Install the required CLIs

You need Git, GitHub CLI, Codex CLI, Railway CLI, and the `agent-harness` binary.

Install Codex CLI if it is not already available:

```sh
npm install -g @openai/codex
codex login
```

Install Railway CLI using the agent-aware installer:

```sh
curl -fsSL agents.railway.com | sh
railway login
railway --version
```

Homebrew and npm are also supported:

```sh
brew install railway
# or
npm install -g @railway/cli
```

Install GitHub CLI and authenticate it if needed:

```sh
brew install gh
gh auth login
```

### 2. Install the Agent Harness CLI

Download a release binary from [GitHub Releases](https://github.com/vessica-labs/agent-harness/releases). The current release-candidate examples are:

```sh
mkdir -p "$HOME/.local/bin"

# Apple Silicon macOS
curl -fL https://github.com/vessica-labs/agent-harness/releases/download/v0.1.0-rc.32/agent-harness-darwin-arm64 \
  -o "$HOME/.local/bin/agent-harness"

# Intel macOS: use agent-harness-darwin-amd64
# Linux x86-64: use agent-harness-linux-amd64
# Linux ARM64: use agent-harness-linux-arm64

chmod 0755 "$HOME/.local/bin/agent-harness"
export PATH="$HOME/.local/bin:$PATH"
agent-harness version
```

To build from source instead:

```sh
git clone https://github.com/vessica-labs/agent-harness.git
cd agent-harness
make install
agent-harness version
```

`make install` builds a version-stamped binary from the current checkout and
installs it to `$HOME/.local/bin` by default. Set `PREFIX` to choose another
installation prefix.

### 3. Install the Codex plugin

Add this repository as a Codex marketplace, then install the plugin:

```sh
codex plugin marketplace add vessica-labs/agent-harness --ref main
codex plugin add agent-harness@agent-harness --json
```

Restart the Codex desktop app and start a new task so the new skills are loaded. This marketplace flow follows the [official Codex plugin packaging and installation model](https://developers.openai.com/plugins/build/plugins).

### 4. Ask Codex to run setup

Open the repository that should use Agent Harness and tell Codex:

```text
Use $onboard-cloud-runner to set up Agent Harness for this repository. Walk me through Railway, GitHub, Linear, Notion, and Codex authentication, and pause whenever I must log in or enter a secret.
```

Codex will inspect existing state, install the local harness, preview mutations, and continue from completed checkpoints if setup is interrupted.

For a local-only harness without Railway automation, use:

```text
Use $setup-harness to initialize this repository for Linear and Notion. Interview me about the stack, preview every file, and apply it after I approve.
```

## What cloud onboarding creates

```text
Linear webhook
      |
      v
Railway control-plane service ---- Railway Postgres
      |
      +---- isolated ticket sandbox 1 ---- Codex CLI
      +---- isolated ticket sandbox 2 ---- Codex CLI
      +---- isolated ticket sandbox 3 ---- Codex CLI
```

The default limit is three simultaneous source-ticket runs. One source Linear issue has one permanent claim and one resumable run ID. A completed pipeline creates a draft GitHub pull request and never merges automatically.

The localhost UI connects through an authenticated local proxy. The browser never receives the device access or refresh token.

## Guided cloud setup

The onboarding skill performs these steps. This section is also the manual recovery reference if setup stops midway.

### Prerequisites and account permissions

- Railway account with Sandboxes enabled through Priority Boarding.
- Permission to create a Railway project, service, Postgres database, public domain, and workspace-scoped token.
- GitHub organization permission to create and install a GitHub App on selected repositories.
- Linear workspace permission to create a private OAuth application and webhook.
- Notion workspace permission to create an internal connection and share a parent page.
- Enough Codex accounts or sessions for the desired number of independent authentication slots.

Provider connections used interactively by the Codex plugin and credentials used by the cloud control plane are separate boundaries. The cloud runner stores its GitHub, Linear, Notion, and Codex credentials encrypted in Postgres.

Install the Codex plugin and CLI once, then give every repository its own named cloud profile and, when provider isolation matters, its own Railway control plane and Postgres database. Add the non-secret binding to that repository's `.harness/config.yaml`:

```yaml
cloud:
  profile: agent-harness-marketing-site
```

The CLI resolves `cloud.profile` from the nearest `.harness/config.yaml`, including from a nested worktree. That profile selects a local URL and keychain-backed device session; no credential is committed. An explicit `agent-harness cloud --profile <name> ...` or `AGENT_HARNESS_PROFILE=<name>` override wins. A control plane's provider credentials are installation-wide, so use a separate control plane whenever repositories must connect to different Linear workspaces or Notion workspaces/parents.

### A. Install the repository harness

The `$setup-harness` skill:

1. Inspects the repository, stack, commands, remote, default branch, and existing guidance.
2. Interviews the user only for missing choices.
3. Previews every `.harness` and `.agents` file.
4. Applies only approved changes.
5. Validates `.harness/config.yaml`, `.harness/pipeline.yaml`, Git, GitHub CLI authentication, and agent references.

The installed repository contains:

- `.agents/*.md` — stable role contracts.
- `.harness/*.md` — repository-specific product, architecture, testing, security, and deployment guidance.
- `.harness/config.yaml` — non-secret tracker, Notion, Git, automation, and optional local cloud-profile identifiers.
- `.harness/pipeline.yaml` — editable agent DAG, inputs, outputs, parallelism, and deterministic hooks.
- `.harness/scripts/arch-lint.py` and `.harness/arch-lint-rules.json` — deterministic architecture checks.
- ignored runtime locations for journals, worktrees, locks, and injected ADRs.

### B. Create the Railway control plane

Codex can create these resources, or you can create them manually:

```sh
railway init --name <repository>-agent-harness
railway add --service control-plane --json
railway add --database postgres --json
railway domain --service control-plane --json
```

Create a workspace-scoped Railway token for Sandbox management. Enter it only into your local shell or a temporary sealed Railway variable; never paste it into chat or commit it.

Create the worker checkpoint and configure the control plane:

```sh
export RAILWAY_API_TOKEN='<enter privately>'

agent-harness railway upgrade \
  --project <railway-project-id> \
  --environment production \
  --version v0.1.0-rc.32

agent-harness railway init \
  --project <railway-project-id> \
  --environment production \
  --service control-plane \
  --postgres-service Postgres \
  --url https://<control-plane-domain> \
  --checkpoint agent-harness-worker-0.1.0-rc.32 \
  --profile <repository-profile>

agent-harness railway deploy \
  --project <railway-project-id> \
  --environment production \
  --service control-plane \
  --path /path/to/agent-harness/cloud-runner
```

`railway init` generates a one-time bootstrap token and encryption key, seals them in Railway, connects Postgres, and stores the bootstrap credential locally. After the first successful deployment, initialize the team owner:

```sh
agent-harness cloud team initialize --name "Your name" --device "Your laptop"
agent-harness cloud whoami
```

The bootstrap token is accepted only by the one-time owner-initialization endpoint and is rejected by ordinary APIs. Initialization creates the installation's permanent owner identity and this device's revocable session. The current release does not transfer or promote installation ownership. The CLI stores the rotating session in the operating-system keychain with a mode-0600 file fallback.

Do not continue until the deployment reaches `SUCCESS` and both endpoints respond successfully:

```sh
curl -fsS https://<control-plane-domain>/healthz
curl -fsS https://<control-plane-domain>/readyz
```

### C. Connect GitHub

Run the guided GitHub App manifest flow:

```sh
agent-harness cloud auth github --manifest-owner <github-owner>
```

Approve the app and install it only on repositories the runner is allowed to modify. The app requests Metadata read, Contents write, and Pull Requests write, and configures a signed pull-request webhook at `/webhooks/github`. Record the installation ID for repository registration.

To add merge tracking to an existing Agent Harness GitHub App without replacing its App ID, private key, or installations, deploy the current control plane and run:

```sh
agent-harness cloud auth github upgrade-webhook
```

The control plane generates and stores the webhook secret without printing it and updates the existing App's webhook URL. In the GitHub App settings, then enable the webhook and subscribe it to **Pull request** events.

### D. Connect Linear

Open the pre-filled application form:

```sh
agent-harness cloud auth linear manifest --url https://<control-plane-domain>
```

Use these Linear application settings:

| Setting | Value |
|---|---|
| Distribution | Private |
| Redirect URI | `http://127.0.0.1:8743/callback` |
| Public | Off |
| Client credentials | Off |
| Webhooks | On |
| Webhook URL | `https://<control-plane-domain>/webhooks/linear` |
| App name | `Vessica` |
| OAuth actor/scopes | `actor=app`; include `app:assignable` |
| Webhook resources | `AgentSessionEvent`, `Issue`, `Comment`, `OAuthAuthorization`, `PermissionChange` |

Temporarily add the following sealed variables to the Railway `control-plane` service:

- `LINEAR_CLIENT_ID`
- `LINEAR_CLIENT_SECRET`
- `LINEAR_WEBHOOK_SECRET`

Then run:

```sh
railway run --service control-plane --environment production -- \
  agent-harness cloud auth linear
```

Approve the Linear OAuth page. Verify `linear_oauth` and `linear_webhook_secret` with `agent-harness cloud auth status`, then remove all three temporary variables. The durable access and refresh tokens remain encrypted in Postgres.

Repository registration idempotently creates the team-specific **Needs Input** and **For Review** states in Linear's Started category when they are absent. Existing **In Review** or **Review** states satisfy the review-state requirement and are not duplicated. The authorizing Linear member must be allowed to manage the team's workflow statuses.

If the OAuth app predates native agent dispatch, rename or recreate it as **Vessica**, add `app:assignable`, and add `AgentSessionEvent` to its webhook resources before reauthorizing it. New manifests include the complete resource set.

### E. Connect Notion

Create an internal Notion connection named `Agent Harness` with read, insert, and update content permissions. Open the desired parent page and add the connection through the page's **Connections** menu. Sharing the parent gives the connection access to its children. See [Notion's internal connection guide](https://developers.notion.com/guides/get-started/internal-connections).

Temporarily add the integration token to Railway as the sealed variable `NOTION_TOKEN`, then run:

```sh
railway run --service control-plane --environment production -- \
  agent-harness cloud auth notion
```

The onboarding skill verifies that the parent page is readable and writable by creating and immediately archiving a temporary child page. It then removes `NOTION_TOKEN`; the encrypted control-plane credential remains.

### F. Add Codex authentication slots

Create three independent Codex sessions:

```sh
agent-harness cloud auth codex add --slots 3
```

Complete each device-login prompt. The CLI tests whether a single session can safely serve multiple simultaneous Codex processes. If it cannot, the scheduler uses one exclusively leased slot per process rather than copying an active refresh session.

### G. Register a repository

Repository registration validates the GitHub installation, Linear workspace/team/project, and Notion parent. IDs—not display names—are required for provider identifiers. Discover the Linear IDs from the encrypted OAuth connection:

```sh
agent-harness cloud repo discover-linear
```

```sh
agent-harness cloud repo add \
  --name <repository-name> \
  --github-owner <owner> \
  --github-repo <repository> \
  --github-installation <installation-id> \
  --linear-workspace <workspace-id> \
  --linear-team <team-id> \
  --linear-project <optional-project-id> \
  --linear-agent Vessica \
  --notion-parent <parent-page-id> \
  --base-branch main

agent-harness cloud repo list
agent-harness cloud auth status
```

The Linear OAuth app must be installed as the assignable app actor **Vessica**, and that name must match `.harness/config.yaml`. Do not delegate a real issue until all credentials, Codex slots, health checks, and repository registration are green.

### H. Add and administer teammates

The owner or an administrator can create a single-use invitation for a viewer, operator, or administrator:

```sh
agent-harness cloud team invite \
  --role operator \
  --label "Teammate" \
  --expires 1h
```

Send the complete link through a secure channel. The invitation secret is stored in the URL fragment, is not sent when the landing page loads, and is returned only when the invitation is created. Links expire after one use; their lifetime can be between one minute and seven days.

The recipient joins from their own device:

```sh
agent-harness cloud join 'https://<control-plane-domain>/join#invite=...' \
  --name "Teammate" \
  --device "Work laptop"
agent-harness cloud whoami
```

Every redemption creates a named member and an individually revocable device session. Use the CLI or the dashboard's **Team** view to inspect members, invitations, sessions, and authentication audit events. Administrators can change any non-owner member among the viewer, operator, and administrator roles, or revoke a member, invitation, or device session without rotating anyone else's credentials.

| Role | Access |
|---|---|
| Viewer | Read status, runs, artifacts, and the live event stream |
| Operator | Viewer access plus Inbox responses, run input, resume, cancel, and reconcile actions and disposable Linear issue utilities |
| Administrator | Operator access plus repositories, provider credentials, Codex slots, invitations, roles, sessions, and the authentication audit |
| Owner | Administrator access plus the immutable installation-owner identity |

## Run and monitor

### Automatic cloud execution

Delegate an eligible root Linear issue to the **Vessica** agent. Linear creates a native AgentSession, and the signed webhook is acknowledged and persisted before work begins. Repeated delegation and later qualifying updates resolve to the existing run.

To hold one source issue behind another, add an explicit line such as `Depends on AGE-22` to the dependent issue's description before delegating it to Vessica. Multiple issue keys may be comma-separated or declared on multiple `Depends on` lines. The run remains queued without consuming a sandbox until every referenced Linear issue is in a completed workflow state; a dependency update releases it automatically.

Monitor from the CLI:

```sh
agent-harness cloud runs list
agent-harness cloud runs show <run-id>
agent-harness cloud runs watch --run <run-id>
agent-harness cloud runs input <run-id> --file clarified-request.md
agent-harness cloud runs reconcile <run-id>
```

Open the localhost dashboard:

```sh
agent-harness ui
```

The UI binds to `127.0.0.1` and streams authenticated events without exposing device credentials to browser JavaScript. Its top-level **Inbox** lists open Product and Architecture questions; operators can select the recommended or alternate choice, provide free text, and atomically queue the checkpointed run. The remaining **Runs** surface is read-only. For owners and administrators, the **Team** view can issue one-time invitation links, change non-owner roles, revoke members, invitations, or devices, and inspect authentication history.
Selecting a pipeline run filters the event stream to that run. Run details include duration, model, token counts, and estimated API-equivalent token cost; Playwright execution in Railway sandboxes is resource-capped without reducing the number of independent ticket pipelines.

Only Product and Architecture may request human input, and each may do so once. The runner uploads the journal, records the request, exits the disposable sandbox without retrying, moves Linear to **Needs Input**, and accepts the first answer from either the Inbox or a reply to the exact question thread. Answers accepted through the web UI or another non-Linear channel such as Slack are recorded in one idempotent Linear comment; an answer posted in Linear is already present there and is not copied. All later stages are prompt- and runtime-constrained from waiting for a user.

Resume, cancel, or export a run:

```sh
agent-harness cloud runs resume <run-id>
agent-harness cloud runs cancel <run-id>
agent-harness cloud runs export <run-id> --repo /path/to/repository
```

### Local execution from Codex

Examples after the plugin is installed:

```text
Use $run-harness to run the full pipeline for Linear issue L-123.
Use $run-harness to run only product and arch for L-111.
Use $inspect-harness to list the tickets associated with L-222.
Use $inspect-harness to watch cloud run <run-id> and report meaningful progress.
```

Named stages run exactly those stages in pipeline order. An unfinished run resumes by default; starting another run for the same source issue requires explicit new-run language.

## Security model

- Provider credentials are encrypted with AES-256-GCM in Postgres and never written into repositories.
- Codex subprocesses do not receive Railway, Linear, Notion, encryption, or management credentials.
- GitHub installation tokens are repository-scoped, minted just in time, and expire after one hour.
- Each source issue has one permanent claim and one resumable cloud run.
- Sandboxes are disposable; Postgres journals and pushed integration branches are recovery authorities.
- Pull requests are drafts and are never merged automatically.
- Management and event endpoints require short-lived, role-scoped member access. The only non-member routes are health checks, signed Linear and GitHub webhooks, the inert join page, one-time bootstrap initialization, invitation redemption, and token rotation; initialization still requires the bootstrap bearer.
- Team invitations are single-use, expire after one hour by default, and may be configured for no more than seven days. Access tokens last 15 minutes and device refresh tokens last 30 days. The CLI refreshes and rotates them automatically; replay of the previous refresh token revokes that device session.
- Revoking a member revokes all of that member's device sessions. Revoking a single session or logging out affects only that device. The bootstrap owner cannot be demoted or revoked, and owner transfer is not supported in this release.

## Development

Run all repository checks:

```sh
make verify
```

Build or install the current checkout with `make build` or `make install`.

## Maintainer release

Production uses two artifacts from the same version: the tagged GHCR image for
the control plane and a Railway checkpoint for disposable workers. From a clean,
rebased `main`, run:

```sh
make release-check
make release
```

Both commands inspect the release-candidate tags on `origin` and select the next
RC on the newest version line, such as `v0.1.0-rc.33` after `v0.1.0-rc.32`.
`release-check` is read-only with respect to GitHub and Railway. `release` reruns
verification, pushes `main` and the selected version tag, waits for GitHub
Actions to publish all release assets and the GHCR image, creates the matching
Railway worker checkpoint, points the production control plane at the tagged
image, and waits for terminal Railway success plus `/healthz` and `/readyz`.

To start a new version line or override the automatic choice, provide an
explicit version:

```sh
make release VERSION=v0.2.0-rc.1
```

Each external stage is also independently resumable:

```sh
make publish VERSION=v0.1.0-rc.27
make checkpoint VERSION=v0.1.0-rc.27
make deploy-production VERSION=v0.1.0-rc.27
make production-status
```

The production Railway project, environment, service, and public URL have
repository defaults. Override `RAILWAY_PROJECT`, `RAILWAY_ENVIRONMENT`,
`RAILWAY_SERVICE`, or `PUBLIC_URL` when targeting a different installation.

For local control-plane development and implementation details, see [cloud-runner/README.md](cloud-runner/README.md).
