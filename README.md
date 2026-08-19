# Agent Harness

Agent Harness is a lean, editable issue-to-pull-request coding workflow for Codex. Each repository owns its context documents, agent definitions, deterministic pipeline YAML, architecture rules, and durable run journal. The optional Railway cloud runner watches labeled Linear issues and executes the same repository-owned workflow in isolated sandboxes.

## What is included

- `harness-templates/base` — canonical `.harness` and `.agents` bootstrap files.
- `plugins/agent-harness` — Codex skills for setup, cloud onboarding, execution, and inspection.
- `cloud-runner` — one Go binary for the Railway control plane, isolated workers, local management CLI, and read-only monitor.
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
curl -fL https://github.com/vessica-labs/agent-harness/releases/download/v0.1.0-rc.10/agent-harness-darwin-arm64 \
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
cd agent-harness/cloud-runner
make build
mkdir -p "$HOME/.local/bin"
cp bin/agent-harness "$HOME/.local/bin/agent-harness"
```

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

The localhost UI connects through an authenticated local proxy. The browser never receives the cloud management token.

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
- `.harness/config.yaml` — non-secret tracker, Notion, Git, and automation identifiers.
- `.harness/pipeline.yaml` — editable agent DAG, inputs, outputs, parallelism, and deterministic hooks.
- `.harness/scripts/arch-lint.py` and `.harness/arch-lint-rules.json` — deterministic architecture checks.
- ignored runtime locations for journals, worktrees, locks, and injected ADRs.

### B. Create the Railway control plane

Codex can create these resources, or you can create them manually:

```sh
railway init --name agent-harness
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
  --version v0.1.0-rc.10

agent-harness railway init \
  --project <railway-project-id> \
  --environment production \
  --service control-plane \
  --postgres-service Postgres \
  --url https://<control-plane-domain> \
  --checkpoint agent-harness-worker-0.1.0-rc.10

agent-harness railway deploy \
  --project <railway-project-id> \
  --environment production \
  --service control-plane \
  --path /path/to/agent-harness/cloud-runner
```

`railway init` generates the management token and encryption key, seals them in Railway, connects Postgres, and stores the local management token in the operating-system keychain with a mode-0600 file fallback.

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

Approve the app and install it only on repositories the runner is allowed to modify. The app requests Metadata read, Contents write, and Pull Requests write. Record the installation ID for repository registration.

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
| Webhook resources | `Issue`, `OAuthAuthorization` |

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
  --trigger-label agent-harness \
  --notion-parent <parent-page-id> \
  --base-branch main

agent-harness cloud repo list
agent-harness cloud auth status
```

The trigger label must match `.harness/config.yaml`. Do not add it to a real issue until all credentials, Codex slots, health checks, and repository registration are green.

## Run and monitor

### Automatic cloud execution

Add the configured `agent-harness` label to an eligible root Linear issue. The webhook is acknowledged and persisted before work begins. Duplicate deliveries and later qualifying updates resolve to the existing run.

Monitor from the CLI:

```sh
agent-harness cloud runs list
agent-harness cloud runs show <run-id>
agent-harness cloud runs watch --run <run-id>
agent-harness cloud runs input <run-id> --file clarified-request.md
```

Open the local read-only dashboard:

```sh
agent-harness ui
```

The UI binds to `127.0.0.1` and streams authenticated events without exposing the management token to browser JavaScript.

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
- Management and event endpoints require a bearer token; only health checks and the signed Linear webhook are public.

## Development

Run all repository checks:

```sh
python3 -m unittest discover -s tests -v
cd cloud-runner
make verify
```

For local control-plane development and implementation details, see [cloud-runner/README.md](cloud-runner/README.md).
