# Status JSON version 1

Run `gh x status --json`. The [README example](../README.md#json-status-for-programs)
shows the complete document structure. The JSON and terminal views use the same
collector, enrichment, cache and health predicates. The format is for read-only
status inspection; cleanup candidates never cause deletion.

## Compatibility and errors

`schemaVersion` is `1`. New fields may be added without changing this version.
Removing a field, changing its type or changing its meaning requires a new
version. Consumers must ignore unknown fields and reject unsupported versions.
All documented fields are present. Lists use `[]`, never `null`. Unknown times
and unknown AI cleanliness use `null`. Times are RFC 3339 strings in UTC.
Counts are JSON integers; cache ages are nonnegative JSON numbers in seconds.
Workflow IDs are strings to avoid integer precision loss in consumers.

Success, including unavailable GitHub sections, exits zero. Each section has
its own status and error reason. A failed enrichment keeps available rows but
marks the section unavailable. A failed primary list fetch has an empty list.
These are bounded snapshots, not complete paginated inventories: open issues
and PRs are limited to 30 each, workflow runs to 5, merged PRs to `--merged`.
Ordering matches the terminal, including newest merge first for merged PRs.

Fatal local collection errors exit nonzero and emit only this envelope:

```json
{
  "schemaVersion": 1,
  "generatedAt": "2026-10-21T02:30:00-04:00",
  "error": {"code": "status_failed", "message": "git status: not a git repository"}
}
```

The error code is stable; the message is diagnostic text and may change. Error
documents have no repository, health or list fields. Argument parsing and help
diagnostics go to stderr. Help requests print usage without a JSON document.
Consumers should check the process exit code and `error` before reading health.

## Document fields

| Field | Type | Meaning |
| --- | --- | --- |
| `schemaVersion` | integer | Public contract version, currently 1 |
| `generatedAt` | timestamp | Time the completed document was assembled |
| `repository.nameWithOwner` | string | Resolved `owner/name`; empty when hosted identity is unavailable |
| `repository.rootPath` | string | Absolute path of the current worktree, using host path conventions |
| `repository.defaultBranch` | string | Resolved default branch; empty if unknown |
| `health` | object | Local branch, branch inventory, worktree and stash verdicts |
| `issues` | array | Open issue rows |
| `pullRequests.open` | array | Enriched open PR rows |
| `pullRequests.merged` | array | Recently merged PR rows; empty when disabled |
| `pullRequests.mergedLimit` | integer | Effective `--merged` value, default 5 |
| `workflowRuns` | array | Recent workflow rows |
| `workflowRunsPerfect` | boolean | Whether all five recent runs completed successfully |
| `sections` | array | Four GitHub acquisition metadata entries, described below |

## Health fields

Every health row has `status`, `reason` and `clean`. `status` is `available` or
`unavailable`. `reason` is empty on success and diagnostic text on failure.
Unavailable rows have `clean: false`; their numeric and boolean detail fields
are placeholders and must not be treated as observations.

| Row | Additional fields | Clean verdict |
| --- | --- | --- |
| `health.defaultBranch` | Branch fields below | Same check as the terminal: checked out in its own worktree, synced with an existing upstream, no working changes |
| `health.currentBranch` | Branch fields below | Current branch severity is healthy: upstream configured, no ahead/behind commits or working changes |
| `health.branches` | Integer `local`, `remote`, `dangling` | 1 local, 1 remote-tracking, 0 dangling |
| `health.worktrees` | Integer `total`, `cleanupCandidates`; array `items` | 1 total, 0 cleanup candidates |
| `health.stashes` | Integer `count` | 0 stashes |

Each branch row also has string `name` and `upstream`, boolean `checkedOut`
and `synced`, integer `ahead` and `behind`, and object `changes`.
`synced` requires a configured upstream, zero ahead/behind commits and no known
gone upstream. The default branch's `checkedOut` can describe a different
worktree from `repository.rootPath`. An empty current branch name means detached
HEAD. A branch's `clean` is the health verdict, while `changes.clean` describes
only working-tree cleanliness.

`changes` contains boolean `clean` and integer counts `staged`, `modified`,
`deleted`, `renamed`, `untracked`, `conflicted`. These are the same counts as
the terminal; categories can overlap, so their sum is not a unique file count.
`changes.clean` is true when all counts are zero.

Each `health.worktrees.items` entry has:

| Fields | Type | Meaning |
| --- | --- | --- |
| `path`, `head`, `branch` | string | Worktree path, commit and branch; branch can be empty |
| `primary`, `current` | boolean | Primary worktree and current invocation's worktree |
| `detached`, `detachedMerged` | boolean | Detached HEAD and proof it is merged into the default branch |
| `locked`, `prunable` | boolean | Git worktree inventory flags |
| `prunableReason` | string | Git's reason for a prunable record; empty otherwise |
| `exists` | boolean | Whether the worktree directory exists |
| `cleanKnown`, `clean` | boolean | Whether cleanliness was inspected, and its result; ignore `clean` when `cleanKnown` is false |
| `cleanupCandidate` | boolean | Existing conservative cleanup rule's verdict |
| `cleanupReason` | string | Candidate explanation, empty for non-candidates |

## Issue rows

| Fields | Type | Meaning |
| --- | --- | --- |
| `number` | integer | Issue number |
| `url` | string | Issue URL |
| `title`, `author`, `state` | string | Terminal title, author display name and normalized state |
| `pullRequests`, `parent`, `subIssues` | string | Terminal relationship summaries, including `-` for none and `?` for unavailable |
| `labels`, `assignees` | string | Comma-separated display labels and assignees |
| `updated` | string | Relative update age, recomputed even for cached data |
| `updatedAt` | timestamp or null | Absolute update time |

## Pull request rows

Both PR arrays use the same row shape:

| Fields | Type | Meaning |
| --- | --- | --- |
| `number` | integer | PR number |
| `url` | string | PR URL |
| `issues` | string | Linked issue summary shown in the terminal |
| `title`, `author`, `state`, `branch` | string | Terminal display values |
| `review` | string | Terminal review verdict, such as `approved`, `review`, `changes`, `-` |
| `approvals` | integer | Distinct current approver count |
| `approvers` | array | Entries with string `login`, timestamp `approvedAt` and relative string `age` |
| `approvalsIncomplete` | boolean | Approval acquisition was incomplete |
| `checks` | string | Existing aggregate checks verdict, such as `pass`, `fail`, `pending`, `-` |
| `comments` | string | Existing terminal comments/thread summary |
| `aiReview` | string | Existing aggregate AI verdict, such as `pass`, `fail`, `pending`, `-` |
| `aiClean` | boolean or null | Existing clean AI determination, null when unknown |
| `updated` | string | Relative update age |
| `updatedAt`, `mergedAt` | timestamp or null | Absolute update/merge times; merge time is null for open PRs |

Display verdicts are produced by gh-x's existing status rules. Use these values
directly instead of reconstructing review/check policy. Unknown display values
must remain visible to consumers. Relationship summaries, labels and author
names are display strings, not identifiers to split or parse.

## Workflow rows

| Fields | Type | Meaning |
| --- | --- | --- |
| `id`, `url` | string | Workflow run ID and URL |
| `status` | string | Terminal marker: `✓` success, `X` failure, `*` running, `○` queued, `!` attention, `—` skipped/neutral, `·` unknown |
| `rawStatus` | string | GitHub status, such as `completed` or `in_progress` |
| `title`, `workflow`, `branch`, `event` | string | Terminal display values |
| `elapsed`, `age` | string | Relative duration and age, refreshed on cache hits |
| `createdAt`, `startedAt`, `updatedAt` | timestamp or null | Absolute run times |

Row strings match the terminal's compact columns. Titles, workflow names and
branches can be truncated by the existing formatter. JSON contains no terminal
color or hyperlink escape sequences. Follow `url` for the full hosted record.

## Section freshness and failures

`sections` always contains entries named `issues`, `pullRequests.open`,
`pullRequests.merged` and `workflowRuns`, in that order.

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | Path of the corresponding list |
| `status` | string | `available`, `unavailable`, or `disabled` |
| `reason` | string | Error detail when unavailable; empty otherwise |
| `cached` | boolean | This particular section was restored from cache and not fetched again |
| `fetchedAt` | timestamp or null | Completion time of its last acquisition attempt, including failures; null when disabled |
| `cacheAgeSeconds` | number | Age since `fetchedAt` for cached sections; zero for live/disabled sections |

`--refresh` bypasses the cache for all enabled sections. Independent section
expiry can produce a mix of cached and live sections in one document. Cached
failures retain `unavailable` and their reason until retried. Local health is
always collected again. Existing authentication and repository isolation rules
apply to JSON as they do to the terminal view.

With `--merged=0`, the merged list is `[]` and its section is `disabled`, with
empty reason, `cached: false`, `fetchedAt: null`, `cacheAgeSeconds: 0`. No merged
PR request runs. Inspect `status` separately from `cached`; cached data can be
unavailable, and live data can fail.
