# State and Recovery

## Authority

`.harness/runs/<run-id>/state.json` is authoritative. Provider comments are an external projection; Notion is the external artifact surface. Never infer a successful mutation only from agent narration.

Each run directory contains:

```text
state.json
agent-output/
artifacts/
logs/
```

Worktrees and injected ADRs remain under ignored `.harness` runtime directories.

## State rules

- Write state atomically through `harnessctl.py`.
- A stage becomes completed only after local validation and all required external synchronization succeeds.
- A blocked stage pauses the run. Do not mark later stages skipped unless the user explicitly chose a partial run that excludes them.
- The issue lease prevents another fresh executor from starting within its lease window. Release it on completion or pause.
- Never delete a failed run. Create a new run only on explicit request.

## Recovery

1. Run `list-runs --issue-key <key>` and read the newest unfinished journal.
2. Inspect the lease. Reclaim only when stale or explicitly authorized.
3. Re-read the provider parent, children, comments, Notion hub/pages, branch, commits, and PR.
4. Reconcile `external.sync_pending` by marker. Record identities for writes that actually succeeded; retry missing writes.
5. Remove or recreate only disposable ticket worktrees. Preserve committed changes and the integration branch.
6. Resume at the first selected stage not completed. Never rerun a completed side effect without an idempotency lookup.
