<!--
Render this template to the repository root as AGENTS.md.
Replace every prompt with project-specific facts. Do not invent missing information.
Keep this file short: it is a map and operating contract, not the full manual.
If a section does not apply, retain it and state why.
-->

# {{PROJECT_NAME}} Agent Guide

- Status: `{{STATUS}}`
- Owner: `{{OWNER}}`
- Last verified: `{{LAST_VERIFIED}}`
- Scope: `{{SCOPE}}`

## Project Purpose

<!-- State what the project does, who it serves, and its primary outcome. -->

## Repository Map

| Path | Responsibility |
| --- | --- |
| `{{PATH}}` | {{RESPONSIBILITY}} |

## Sources of Truth

| Topic | Source |
| --- | --- |
| Architecture | [`.harness/ARCHITECTURE.md`](.harness/ARCHITECTURE.md) |
| Product and UI design | [`.harness/DESIGN.md`](.harness/DESIGN.md) |
| Testing | [`.harness/TESTING.md`](.harness/TESTING.md) |
| Security | [`.harness/SECURITY.md`](.harness/SECURITY.md) |
| Deployment | [`.harness/DEPLOY.md`](.harness/DEPLOY.md) |
| Relevant architecture decisions | [`.harness/adrs/INDEX.md`](.harness/adrs/INDEX.md) |

## Essential Commands

| Task | Command |
| --- | --- |
| Setup | `{{SETUP_COMMAND}}` |
| Develop | `{{DEVELOP_COMMAND}}` |
| Format | `{{FORMAT_COMMAND}}` |
| Lint | `{{LINT_COMMAND}}` |
| Test | `{{TEST_COMMAND}}` |
| Build | `{{BUILD_COMMAND}}` |

## Non-Negotiable Rules

<!-- List only rules that apply broadly to every change. Put detailed rules in the linked documents. -->

## Change Workflow

<!-- Define the required sequence from understanding the task through implementation, verification, and handoff. -->

## Definition of Done

<!-- List the evidence and checks required before an agent may report completion. -->

## Escalation Conditions

<!-- List decisions, risks, destructive actions, or ambiguities that require human approval. -->
