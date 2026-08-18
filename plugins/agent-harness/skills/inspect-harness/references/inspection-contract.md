# Inspection Contract

## Local commands

```text
python3 <plugin>/scripts/harnessctl.py list-runs --repo <repo>
python3 <plugin>/scripts/harnessctl.py list-runs --repo <repo> --issue-key <key>
python3 <plugin>/scripts/harnessctl.py render-comment --run-dir <run-dir> --kind parent
python3 <plugin>/scripts/harnessctl.py render-comment --run-dir <run-dir> --kind ticket --ticket-key <logical-key>
python3 <plugin>/scripts/harnessctl.py render-comment --run-dir <run-dir> --kind summary
```

Read the local journal before remote systems because it is execution-authoritative. Treat missing or corrupt state as a concrete failure, not permission to reconstruct and write automatically.

## Associated-ticket output

Use this compact table for each run:

```text
Run | Logical ticket | Provider ticket | Depends on | Status | Owner | Commit | URL
```

Include QA-generated children as well as product-generated children. Clearly distinguish:

- locally planned but not created;
- remotely created and synchronized;
- remote-only marker records requiring reconciliation;
- completed/integrated tickets.

## Recovery preview

A recovery preview contains:

1. authoritative run and last successful event;
2. current lease and whether it is fresh or stale;
3. incomplete selected stages;
4. pending provider/Notion writes and marker lookup results;
5. branch/worktree/commit condition;
6. exact proposed resume stage and any cleanup that would occur.

Do not execute the preview without an explicit recovery request.
