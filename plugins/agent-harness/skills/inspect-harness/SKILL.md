---
name: inspect-harness
description: Inspect local or Railway-hosted Agent Harness runs and associated Jira or Linear child tickets without changing them. Use for status, cloud event streaming, ticket listing, artifact or PR lookup, failure diagnosis, lease inspection, reconciliation previews, or recovery guidance.
---

# Inspect Harness

Default to read-only inspection. Read `references/inspection-contract.md` before reporting or proposing recovery.

## Procedure

1. Validate that `.harness/config.yaml` exists and identify the configured tracker.
2. Run `harnessctl.py list-runs`, filtered by issue key when supplied. Read the relevant `state.json`, stage outputs, hook logs, and evidence.
3. Fetch the source issue, its actual children, and comments through the configured provider. Identify harness records only by stable run/ticket markers; do not assume every child belongs to the harness.
4. Fetch recorded Notion hub/artifact pages and inspect the Git branch/commits/PR when relevant.
5. Compare local and external state. Report local journal facts first, then external projection differences and pending synchronization.
6. For “list associated tickets,” group by run and show logical key, provider key, dependencies, status, owner, commit, and URL.
7. For failures, name the visible symptom, failed stage/hook/tool, last successful checkpoint, underlying cause supported by evidence, and safest resumption point.
8. Do not reclaim leases, update comments, retry writes, edit files, or resume execution unless the user explicitly asks for recovery or execution; then hand off to `run-harness`.

## Cloud inspection

When a run is hosted, use `agent-harness cloud runs show <run-id>` and `agent-harness cloud runs watch --run <run-id>` before provider projections. Use `cloud runs export <run-id> --repo <path>` only when the user asks to materialize the journal locally and the target run directory does not exist. Report event sequence, sandbox/session, queue reason, current stage, last heartbeat, and required sync failures.
