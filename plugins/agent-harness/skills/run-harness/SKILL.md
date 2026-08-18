---
name: run-harness
description: Execute or resume the editable Agent Harness workflow YAML from a user request or Jira/Linear issue. Use for full pipelines, exact named agent stages, product/architecture-only work, individual stages, YAML-defined parallelism, or explicit new runs.
---

# Run Harness

Treat `.harness/pipeline.yaml` and the local run journal as authoritative. The orchestrator owns provider mutations, worktree integration, hooks, and checkpoints; role agents obey `.agents/*.md` and return their exact JSON contracts.

Read `references/orchestration.md`, `references/provider-contracts.md`, and `references/state-and-recovery.md` before executing a run.

## Entry

1. Verify `.harness/config.yaml`, `.harness/pipeline.yaml`, and every selected agent file with `scripts/harnessctl.py`. Do not substitute a built-in stage order or concurrency value for the YAML.
2. Parse the command:
   - `full pipeline`, `all`, or equivalent selects every stage;
   - named stages select only those stages, normalized through the helper;
   - default to resuming the active unfinished run for the issue;
   - pass `--new-run` only when the user explicitly requests a new run.
3. Resolve the product feature request using the ordered `sources` declared for its input. Use explicit user request text when supplied; otherwise write the tracker title and body to the declared feature-request file. Fetch issue comments as supplementary product context, not as a replacement for that file.
4. Initialize the run and acquire its lease. Record the returned run directory and session token. Never reclaim a fresh lease without explicit user direction.
5. Checkpoint source issue ID, URL, title, branch/base, and the intended provider mutations. Create or update the parent canonical comment before the first agent stage.

## Execution

- Execute selected stages in pipeline order. A selected stage whose prerequisite is neither selected nor completed must pause before running.
- Give every agent access to its repository worktree plus only the input files declared for that stage. Required missing files block the stage before delegation.
- Run every `before` hook, the stage, then every `after` hook. On failure, run `on_failure`, checkpoint evidence, update tracker comments, pause, and release the lease.
- Delegate single stages to a subagent when available. Give it the role definition, declared input files, relevant repository guidance, repository worktree, and exact result file. Validate its JSON with the `result.agent` contract before accepting it.
- For `ticket_parallel`, materialize the declared generated input for every ticket, calculate dependency waves with `harnessctl.py waves`, and spawn at most `stage.parallelism` isolated ticket agents. Never assign overlapping owned paths in one wave.
- After a valid result message, materialize every declared output by extracting its `from_result` field into its run-relative file. A stage is incomplete until its result and all outputs exist.
- Checkpoint locally before each external write and clear `external.sync_pending` only after the exact mutation succeeds. A replay must search for stable markers before creating anything.
- Refresh parent and affected child canonical comments after every stage transition. Do not mutate provider workflow status fields.
- At completion or pause, search for `<!-- agent-harness:summary:<run-id> -->` and create exactly one human-readable terminal summary rendered with `harnessctl.py render-comment --kind summary`.
- Always release the issue lease at a terminal completion or pause. Preserve the run journal and worktrees needed for recovery.

## Authorization

A direct full-pipeline request authorizes issue/Notion writes, scoped commits, integration, push, and PR creation. A partial-stage request authorizes only effects intrinsic to those stages. Never merge a pull request. Stop for ambiguous product intent, architectural ambiguity, secrets, destructive migrations, unavailable required connections, or a fresh conflicting lease.
