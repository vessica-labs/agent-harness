<!--
Replace every prompt with project-specific facts. Do not describe aspirational architecture as current architecture.
If a section does not apply, retain it and state why.
-->

# {{PROJECT_NAME}} Architecture

- Status: `{{STATUS}}`
- Owner: `{{OWNER}}`
- Last verified: `{{LAST_VERIFIED}}`
- Scope: `{{SCOPE}}`

## System Context

<!-- Define the system boundary, users, external systems, and the system's responsibilities. -->

## Component Map

| Component | Responsibility | Owning Path | Public Interface |
| --- | --- | --- | --- |
| {{COMPONENT}} | {{RESPONSIBILITY}} | `{{PATH}}` | {{INTERFACE}} |

## Dependency Rules

<!-- State allowed dependency directions, forbidden couplings, and layer boundaries. -->

## Critical Flows

<!-- Describe the major request, event, job, or data flows. Use links to code where useful. -->

## External Interfaces

| Interface | Direction | Contract | Failure Behavior |
| --- | --- | --- | --- |
| {{INTERFACE}} | {{INBOUND_OR_OUTBOUND}} | {{CONTRACT}} | {{FAILURE_BEHAVIOR}} |

## Architectural Invariants

<!-- List rules that must remain true across implementations and how each is enforced. The standard architecture-document, 800-line source-file, and environment-file checks live in .harness/arch-lint-rules.json. Add deterministic path, dependency-text, and required-boundary checks specific to this repository. -->

## Known Constraints

<!-- Record current scaling limits, legacy boundaries, and intentionally deferred work. -->

## Architecture Decision Records

Relevant ADRs for the current run are injected into [`.harness/adrs/`](./adrs/). Treat the directory contents as run-specific architectural context. Do not add individual ADR links here.
