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

<!-- List rules that must remain true across implementations and how each is enforced. The standard architecture-document, 500-line TypeScript/JavaScript, 800-line other-source, and environment-file checks live in .harness/arch-lint-rules.json. Add deterministic path, dependency-text, shared-schema, and required-boundary checks specific to this repository. -->

## Known Constraints

<!-- Record current scaling limits, legacy boundaries, and intentionally deferred work. -->

## Architecture Decision Records

The applicability and supersession index is [`.harness/adrs/INDEX.md`](./adrs/INDEX.md). Durable accepted records live in `.harness/adrs/accepted/`; ignored runtime-injected records may also appear directly under `.harness/adrs/`. Read the index first and load only accepted, non-superseded decisions that intersect the affected paths, components, interfaces, or concerns.
