---
title: Agent Harness User Guide
description: Install, configure, run, customize, monitor, and recover Agent Harness issue-to-pull-request workflows.
product: Agent Harness
product_version: v0.1.0-rc.33
release_status: Release candidate
last_verified: 2026-08-21
---

# Agent Harness User Guide

Agent Harness is an editable, issue-to-pull-request software-development workflow for Codex. It turns a well-scoped Linear issue into a product plan, architecture decision, dependency-aware implementation tickets, tested code, QA evidence, and a draft GitHub pull request. The workflow lives in the repository, while deterministic orchestration manages dependencies, isolated worktrees, validation, external synchronization, durable state, and recovery.

This guide covers the complete user-facing Agent Harness platform: the Codex plugin, repository harness, local execution, Railway cloud runner, command-line interface, localhost dashboard, Linear, GitHub and Notion integrations, workflow customization, security, and recovery.

> **Release status:** Agent Harness is currently a release candidate. These instructions were verified against `v0.1.0-rc.33`. Jira integration is coming soon and is not part of the supported workflow documented here.

## Quickstart

This is the shortest supported path from a new machine to an automatically executed Linear issue. The guided Codex onboarding handles the detailed checks and pauses when you must sign in, approve provider access, or enter a secret privately.

### 1. Install the required command-line tools

You need Git, GitHub CLI, Codex CLI, Railway CLI, and the Agent Harness CLI. On macOS:

```sh
npm install -g @openai/codex
codex login

brew install gh
gh auth login

curl -fsSL agents.railway.com | sh
railway login
```

Download Agent Harness for Apple Silicon macOS:

```sh
mkdir -p "$HOME/.local/bin"
curl -fL \
  https://github.com/vessica-labs/agent-harness/releases/download/v0.1.0-rc.33/agent-harness-darwin-arm64 \
  -o "$HOME/.local/bin/agent-harness"
chmod 0755 "$HOME/.local/bin/agent-harness"
export PATH="$HOME/.local/bin:$PATH"
agent-harness version
```

For Intel macOS, Linux x86-64, or Linux ARM64, replace the filename with the matching release asset:

- `agent-harness-darwin-amd64`
- `agent-harness-linux-amd64`
- `agent-harness-linux-arm64`

### 2. Install the Codex plugin

```sh
codex plugin marketplace add vessica-labs/agent-harness --ref main
codex plugin add agent-harness@agent-harness --json
```

Restart the Codex desktop app and open a new task so the plugin skills are loaded.

### 3. Open the repository you want Agent Harness to manage

In Codex, open the target Git repository and ask:

```text
Use $onboard-cloud-runner to set up Agent Harness for this repository.
Walk me through Railway, GitHub, Linear, Notion, and Codex authentication,
and pause whenever I must log in, approve access, or enter a secret.
```

Codex will:

1. Inspect the repository and install its local harness.
2. Preview repository changes before applying them.
3. Create or reuse the Railway control plane and Postgres database.
4. Connect GitHub, Linear, Notion, and isolated Codex login slots.
5. Register the repository and verify the complete configuration.

### 4. Start the first run

After onboarding reports that all checks are green, delegate an eligible root Linear issue to the **Vessica** agent. Linear creates a native AgentSession, and the issue title and description become the initial feature request.

Agent Harness claims the issue once, launches an isolated Railway sandbox, reads the repository's checked-in workflow, and begins the pipeline. Repeated webhook deliveries resolve to the same durable run.

### 5. Monitor progress

From a terminal:

```sh
agent-harness cloud runs list
agent-harness cloud runs watch --run <run-id>
```

Or open the authenticated localhost dashboard:

```sh
agent-harness ui
```

The completed workflow produces a draft GitHub pull request. Agent Harness never merges it automatically.

### Local-only quickstart

If you do not want Railway automation, ask Codex from the target repository:

```text
Use $setup-harness to initialize this repository for Linear and Notion.
Interview me about the stack, preview every file, and apply it after I approve.
```

Then run a workflow interactively:

```text
Use $run-harness to run the full pipeline for Linear issue ENG-123.
```

Local execution supports full or selected stages and remains under direct Codex control. The rest of this guide explains the differences between local and cloud execution.

### Guide map

- **Understand:** platform concepts, architecture, and local-versus-cloud behavior.
- **Configure:** prerequisites, installation, repository setup, and cloud onboarding.
- **Run:** source issues, pipeline stages, tickets, retries, and repair loops.
- **Monitor and recover:** CLI inspection, dashboard, operator actions, artifacts, and durable recovery.
- **Customize:** repository guidance, agent roles, pipeline YAML, hooks, and architecture lint.
- **Operate securely:** credentials, capabilities, reliability, configuration, and self-hosting.
- **Reference:** complete operator CLI, troubleshooting, limits, and glossary.

## 1. What Agent Harness does

Agent Harness combines flexible agent work with deterministic engineering controls. Codex agents perform product, architecture, coding, lint, QA, and delivery work. The orchestration layer determines what may run, which inputs are available, how parallel work is isolated, which outputs are valid, when remote systems may be updated, and where recovery resumes.

The platform provides:

- A Codex plugin for setup, cloud onboarding, execution, and inspection.
- Repository-owned agent definitions and project guidance.
- An editable YAML workflow with explicit dependencies, inputs, outputs, parallelism, hooks, and repair loops.
- Dependency-aware ticket execution in isolated Git worktrees.
- Durable local journals and cloud checkpoints.
- Linear issue intake, child-ticket synchronization, comments, and cloud workflow-state updates.
- Notion publication for PRDs, ADRs, and other artifacts.
- GitHub App authentication, integration branches, and draft pull requests.
- A Railway control plane and disposable sandbox workers.
- A localhost dashboard with live activity, run details, ticket state, artifacts, and usage telemetry.
- Explicit pause, clarification, resume, cancellation, reconciliation, export, and recovery operations.

### Core principles

**The repository owns the workflow.** The checked-in `.harness/pipeline.yaml`, `.agents/*.md`, and `.harness/*.md` files define how work is performed. The cloud runner does not contain a hidden alternative pipeline.

**State is durable.** Local runs persist an atomic journal under `.harness/runs`. Cloud runs persist claims, leases, events, usage, synchronization identities, and a compressed journal in Postgres-backed control-plane storage.

**External systems are projections.** Linear comments and child issues, Notion pages, branches, and pull requests reflect the run, but they do not replace the authoritative journal.

**Side effects are idempotent.** Stable markers and persisted provider identities let recovery update existing comments, issues, and pages instead of creating duplicates.

**Delivery remains human-controlled.** The workflow may create and update issues, commit code, push an integration branch, and open a draft pull request. It never merges the pull request.

## 2. Platform architecture

The cloud architecture is:

```text
Root Linear issue delegated to Vessica
                |
                v
       Signed Linear webhook
                |
                v
    Railway control-plane service <----> Railway Postgres
                |
                +---- isolated Railway sandbox ---- Codex CLI
                |              |                         |
                |              +---- Git worktrees       |
                |              +---- test tools          |
                |              +---- run journal --------+
                |
                +---- GitHub App: branch and draft PR
                +---- Linear OAuth: children, comments, states
                +---- Notion connection: published artifacts

Local agent-harness CLI ---- authenticated management API
             |
             +---- localhost-only dashboard and SSE proxy
```

### Repository harness

Each configured repository contains:

```text
AGENTS.md
.agents/
  product.md
  architect.md
  coder.md
  lint.md
  qa.md
  docs.md
  pr.md
.harness/
  config.yaml
  pipeline.yaml
  ARCHITECTURE.md
  DESIGN.md
  TESTING.md
  SECURITY.md
  DEPLOY.md
  arch-lint-rules.json
  scripts/arch-lint.py
  adrs/
  runs/
  worktrees/
  .locks/
```

The guidance and configuration are tracked. Runtime journals, worktrees, locks, and injected run-specific ADRs are ignored.

### Control plane

The `agent-harness server` process provides:

- Signed Linear webhook intake.
- Permanent source-issue claims and deduplication.
- Repository registration.
- Scheduler leases and concurrency control.
- Encrypted provider and Codex credentials.
- Run, stage, ticket, artifact, synchronization, usage, and event records.
- Worker capabilities and just-in-time GitHub tokens.
- Authenticated management and Server-Sent Events endpoints.
- Health and readiness checks.

### Sandbox worker

For each runnable cloud issue, the scheduler creates a disposable Railway sandbox from a versioned checkpoint. The worker:

1. Obtains a short-lived, repository-scoped GitHub installation token.
2. Clones the repository and checks out the durable integration branch if one exists.
3. Loads the repository's `.harness/pipeline.yaml`.
4. Restores the latest compressed run journal or initializes it.
5. Executes incomplete stages in declared order.
6. Uploads checkpoints throughout the run.
7. Synchronizes Linear and Notion through the control plane.
8. Returns updated Codex authentication material to its encrypted slot.
9. Ends after completion, a durable input checkpoint, or pause; the control plane then destroys the sandbox.

Sandboxes are disposable. The journal, pushed branch, and control-plane records are the recovery authorities.

## 3. Local and cloud execution

Agent Harness supports two ways to execute a repository workflow.

| Capability | Local execution | Railway cloud execution |
|---|---|---|
| Started by | Direct Codex request | Trigger label on a root Linear issue |
| Supported tracker | Linear | Linear |
| Jira | Coming soon | Coming soon |
| Full pipeline | Yes | Yes |
| Selected stages | Yes | No; cloud runs the checked-in full pipeline |
| Durable journal | Repository filesystem | Postgres artifact plus restored sandbox journal |
| Isolation | Local worktrees | Disposable Railway sandbox and worktrees |
| Concurrency | Pipeline YAML | Pipeline YAML plus control-plane run capacity and auth slots |
| Monitoring | Codex and local journal | CLI, SSE, dashboard, Linear, and Notion |
| Linear comments/children | Yes | Yes |
| Linear workflow states | Unchanged | Automatically synchronized |
| Draft pull request | When PR stage is selected | Yes, when the full pipeline succeeds |
| New run for same issue | Only when explicitly requested | One permanent durable claim per source issue |

### Status-management differences

This difference is intentional and should be understood before choosing an execution mode.

In **local execution**, Agent Harness creates or updates canonical comments and child issues, but it does not change Linear workflow-state fields. The user or team retains direct control of Todo, In Progress, For Review, and Done.

In **cloud execution**, Agent Harness uses the configured Linear team's real workflow-state IDs:

- A newly claimed parent immediately moves to Todo and receives an Agent Harness activity comment.
- The first pipeline stage moves the parent to In Progress; Vessica publishes native Agent Activities for stage starts, completions, retries, input waits, failures, and the final response.
- A Product or Architecture input request moves the parent to Needs Input and creates one replyable question thread. Answering in that thread or the control-plane Inbox returns it to In Progress.
- New child tickets are created directly in the team's Todo state.
- A coder claim moves that child to In Progress.
- A child moves to Done after its commit is integrated.
- A failed child and a paused parent move to Needs Input so a stopped run cannot remain visually active.
- A completed pipeline with its pull request moves the parent to For Review.
- A signed GitHub pull-request merge webhook moves the parent to Done.

Cloud repository registration and lifecycle synchronization idempotently install **Needs Input** and **For Review** in the team's Started category when they are absent. An existing **In Review** or **Review** state is reused instead of creating a duplicate For Review state. The Linear member who authorizes the app must have permission to manage that team's workflow statuses.

`agent-harness cloud runs reconcile <run-id>` can restore those states and missing stage activity from durable run truth if provider state drifts.

## 4. Prerequisites and permissions

### Local tools

| Tool | Purpose |
|---|---|
| Git | Repository inspection, branches, commits, and worktrees |
| GitHub CLI (`gh`) | Authentication and pull-request operations |
| Codex CLI | Agent execution and device authentication |
| Agent Harness CLI | Cloud setup, operation, and dashboard |
| Railway CLI | Control-plane deployment and sandbox management |
| Python 3 | Deterministic repository helpers and architecture lint |

The cloud worker checkpoint includes Git, GitHub CLI, Codex CLI, Railway CLI, Python, Node.js 24, pnpm 11, Playwright, and Chromium. Playwright's CLI and browser runtime are warm in every sandbox, while target repositories still declare `@playwright/test` and other libraries in their own package manifests and lockfiles. A target repository may still require another language runtime, services, or project-specific dependencies.

### Account permissions

For the cloud runner, the operator needs:

- Railway permission to create a project, service, Postgres database, domain, variables, and Sandboxes.
- A workspace-scoped Railway token that can manage Sandboxes.
- GitHub permission to create a GitHub App and install it on selected repositories. The App needs Metadata read plus Contents, Pull requests, and Workflows write; GitHub treats workflow-file pushes as a separate permission from ordinary contents writes.
- Linear permission to create a private OAuth application and webhook.
- Notion permission to create an internal connection and share a parent page.
- Enough Codex login sessions for the desired concurrent capacity.

### Keep the credential boundaries separate

The Codex plugin's interactive app connections and the cloud control plane's service credentials are separate systems. Do not copy a Codex app credential into Railway or paste provider secrets into chat. The onboarding flow uses device login, local callbacks, private shell entry, or temporary sealed Railway variables.

### One installation, repository-specific control planes

Install the Codex plugin and Agent Harness CLI once. Each repository can bind itself to a different named local profile with a non-secret entry in `.harness/config.yaml`:

```yaml
cloud:
  profile: vessica-cli
```

The CLI searches from the current directory upward for the nearest `.harness/config.yaml`, so the binding also works in nested directories and ticket worktrees. The selected profile contains only a control-plane URL in the local config directory; its rotating device session stays in the operating-system keychain or the mode-0600 fallback. Profile resolution is:

1. `AGENT_HARNESS_URL` plus `AGENT_HARNESS_TOKEN` for non-refreshing automation.
2. `agent-harness cloud --profile NAME ...`.
3. `AGENT_HARNESS_PROFILE=NAME`.
4. The repository's `cloud.profile`.
5. The current local profile, then `default`.

GitHub, Linear, Notion, and Codex credentials are scoped to a control-plane installation, not to an individual repository registration inside it. When repositories must use different Linear workspaces or different Notion workspaces/parents, deploy one Railway project, Postgres database, domain, and named profile per repository. Sharing one control plane is appropriate only when those provider credentials are intentionally shared.

## 5. Install and upgrade

### Install a release binary

Release assets are published for macOS and Linux on AMD64 and ARM64. This example installs `v0.1.0-rc.33` on Apple Silicon macOS:

```sh
mkdir -p "$HOME/.local/bin"

curl -fL \
  https://github.com/vessica-labs/agent-harness/releases/download/v0.1.0-rc.33/agent-harness-darwin-arm64 \
  -o "$HOME/.local/bin/agent-harness"

chmod 0755 "$HOME/.local/bin/agent-harness"
export PATH="$HOME/.local/bin:$PATH"
agent-harness version
```

To verify the release checksum, download `SHA256SUMS` beside the binary and verify the matching line before installing it.

### Build from source

```sh
git clone https://github.com/vessica-labs/agent-harness.git
cd agent-harness
make install
agent-harness version
```

The default installation prefix is `$HOME/.local`; override `PREFIX` when
needed. The locally built binary reports the source tag or commit used to build
it.

### Install the Codex plugin

```sh
codex plugin marketplace add vessica-labs/agent-harness --ref main
codex plugin add agent-harness@agent-harness --json
```

The plugin includes four user-facing skills:

| Skill | Use it for |
|---|---|
| `$setup-harness` | Initialize or reconfigure a repository harness |
| `$onboard-cloud-runner` | Deploy or repair the Railway control plane and register a repository |
| `$run-harness` | Execute, resume, or monitor a local or cloud workflow |
| `$inspect-harness` | Inspect runs, tickets, artifacts, failures, leases, and recovery options without changing state |

Restart Codex and open a new task after installation or upgrade.

### Upgrade the cloud runner

The maintained production release uses one version for the GitHub assets, GHCR
control-plane image, and Railway worker checkpoint. From a clean, rebased
`main`, validate and release it from the repository root:

```sh
make release-check
make release
```

These commands read the RC tags from `origin` and automatically select the next
number on the newest release-candidate version line. For example, an existing
`v0.1.0-rc.33` produces `v0.1.0-rc.34`. Pass an explicit version to override the
selection or start a new version line:

```sh
make release VERSION=v0.2.0-rc.1
```

The release command waits for the selected tag's GitHub workflow, creates the
worker checkpoint, updates Railway's pinned control-plane image, waits for
terminal deployment success, and verifies `/healthz` and `/readyz`. To resume a
partially completed release, run its stages separately with the selected
version:

```sh
make publish VERSION=vX.Y.Z
make checkpoint VERSION=vX.Y.Z
make deploy-production VERSION=vX.Y.Z
make production-status
```

For a separate source-based installation, upgrade the versioned worker
checkpoint before deploying the matching local control-plane source:

```sh
export RAILWAY_API_TOKEN='<enter privately>'

agent-harness railway upgrade \
  --project <railway-project-id> \
  --environment production \
  --version v0.1.0-rc.33

agent-harness railway deploy \
  --project <railway-project-id> \
  --environment production \
  --service control-plane \
  --path /path/to/agent-harness/cloud-runner
```

Wait for Railway to report terminal success, then verify both endpoints:

```sh
curl -fsS https://<control-plane-domain>/healthz
curl -fsS https://<control-plane-domain>/readyz
```

## 6. Set up a repository

Repository setup follows an inspect, interview, preview, approve, apply, and verify sequence. This keeps existing guidance and unrelated worktree changes safe.

### Guided setup

From the target repository, ask Codex:

```text
Use $setup-harness to initialize this repository for Linear and Notion.
Inspect the repository first, ask only for missing choices, preview every file,
and apply the setup only after I approve it.
```

The skill inspects:

- Existing repository instructions.
- Technology stack and directory structure.
- Build, test, lint, preview, and deployment commands.
- UI conventions and accessibility requirements.
- Security and data boundaries.
- Git remote, default branch, and GitHub CLI status.

It asks only for facts it cannot discover, such as the Linear workspace/team/project, Notion parent page, or a nonstandard Git remote and base branch.

> **Jira:** Jira integration is coming soon. Configure new supported repositories with Linear.

### Preview and conflicts

The setup tool previews every create, unchanged, and conflict result. It does not modify the repository until `--apply` is used. A conflicting existing file is preserved unless its replacement is explicitly approved and `--force` is supplied.

After the base files are installed, Codex fills the project-specific guidance from repository evidence. Stable `.agents/*.md` role contracts are not casually rewritten during setup.

### Configuration file

The installed `.harness/config.yaml` contains non-secret integration identifiers:

```yaml
version: 1
tracker:
  provider: linear
  workspace: <linear-workspace-id>
  project: <linear-team-or-project-id>
notion:
  parent_page_id: <notion-page-id>
git:
  remote: origin
  base_branch: main
cloud:
  profile: <repository-profile>
automation:
  enabled: true
  trigger:
    provider: linear
    type: agent
    agent: Vessica
```

Repositories that want live previews of completed runs add an optional `preview` block:

```yaml
preview:
  command: npm run start
  port: 3000
  healthcheck: /
```

`command` starts the application inside the sandbox after the draft pull request is created, `port` is the port the application listens on (the worker sets `PORT` to it), and `healthcheck` is an optional absolute path polled until the application responds. Repositories without a `preview` block skip preview publication.

Do not store OAuth tokens, private keys, Codex sessions, management tokens, or Railway tokens in this file.

### Validate the installation

The setup skill runs the equivalent of:

```sh
python3 <plugin>/scripts/harnessctl.py \
  validate-config .harness/config.yaml

python3 <plugin>/scripts/harnessctl.py \
  validate-pipeline .harness/pipeline.yaml --repo .
```

It also checks the Git remote/base branch, GitHub CLI authentication, agent references, and selected provider connections.

## 7. Onboard the Railway cloud runner

Use `$onboard-cloud-runner` for the supported guided path. It inspects existing state and resumes from the first incomplete checkpoint instead of recreating resources.

### Step 1: Prepare Railway

For the repository being onboarded, create or reuse one dedicated Railway project containing:

- One single-replica `control-plane` service.
- One Railway Postgres service.
- One public HTTPS domain on the control plane.

Railway Sandboxes must be enabled for the project. The onboarding flow verifies access before provisioning the runner.

If creating the resources manually:

```sh
railway init --name <repository>-agent-harness
railway add --service control-plane --json
railway add --database postgres --json
railway domain --service control-plane --json
```

To enable live previews of completed runs, also create the public preview edge service and give it a domain:

```sh
railway add --service preview-edge --json
railway domain --service preview-edge --json
```

Create a workspace-scoped Railway token and enter it privately as `RAILWAY_API_TOKEN`. Never commit it or paste it into chat.

Create the worker checkpoint, configure the control plane, and deploy:

```sh
agent-harness railway upgrade \
  --project <project-id> \
  --environment production \
  --version v0.1.0-rc.33

agent-harness railway init \
  --project <project-id> \
  --environment production \
  --service control-plane \
  --postgres-service Postgres \
  --url https://<control-plane-domain> \
  --checkpoint agent-harness-worker-0.1.0-rc.33 \
  --profile <repository-profile> \
  --preview-url https://<preview-edge-domain>

agent-harness railway deploy \
  --project <project-id> \
  --environment production \
  --service control-plane \
  --path /path/to/agent-harness/cloud-runner
```

`railway init` generates a one-time bootstrap token and encryption key, configures Railway variables, connects Postgres, and stores the bootstrap credential under the named local profile. Use the same profile name in this repository's `cloud.profile`. When `--preview-url` is provided it also generates the shared preview-edge token and configures the `preview-edge` service role and upstream (deploy the same `cloud-runner` path to that service with `railway deploy --service preview-edge`). Omit `--preview-url` to leave previews disabled. After the first successful deployment, exchange the bootstrap credential for the first owner device session:

```sh
agent-harness cloud team initialize --name "Your name" --device "Your laptop"
agent-harness cloud whoami
```

The bootstrap bearer is accepted only by this one-time initialization endpoint and is rejected by ordinary API endpoints. Initialization creates the installation's permanent owner identity and the first revocable device session. The owner cannot be demoted or revoked, and this release does not support transferring ownership or promoting another member to owner.

Do not connect providers until Railway reports a successful deployment and both health endpoints pass.

### Step 2: Connect GitHub

Run the guided GitHub App manifest flow:

```sh
agent-harness cloud auth github --manifest-owner <github-organization>
```

For a personal app, use `--manifest-owner @me`.

The manifest requests only:

- Metadata: read.
- Contents: write.
- Pull requests: write.

It also configures the control plane's signed `/webhooks/github` endpoint and subscribes the app to pull-request events so a merged PR can close the Linear lifecycle. The generated webhook secret is encrypted with the private key in the control plane.

Install the app only on repositories Agent Harness is allowed to modify. Record the installation ID for registration. The generated private key is sent directly to the authenticated control plane and is not written as a local plaintext key by the manifest flow.

For an App created before signed merge tracking was added, preserve the existing App and its installations. After deploying the current control plane, run:

```sh
agent-harness cloud auth github upgrade-webhook
```

This command generates a new secret inside the control plane, updates the existing GitHub App webhook to `<control-plane>/webhooks/github`, and stores the matching secret with the encrypted App credential. It does not print the secret or change repository permissions. Finish in the GitHub App settings by enabling the webhook and subscribing to **Pull request** events.

### Step 3: Connect Linear

Open the pre-filled Linear application form:

```sh
agent-harness cloud auth linear manifest \
  --url https://<control-plane-domain>
```

Use these settings:

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

The OAuth application requests `read`, `write`, `issues:create`, `comments:create`, and `app:assignable` as the Vessica app actor. The assignable scope makes Vessica appear in Linear's Agent picker; delegation creates the native AgentSession that dispatches the run.

Place the client ID, client secret, and webhook signing secret in temporary sealed Railway variables:

- `LINEAR_CLIENT_ID`
- `LINEAR_CLIENT_SECRET`
- `LINEAR_WEBHOOK_SECRET`

Then run the OAuth flow in the Railway service context:

```sh
railway run --service control-plane --environment production -- \
  agent-harness cloud auth linear
```

Approve the browser consent screen. Verify `linear_oauth` and `linear_webhook_secret` with `agent-harness cloud auth status`, then remove the three temporary Railway variables. The durable rotating OAuth token set remains encrypted in Postgres.

### Step 4: Connect Notion

Create an internal Notion connection named `Agent Harness` with read, insert, and update content capabilities. Open the intended parent page and add the connection through the page's **Connections** menu.

Place the integration token in a temporary sealed Railway variable named `NOTION_TOKEN`, then run:

```sh
railway run --service control-plane --environment production -- \
  agent-harness cloud auth notion
```

The guided onboarding validates that the parent is readable and writable, optionally creating and immediately archiving a temporary child page after giving notice. Remove `NOTION_TOKEN` after the encrypted credential is verified.

### Step 5: Add Codex capacity

Create three independent login sessions by default:

```sh
agent-harness cloud auth codex add --slots 3
```

Complete each device-login flow. The command tests whether the first session can safely serve multiple concurrent Codex processes. If it cannot, the scheduler exclusively leases independent slots instead of copying one active refresh session across processes.

### Step 6: Register the repository

Discover the connected Linear IDs:

```sh
agent-harness cloud repo discover-linear
```

Register the repository with IDs rather than display names:

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
```

Registration validates the GitHub installation, Linear workspace/team/project, the Vessica app-actor identity, and Notion parent before saving the record.

Verify the finished setup:

```sh
agent-harness cloud repo list
agent-harness cloud auth status
curl -fsS https://<control-plane-domain>/healthz
curl -fsS https://<control-plane-domain>/readyz
```

Confirm that the registered Linear agent is `Vessica` and exactly matches `.harness/config.yaml`. Only then delegate a real root Linear issue to Vessica.

## 8. Start and manage source issues

### Start from Linear

Assign Vessica from the Agent section of a root issue in the registered workspace, team, and optional project. Linear keeps the human owner as the primary assignee and sets Vessica as the issue delegate. That delegation creates a native AgentSession; its signed `AgentSessionEvent` webhook is the only cloud dispatch trigger. Child, archived, cancelled, mentioned-only, and differently delegated issues are ignored.

The first eligible AgentSession webhook creates one durable run. Duplicate deliveries and repeated delegations resolve to that run instead of creating another one. Ordinary Issue webhooks never dispatch work.

To sequence source issues, put an explicit dependency instruction in the dependent issue's description before delegating it to Vessica:

```text
Depends on AGE-22
```

You may list multiple issue keys on that line or use multiple `Depends on` lines. Agent Harness resolves each key through Linear and checks its workflow-state type. If any dependency is not completed—or cannot currently be resolved—the new run stays queued with a `dependencies_pending` or `dependencies_check_failed` reason and cannot be claimed by the scheduler. A later Linear update to a referenced issue rechecks the full dependency set and releases the run only after every dependency is Done. This source-issue gate is separate from the logical child-ticket DAG created inside a running pipeline.

### Create an issue through Agent Harness

The CLI can create a root Linear issue and delegate it to the repository's Vessica app actor:

```sh
agent-harness cloud repo issue create \
  --repo <repository-id> \
  --title "Add organization invitations" \
  --description-file feature-request.md
```

Use `--description-file -` to read Markdown from standard input. The description must be non-empty and smaller than 64 KiB. Linear creates a native AgentSession for Vessica, and the signed AgentSession webhook remains the only claim path; the create command does not bypass normal intake.

### Archive a disposable issue safely

```sh
agent-harness cloud repo issue archive \
  --repo <repository-id> \
  --issue ENG-123 \
  --yes
```

Archival requires explicit confirmation. Agent Harness refuses to archive a canonical source issue or a mapped child issue belonging to any durable run. This command is intended for disposable or duplicate issues that are not part of execution history.

## 9. The default workflow

The current default pipeline is:

```text
product -> arch -> coder -> lint -> qa -> pr
```

The pipeline is copied into the target repository during setup and is intended to be edited. The checked-in repository copy is authoritative for both local and cloud runs.

### Product

The product agent converts the feature request and repository evidence into:

- A structured PRD.
- Goals and non-goals.
- In-scope and out-of-scope behavior.
- Stable requirements and acceptance criteria.
- Product and UI/UX direction.
- Responsive and accessibility requirements.
- Risks, assumptions, and constraints.
- A dependency-aware logical ticket plan.

Each ticket includes an objective, acceptance criteria, owned paths, dependencies, and focused verification commands. Agent Harness validates the ticket graph before accepting it.

The PRD is published to Notion and the ticket plan is synchronized as Linear child issues.

### Architecture

The architecture agent creates an ADR covering:

- Context and decision drivers.
- Components and dependency boundaries.
- Interfaces and contracts.
- Data and state.
- Failure handling and observability.
- Security and privacy.
- Deployment and compatibility.
- Consequences, alternatives, tradeoffs, and risks.

It may add ticket dependencies and owned-path constraints. Agent Harness applies those constraints to the logical ticket plan, rejects unknown ticket references, and revalidates the graph and path ownership before coding.

Applicable ADRs are materialized into the run worktree under `.harness/adrs/`, and the run ADR is published to Notion.

### Coder

The coder stage uses `ticket_parallel` mode. Agent Harness:

1. Materializes one declared JSON input per logical ticket.
2. Computes dependency waves.
3. Prevents overlapping owned paths in the same wave.
4. Creates an isolated Git worktree per runnable ticket.
5. Starts one top-level Codex coordinator for the ready wave.
6. Has that coordinator delegate one native coder subagent per ticket, with no more than the stage's declared parallelism active at once.

Each coder subagent owns exactly one ticket. The coordinator does not implement ticket code; it supplies the isolated assignment, waits for every subagent, and reports the wave result. Each coder subagent follows red-green-refactor TDD, runs focused checks, commits a scoped change locally, returns its exact JSON result, and leaves a clean worktree. Neither the coordinator nor coder subagents push.

Tickets that add or update libraries own the affected package manifests and package-manager lockfile. Coders use the repository package manager and commit both manifest and lockfile changes; undeclared global imports, hand-written type shims, and lockfile-only dependency edits are rejected by the role contract.

The orchestrator integrates successful commits into the run branch in stable logical-key order, reruns focused checks, synchronizes ticket progress, pushes the durable branch, and removes disposable ticket worktrees. If a sibling in the same dependency wave fails, completed siblings are integrated and checkpointed before the stage retries, so recovery reruns only unfinished tickets. The failed ticket's result and blocker are preserved in the root journal and child-ticket progress before its disposable worktree is removed. Execution failures remain In Progress in Linear; Needs Input is reserved for a durable Product or Architecture question that appears in the Inbox.

### Lint

The lint stage runs repository lint and build commands, repairs deterministic failures when safely contained, and creates scoped repair commits. It also runs the repository's architecture-lint script.

After the agent finishes, the pipeline's `architecture-lint` after-hook runs the deterministic check again. That hook result—not the agent's narrative—is the authoritative architecture gate.

### QA

The QA agent verifies every PRD acceptance criterion and produces criterion-level evidence. For user-facing behavior it uses Playwright and records the route, action, expected result, actual result, and evidence.

Safe, contained defects may be repaired and committed during QA. If the required change is broader, QA emits structured new tickets. A matching repair loop can return execution to coder, then lint and QA, up to the configured maximum.

Cloud workers preinstall the Playwright CLI and system Chromium, then cap Playwright at two workers by default to avoid exhausting sandbox process and thread limits. Repositories should declare their Playwright test dependency, read `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` and `HARNESS_PLAYWRIGHT_WORKERS` in Playwright configuration, or pass the worker value as `--workers`.

### Pull request

The PR stage requires the declared PRD, ADR, ticket results, lint report, and QA evidence. It verifies that the integration worktree is clean, prepares the title and body, pushes the delivery branch, and creates or resolves the canonical draft GitHub pull request.

If a recovery run finds an already published base run branch, Agent Harness creates a distinct delivery branch rather than force-pushing over the existing remote branch.

The pull-request body summarizes:

- Outcome.
- Acceptance-criterion coverage.
- Implementation.
- Architecture.
- Verification.
- Documentation.
- Review guidance.
- Risks and follow-ups.

Agent Harness never merges the pull request.

### Optional documentation agent

The repository includes a stable `.agents/docs.md` documentation-agent contract, but the documentation stage is **optional and is not included in the default pipeline**.

Teams that want a dedicated documentation pass can add a stage to `.harness/pipeline.yaml`, declare its dependencies and file contracts, and reference `.agents/docs.md`. The custom pipeline must pass validation before use. The documentation agent should update repository documentation and return structured publishable documents without duplicating execution state owned by the journal.

## 10. Tickets, parallelism, and Git behavior

### Logical tickets and provider children

Logical ticket keys are stable within a run. A ticket may be locally planned before its Linear child exists, remotely synchronized with a provider key and URL, running in a worktree, completed with an integrated commit, or failed with evidence.

Stable child markers and persisted provider IDs allow recovery to update the same Linear issue instead of relying on title searches alone.

### Dependency waves

Tickets run only when all declared dependencies are complete. Within a wave, one Codex coordinator enforces the coder stage's native-subagent parallelism and Agent Harness rejects overlapping owned paths. Later waves start from the updated integration head.

The default coder-subagent parallelism is three. This is separate from the default maximum of three simultaneously active source-issue runs, each of which owns one Railway sandbox, one top-level Codex execution lane, and one independently leased Codex auth slot.

### Branches and commits

Cloud branches live beneath Railway's permitted `sandbox/` namespace and include the source issue and run suffix. Coder commits remain local until the orchestrator integrates them. The orchestrator owns remote pushes and GitHub credentials.

Agent Harness resolves only unambiguous integration conflicts automatically. A conflict requiring design or product judgment pauses the run with evidence.

## 11. Run states, retries, and repair loops

### Run states

| State | Meaning |
|---|---|
| `queued` | Claimed and waiting for declared Linear dependencies, run capacity, auth capacity, or Railway capacity |
| `running` | Leased to the scheduler and executing in a sandbox |
| `awaiting_input` | Product or Architecture checkpointed one structured question round and stopped the disposable sandbox |
| `paused` | Execution stopped with a recoverable execution, contract, or infrastructure error |
| `completed` | The full selected workflow completed successfully |
| `cancelled` | An operator explicitly cancelled a non-terminal run |

A completed or cancelled run cannot be downgraded by a late worker event or recovery race.

### Stage states

Stages are registered from the repository pipeline and move through pending, running, waiting for input, completed, or blocked states. Only Product and Architecture can enter waiting for input, and each can do so at most once. Stage details retain the declared order, dependencies, mode, parallelism, and retry information.

### Automatic retry

A cloud stage is attempted up to three times. Between attempts, Agent Harness persists the failure, resets the stage to pending, uploads the journal, and retries from durable state. In a parallel ticket stage, successful sibling commits are integrated and checkpointed first, so later attempts skip them. If an operator repairs the isolated run branch before resuming, the worker can adopt a completed agent result whose commit is already an ancestor of the run branch instead of failing on an empty cherry-pick. A context cancellation, structured QA repair request, or valid Product/Architecture input request does not consume ordinary retries in the same way. A request from any other stage, or a second request round, is rejected immediately as a contract violation rather than retried as a question.

After the final failed attempt, the run pauses.

### QA repair loop

The default pipeline allows QA to re-enter coder, continue through lint, and return to QA up to two times. Repair counts, new tickets, and completed work are persisted. If a run paused after QA produced a valid `requeue` result but before the loop was available, resuming after adding the matching loop consumes that checkpointed repair request without rerunning QA. QA runs normally after the coders finish so the repaired acceptance criteria are verified. Exceeding the configured limit pauses the run instead of looping indefinitely.

## 12. Monitor runs

### List and inspect

```sh
agent-harness cloud runs list
agent-harness cloud runs show <run-id>
```

`list` returns registered runs. `show` returns the run plus its stage DAG, logical tickets, artifact metadata, and external synchronization records.

### Watch live events

```sh
agent-harness cloud runs watch --run <run-id>
```

The event stream remains connected until cancelled and reconnects through the caller's context rather than an ordinary request timeout. To resume after a known event cursor:

```sh
agent-harness cloud runs watch --run <run-id> --after <event-sequence>
```

Omit `--run` to watch all runs.

Events include run claims, queue changes, sandbox lifecycle, pipeline stages, tickets, retries, provider synchronization, pull-request creation, Codex commands, file edits, and usage updates. Command events intentionally omit aggregated command output; the dashboard displays the command, state, duration, and exit status without turning the event feed into a repository-output log.

### Use `$inspect-harness`

Ask Codex for read-only investigation:

```text
Use $inspect-harness to show the current status of cloud run <run-id>.
```

```text
Use $inspect-harness to list the tickets associated with Linear issue ENG-123.
```

```text
Use $inspect-harness to diagnose why run <run-id> paused. Do not resume it.
```

Inspection reports authoritative journal facts first, then differences in Linear, Notion, Git, or GitHub. It does not reclaim leases, update comments, retry writes, edit files, or resume execution unless explicitly asked.

### Live previews

When previews are configured (a repository `preview` block plus a deployed preview edge), a completed run keeps its sandbox alive and serves the application behind a capability link. The link appears on the Run Detail page and is upserted into the Linear parent issue next to the draft pull request. Opening the link exchanges the one-time `?cap=` token for an HTTP-only cookie and shows the application with an overlay badge identifying the run and pull request; the badge expands into a panel reserved for future interactive editing.

Preview access uses a sliding one-hour inactivity window (each authorized request extends it) with an absolute cap, after which the forward is stopped and the sandbox is destroyed. The control plane restores live previews after a restart.

## 13. Use the localhost dashboard

Start the dashboard with the current cloud profile:

```sh
agent-harness ui
```

Use a named profile or alternate loopback address when needed:

```sh
agent-harness ui --profile staging --address 127.0.0.1:7474
```

The UI refuses a public bind. Its local backend reads and refreshes the current device session, attaches the short-lived access token to proxied REST and SSE calls, and serves the browser on loopback. Browser JavaScript never receives or stores access or refresh tokens. The Team view provides invitation, member, role, device-session, and authentication-audit management for administrators.

### Pipeline runs

The dashboard separates active runs from completed and cancelled history. Each run card shows:

- Source issue and title.
- Run state.
- Current stage or terminal label.
- Duration.
- Total input and output tokens.
- Last update time.

### Run detail

Select a run to view:

- Run ID and source issue link.
- Current state and stage.
- Sandbox ID and queue reason.
- Integration branch and draft pull request.
- Terminal error when present.
- Pipeline DAG, execution order, dependencies, modes, and parallelism.
- Logical ticket state, dependencies, Linear link, and commit SHA.
- Local journal artifacts and published Notion artifacts.
- Open or answered input requests, channel deliveries, and accepted responses.

### Usage telemetry

Each run reports:

- Execution duration.
- Explicit Codex model.
- Model-call count.
- Input tokens.
- Cached-input tokens.
- Output tokens.
- Reasoning-output tokens.
- Estimated API-equivalent cost.

The estimate uses the checked-in model pricing table. Codex authenticated through a ChatGPT plan may be billed through that plan rather than as direct API-token charges, so this number is a comparison estimate rather than a billing statement.

### Live activity

The activity feed can show all runs or the selected run. Command and file-edit activities use distinct cards with action state, duration, command or repository-relative paths, event type, and timestamp. The dashboard keeps the most recent activity readable and refreshes run details as events arrive.

The Inbox button at the top of the dashboard shows the current response count. Its list opens the relevant Run Detail, where operators can choose an option, see which choice the agent recommends, or provide an alternate free-text answer. Submitting the complete response automatically queues the checkpointed run. The rest of the **Runs** view is read-only; use the CLI for legacy paused-run input, manual resume, cancel, reconcile, export, or configuration. The **Team** view lets owners and administrators create invitations, change non-owner roles, and revoke members, invitations, or device sessions.

## 14. Clarify, resume, cancel, reconcile, and export

### Answer a Product or Architecture question

Open the dashboard Inbox and select the request, respond in the Linear AgentSession chat, or reply directly beneath the matching Agent Harness question comment in Linear. The complete question and its choices appear in both Linear surfaces. UI answers require one response per question and accept either a listed choice or the free-text alternative. A Linear chat or thread reply is treated as one complete free-text response bundle. The first accepted response wins across all channels, is recorded with its actor and channel, moves the Linear issue from Needs Input to In Progress, and automatically queues a fresh sandbox that restores the journal. Answers accepted through the web UI or another non-Linear channel such as Slack are also written to one marker-backed Linear comment. Linear-origin answers are not copied because the original prompt or reply is already visible in Linear.

Product and Architecture are instructed to inspect the issue and repository first, bundle all material decisions into one round, recommend an option, and avoid asking when a safe reversible assumption is available. Coder, lint, documentation, QA, pull-request, and custom downstream stages cannot wait for a user and must work from their supplied context until a terminal result.

### Update a paused run's input

The legacy paused-run input endpoint remains for operator-directed recovery from an execution failure. To replace its feature request, write the revised request to a Markdown file and submit it:

```sh
agent-harness cloud runs input <run-id> \
  --file clarified-request.md
```

Use `--file -` for standard input. Input can be replaced only while the run is paused. Updating it does not resume the run automatically.

### Resume

Inspect the pause reason and any external-sync failures first. Then:

```sh
agent-harness cloud runs resume <run-id>
```

The run returns to the queue. A new sandbox restores the journal and durable branch, skips completed work, and starts at the first incomplete stage.

### Cancel

```sh
agent-harness cloud runs cancel <run-id>
```

Cancellation is explicit and terminal. The scheduler destroys the sandbox and may quarantine leased authentication slots if the worker could not safely return them. A completed or already cancelled run is not modified.

### Reconcile provider projections

```sh
agent-harness cloud runs reconcile <run-id>
```

Reconciliation re-applies Linear child and parent workflow states and stage activity from durable ticket/run truth. It also restores known Notion hub and artifact pages that were archived. It does not rerun agent stages or create a new source claim.

### Export the run journal

```sh
agent-harness cloud runs export <run-id> \
  --repo /path/to/repository
```

The journal is extracted into `.harness/runs/<run-id>`. Export refuses to overwrite an existing run directory and rejects unsafe archive paths, links, and unsupported entries.

## 15. Artifacts and external synchronization

### Local run directory

The authoritative local journal is under:

```text
.harness/runs/<run-id>/
  state.json
  inputs/
  agent-output/
  artifacts/
  logs/
```

`state.json` records selected stages, stage state, ticket state, external synchronization intent and identities, Git delivery data, repair counts, and events. State writes are atomic.

### Notion hierarchy

For each source issue, Agent Harness upserts one issue hub beneath the configured parent. Run artifacts are children of that hub. Stable markers distinguish the hub and each run-specific artifact.

Typical pages include:

- PRD.
- ADR.
- Optional custom documents emitted by additional stages.

Replaying a run updates the matching page. Distinct explicit local runs receive distinct run-specific artifact pages.

### Linear projections

Agent Harness uses stable hidden markers for:

- Parent run comment.
- Child issue body.
- Child progress comment.
- Terminal summary.
- Human-input question thread.

The provider IDs and URLs returned by successful writes are checkpointed immediately. If a write fails, the pending synchronization remains recorded so recovery can search remote state before retrying.

### Cloud journal artifact

The sandbox periodically compresses and uploads the local run directory as `journal/run.tar.gz`. The control plane stores its SHA-256 hash, media type, size, and body. The maximum accepted journal size is 100 MiB by default.

Never delete a failed journal merely to make the run appear clean. Inspect it, repair the underlying problem, and resume from the durable checkpoint.

## 16. Customize repository guidance

Agent Harness installs a concise repository map at root and detailed guidance under `.harness`.

### `AGENTS.md`

The root guide gives every agent the project purpose, repository map, sources of truth, essential commands, non-negotiable rules, change workflow, definition of done, and escalation conditions.

### `ARCHITECTURE.md`

Documents system context, components, dependency rules, critical flows, external interfaces, invariants, constraints, and the `.harness/adrs/` decision-record directory.

### `DESIGN.md`

Documents product and UI design only: users, journeys, information architecture, interaction states, visual system, responsive behavior, accessibility, and design validation. Backend and deployment architecture belong elsewhere.

### `TESTING.md`

Defines test layers, commands, change-type requirements, data and dependencies, flake policy, CI gates, required evidence, and cloud browser-worker limits.

### `SECURITY.md`

Defines data classification, trust boundaries, authentication, authorization, secret handling, input/output controls, dependency risks, verification, and escalation.

### `DEPLOY.md`

Defines environments, build artifact, configuration, preconditions, deployment, state changes, post-deployment checks, rollback, recovery, and deployment authority.

Keep these documents current. Agents are expected to treat them as project truth, not generic suggestions.

## 17. Customize agent roles

Role definitions live under `.agents` and are referenced by pipeline stages. Each contract defines:

- Mission.
- Inputs.
- Work method.
- Boundaries.
- Exact result schema.

The standard roles are product, architect, coder, lint, QA, documentation, and pull request.

Safe customization may add repository-specific expectations, but should not transfer deterministic responsibilities to the agent. In particular:

- Product must return stable requirements, acceptance criteria, and a valid ticket graph.
- Architect may constrain known tickets but may not silently invent implementation work outside the plan.
- Coder owns one ticket, follows TDD, commits locally, and does not push.
- Lint must report every configured deterministic gate.
- QA must report every acceptance criterion and emit structured repair tickets when necessary.
- PR prepares delivery content; the orchestrator owns credentials, push, and canonical PR creation.
- Documentation is optional and must be explicitly added to the pipeline.

Agent JSON is validated before outputs are materialized or a stage can complete.

## 18. Customize `.harness/pipeline.yaml`

The pipeline is a declarative DAG. A stage declares:

```yaml
- id: qa
  agent: .agents/qa.md
  needs: [lint]
  mode: single
  parallelism: 1
  inputs:
    - id: prd
      file: artifacts/prd.md
      format: markdown
      required: true
  outputs:
    - id: qa_evidence
      file: artifacts/qa-evidence.json
      format: json
      from_result: acceptance_results
  result:
    file: agent-output/qa.json
    format: json
    agent: qa
  hooks:
    before: []
    after: []
    on_failure: []
```

### Stage modes

- `single` runs one agent invocation.
- `ticket_parallel` materializes one assignment and isolated worktree per ticket, then invokes one Codex coordinator per dependency-ready wave. The coordinator uses native coder subagents up to the declared parallelism.

Parallelism must be between 1 and 16. Dependencies must reference known stages and form a valid DAG.

### File contracts

Every input, output, and result path is relative to `run_root`. Required inputs must exist before the stage starts. Outputs are extracted from validated JSON with `from_result`.

`$` means the complete JSON result or collection. `{ticket_key}` is replaced only for a materialized ticket invocation. Globs may expand read-only inputs but may not be used as output paths.

### Input sources

The product feature request can declare ordered sources such as:

```yaml
sources: [user_prompt, tracker_title_and_body]
```

The first available declared source is materialized. Issue comments supplement the request but do not replace the declared input file.

### Repair loops

```yaml
repair_loops:
  - from: qa
    to: coder
    through: qa
    max_reentries: 2
```

A repair loop must start from a later stage, re-enter an earlier stage, continue through a valid stage boundary, and use a positive bounded re-entry count.

### Validate every change

After editing the workflow:

```sh
python3 <plugin>/scripts/harnessctl.py \
  validate-pipeline .harness/pipeline.yaml --repo .
```

Validation rejects duplicate stage IDs, missing agent files, unknown dependencies, cycles, unsafe paths, invalid modes or parallelism, malformed file contracts, unsafe hooks, and invalid repair loops.

## 19. Hooks and architecture lint

### Deterministic hooks

A hook uses an argument array, repository-relative working directory, and timeout:

```yaml
- id: schema-check
  argv: ["./scripts/check-schema", "--strict"]
  cwd: .
  timeout_seconds: 300
```

Hooks do not use shell interpolation. They receive only:

- `HARNESS_RUN_ID`
- `HARNESS_ISSUE_KEY`
- `HARNESS_STAGE`
- `HARNESS_ARTIFACT_DIR`
- `HARNESS_WORKTREE`

The helper preserves only essential process variables such as `PATH` and temporary-directory settings. Hook results are stored under `logs/hooks`. A timeout or non-zero exit blocks the stage.

### Architecture-lint rules

The deterministic architecture linter supports:

| Rule | Purpose |
|---|---|
| `require_path` | Require a file or directory |
| `forbid_path` | Reject matching files or directories |
| `require_text` | Require a regular-expression match in one file |
| `forbid_text` | Reject a regular-expression match across globs |
| `max_file_lines` | Enforce a line-count maximum across globs |

The standard baseline:

- Requires `.harness/ARCHITECTURE.md`.
- Limits ordinary source files to 800 lines while excluding generated, dependency, build, coverage, and migration trees.
- Rejects committed `.env*` files except example, sample, and template variants.

Add repository-specific rules only when they encode an accepted architectural decision that can be checked without model judgment.

Run it directly:

```sh
python3 .harness/scripts/arch-lint.py
python3 .harness/scripts/arch-lint.py --json
```

Exit code `0` means pass, `1` means rule violations, and `2` means invalid configuration or an execution error.

## 20. Security model

### Credential storage

GitHub, Linear, Notion, and Codex credentials are encrypted in Postgres using AES-256-GCM with purpose-specific associated data. The control plane encryption key is supplied separately through `HARNESS_CREDENTIAL_KEY`.

The repository contains only non-secret identifiers. Codex subprocesses do not receive the Railway API token, bootstrap token, encryption key, Linear credential, Notion credential, or team device credentials.

### GitHub tokens

The control plane stores the GitHub App identity. For a worker, it mints a repository-scoped installation token just in time. The token expires and is passed only to controlled Git and GitHub CLI subprocesses.

### Worker capabilities

Each sandbox receives a signed, expiring capability scoped to one run. The capability authorizes only that run's internal event, journal, heartbeat, auth-return, GitHub-token, and synchronization operations.

### Bootstrap and installation ownership

`HARNESS_MANAGEMENT_TOKEN` is a one-time installation bootstrap bearer, not a shared operator credential. `agent-harness railway init` stores it in the initial local profile. After the first successful deployment, `cloud team initialize` sends it only to `/auth/v1/initialize`, atomically creates the owner and first device session, and replaces the local bootstrap credential with rotating device credentials.

The bootstrap bearer is never accepted by ordinary `/v1/*` management or event endpoints. Initialization can succeed only once. The resulting owner has administrator capabilities and is also the permanent installation anchor: the owner cannot be demoted or revoked, invitations cannot grant the owner role, and this release has no ownership-transfer or owner-promotion command.

### Roles and authorization

Every `/v1/*` request is authenticated as an active member and device session, then checked against the route's minimum role.

| Role | Capabilities |
|---|---|
| `viewer` | Read control-plane status, identity, runs, run artifacts, and the replayable event stream; log out the current device |
| `operator` | Viewer capabilities plus submit clarified run input, resume, cancel, and reconcile runs, and create or archive disposable Linear issues |
| `admin` | Operator capabilities plus manage repositories, provider credentials, Codex authentication slots, invitations, non-owner roles, members, device sessions, and authentication audit history |
| `owner` | Administrator capabilities plus the immutable installation-owner identity |

Authorization failures return `403` and are appended to the authentication audit. Invalid, expired, logged-out, or revoked sessions return `401`.

### Invitations and joining

An owner or administrator creates a one-time invitation for a viewer, operator, or administrator:

```sh
agent-harness cloud team invite \
  --role operator \
  --label "Teammate" \
  --expires 1h
```

The default lifetime is one hour. The server accepts lifetimes from one minute through seven days. The label is administrative context, not an identity check. The invitation secret is returned only in the newly created join URL; invitation listings never return it.

Send the complete link through a secure channel. The secret is stored in the URL fragment, so opening `/join` does not send it to the control plane or in an HTTP referrer. The landing page is inert except for rendering a local CLI command and uses a restrictive content-security policy.

The teammate can paste the complete link into Codex and ask it to join, or run on their own device:

```sh
agent-harness cloud join 'https://<control-plane>/join#invite=...' \
  --name "Teammate" \
  --device "Work laptop"
agent-harness cloud whoami
```

`cloud join` accepts HTTPS links, with HTTP permitted only for `localhost` or `127.0.0.1`. It extracts the fragment locally, redeems the invitation once, and saves a new named member and device session. Used, expired, invalid, or revoked invitations return `410 Gone` and cannot be replayed.

### Device sessions and token rotation

Each successful initialization or invitation redemption creates a device session with a 15-minute access token and a 30-day refresh token. The CLI refreshes within one minute of access-token expiry and retries once after an authenticated request receives `401`. Every refresh rotates both tokens and persists the new pair before continuing. Reuse of the immediately previous refresh token is treated as replay and revokes the whole device session.

The local profile stores the control-plane URL separately from credentials. On macOS, credentials are stored in Keychain when available. The fallback credential file and profile metadata use mode `0600`. `AGENT_HARNESS_URL` plus `AGENT_HARNESS_TOKEN` provide a non-refreshing automation override and both variables must be present.

`agent-harness cloud logout` revokes the current device session and removes its locally stored credential. An administrator can revoke one device without affecting another member, or revoke a non-owner member to revoke all sessions associated with that member. Revoking a pending invitation prevents redemption without rotating existing sessions.

Review the current state with:

```sh
agent-harness cloud whoami
agent-harness cloud team members
agent-harness cloud team sessions
agent-harness cloud team audit
agent-harness cloud team revoke member <member-id>
agent-harness cloud team revoke session <session-id>
agent-harness cloud team revoke invite <invitation-id>
```

The audit includes initialization, invitation creation and redemption, token refresh and detected replay, logout and administrative revocation, role changes, and denied authorization attempts.

### Dashboard authentication and administration

The dashboard binds only to loopback. Its local backend reads and refreshes the selected profile, attaches access tokens to proxied REST and SSE calls, and never sends access or refresh tokens to browser JavaScript. The **Runs** view is read-only. The **Team** view lets owners and administrators create and revoke invitations, change non-owner roles, revoke members or device sessions, and inspect authentication history.

### Non-member endpoints

The control plane exposes only these routes without a member access token:

| Endpoint | Protection and purpose |
|---|---|
| `GET /healthz` | Unauthenticated process liveness |
| `GET /readyz` | Unauthenticated readiness, including Postgres reachability |
| `POST /webhooks/linear` | Linear signature and recent timestamp; duplicate deliveries are persisted and deduplicated |
| `POST /webhooks/github` | GitHub App HMAC signature; merged managed pull requests move their source Linear issues to Done |
| `GET /join` | Inert, no-referrer landing page; the invitation secret stays in the URL fragment |
| `POST /auth/v1/initialize` | One-time bootstrap bearer; creates the owner and first device session |
| `POST /auth/v1/invitations/redeem` | Single-use invitation secret; creates a member and device session |
| `POST /auth/v1/token` | Active refresh token; rotates the device credential pair |

All `/v1/*` routes require a role-scoped member access token. All `/internal/v1/*` worker routes require a separate, expiring run capability.

### Event redaction

Worker event messages and JSON payloads are redacted for known control-plane secrets and sensitive fields before persistence. Command activity excludes aggregated command output. Do not treat redaction as permission to print arbitrary credentials; agents and hooks must avoid secrets in logs and results.

### Delivery boundaries

Agent Harness may create provider children and comments, change Linear workflow states in cloud mode, publish Notion pages, commit code, push a scoped branch, and create a draft pull request. It does not merge PRs, deploy target applications, approve destructive migrations, or broaden provider permissions automatically.

## 21. Reliability and recovery

### Claims and leases

One source Linear issue has one permanent cloud claim and one resumable run ID. The scheduler uses a renewable lease to ensure only one control-plane executor owns the run. Codex authentication slots have separate exclusive leases.

The default maximum is three active source runs. When that limit is reached, additional runs remain queued with `concurrency_limit` as the visible reason.

### Heartbeats

The worker renews its control-plane lease while it runs. The scheduler also checks sandbox health. If a sandbox disappears, the run is requeued with `sandbox_lost`, its uncertain auth slot is quarantined, and a future sandbox restores the journal and branch.

### Recovery authority

Recovery uses:

1. Control-plane run, event, stage, ticket, and synchronization records.
2. The compressed run journal.
3. The last pushed integration branch.
4. Stable provider markers and durable external IDs.

It does not infer success from an agent message alone.

### Safe recovery sequence

1. Inspect the run and pause reason.
2. Read the latest journal and last successful event.
3. Check the lease, sandbox, branch, commits, and PR.
4. Check pending Linear and Notion synchronizations.
5. Supply clarified input if needed.
6. Reconcile external projections when durable state is already correct.
7. Resume from the first incomplete stage.

Do not manually delete state, fabricate completed stages, reuse a fresh lease, or create a second cloud run for the same source issue.

## 22. CLI reference

This section covers the operator-facing command surface. The public REST contract is checked in as `cloud-runner/openapi.yaml`; Section 24 explains its authentication, authorization, and event boundaries, and the website publishes that material on a dedicated API Reference route.

### Global commands

```text
agent-harness version
agent-harness help
agent-harness ui [--address 127.0.0.1:7373] [--profile NAME]
```

`server` and `worker` are service entrypoints used by the Railway deployment and sandbox scheduler, not normal interactive commands.

### Cloud profile

```text
agent-harness cloud profile list
agent-harness cloud profile copy --from NAME --to NAME
agent-harness cloud profile set --url URL --token TOKEN [--name NAME]
agent-harness cloud --profile NAME <team|repo|runs|auth|whoami|logout> ...
```

`profile list` shows profile names, URLs, the global current profile, and the profile selected for the current repository; it never prints credentials. `profile copy` creates a local alias without exposing the stored session. `profile set` establishes a named URL and bearer credential. During first installation that credential is the bootstrap token, usable only by `cloud team initialize`; initialization replaces it with the owner's rotating device session. `cloud join` creates or replaces the selected profile with the invited member's rotating device session. `AGENT_HARNESS_PROFILE` overrides repository selection. `AGENT_HARNESS_URL` and `AGENT_HARNESS_TOKEN` together remain the highest-precedence non-refreshing automation override.

### Team access

```text
agent-harness cloud team initialize [--name NAME] [--device DEVICE] [--profile NAME]
agent-harness cloud team invite [--role operator] [--label LABEL] [--expires 1h]
agent-harness cloud team members
agent-harness cloud team sessions
agent-harness cloud team audit
agent-harness cloud team revoke <member|session|invite> ID
agent-harness cloud join INVITE_LINK [--name NAME] [--device DEVICE] [--profile NAME]
agent-harness cloud whoami
agent-harness cloud logout [--profile NAME]
```

`team initialize` is a one-time bootstrap exchange. `team invite` accepts `viewer`, `operator`, or `admin`; its expiry defaults to one hour and the server limits it to seven days. `join` requires HTTPS except for localhost development. Team listing, invitation, role, session, and audit operations require `admin` or `owner`. `whoami` and `logout` are available to every member role.

### Authentication

```text
agent-harness cloud auth status
agent-harness cloud auth codex add [--slots 3]
agent-harness cloud auth github --manifest-owner OWNER [--name NAME]
agent-harness cloud auth github upgrade-webhook
GITHUB_WEBHOOK_SECRET=... agent-harness cloud auth github --app-id ID --private-key-file FILE
agent-harness cloud auth linear manifest --url HTTPS_URL
agent-harness cloud auth linear --client-id ID --client-secret SECRET --webhook-secret SECRET
agent-harness cloud auth notion
```

Direct GitHub credential import requires `GITHUB_WEBHOOK_SECRET` and an existing GitHub App webhook configured for `<control-plane>/webhooks/github` with pull-request events. The direct Linear token-import path also accepts `LINEAR_ACCESS_TOKEN`, `LINEAR_REFRESH_TOKEN`, `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET`, `LINEAR_EXPIRES_AT`, and `LINEAR_WEBHOOK_SECRET`. Prefer the guided manifest and app-actor OAuth flows for new installations.

`cloud auth notion` reads `NOTION_TOKEN` from the environment. Use a temporary sealed variable and remove it after transfer.

### Repositories

```text
agent-harness cloud repo discover-linear
agent-harness cloud repo list
agent-harness cloud repo add [registration options]
agent-harness cloud repo remove <repository-id>
```

`repo remove` disables automation for the registration. It does not delete run history.

Registration options are:

- `--id`: optional stable repository ID.
- `--name`: display name.
- `--github-owner`: GitHub owner.
- `--github-repo`: GitHub repository.
- `--github-installation`: GitHub App installation ID.
- `--base-branch`: default `main`.
- `--linear-workspace`: Linear workspace ID.
- `--linear-team`: Linear team ID.
- `--linear-project`: optional project ID.
- `--linear-agent`: default `Vessica`; must match the installed Linear app actor.
- `--notion-parent`: Notion parent page ID.

### Linear issue utilities

```text
agent-harness cloud repo issue create \
  --repo REPOSITORY_ID --title TITLE --description-file FILE

agent-harness cloud repo issue archive \
  --repo REPOSITORY_ID --issue ISSUE_ID_OR_KEY --yes
```

### Runs

```text
agent-harness cloud runs list
agent-harness cloud runs show RUN_ID
agent-harness cloud runs watch [--run RUN_ID] [--after EVENT_SEQUENCE]
agent-harness cloud runs input RUN_ID --file FILE
agent-harness cloud runs resume RUN_ID
agent-harness cloud runs cancel RUN_ID
agent-harness cloud runs reconcile RUN_ID
agent-harness cloud runs export RUN_ID [--repo PATH]
```

`list`, `show`, and `watch` are read-only. `input`, `resume`, `cancel`, and `reconcile` mutate durable or external state. `export` writes a local run directory but does not mutate the cloud run.

### Railway

```text
agent-harness railway upgrade \
  --project ID --environment NAME --version TAG [--checkpoint NAME]

agent-harness railway init \
  --project ID --environment NAME --service NAME --url HTTPS_URL \
  [--checkpoint NAME] [--postgres-service NAME] [--profile NAME]

agent-harness railway deploy \
  --project ID --environment NAME --service NAME [--path DIRECTORY]

agent-harness railway status [Railway CLI arguments]
agent-harness railway logs [Railway CLI arguments]
```

`upgrade` builds a worker template from the published GitHub release, creates a temporary source sandbox, captures the versioned checkpoint, and destroys the temporary sandbox. `init` verifies Sandbox access, generates secrets, configures the service, and creates the local profile. `deploy` runs a detached Railway deployment. `status` and `logs` pass through to the Railway CLI with Agent Harness caller metadata.

The repository-level maintainer targets are stricter than the generic
`agent-harness railway deploy` command: they deploy the immutable tagged GHCR
image, update the matching worker checkpoint, wait for terminal Railway status,
and verify the public health endpoints before reporting success.

### Deterministic helper commands

The Codex plugin and cloud worker use `harnessctl.py` for validation and state transitions. Advanced operators may use it for diagnosis and supported recovery:

```text
validate-config
validate-pipeline
resolve-stages
waves
validate-agent-output
materialize-source
materialize-generated-inputs
materialize-result
init-run
checkpoint
set-stage
render-comment
list-runs
markers
run-hook
release-lease
```

Prefer `$inspect-harness` and `$run-harness` over manually editing `state.json` or assembling helper calls.

### Bootstrap helper

Run the Git and GitHub preflight without changing the target repository:

```text
python3 <plugin>/scripts/bootstrap.py preflight \
  --target REPOSITORY [--remote origin] [--base-branch main]
```

Preview an installation:

```text
python3 <plugin>/scripts/bootstrap.py bootstrap \
  --target REPOSITORY \
  --provider linear \
  --workspace WORKSPACE_ID \
  --project TEAM_OR_PROJECT_ID \
  --notion-parent-page-id PAGE_ID \
  [--remote origin] [--base-branch main] [--linear-agent Vessica] \
  [--cloud-profile REPOSITORY_PROFILE]
```

Add `--apply` only after reviewing the preview. Add `--force` only when replacement of every reported conflict has been explicitly approved.

### `harnessctl.py` syntax

| Command | Key arguments | Purpose |
|---|---|---|
| `validate-config` | `PATH` | Validate `.harness/config.yaml` |
| `validate-pipeline` | `PATH [--repo REPOSITORY]` | Validate the pipeline, referenced agents, DAG, contracts, hooks, and loops |
| `resolve-stages` | `--pipeline PATH --stages LIST [--completed LIST]` | Normalize `full` or exact named stages and verify prerequisites |
| `waves` | `--input TICKET_PLAN_JSON` | Validate the ticket graph and return dependency waves |
| `validate-agent-output` | `--agent ROLE --input JSON [--tickets PRODUCT_JSON]` | Validate an exact role result and cross-check architect ticket references |
| `materialize-source` | `--pipeline PATH --run-dir DIR --stage ID --input-id ID --source NAME --content-file FILE` | Write a declared source into its run input path |
| `materialize-generated-inputs` | `--pipeline PATH --run-dir DIR --stage ID` | Create per-ticket inputs from a declared collection |
| `materialize-result` | `--pipeline PATH --run-dir DIR --stage ID --input JSON [--ticket-key KEY]` | Validate and extract declared outputs from an agent result |
| `init-run` | `--repo DIR --provider linear --issue-key KEY --stages LIST [--new-run] [--run-id ID] [--reclaim-lease] [--lease-seconds N]` | Resume or initialize a journal and acquire its local lease |
| `checkpoint` | `--run-dir DIR --patch-json OBJECT [--event TYPE] [--event-details-json OBJECT]` | Atomically update journal state and append an event |
| `set-stage` | `--run-dir DIR --stage ID --status STATUS [--details-json OBJECT]` | Record a stage transition |
| `render-comment` | `--run-dir DIR --kind parent\|ticket\|summary [--ticket-key KEY]` | Render a canonical provider comment from journal state |
| `list-runs` | `--repo DIR [--issue-key KEY]` | List local journals, optionally filtered by source issue |
| `markers` | `--run-id ID --provider linear --issue-key KEY [--ticket-key KEY] [--artifact NAME]` | Generate stable idempotency markers |
| `run-hook` | `--repo DIR --spec-json OBJECT --env-json OBJECT` | Execute one validated hook in the fixed environment |
| `release-lease` | `--repo DIR --issue-key KEY --session-token TOKEN` | Release the matching local issue lease |

The helper also recognizes agent-result contracts for the optional `docs` role and the coming-soon Jira provider, but the supported user workflow in this release is Linear.

## 23. Configuration reference

### Control-plane variables

| Variable | Required | Purpose |
|---|---:|---|
| `DATABASE_URL` | Yes | Railway Postgres connection |
| `HARNESS_MANAGEMENT_TOKEN` | Yes | One-time bootstrap bearer for first-owner initialization; never a shared `/v1/*` credential |
| `HARNESS_CREDENTIAL_KEY` | Yes | AES-256-GCM key for encrypted credentials |
| `HARNESS_PUBLIC_URL` | With scheduler | Public HTTPS control-plane origin |
| `HARNESS_RAILWAY_PROJECT` | With scheduler | Sandbox project ID |
| `HARNESS_RAILWAY_ENVIRONMENT` | With scheduler | Sandbox environment ID or name |
| `HARNESS_SANDBOX_CHECKPOINT` | With scheduler | Versioned worker checkpoint |
| `RAILWAY_API_TOKEN` | With scheduler | Workspace-scoped Sandbox-management token |
| `HARNESS_MAX_ACTIVE_RUNS` | No | Maximum concurrent source runs; default `3` |
| `HARNESS_CODEX_MODEL` | No | Explicit worker model |
| `HARNESS_PLAYWRIGHT_WORKERS` | No | Browser-test workers per sandbox; default `2` |
| `HARNESS_SCHEDULER_ENABLED` | No | Set `false` for local control-plane verification |
| `HARNESS_LISTEN_ADDRESS` | No | Server bind address; defaults from `PORT` |
| `PORT` | No | Default server port; default `8080` |
| `HARNESS_MAX_REQUEST_BYTES` | No | General request-body limit; default 4 MiB |
| `HARNESS_MAX_JOURNAL_BYTES` | No | Journal-upload limit; default 100 MiB |
| `HARNESS_RAILWAY_BINARY` | No | Railway executable path |
| `HARNESS_PREVIEW_PUBLIC_URL` | For previews | Public HTTPS origin of the preview edge; empty disables previews |
| `HARNESS_PREVIEW_EDGE_TOKEN` | For previews | Shared secret proving requests came through the preview edge |
| `HARNESS_PREVIEW_TTL` | No | Sliding preview inactivity window; default `1h` |
| `HARNESS_PREVIEW_MAX_AGE` | No | Absolute preview lifetime cap; default `4h` |
| `HARNESS_WORKER_PATH` | No | Worker executable path inside a sandbox |

`agent-harness railway init` configures the ordinary production variables. Provider credentials are transferred separately and stored encrypted in Postgres.

### Temporary provider variables

| Variable | Use |
|---|---|
| `LINEAR_CLIENT_ID` | Linear OAuth application ID |
| `LINEAR_CLIENT_SECRET` | Linear OAuth application secret |
| `LINEAR_WEBHOOK_SECRET` | Linear webhook signing secret |
| `LINEAR_ACCESS_TOKEN` | Optional direct OAuth token import |
| `LINEAR_REFRESH_TOKEN` | Optional direct rotating-token import |
| `LINEAR_EXPIRES_AT` | Token expiry as seconds from now or RFC3339 |
| `NOTION_TOKEN` | Notion internal-connection token |

Remove temporary Railway variables after the encrypted credential is verified.

### Preview-edge variables

The preview edge is the same binary running with `HARNESS_SERVICE_ROLE=preview-edge` (or the `preview-edge` subcommand) as a second public Railway service. `agent-harness railway init --preview-url <https-origin>` provisions it.

| Variable | Purpose |
|---|---|
| `HARNESS_SERVICE_ROLE` | Set to `preview-edge` to run the edge role |
| `HARNESS_PREVIEW_UPSTREAM` | Private control-plane HTTP origin (`*.railway.internal`) |
| `HARNESS_PREVIEW_EDGE_TOKEN` | Shared secret injected into forwarded requests |

### Local profile overrides

| Variable | Purpose |
|---|---|
| `AGENT_HARNESS_URL` | Override stored control-plane URL |
| `AGENT_HARNESS_TOKEN` | Override the stored bearer with a non-refreshing access token |

Both must be present for the override to take effect.

### Worker variables

The scheduler injects run IDs, issue IDs, repository coordinates, feature input, encrypted-session material, model configuration, Playwright limits, and a run capability into each sandbox. Operators should not manually construct these variables; their values and capability scope are part of the scheduler-worker protocol.

## 24. API and events

Use the CLI for ordinary operation. `cloud-runner/openapi.yaml` is the machine-readable public contract; the implemented server routes and authorization guard remain the runtime authority.

### Authentication endpoints

| Method and path | Authentication | Result |
|---|---|---|
| `POST /auth/v1/initialize` | One-time bootstrap bearer | Atomically create the owner and first device session; returns `409` after initialization |
| `POST /auth/v1/invitations/redeem` | Unused invitation secret in the JSON body | Consume the invitation and create a member plus device session; invalid lifecycle state returns `410` |
| `POST /auth/v1/token` | Active refresh token in the JSON body | Rotate the access and refresh tokens; detected replay returns `401` and revokes the session |
| `GET /v1/whoami` | Viewer or higher | Return the authenticated member, role, and redacted device session |
| `POST /v1/logout` | Viewer or higher | Revoke the current device session |

### Team-administration endpoints

The following routes require `admin` or `owner`:

| Method and path | Purpose |
|---|---|
| `GET /v1/team/members` | List active and revoked members |
| `PATCH /v1/team/members/{member_id}` | Change a non-owner role to viewer, operator, or administrator |
| `DELETE /v1/team/members/{member_id}` | Revoke a non-owner member and all associated sessions |
| `GET /v1/team/invitations` | List invitation history without secrets |
| `POST /v1/team/invitations` | Return a one-time invitation URL exactly once |
| `DELETE /v1/team/invitations/{invitation_id}` | Revoke an invitation |
| `GET /v1/team/sessions` | List redacted device sessions; optionally filter by `member_id` |
| `DELETE /v1/team/sessions/{session_id}` | Revoke one device session |
| `GET /v1/team/audit` | Return authentication and authorization audit events |

### Management role boundaries

- Viewer: `GET /v1/status`, `GET /v1/runs`, `GET /v1/runs/*`, `GET /v1/events`, `GET /v1/whoami`, and `POST /v1/logout`.
- Operator: every viewer route plus non-GET `/v1/runs/*` actions and Linear issue create/archive utilities.
- Administrator and owner: every management route, including repositories, provider connection records, Codex slots, and team administration.

All `/v1/*` calls require a short-lived member bearer. Missing or invalid credentials return `401`; an insufficient role returns `403` and creates an audit event. `/internal/v1/*` worker endpoints use a different signed, expiring capability scoped to one run.

### Events and run detail

`GET /v1/events` is a replayable Server-Sent Events stream. Use the `after` query parameter or `Last-Event-ID`; optionally filter by `run_id`. Event objects use protocol `agent-harness.events/v1` and include global and run-local sequence numbers. Run detail includes the run, stage DAG, tickets, artifacts, and external synchronization records.

The CLI automatically refreshes expiring device access tokens and reconnects the event stream from the last observed event ID. Browser clients should use the localhost dashboard proxy so device credentials never enter browser JavaScript.

## 25. Self-hosting and operations

### Local control-plane verification

```sh
cd cloud-runner
docker compose up -d postgres

export DATABASE_URL='postgres://harness:harness@127.0.0.1:55432/agent_harness?sslmode=disable'
export HARNESS_MANAGEMENT_TOKEN='<random-bearer-token>'
export HARNESS_CREDENTIAL_KEY='<base64-encoded-32-byte-key>'
export HARNESS_SCHEDULER_ENABLED=false

go run ./cmd/agent-harness server
```

Configure a local profile in another terminal:

```sh
agent-harness cloud profile set \
  --name local-development \
  --url http://127.0.0.1:8080 \
  --token "$HARNESS_MANAGEMENT_TOKEN"

agent-harness cloud team initialize --name "Local owner" --device "Development machine"
agent-harness ui
```

### Production topology

Run exactly one control-plane replica. Postgres owns scheduling claims, but the supported Railway configuration intentionally uses a single replica and an `ON_FAILURE` restart policy. The readiness endpoint checks database reachability and is used as the Railway health check.

### Capacity planning

Plan four related capacities separately:

1. `HARNESS_MAX_ACTIVE_RUNS`: simultaneous source-issue pipelines and Railway sandboxes.
2. Codex authentication slots: independent top-level run capacity; one slot is leased per active source issue.
3. Pipeline `parallelism`: simultaneous native coder subagents inside one run's coordinator.
4. `HARNESS_PLAYWRIGHT_WORKERS`: browser workers inside each sandbox.

Railway Sandbox quota is an additional external limit. Runs wait with a visible queue reason rather than silently disappearing.

### Backups and retention

Back up Railway Postgres according to your operational requirements. It contains run metadata, credentials encrypted under the separately managed key, events, synchronization identities, usage, and compressed journals. Integration branches and GitHub pull requests provide an additional durable record of code delivery but do not replace database and journal backups.

## 26. Troubleshooting

### Codex does not recognize the skills

**Likely cause:** The plugin was installed after the current Codex task started.

**Resolution:** Verify the marketplace/plugin installation, restart Codex, and open a new task.

### Setup reports file conflicts

**Likely cause:** The repository already contains a path Agent Harness would install.

**Resolution:** Review the exact preview. Preserve the existing file unless replacement is intentional. Approve only specific conflicts before using the forced apply path.

### Configuration or pipeline validation fails

Run:

```sh
python3 <plugin>/scripts/harnessctl.py validate-config .harness/config.yaml
python3 <plugin>/scripts/harnessctl.py validate-pipeline .harness/pipeline.yaml --repo .
```

Check provider values, missing agent files, duplicate stages, unknown dependencies, cycles, unsafe paths, invalid hooks, and repair-loop direction.

### Cloud profile is not configured

Run:

```sh
agent-harness cloud profile set \
  --name <repository-profile> \
  --url https://<control-plane-domain> \
  --token '<bootstrap-or-member-access-token>'
```

Run `agent-harness cloud profile list` from the repository. If `selected` is not the intended profile, correct `.harness/config.yaml` or use `agent-harness cloud --profile NAME ...`. If the token is missing from Keychain or the protected fallback file, set that named profile again.

### A team invitation or device session fails

- `410 Gone` while joining means the invitation is invalid, expired, revoked, or already used. Ask an owner or administrator for a new link; do not try to reuse the old one.
- `401` on an ordinary command means the access token and refresh path could not establish an active session. The device may have been logged out, revoked, expired after 30 days without renewal, or revoked after refresh-token replay. Rejoin with a new invitation.
- `403` means the session is valid but its viewer or operator role does not authorize the action. Check `cloud whoami` and ask an administrator for the minimum necessary role.
- If only one device should lose access, revoke its session. Revoking a member invalidates all sessions associated with that member.

Use `cloud team audit` from an administrator session to inspect logout, administrative revocation, replay detection, and denied authorization; compare the session timestamps when checking expiration. Do not replace `HARNESS_MANAGEMENT_TOKEN`: it is only the initialization bootstrap and cannot restore ordinary member access.

### Railway Sandboxes are unavailable

**Symptom:** `railway init` or `railway upgrade` fails before creating a worker.

**Cause:** Sandboxes or Priority Boarding are not enabled for the target project/workspace.

**Resolution:** Enable the feature, confirm the workspace token can list Sandboxes, and retry the same onboarding checkpoint.

### Health succeeds but readiness fails

`/healthz` confirms that the process is alive. `/readyz` also requires Postgres to respond. Check `DATABASE_URL`, Railway service references, database status, migrations, and control-plane logs.

### Assigning Vessica does not start a run

Verify:

- The repository registration is enabled.
- Workspace, team, and optional project IDs match.
- The issue is a root issue, not a child.
- The issue is not cancelled.
- Vessica is installed as an app actor with the `app:assignable` scope and team access.
- The issue is delegated to Vessica, not merely assigned to a synthetic user or @mentioned.
- The webhook URL and `AgentSessionEvent` resource are enabled.
- The webhook secret stored in the control plane matches Linear.
- `/webhooks/linear` is reachable publicly over HTTPS.

Ignored and duplicate deliveries are recorded with a reason.

Repository registration also verifies that the Linear team has Todo, In Progress, and Done workflow states. It idempotently creates Needs Input and For Review in the Started category when absent; an existing In Review or Review state is reused. The authorizing Linear member must have team workflow-management permission.

### A merged PR does not move Linear to Done

Verify that the GitHub App webhook is active at `<control-plane>/webhooks/github`, is subscribed to pull-request events, and uses the webhook secret stored with the control-plane GitHub App credential. Apps imported directly must provide `GITHUB_WEBHOOK_SECRET`. Upgrade an older App in place with `agent-harness cloud auth github upgrade-webhook`; do not rerun the manifest unless you intentionally want a replacement App and new installations.

### A run remains queued

Inspect `queue_reason` with `cloud runs show` or the dashboard:

- `concurrency_limit`: wait for an active run to finish or raise configured capacity carefully.
- `auth_slot_unavailable`: add or recover independent Codex login slots.
- `railway_quota`: free Sandbox capacity or increase Railway quota.
- `sandbox_lost`: the scheduler is waiting to restore the run in a new sandbox.

### A run pauses

Use `cloud runs show`, `cloud runs watch`, or `$inspect-harness`. Identify:

- Failed stage or hook.
- Last successful checkpoint.
- Missing input or invalid result.
- Provider synchronization failure.
- Git conflict or push failure.
- Test or architecture-lint failure.
- Product, architecture, migration, or secret ambiguity.

Fix the underlying issue, submit clarified input if appropriate, reconcile provider state when necessary, and only then resume.

### Codex auth slots are unavailable or quarantined

An auth slot is exclusively leased while in use. If a sandbox is lost or cancelled before returning updated authentication safely, the slot may be quarantined with an error. Inspect `cloud auth status`; re-add independent sessions rather than copying unknown active refresh material.

### Playwright exhausts sandbox resources

Read `HARNESS_PLAYWRIGHT_WORKERS` in the repository's Playwright configuration or pass it as `--workers`. Do not use the CPU-visible default inside the sandbox. The current cloud default is two.

### Notion publication fails

Verify that:

- The internal connection still exists.
- It has read, insert, and update capabilities.
- The configured parent page is shared with the connection.
- The encrypted Notion credential is present.
- The page was not moved beyond the connection's access.

If known pages were merely archived, run `cloud runs reconcile` to restore them.

### Linear children or states drift

Run:

```sh
agent-harness cloud runs reconcile <run-id>
```

Reconciliation uses durable ticket identities and current team workflow-state IDs. Do not create replacement children manually unless inspection proves no canonical identity exists.

### Draft PR creation fails

Check:

- GitHub App installation still covers the repository.
- Contents, pull-request, and workflow permissions are unchanged. If a push that adds or edits `.github/workflows/*` is rejected, grant the App **Workflows: Read and write**, approve the installation's updated permissions, and resume from the paused journal.
- Base branch exists.
- Integration worktree is clean.
- Required PR inputs and verification results exist.
- The branch satisfies the Railway sandbox push namespace.

Resume after correcting the provider or repository problem; do not manually mark the PR stage complete.

### Journal export fails

Export refuses to overwrite `.harness/runs/<run-id>`. Inspect and preserve an existing directory, then choose a clean target repository. Unsafe or oversized archives are rejected rather than partially trusted.

## 27. Limits and current boundaries

| Area | Current behavior |
|---|---|
| Release status | Release candidate |
| Supported cloud trigger | Root Linear issue delegated to the Vessica app actor |
| Jira | Coming soon |
| Cloud pipeline selection | Full checked-in pipeline |
| Local pipeline selection | Full or exact named stages |
| Source-issue claims | One permanent cloud run per source issue |
| Default active cloud runs | 3, configurable |
| Default coder parallelism | 3, repository-configurable |
| Maximum YAML stage parallelism | 16 |
| Default Playwright workers | 2, configurable |
| Automatic stage attempts | 3 |
| Default QA repair re-entries | 2, pipeline-configurable |
| Feature/issue input | Non-empty and under 64 KiB |
| Default request limit | 4 MiB |
| Default journal limit | 100 MiB |
| Dashboard | Localhost-only; Runs view is read-only, Team view performs authorized access administration |
| Team ownership | One permanent bootstrap owner; no owner transfer or promotion in this release |
| Invitations | Single-use; one hour by default, configurable from one minute through seven days |
| Device tokens | 15-minute access token and rotating 30-day refresh token |
| Notion | Required for cloud repository registration |
| Pull requests | Draft; never automatically merged |
| Target deployments | Not performed by the default workflow |
| Usage cost | API-equivalent estimate, not a billing statement |
| Management API documentation | OpenAPI contract plus the user-guide and MDX API reference |

## 28. Glossary

**Agent contract:** A Markdown role definition describing an agent's mission, inputs, method, boundaries, and exact output.

**Artifact:** A materialized run output such as a PRD, ADR, QA report, or pull-request record.

**Authentication slot:** An encrypted Codex login session that the scheduler leases to a run.

**Canonical comment:** A Linear comment identified by a stable hidden marker and updated in place.

**Control plane:** The long-running service that owns claims, credentials, scheduling, events, journals, and provider synchronization.

**Dependency wave:** A set of tickets whose prerequisites are complete and whose owned paths do not conflict, allowing parallel execution.

**External projection:** A Linear issue/comment/state, Notion page, Git branch, or PR reflecting authoritative run state.

**Hook:** A deterministic command executed before, after, or upon failure of a stage.

**Journal:** The durable run directory containing state, inputs, results, artifacts, logs, and recovery checkpoints.

**Logical ticket:** A stable implementation unit emitted by the product agent before or independently of its Linear child identity.

**Pipeline:** The repository-owned YAML DAG describing stages, dependencies, file contracts, parallelism, hooks, and repair loops.

**Repair loop:** A bounded workflow edge that sends structured QA work back through implementation and verification.

**Run:** One durable execution claim associated with a source issue.

**Run capability:** A signed, expiring bearer credential scoped to one worker and run.

**Sandbox:** A disposable Railway execution environment created from a versioned worker checkpoint.

**Source issue:** The root Linear issue that supplies the feature request and owns the durable cloud claim.

**Stage:** One declared unit in the pipeline, such as product, architecture, coder, lint, QA, or PR.

**Worktree:** An isolated Git checkout used by one logical implementation ticket.

## Next steps

- For the fastest first run, follow the [Quickstart](#quickstart).
- To understand operational differences, review [Local and cloud execution](#3-local-and-cloud-execution).
- To change agent behavior, start with [Customize agent roles](#17-customize-agent-roles).
- To change execution order or parallelism, see [Customize `.harness/pipeline.yaml`](#18-customize-harnesspipelineyaml).
- For a paused run, follow [Reliability and recovery](#21-reliability-and-recovery).
- For programmatic integration, review [API and events](#24-api-and-events); the website publishes the same material on a dedicated API Reference route.
