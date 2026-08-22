# Orchestration Contract

## Deterministic helpers

Resolve `<plugin>` as two directories above the skill directory.

```text
python3 <plugin>/scripts/harnessctl.py validate-config <repo>/.harness/config.yaml
python3 <plugin>/scripts/harnessctl.py validate-pipeline <repo>/.harness/pipeline.yaml --repo <repo>
python3 <plugin>/scripts/harnessctl.py init-run --repo <repo> \
  --provider <provider> --issue-key <key> --stages <comma-list|full>
```

Use `--new-run` only for an explicit new run. Use `--reclaim-lease` only after confirming the previous executor is gone and the user requested recovery.

For each transition:

```text
python3 <plugin>/scripts/harnessctl.py set-stage \
  --run-dir <run-dir> --stage <stage> --status <status> \
  --details-json <json-object>
```

Use `checkpoint --patch-json` for tickets, artifacts, provider IDs, Notion IDs, commits, branch, PR, repair loops, and sync intent. The YAML declares every input, output, result path, dependency, and parallelism value. Validate each result with `validate-agent-output --agent <result.agent>`, then extract each `outputs[].from_result` value to its declared file.

All YAML file paths are relative to `run_root`. `$` means the complete JSON result or collection. `{ticket_key}` is replaced only for a materialized ticket invocation. A glob is read-only input expansion and must not be used as an output path.

Use the deterministic file-contract commands rather than hand-copying artifacts:

```text
harnessctl.py materialize-source --pipeline <pipeline> --run-dir <run> \
  --stage product --input-id feature_request --source <declared-source> --content-file <source-file>
harnessctl.py materialize-generated-inputs --pipeline <pipeline> --run-dir <run> --stage coder
harnessctl.py materialize-result --pipeline <pipeline> --run-dir <run> \
  --stage <stage> --input <agent-result.json> [--ticket-key <key>]
```

## Hooks

A hook has this shape:

```yaml
- id: schema-check
  argv: ["./scripts/check-schema", "--strict"]
  cwd: .
  timeout_seconds: 300
```

Invoke it with `harnessctl.py run-hook`, passing only:

- `HARNESS_RUN_ID`
- `HARNESS_ISSUE_KEY`
- `HARNESS_STAGE`
- `HARNESS_ARTIFACT_DIR`
- `HARNESS_WORKTREE`

The helper uses no shell interpolation and preserves only essential host process variables such as `PATH` and temporary-directory settings. Store its JSON result under `logs/hooks/`. A timeout or non-zero exit blocks the stage.

## Stage contracts

### Product

Write the declared feature-request input from the first available YAML source: user prompt, then tracker title and body. Fetch issue comments as additional context. Run `.agents/product.md`, validate the result, materialize the PRD and ticket plan at the YAML output paths, publish the PRD to Notion, and create marked provider child issues immediately.

### Arch

Run `.agents/architect.md` with its declared PRD input, ticket graph, repository evidence, ADR index, and applicable injected ADR context. Validate with `--agent architect --tickets <product-result>`. Materialize and publish the ADR; apply added ticket dependencies, owned paths, focused checks, and compact constraints; revalidate the graph; then generate one ticket context packet per ticket.

### Coder

Create `agent-harness/<issue-key-lower>-<run-suffix>` from the configured remote/base. Materialize one declared compact context packet per ticket. For each dependency wave, create ticket worktrees from the current integration head and invoke one top-level Codex coordinator with native multi-agent support enabled. The coordinator delegates one coder subagent per ticket and keeps no more than the YAML `parallelism` value active at once. Each coder subagent follows TDD, writes its declared result, and makes one scoped commit; neither coordinator nor subagents push. Integrate successful commits in sorted logical-key order and retry only failed tickets from durable checkpoints.

### Lint

Run the lint agent on the integration worktree. It executes repository lint/build commands and `python3 .harness/scripts/arch-lint.py`, fixes deterministic violations, and creates scoped repair commits until green or blocked. After the agent returns, the YAML `architecture-lint` after-hook runs the same command again; only that hook result is the authoritative architecture gate.

### QA

Use Playwright to execute every acceptance criterion from the declared PRD input and materialize QA evidence. Safe fixes become scoped commits. If QA returns requeue, follow a matching YAML `repair_loops` declaration; when none exists, pause with the new tickets and evidence.

### Docs

After QA passes, update repository and `.harness` current-state documentation, copy the accepted ADR into `.harness/adrs/accepted/`, update the ADR index, validate and commit the result. The default stage returns no external evidence documents. A blocked documentation result prevents PR preparation.

### PR

Require every declared PR input and clean lint/build/QA/documentation results. Run the PR agent, rebase on the configured remote/base, verify, push normally or with force-with-lease after a rebase, and create the GitHub PR with `gh`. Materialize the PR record, store the canonical URL, and never merge.
