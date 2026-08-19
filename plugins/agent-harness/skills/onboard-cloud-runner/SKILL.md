---
name: onboard-cloud-runner
description: Set up, join, or repair the Agent Harness Railway cloud runner from first login through repository registration and team access. Use when a user asks to deploy the control plane, join one from an invitation link, invite or revoke teammates, enable automatic Linear issue execution, connect Railway, GitHub, Linear, Notion, or Codex authentication, create a cloud profile, register a repository, or finish an interrupted cloud onboarding.
---

# Onboard Cloud Runner

Drive the onboarding to a verified, registered repository. Perform safe steps directly and pause only for a human login, consent screen, secret entry, or an external choice that cannot be inferred.

## Rules

- Inspect before changing anything. Resume completed steps instead of recreating resources or credentials.
- Keep secrets out of chat, commands shown to the user, source files, logs, and Codex subprocesses.
- Treat interactive Codex app connections and cloud control-plane credentials as separate security boundaries; never copy one into the other.
- Use a hidden local prompt or temporary sealed Railway variable for credential entry. Transfer it into the control plane, verify the encrypted credential, then remove the temporary value.
- Show the target Railway project, service, environment, GitHub repositories, Linear team/project, Notion parent, and trigger label before registering the repository.
- Never add the trigger label to a real Linear issue during onboarding.
- Never claim a provider is ready from configuration alone. Run the checks specified below.
- Never repeat, log, or inspect a magic-link fragment. Pass a user-provided invitation link directly to `agent-harness cloud join`, then discard it from working context.

## Join an existing team

When the user supplies an Agent Harness invitation link, this is a short path—not a deployment:

1. Confirm `agent-harness` is installed.
2. Run `agent-harness cloud join '<invite-link>'`. Let the CLI derive the control-plane URL, redeem the one-time secret, and store this device's rotating session in the OS keychain.
3. Run `agent-harness cloud whoami` and report the member name, role, and device. Never print stored access or refresh tokens.
4. Run `agent-harness cloud runs list` to verify the granted access. Stop if the role does not permit the requested action.

The link is single-use and normally expires in one hour. Ask an administrator for a new link if redemption reports that it is expired, revoked, or already used.

## 1. Inspect

1. Confirm the current directory is the intended Git repository and inspect `.harness/config.yaml`, `.harness/pipeline.yaml`, the GitHub remote, and base branch.
2. If the repository harness is absent, follow `$setup-harness` before cloud registration.
3. Check for `git`, `gh`, `codex`, `railway`, and `agent-harness`. Install only missing tools.
   - Preferred Railway installer: `curl -fsSL agents.railway.com | sh`
   - Alternatives: `brew install railway` or `npm install -g @railway/cli`
4. Run `gh auth status`, `railway whoami --json`, `agent-harness version`, and `agent-harness cloud auth status` when a cloud profile already exists.
5. Report completed, missing, and blocked steps before continuing.

## 2. Prepare Railway

1. Sign in with `railway login` when needed. Relay a device-login link immediately if the CLI cannot open a browser.
2. Confirm Railway Sandboxes are enabled before provisioning the runner.
3. Reuse or create one Railway project containing:
   - a single-replica `control-plane` service;
   - a Railway Postgres service;
   - a public HTTPS domain on `control-plane`.
4. Ask the user to create a workspace-scoped Railway token and enter it privately as `RAILWAY_API_TOKEN`. Never ask them to paste it into chat.
5. Create the versioned worker checkpoint, initialize the service secrets/profile, and deploy:

```text
agent-harness railway upgrade --project <project-id> --environment production --version <release-tag>
agent-harness railway init --project <project-id> --environment production \
  --service control-plane --postgres-service Postgres \
  --url https://<control-plane-domain> \
  --checkpoint agent-harness-worker-<version>
agent-harness railway deploy --project <project-id> --environment production \
  --service control-plane --path <agent-harness-repo>/cloud-runner
```

6. Wait for terminal Railway success. Verify `/healthz`, `/readyz`, the local cloud profile, and Sandbox listing before provider setup.

## 2A. Initialize team access

Immediately after the first successful deployment, exchange the generated bootstrap token for the first named owner session:

```text
agent-harness cloud team initialize --name <owner-name> --device <device-name>
agent-harness cloud whoami
```

This is an atomic one-time operation. After it succeeds, `HARNESS_MANAGEMENT_TOKEN` is rejected by ordinary APIs. The CLI stores a short-lived access token and rotating refresh token in the OS keychain, with a mode-0600 fallback. Do not proceed with provider setup until `whoami` reports the owner role.

To add teammates later, the owner or an administrator runs:

```text
agent-harness cloud team invite --role operator --label <recipient> --expires 1h
```

Send the returned magic link privately. Use `cloud team members`, `cloud team sessions`, and `cloud team revoke member|session|invite <id>` to review or revoke access.

## 3. Connect GitHub

1. Run `agent-harness cloud auth github --manifest-owner <owner>`.
2. Ask the user to approve the manifest and install the app only on repositories the runner may modify.
3. Record the installation ID without storing a private key locally.
4. Run `agent-harness cloud auth status` and require `github_app` to be configured.

## 4. Connect Linear

1. Run `agent-harness cloud auth linear manifest --url https://<control-plane-domain>`.
2. Guide the user to create a private Linear application with:
   - redirect URI `http://127.0.0.1:8743/callback`;
   - webhooks enabled at `https://<control-plane-domain>/webhooks/linear`;
   - resource types `Issue` and `OAuthAuthorization`;
   - Public and Client credentials disabled.
3. Have the user place the client ID, client secret, and webhook signing secret into temporary sealed Railway variables named `LINEAR_CLIENT_ID`, `LINEAR_CLIENT_SECRET`, and `LINEAR_WEBHOOK_SECRET`.
4. Run the app-actor OAuth flow through `agent-harness cloud auth linear`, wait for consent, and require both `linear_oauth` and `linear_webhook_secret` to be configured.
5. Remove all three temporary Railway variables and verify the control plane redeploys successfully.

## 5. Connect Notion

1. Ask the user to create an internal Notion connection named `Agent Harness` with read, insert, and update content capabilities.
2. Ask for the non-secret parent-page URL. Require the user to add the connection to that page.
3. Have the user place the integration token in a temporary sealed Railway variable named `NOTION_TOKEN`.
4. Run `agent-harness cloud auth notion`, verify `notion` is configured, and validate the parent page through the API.
5. With explicit notice, create one temporary child page and archive it immediately to prove write access. Report the result.
6. Remove `NOTION_TOKEN` and verify the control plane redeploys successfully.

## 6. Add Codex capacity

1. Run `agent-harness cloud auth codex add --slots <max-active-runs>`; default to three slots.
2. Walk the user through each isolated device-login session.
3. Let the CLI test whether one session can safely serve the configured coder concurrency. Preserve the CLI's safer one-slot-per-process fallback.
4. Require the expected number of ready, unleased slots before enabling automation.

## 7. Register and verify

1. Run `agent-harness cloud repo discover-linear` and select the Linear workspace, team, and optional project IDs from the connected account. Do not use display names where IDs are required.
2. Preview, then run:

```text
agent-harness cloud repo add \
  --name <name> \
  --github-owner <owner> --github-repo <repo> \
  --github-installation <installation-id> \
  --linear-workspace <workspace-id> --linear-team <team-id> \
  --linear-project <optional-project-id> \
  --trigger-label agent-harness \
  --notion-parent <page-id> --base-branch <branch>
```

3. Run `agent-harness cloud repo list`, `agent-harness cloud auth status`, and both health endpoints.
4. Confirm `.harness/config.yaml` uses the same trigger label and provider identifiers.
5. Finish with the local monitor command `agent-harness ui` and explain that adding the configured label to a root Linear issue starts its one durable run.

## Recovery

On a retry, re-run inspection and continue from the first incomplete checkpoint. Do not create a second Railway project, GitHub App, Linear App, Notion connection, credential set, or repository registration merely because a prior command was interrupted.
