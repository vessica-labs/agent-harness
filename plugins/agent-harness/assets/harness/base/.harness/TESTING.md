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

## Test Data and Dependencies

<!-- Define fixtures, mocks, containers, credentials, external services, and cleanup requirements. -->

## Determinism and Flake Policy

<!-- Define isolation, retry limits, prohibited nondeterminism, and how flaky tests are handled. -->

For Playwright, make the worker count configurable through `HARNESS_PLAYWRIGHT_WORKERS`; cloud sandboxes set this value to a resource-safe parallel limit. Do not hard-code a higher worker count in the Playwright configuration or package scripts.

## CI Gates

<!-- List required merge checks and identify which failures block completion. -->

## Required Evidence

<!-- Specify logs, reports, screenshots, recordings, or other artifacts required in the handoff. -->
