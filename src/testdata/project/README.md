# Project fixtures

Recorded from GitHub REST and GraphQL on mini on 2026-10-21, with `gh api`.
Repository names, issue numbers, titles, URLs and project ID were replaced with
example values. REST issue payloads retain only the fields the command reads.
Labels, timestamps, Status options, connection shapes and option IDs retain
the transport's values. Complete private recordings stay outside the repository.

- `open.json`, `closed.json`: repository issues endpoint, `state=open` or
  `state=closed`, `per_page=100`, `sort=created`, `direction=asc` (first four).
- `memberships.json`: `repository.issue.projectItems(first:100)`, including
  `project`, `fieldValueByName(name:"Status")` and `pageInfo`.
- `project.json`: `repositoryOwner.projectV2(number:1).fields(first:100)`,
  projected to the Status field and its options, with the original page info.

Tests also mutate these responses to exercise pagination, partial errors,
missing permissions, missing fields and recently closed boundaries. Linked PR
and sub-issue enrichment values are synthetic and explicitly asserted.

`project-hidden.json` records the real GraphQL NOT_FOUND response from a read-only
GitHub Actions token on 2026-10-21. The request selected an existing org project
that the installation token could not access; the response contains no private
project metadata or credentials.

`merged-closing-pr.json` records the repository issue relationship query without
`includeClosedPrs` on 2026-10-21, and the explicit true variant returned identical
bytes. The API returns merged PR 231 for issue 230.
The current producer also includes that PR in the recently closed section;
merged closing PRs are not lost by the shared relationship collector.
