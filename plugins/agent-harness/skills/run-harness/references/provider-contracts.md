# Provider Contracts

All creates and updates are idempotent. Search existing children, comments, and Notion pages for markers before mutation. Persist returned IDs and URLs immediately.

## Markers

```text
Parent comment: <!-- agent-harness:run:<run-id> -->
Child comment:  <!-- agent-harness:ticket:<run-id>:<logical-key> -->
Child body:     <!-- agent-harness:child:<run-id>:<logical-key> -->
Final summary:  <!-- agent-harness:summary:<run-id> -->
Notion hub:     <!-- agent-harness:notion-hub:<provider>:<issue-key-slug> -->
Notion page:    <!-- agent-harness:notion-artifact:<run-id>:<artifact-slug> -->
```

Generate exact markers with `harnessctl.py markers` and render comment bodies with `harnessctl.py render-comment`. The adapter must edit the marked comment in place. If the selected connection cannot edit comments, pause with a capability error rather than creating milestone-comment noise.

## Linear adapter

Use the official Linear app. Typical operations are `get_issue`, `list_issues`, `list_comments`, `create_issue`, `update_issue`, and `create_comment`; use the exposed comment-update operation for canonical comments.

- Fetch by provider ID/key and confirm configured team/project.
- Create child issues with the source issue as `parentId` or the current equivalent parent field.
- Put the logical key, objective, acceptance criteria, owned paths, dependencies, focused checks, run marker, and Notion PRD/ADR links in the child description.
- Never change Linear status fields.

## Jira adapter

Use the official Atlassian Rovo app. Resolve `cloudId`, project, and issue metadata before writes. Typical operations include issue fetch/search, `createJiraIssue`, `addCommentToJiraIssue`, and the exposed edit issue/comment operations.

- Create the configured child issue type with `parent` set to the source issue key/ID.
- Include the same logical ticket contract and markers as Linear.
- Never substitute an ordinary linked issue when true parent/child creation is unavailable.
- Never change Jira workflow status fields.

## Notion adapter

Use `search` and `fetch` before create/update. Use `notion-create-pages` with an explicit parent and `pages` array. Fetch an existing page before `notion-update-page`.

- Under `config.notion.parent_page_id`, upsert one issue hub titled `[<issue-key>] <issue-title>` and marked with the provider issue URL.
- Under the hub, upsert run-specific children titled `PRD — <run-id>`, `ADR — <run-id>`, and `<document name> — <run-id>`.
- Store the run/artifact marker in page content and persist page ID/URL in `state.json`.
- Replays update the matching page. Explicit new runs create distinct children.

## Write checkpoint

Before a remote write, append an event and add an entry to `external.sync_pending` containing provider, operation, marker, and deterministic payload. After success, checkpoint returned identity and remove that entry. On failure, leave it present and pause; resume re-reads remote state before retrying.
