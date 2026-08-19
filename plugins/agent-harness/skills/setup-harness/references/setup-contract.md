# Setup Contract

## Packaged commands

Resolve the plugin root as two directories above this skill directory.

```text
python3 <plugin>/scripts/bootstrap.py preflight \
  --target <repo> --remote <remote> --base-branch <branch>

python3 <plugin>/scripts/bootstrap.py bootstrap \
  --target <repo> --provider <linear|jira> \
  --workspace <workspace> --project <project> \
  --child-issue-type <jira-child-type-if-needed> \
  --notion-parent-page-id <page-id> \
  --remote <remote> --base-branch <branch> \
  --trigger-label <linear-label>
```

The second command is preview-only until `--apply` is supplied. Conflicting files require both `--apply` and explicit `--force` approval.

After applying, run:

```text
python3 <plugin>/scripts/harnessctl.py validate-config <repo>/.harness/config.yaml
python3 <plugin>/scripts/harnessctl.py validate-pipeline <repo>/.harness/pipeline.yaml --repo <repo>
```

## Provider preflight

### Linear

- Resolve the configured team with `list_teams`/`get_team` and optionally the project with `list_projects`.
- Confirm issue read, child creation through a parent relationship, comment creation, and comment editing capabilities are exposed.
- Do not create anything during setup.

### Jira

- Resolve the accessible Atlassian resource and configured project.
- Call `getJiraProjectIssueTypesMetadata` or the equivalent metadata tool.
- Require the configured child type to accept `parent` during issue creation.
- Confirm issue read, child creation, comment creation, and comment editing capabilities are exposed.

### Notion

- Fetch the configured parent page.
- Confirm `notion-create-pages` and `notion-update-page` are exposed.
- Use the page ID/URL accepted by the connected Notion tools; store only that identifier.

If a provider exposes comment creation but not editing, report the capability mismatch. Do not silently switch to noisy append-only milestone comments.

## Installation result

The target repository contains:

- `AGENTS.md`: the root Codex entry point and table of contents for repository guidance;
- `.agents/*.md`: stable agent role contracts;
- `.harness/*.md`: repository-specific guidance linked from root `AGENTS.md`;
- `.harness/config.yaml`: non-secret provider and repository integration settings;
- `.harness/pipeline.yaml`: editable deterministic agent DAG, file contracts, parallelism, results, and hooks;
- `.harness/config.yaml` may include the non-secret Linear label trigger used by the Railway cloud runner;
- ignored runtime directories for runs, worktrees, leases, and injected ADRs.
