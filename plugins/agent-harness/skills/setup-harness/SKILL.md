---
name: setup-harness
description: Initialize or reconfigure Agent Harness in a Git repository or attach it to the Railway cloud runner. Use when a user asks to set up, bootstrap, install, configure, reconnect, enable automatic Linear-ticket execution, register a cloud repository, or change the editable agent workflow YAML.
---

# Set Up Harness

Initialize the repository through an inspect, interview, preview, apply, and verify sequence. Never store OAuth tokens or replace an existing file without explicit approval.

## Procedure

1. Locate the plugin root from this skill directory and read `references/setup-contract.md`.
2. Inspect the repository, its tracked guidance, build/test commands, remote, default branch, UI conventions, security boundaries, and deployment shape. Resolve facts from files before asking questions.
3. Gather only missing choices:
   - exactly one tracker: `linear` or `jira`;
   - tracker workspace and team/project;
   - Jira child issue type when Jira is selected;
   - Notion parent page;
   - Git remote/base branch.
   - for cloud automation, a local profile name unique to this repository.
4. Verify the selected tracker and Notion tools are connected with read-only calls. For Jira, inspect project issue-type metadata and require a true child type. For Linear, resolve workspace/team identifiers. Fetch the Notion parent and confirm the create/update tools are exposed. If a required app is unavailable, stop and ask the user to connect it.
5. Run `scripts/bootstrap.py preflight`. Report failures; do not weaken them silently.
6. Run `scripts/bootstrap.py bootstrap` without `--apply`. Show the exact create/unchanged/conflict plan.
7. Draft completed contents for every `.harness/*.md` template from repository evidence and the user's answers. Keep `DESIGN.md` limited to product and UI design. Preserve the standard rules in `.harness/arch-lint-rules.json`, then add only clearly deterministic, accepted repository-specific architecture invariants. Show all proposed document and rule changes with the file preview.
8. Obtain explicit approval for the preview. Then run bootstrap with `--apply`; use `--force` only for individually approved conflicts. Apply the approved document contents without changing `.agents/*.md` role contracts.
9. Validate `.harness/config.yaml`, `.harness/pipeline.yaml`, agent references, Git remote/base, and `gh auth status` using the commands in the setup contract.
10. Report configured provider identifiers, files created/preserved, connection results, and any capability that still needs a first-run write check.

## Cloud attachment

When the user asks to enable Railway execution, first complete the local setup and confirm `automation.trigger` names the intended Linear agent app actor (default `Vessica`). If the control plane or local cloud profile does not exist, follow `$onboard-cloud-runner`; do not assume infrastructure or credentials are already configured. For an existing cloud profile:

1. Require the `agent-harness` Go CLI and an existing local cloud profile; never copy Codex app credentials into Railway. Put its non-secret name in `.harness/config.yaml` as `cloud.profile`. Use a separate control plane/profile when this repository needs a different Linear workspace or Notion workspace/parent from another repository.
2. Run `agent-harness cloud profile list` and `agent-harness cloud auth status` from the repository. Confirm the selected profile is the repository binding, then report missing Codex slots, GitHub App, Linear, or Notion service credentials. Confirm the Linear installation is the assignable `Vessica` app actor and subscribes to `AgentSessionEvent`.
   - For a new GitHub App, use `agent-harness cloud auth github --manifest-owner <organization>` and install it only on selected repositories.
   - For a new Linear app, use `agent-harness cloud auth linear manifest --url <control-plane>` followed by the app-actor OAuth command. Never copy returned tokens through the chat.
3. Preview the repository registration command, including GitHub installation, Linear workspace/team/project, Linear agent name, Notion parent, and base branch.
4. After approval, run `agent-harness cloud repo add` with those exact identifiers.
5. Verify with `agent-harness cloud repo list`. Do not delegate a real issue to Vessica during setup.

## Workflow Changes

When the harness already exists and the user only wants to change the agent workflow, edit `.harness/pipeline.yaml` directly instead of rerunning bootstrap. Preserve its declarative contract: stage dependencies, agent definition, run-relative inputs and outputs, result-message file, parallelism, and hooks. Preview the YAML change, apply it after approval, and run `harnessctl.py validate-pipeline --repo <repo>`. Do not hard-code stage order or concurrency anywhere else.

## Boundaries

- Treat app connections as the credential boundary. Never request or write provider tokens.
- Do not create test tickets, comments, Notion pages, branches, commits, or pull requests during setup.
- Do not claim Notion or comment write permission from a read result alone. Confirm exposed tools now and record that the first real run is the write check.
- Preserve unrelated working-tree changes.
