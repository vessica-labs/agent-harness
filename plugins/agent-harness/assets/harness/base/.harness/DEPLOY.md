<!--
Replace every prompt with exact project-specific procedures and commands.
If a section does not apply, retain it and state why.
-->

# {{PROJECT_NAME}} Deployment

- Status: `{{STATUS}}`
- Owner: `{{OWNER}}`
- Last verified: `{{LAST_VERIFIED}}`
- Scope: `{{SCOPE}}`

## Environments

| Environment | Purpose | Trigger | Authority |
| --- | --- | --- | --- |
| {{ENVIRONMENT}} | {{PURPOSE}} | {{TRIGGER}} | {{AUTHORITY}} |

## Build Artifact

<!-- Define what is built, from which source, how it is versioned, and where it is stored. -->

## Configuration and Secrets

<!-- List required configuration names, their owners, and where they are managed. Never include values. -->

## Deployment Preconditions

<!-- List required tests, approvals, repository state, compatibility checks, and backups. -->

## Deployment Procedure

<!-- Provide the deterministic deployment entry point and ordered steps. -->

## Database and State Changes

<!-- Define migration ordering, backward compatibility, data safety, and recovery requirements. -->

## Post-Deployment Verification

<!-- Define health checks, smoke tests, metrics, logs, and success criteria. -->

## Rollback and Recovery

<!-- Define rollback triggers, exact recovery procedure, and forward-fix policy. -->

## Deployment Authority

<!-- Define who or what may deploy, promote, roll back, or approve each environment. -->
