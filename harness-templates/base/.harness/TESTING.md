<!--
Replace every prompt with exact project-specific expectations and runnable commands.
If a section does not apply, retain it and state why.
-->

# {{PROJECT_NAME}} Testing

- Status: `{{STATUS}}`
- Owner: `{{OWNER}}`
- Last verified: `{{LAST_VERIFIED}}`
- Scope: `{{SCOPE}}`

## Testing Objectives

<!-- Identify the primary product and engineering risks the test strategy must control. -->

## Test Layers

| Layer | Purpose | Location | Required For |
| --- | --- | --- | --- |
| {{LAYER}} | {{PURPOSE}} | `{{PATH}}` | {{CHANGE_TYPES}} |

## Commands

| Check | Command | Expected Result |
| --- | --- | --- |
| {{CHECK}} | `{{COMMAND}}` | {{EXPECTED_RESULT}} |

## Requirements by Change Type

<!-- Define the minimum tests for bugs, features, refactors, migrations, dependencies, and infrastructure changes. -->

Use test-first red-green-refactor for bugs and new behavioral contracts when practical, not as a ritual for every edit. Ticket plans separate fast `iteration_checks`, a one-time affected-package `ticket_gate`, and downstream `pipeline_gates`. Coder agents must not run full repository or browser suites unless the ticket explicitly owns that boundary; lint and QA deduplicate and run their assigned pipeline gates.

## Test Data and Dependencies

<!-- Define fixtures, mocks, containers, credentials, external services, and cleanup requirements. -->

## Determinism and Flake Policy

<!-- Define isolation, retry limits, prohibited nondeterminism, and how flaky tests are handled. The same unchanged command/failure pair must not be repeated more than twice without a causal diagnosis. -->

For Playwright, make the worker count configurable through `HARNESS_PLAYWRIGHT_WORKERS`; cloud sandboxes set this value to a resource-safe parallel limit. Do not hard-code a higher worker count in the Playwright configuration or package scripts.

## CI Gates

<!-- List required merge checks and identify which failures block completion. -->

## Required Evidence

<!-- Specify logs, reports, screenshots, recordings, or other artifacts required in the handoff. -->
