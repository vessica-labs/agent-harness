---
name: setup-harness
description: Initialize or reconfigure Agent Harness in a Git repository. Use when a user asks to set up, bootstrap, install, configure, reconnect, or change the editable agent workflow YAML for a repository harness with Jira or Linear, Notion, and GitHub.
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
4. Verify the selected tracker and Notion tools are connected with read-only calls. For Jira, inspect project issue-type metadata and require a true child type. For Linear, resolve workspace/team identifiers. Fetch the Notion parent and confirm the create/update tools are exposed. If a required app is unavailable, stop and ask the user to connect it.
5. Run `scripts/bootstrap.py preflight`. Report failures; do not weaken them silently.
6. Run `scripts/bootstrap.py bootstrap` without `--apply`. Show the exact create/unchanged/conflict plan.
7. Draft completed contents for every `.harness/*.md` template from repository evidence and the user's answers. Keep `DESIGN.md` limited to product and UI design. Preserve the standard rules in `.harness/arch-lint-rules.json`, then add only clearly deterministic, accepted repository-specific architecture invariants. Show all proposed document and rule changes with the file preview.
8. Obtain explicit approval for the preview. Then run bootstrap with `--apply`; use `--force` only for individually approved conflicts. Apply the approved document contents without changing `.agents/*.md` role contracts.
9. Validate `.harness/config.yaml`, `.harness/pipeline.yaml`, agent references, Git remote/base, and `gh auth status` using the commands in the setup contract.
10. Report configured provider identifiers, files created/preserved, connection results, and any capability that still needs a first-run write check.

## Workflow Changes

When the harness already exists and the user only wants to change the agent workflow, edit `.harness/pipeline.yaml` directly instead of rerunning bootstrap. Preserve its declarative contract: stage dependencies, agent definition, run-relative inputs and outputs, result-message file, parallelism, and hooks. Preview the YAML change, apply it after approval, and run `harnessctl.py validate-pipeline --repo <repo>`. Do not hard-code stage order or concurrency anywhere else.

## Boundaries

- Treat app connections as the credential boundary. Never request or write provider tokens.
- Do not create test tickets, comments, Notion pages, branches, commits, or pull requests during setup.
- Do not claim Notion or comment write permission from a read result alone. Confirm exposed tools now and record that the first real run is the write check.
- Preserve unrelated working-tree changes.
