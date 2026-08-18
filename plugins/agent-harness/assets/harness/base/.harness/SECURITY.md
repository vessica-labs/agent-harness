<!--
Replace every prompt with project-specific controls. Do not include secret values.
If a section does not apply, retain it and state why.
-->

# {{PROJECT_NAME}} Security

- Status: `{{STATUS}}`
- Owner: `{{OWNER}}`
- Last verified: `{{LAST_VERIFIED}}`
- Scope: `{{SCOPE}}`

## Security Scope

<!-- Identify protected users, systems, assets, and explicitly excluded areas. -->

## Data Classification

| Data Class | Examples | Storage | Handling Rules |
| --- | --- | --- | --- |
| {{CLASS}} | {{EXAMPLES}} | {{STORAGE}} | {{RULES}} |

## Trust Boundaries

<!-- Identify actors, entry points, privilege boundaries, and external systems. -->

## Authentication and Authorization

<!-- Define identity, session, service-authentication, authorization, and least-privilege rules. -->

## Secrets and Configuration

<!-- Define approved storage, local-development handling, rotation, and logging restrictions. -->

## Secure Input and Output Handling

<!-- Define validation, encoding, file handling, command execution, network access, and sensitive-output rules. -->

## Dependencies and Supply Chain

<!-- Define approved sources, pinning, update, provenance, and scanning requirements. -->

## Security Verification

<!-- List required security tests, scanners, commands, and expected evidence. -->

## Escalation and Reporting

<!-- Define security conditions that require agents to stop, preserve evidence, and request human review. -->
