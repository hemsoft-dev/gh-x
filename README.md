# gh-x

[![CI status][ci]][ci-runs] [![Latest release][release]][releases] [![MIT license][license]][license-file]

[ci]: https://github.com/hemsoft-dev/gh-x/actions/workflows/ci.yml/badge.svg?branch=main
[ci-runs]: https://github.com/hemsoft-dev/gh-x/actions/workflows/ci.yml
[release]: https://img.shields.io/github/v/release/hemsoft-dev/gh-x
[releases]: https://github.com/hemsoft-dev/gh-x/releases
[license]: https://img.shields.io/github/license/hemsoft-dev/gh-x
[license-file]: LICENSE

A GitHub CLI extension that supercharges `gh pr list` with a richer,
color-coded table view — approvals, AI reviewer status, check details,
comment resolution, and clickable PR links.
Also includes `gh x pr atm` for org-wide PR visibility.

```text
#   Title                                             Author State Rev AI   Appv Checks  Cmts  Branch                Upd
#12 PLAT-18678: Migrate user-groups to .NET 10        jdoe   open  ✓   pass 2    pending 19/19 feature/PLAT-18678    23h
#10 .net 10 upgradation                               asmith open  •   -    0    fail    -     feature/PLAT-8516     17d
#5  feat(user-groups): Add golden-path IaC structure  bclark open  •   fail 0    pass    2/4   golden-path-alignment 4mo
```

## Installation

Requires [GitHub CLI](https://cli.github.com/) (`gh`) authenticated with your account.

```bash
gh extension install hemsoft-dev/gh-x
```

That's it. Prebuilt binaries are available for all platforms — no Go toolchain needed.

## Usage

```bash
gh x pr list [flags]    # enriched PR list for current repo
gh x pr me [flags]      # your open PRs plus Dependabot PRs across an owner
gh x pr atm [flags]     # org-wide PRs needing your attention
gh x pr review [number] # read-only agentic PR review
gh x pr changelog       # show release notes for recent versions
gh x monitor            # full-terminal multi-repository PR and issue dashboard
gh x status             # repository health with open and recently merged PRs
gh x project            # issues grouped by project Status (alias: p)
gh x run list [flags]   # workflow runs with clickable IDs
gh x version            # show version and check for updates (also: --version, -v)
```

### Clear the terminal before output

Use the global `--clear` flag to start with a clean terminal display:

```bash
gh x --clear status
gh x s --clear
gh x pr me --clear
```

The flag may appear before the command or among its flags. It clears the visible
display and moves the cursor to the top once, before the banner and command
output. It defaults to false; `--clear=false` also leaves the display unchanged.
JSON output and non-terminal stdout ignore clearing, so pipes and redirected
files do not receive terminal-clearing sequences. Existing watch and monitor
refresh and terminal restoration behavior stays the same.

### Multi-account fallback

If you switch between personal and work accounts, `gh x` adapts automatically.
When the active account cannot access the target repository, commands retry
with each other logged-in account until one succeeds (`gh auth status --json
hosts` supplies candidates) and print a one-line notice on stderr, for
example:

```text
[gh-x] note: retried as fhemmerrelias after an access failure
```

The target host is resolved following gh's own precedence: an explicit
`--hostname`, an explicit `HOST/OWNER/REPO` value on `--repo`/`-R`, then the
`GH_REPO` environment variable, then the current repository's git remote, then
`GH_HOST`, then `github.com`. SSH and SCP-style remote aliases are expanded
through `ssh -G`; the alias retains its original case for OpenSSH matching, and
a configured `HostName` becomes the API host only when it is `github.com`,
matches `GH_HOST`, or is listed in gh's local host configuration. This lets a
dotted alias such as `github.com-hemsoft` route to `github.com`. Canonical
`github.com` and configured Enterprise API hosts are preserved before SSH
resolution; when a public alias resolves to the `ssh.github.com` transport
endpoint, it maps back to the `github.com` API host. The SSH configuration
lookup has a two-second deadline and bounded pipe drain. If it fails or the SSH destination is not a known API host, a plausible
dotted remote host remains the fallback API host.
Fallback candidates come from that same host in `gh auth status --json hosts`,
and Enterprise Server
retries inject `GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN` instead of
`GH_TOKEN`, so personal and corporate hosts never exchange credentials.

The global active account is never modified — the retry token lives only
inside the retried subprocess. Explicit `GH_TOKEN` or `GITHUB_TOKEN` overrides
are respected for github.com targets, `GH_ENTERPRISE_TOKEN` or
`GITHUB_ENTERPRISE_TOKEN` for Enterprise Server targets (no fallback), auth
commands never fall back, and public repositories never trigger a retry
because either token can read them. Tokens resolve lazily and are cached per
account and host for the current process; they are not stored or shared
between runs. Search-backed pull request and issue lists verify repository
access before accepting an empty result because GitHub can return an empty
successful response when the active account cannot see a private repository.

Two flows are pinned to the active account and never fall back on any host:
identity-scoped
queries (`gh x pr me`, `gh x pr atm`, `gh x codespace list` — they resolve
"me" or list the authenticated user's resources, so retrying under a different
identity could answer with someone else's data), and the entire
`gh x pr review` operation (fetch, agent, and submission are identity-bound,
and posting a review is non-idempotent). `@me` in author and assignee filters resolves to the active account's login up
front for the same reason; in `--search`, only identity qualifiers (`author:`,
`assignee:`, `mentions:`, `commenter:`, `involves:`, `review-requested:`,
`reviewed-by:`) are expanded, leaving other query text untouched.

## What `gh x issue list` adds

The issue table keeps the standard filters and adds compact repository context:

| Column | Description |
|--------|-------------|
| **#** | Issue number, linked to the issue on GitHub in terminals with OSC 8 support |
| **PRs** | Pull requests linked through GitHub's issue-closing relationship. `-` means none and `?` means relationship data was unavailable |
| **Parent** | Immediate parent issue. Same-repository parents use `#NUMBER`; cross-repository parents use `OWNER/REPO#NUMBER` |
| **Sub** | Completed direct sub-issues over total direct sub-issues, such as `2/5` |
| **Title** | Truncated to 51 characters |
| **Author** | Issue author login |
| **State** | `open` or `closed` |
| **Labels** | Comma-separated issue labels |
| **Assignees** | Comma-separated assignee logins |
| **Updated** | Relative update time |

An empty, unfiltered open issue backlog prints one randomly selected
celebration from the shared pool. State, search, author, assignee, milestone,
and label filters keep the factual `No issues found.` message.

## Issue-driven projects

`gh x project` (alias `p`) groups open issues by a GitHub Project v2's Status
field, in its option order. Issues outside the selected project or without a
Status appear under `No status`. Empty Status groups remain visible in the
count header. The default repository resolves from the checkout or `GH_REPO`,
as with `status`; `-R` overrides it.

```bash
gh x project
gh x p --project my-org/1
gh x p --closed-days 14
gh x p --closed-days 0          # hide recently closed
gh x p --group-by label        # force label grouping
gh x p --group-label watching --group-label chief-todo
gh x p --json | jq .schemaVersion
# Optional shell alias: alias gp='gh x p'
```

When several projects contain open issues, the default is the project with the
most of those issues (ties use owner, then project number). `--project OWNER/N`
selects one explicitly, including a project with no open repository issues.
Project access requires `read:project`; the broader `project` scope includes it.
Without that permission the command prints one scope hint and groups by label.
With no project it also uses labels. An invalid explicit selector or a network
failure reports an error rather than presenting an apparently complete view.

Label mode uses each issue's first label, or `No label`. Repeat `--group-label`
to prefer named labels in that order, falling back to the first label when none
matches. The grouping label is omitted from the row's other labels. Label groups
sort by name; issues within each group sort oldest first. Age means time since
creation, and Updated means time since the last update. The closed section sorts
by closure time, newest first, and defaults to the last seven days. All issue
pages are fetched; pull requests returned by the REST issues endpoint are excluded.
Unknown linked PRs or sub-issue counts display `?` with an explanatory hint.

Example (synthetic issues):

```text
Repository example/loops
Project Open loops · https://github.com/orgs/example/projects/1
Waiting on Franz 1 · Chief todo 1 · Watching 0 · Done 0

Waiting on Franz (1)
Issue  Title                Age  Updated  Labels    PRs  Sub
#2     Approve release plan  2d   1h       area:dev  #8   2/5

Chief todo (1)
Issue  Title                Age  Updated  Labels    PRs  Sub
#1     Prepare weekly brief  3d   2h       area:dev       0/0

Recently closed (last 7 days) (1)
Issue  Title                Age  Updated  Labels    PRs  Sub
#3     Review old loops     4d   3h                       0/0
```

### Project JSON

`--json` writes one document, without the banner, update notice, colour or
terminal clearing. Non-TTY text also has no colour and never clears. Argument
and acquisition failures write `{"schemaVersion":1,"error":"message"}` and exit
nonzero. Output-write failures also exit nonzero. JSON timestamps use UTC.

This is a separate schema from [status JSON](docs/status-json.md).
`gh x status` and its schema version 1 are unchanged.

```json
{
  "schemaVersion": 1,
  "repository": "example/loops",
  "project": null,
  "grouping": "label",
  "groups": [{"name": "watching", "count": 1, "issues": [{
    "number": 2, "title": "Approve release plan",
    "url": "https://github.com/example/loops/issues/2",
    "createdAt": "2026-10-19T12:00:00Z",
    "updatedAt": "2026-10-21T11:00:00Z", "closedAt": null,
    "labels": ["area:dev"],
    "pullRequests": [{"number": 8, "url": "https://github.com/example/loops/pull/8"}],
    "subIssues": {"completed": 2, "total": 5}
  }]}],
  "recentlyClosed": [], "closedDays": 7, "hints": []
}
```

`project` is null in label mode, otherwise an object with `id`, `owner`,
`number`, `title`, `url`, and ordered `statusOptions` (`id`, `name`). `groups`
contains `name`, `count`, and `issues`. Each issue has the fields shown above;
`labels` excludes the grouping label in label mode. `recentlyClosed` uses the
same issue shape and preserves all labels. `closedAt` is null for open issues;
`subIssues` is null when unavailable. `pullRequests` is an empty array when
there are none and null when unavailable. Other collection fields are arrays,
including when empty. `hints` contains any scope or enrichment notices that
would appear in text output. `closedDays` records the requested window.

## What `gh x status` adds

In an interactive terminal, status shows local Git results while GitHub data is
loading, then adds completed sections. A rotating cursor names the pending work.
Worktree cleanup stays pending until its evidence is available. Long previews
keep the header and newest results visible; the full compact output prints when
the command finishes. Ctrl-C removes the indicator and restores the cursor.
`NO_COLOR` removes color while keeping loading feedback. Pipes, redirected
output, and `TERM=dumb` receive a single snapshot without animation.
The preview adapts to terminal resizing and keeps account fallback notices
above its results. Windows consoles enable virtual-terminal processing for the
animation and restore their previous mode afterward. Consoles without that
support receive a single snapshot.

`gh x status` starts with a repository health header for the resolved default
branch and current worktree. Each invocation shows the current local date and
time as `YYYY-MM-DD hh:mm AM/PM ZONE`, for example `2026-09-05 12:32 PM EDT`.
The zero-padded 12-hour clock and zone abbreviation follow the machine's local
time zone, including daylight saving time. The timestamp also appears in plain
redirected output and when GitHub sections are unavailable.

The compact header aligns branch, worktree, and stash inventories beneath the
default branch. It always reports stashes, including `0 stashes`, and marks each
health row with a green `✓` only when its clean-state rule passes:

- Default branch: checked out, synced with an existing upstream, and clean.
- Branches: 1 local branch, 0 dangling branches, and 1 remote-tracking branch.
- Worktrees: 1 total worktree and 0 cleanup candidates.
- Stashes: 0 stashes.

The default-branch check uses that branch's own worktree, even when the current
feature worktree has changes. Missing or unavailable default-branch status and
a default branch that is not checked out receive no check.

Other inventory values appear in yellow without a check. No-color output keeps
the checks and alignment. Stash counts come from the shared repository inventory
on every invocation, including from linked worktrees; an inspection failure
shows an unavailable message without a check.

The header counts local, remote, and dangling branches,
then reports linked worktrees and conservative cleanup candidates. A linked
worktree is suggested only when it is unlocked, clean, merged into the default
branch, and has no open pull request. Git-prunable records must also have a
branch or detached commit known to be merged. Current, primary, locked, and
default-branch worktrees are never suggested. Suggestions are informational;
the command never deletes or prunes a worktree.

Open issues, enriched open pull requests, the five most recently merged pull
requests, and the five most recent workflow runs appear in separate tables
below the header. Use `--merged=N` to change the merged pull request count.
`--merged=0` hides that section and skips its fetch. Merged pull requests are
ordered by merge time, newest first, and use the same enriched table as open
pull requests. The repository API returns existing merges even when the remote
retains an old repository name after a move. Workflow runs keep the same
status, title, workflow, branch, event, linked ID, elapsed-time, and age columns
as `gh x run list`. Local Git status still renders
when GitHub data is unavailable. When supplemental pull request data or issue
relationships are unavailable, the affected section prints the same
`unavailable` diagnostic line like the ones `gh x pr list` prints while
healthy rows keep their data.
When either unfiltered open-work section is empty, it prints one randomly
selected celebration from the same pool used by the list commands.
When all five recent workflow runs completed successfully, status prints one
randomly selected perfection message from a workflow-specific pool.

```bash
gh x status              # show the five most recently merged pull requests
gh x status --merged=10  # show up to ten
gh x status --merged=0   # hide recently merged pull requests
gh x status --refresh    # bypass cached GitHub data
```

Successful GitHub-backed status sections are cached for 60 seconds under
`$GIT_COMMON_DIR/gh-x/status-cache-v5`. Linked worktrees share that directory.
The cache is isolated by effective remote configuration (including
`gh repo set-default` and URL rewrites), the active branch's tracking remote,
GitHub authentication context, `GH_REPO`, `GH_HOST`, `--merged`, and color mode.
Environment and credential-store tokens affect the fingerprint;
no tokens or authentication files are stored in snapshots. Local branches,
worktrees, stashes, cleanup candidates, working-tree changes, and relative time labels
are updated on every run.
The hosted default branch is cached when no local remote HEAD is available.
`--refresh` bypasses cached sections and replaces the snapshot, including any
failures. Failed sections retain their error/unknown display and retry after
five seconds while healthy sections keep their original freshness deadlines.
Rate-limit failures wait at least 60 seconds. Expired sections refresh separately;
corrupt and incompatible snapshots are ignored.
Automatic release checks cache successful results for one hour; `gh x version`
forces a new check. See the [status freshness guide](docs/status-performance.md)
for authentication isolation, cooldowns and safe cache diagnostics.

### JSON status for programs

`gh x status --json` writes exactly one JSON object to stdout. It uses the same
collection, cache, enrichment and health rules as the terminal view. It skips
animation, screen clearing, the banner and update checks, including with
`--clear`. `gh x s --json` is equivalent. `--merged` and `--refresh` still apply.

```bash
gh x status --json | jq .schemaVersion
gh x status --json --refresh --merged=0
```

Example with empty GitHub lists and the merged section disabled. This example
omits worktree items for space; real output includes every worktree:

```json
{
  "schemaVersion": 1,
  "generatedAt": "2026-10-21T02:30:00-04:00",
  "repository": {
    "nameWithOwner": "hemsoft-dev/gh-x",
    "rootPath": "/home/franz/github/hemsoft/gh-x",
    "defaultBranch": "main"
  },
  "health": {
    "defaultBranch": {
      "status": "available", "reason": "", "clean": true,
      "name": "main", "checkedOut": true, "upstream": "origin/main",
      "synced": true, "ahead": 0, "behind": 0,
      "changes": {
        "clean": true, "staged": 0, "modified": 0, "deleted": 0,
        "renamed": 0, "untracked": 0, "conflicted": 0
      }
    },
    "currentBranch": {
      "status": "available", "reason": "", "clean": true,
      "name": "main", "checkedOut": true, "upstream": "origin/main",
      "synced": true, "ahead": 0, "behind": 0,
      "changes": {
        "clean": true, "staged": 0, "modified": 0, "deleted": 0,
        "renamed": 0, "untracked": 0, "conflicted": 0
      }
    },
    "branches": {
      "status": "available", "reason": "", "clean": true,
      "local": 1, "remote": 1, "dangling": 0
    },
    "worktrees": {
      "status": "available", "reason": "", "clean": true,
      "total": 1, "cleanupCandidates": 0, "items": []
    },
    "stashes": {"status": "available", "reason": "", "clean": true, "count": 0}
  },
  "issues": [],
  "pullRequests": {"open": [], "merged": [], "mergedLimit": 0},
  "workflowRuns": [],
  "workflowRunsPerfect": false,
  "sections": [
    {"name": "issues", "status": "available", "reason": "", "cached": false,
      "fetchedAt": "2026-10-21T02:30:00-04:00", "cacheAgeSeconds": 0},
    {"name": "pullRequests.open", "status": "available", "reason": "", "cached": false,
      "fetchedAt": "2026-10-21T02:30:00-04:00", "cacheAgeSeconds": 0},
    {"name": "pullRequests.merged", "status": "disabled", "reason": "", "cached": false,
      "fetchedAt": null, "cacheAgeSeconds": 0},
    {"name": "workflowRuns", "status": "available", "reason": "", "cached": false,
      "fetchedAt": "2026-10-21T02:30:00-04:00", "cacheAgeSeconds": 0}
  ]
}
```

`schemaVersion: 1` is the public contract version, independent of the internal
cache version. Consumers should ignore unknown fields. Adding fields is
compatible; removing fields or changing their types or meanings requires a new
schema version. Arrays are always arrays, including when empty. Unknown optional
timestamps and `aiClean` are `null`. Timestamps use RFC 3339; producers emit UTC.
The example uses an Eastern offset for readability.

GitHub failures do not fail the command. The matching `sections` entry becomes
`unavailable` with a `reason`; local health and other lists remain available.
Enrichment failures also make that section unavailable while retaining its rows.
Read the section status before treating an empty list as an empty backlog.
Fatal local collection errors, including a non-Git directory, return a nonzero
exit code with `{"schemaVersion":1,"generatedAt":"...","error":{"code":"status_failed","message":"..."}}`.
Diagnostics may appear on stderr; stdout contains only the document.

See the [version 1 field reference](docs/status-json.md) for every field, cache
provenance, display values and failure semantics.

## What `gh x pr list` adds

Compared to `gh pr list`, this command keeps all existing filters but renders a denser, color-coded table:

| Column   | Description |
|----------|-------------|
| **#**    | PR number — clickable link to the PR on GitHub (terminals with OSC 8 support) |
| **Issues** | Issues linked through GitHub's closing relationship. `-` means none and `?` means relationship data was unavailable |
| **Title**| Truncated to 51 chars |
| **Author**| PR author login |
| **State**| `open`, `draft`, `closed`, or `merged` |
| **Rev**  | Overall review decision: `✓` approved, `✗` changes requested, or `•` review required |
| **AI**   | AI reviewer status: `pass` (approved/no issues), `fail` (issues found), `?` (review data unavailable), or `-` (no AI review). Detects CodeRabbit, Copilot PR reviewer, connected Codex, other `[bot]` reviewers, and Cubic's AI reviewer check. Copilot passes only when its current-head summary explicitly recommends approval; `Needs a closer look` and `Changes recommended` fail |
| **Appv** | Count of unique formal approvals, including bot reviewers |
| **Checks**| CI status: `pass`, `review`, `fail`, `pending`, `merge`, or `-`. `review` (green) means every non-review check passed and only a recognized AI reviewer check remains. `merge` (red) indicates merge conflicts with the base branch. Includes required checks from repo rulesets that haven't reported yet |
| **Cmts** | Review thread resolution: `resolved/total` (e.g., `3/5`). A trailing `!` marks a clean AI verdict with no unresolved AI threads. `-` means no threads, and `?` means thread data was unavailable |
| **Branch**| Head branch name |
| **Upd**  | Relative time: `12m`, `3h`, `2d`, `4mo` |

An open or draft pull request with formal approvals adds one green detail line
per unique approver beneath its table row. Each line shows the approver login,
the latest active approval in local time with its time-zone label, and the
relative age, for example `@octocat approved 2026-09-22 09:15 AM EDT (2h ago)`.
The same details appear in the open pull request section of `gh x status`.
`--json` returns these records in the `approvers` array with `login`,
`approvedAt`, and `age` fields instead of adding formatted text to JSON output.

### Supported flags

| Flag | Description |
|------|-------------|
| `-R, --repo OWNER/REPO` | Target a specific repository |
| `-L, --limit N` | Maximum PRs to show (default: 30) |
| `-s, --state STATE` | Filter: `open`, `closed`, `merged`, `all` |
| `-A, --author USER` | Filter by PR author |
| `-a, --assignee USER` | Filter by assignee |
| `--app APP` | Filter by GitHub App |
| `-B, --base BRANCH` | Filter by base branch |
| `-H, --head BRANCH` | Filter by head branch |
| `-l, --label LABEL` | Filter by label (repeatable) |
| `-S, --search QUERY` | GitHub search syntax |
| `-d, --draft` | Show only draft PRs |
| `-w, --web` | Open in browser |
| `--json` | Output as JSON |

### Examples

```bash
gh x pr list
gh x pr list --author "@me" --state all
gh x pr list --repo owner/repo --limit 10
gh x pr list --label bug --label urgent
gh x pr list --search "review:required status:success"
gh x pr list --json
```

An empty, unfiltered open backlog prints one randomly selected celebration from
the shared pool. Narrowed or historical queries keep the factual
`No pull requests found.` message, and JSON output remains unchanged.

### Unknown supplemental data

The relationship, AI, and comment columns depend on a supplemental GraphQL
query that runs after `gh pr list`. When GitHub cannot serve that query, the
affected rows show `?` instead of guessing: a failed fetch never renders as an
empty relationship or a clean review. One pull request with a failed alias no
longer hides the data GitHub did supply for the rest of the batch; only the
failed aliases stay unknown.

Every affected table prints one diagnostic line explaining the reason, for
example `gh api graphql: You have exceeded a secondary rate limit...`. In
`--json` mode the stdout array stays machine-readable and the diagnostic goes
to stderr. The message carries gh's own error text, which contains request
results and standard CLI messages, not credentials.

A completed current-head AI review still decides the `AI` column on its own:
a review with an unresolved actionable finding reports `fail` even when other
enrichment is unknown, and pending CI keeps reporting `pending` independently
of supplemental enrichment. Connected Codex counts as clean only when its
`Completed` activity row names the current head and the connector adds a later
thumbs-up reaction to the pull request. That reaction does not count as a formal
approval.

## What `gh x monitor` adds

`gh x monitor` is an interactive, read-only dashboard for pull requests and
issues across all repositories owned by the active account and its organizations.
“All repos” discovers those owners on every refresh, including repositories
absent from the configuration. Configured repositories are optional sidebar
shortcuts, each with its own query, counts and row limit. A busy repository
cannot crowd another shortcut out. The sidebar selects a repository in
terminals at least 100 columns wide; compact terminals show the repository in
the header instead. The top tabs switch between pull requests and issues,
configurable sections apply GitHub search filters, and the detail pane shows
the selected item's metadata and scrollable body.
Issue rows include the immediate parent and direct sub-issue progress. The
issue detail pane repeats both values.

```bash
gh x monitor                 # launch the dashboard
gh x m                       # short alias
gh x monitor --print-query   # discover owners and print account-wide queries
```

The first launch creates `config.yml` in the operating system's user config
directory. When possible, it seeds `repos` with the repository in the current
working directory.

| Platform | Configuration file |
| --- | --- |
| Windows | `%AppData%\gh-x\config.yml` |
| macOS | `~/Library/Application Support/gh-x/config.yml` |
| Linux and other Unix systems | `${XDG_CONFIG_HOME:-$HOME/.config}/gh-x/config.yml` |

The repository's `src/cfg.yml` is development data and is not read by
`gh x monitor` in production.

The dashboard supports terminals from 60×16 cells and adapts its columns to
80×24, 120×40, and larger windows. Compact tables keep titles and decision
fields readable; details retain the remaining fields. The monitor asks the
terminal for its background color and selects a dark or light palette. It
starts with the dark palette if the terminal does not answer. Set `NO_COLOR=1`
for text-only status cues, bold focus, and reverse selection. This preserves
alternate-screen, keyboard, and mouse behavior.

| Keys | Action |
| --- | --- |
| `tab` / `shift+tab` | Focus the list, details, or repository navigation. |
| `j` / `k`, arrows, `g` / `G` | Move within the focused pane or jump to its beginning/end. |
| Left / Right | Switch PR and issue tabs; select All open when configured. |
| `1`–`9` | Select a configured section. Sections also support mouse clicks. |
| `/`, Enter, Escape | Type a local filter, apply it, then clear it. |
| Page Up / Page Down | Scroll the selected item's details. |
| `s`, `e`, `?`, `r` | Open settings, edit YAML, show help, or refresh. |
| `o`, `y`, `Y` | Open the selected URL, copy it, or copy its checkout/development command. |
| `q`, Ctrl+C | Save selections, cancel any refresh, and restore the terminal. |

Mouse clicks select tabs, sections, rows, and panes; the wheel moves within the
focused pane. In compact layouts, focus repositories with `tab` and use `j`/`k`,
or click the header to cycle repositories. Settings mark their active field
with `>` and validate before saving. Selection remains visible when another
pane has focus. Long details scroll with a pinned title and position indicator.
The [monitor design record](docs/monitor-design.md) explains the stack and layout.

### Monitor configuration

```yaml
version: 1

repos:
  - hemsoft-dev/gh-x
  - ghe.example.com/acme/platform

defaults:
  limit: 30
  interval: 10m

prSections:
  - title: "Mine"
    filters: "is:open author:@me"
  - title: "Review requests"
    filters: "is:open review-requested:@me"
    limit: 20
  - title: "All open"
    filters: "is:open"

issueSections:
  - title: "Assigned to me"
    filters: "is:open assignee:@me"
  - title: "All open"
    filters: "is:open"
```

| Field | Meaning |
| --- | --- |
| `version` | Configuration schema version. The current value is `1`. |
| `repos` | Optional sidebar shortcuts as `OWNER/REPO` entries. Use `HOST/OWNER/REPO` for GitHub Enterprise Server. An empty list still supports All repos. |
| `defaults.limit` | Default rows fetched per section, from 1 to 100. The generated default is 30. |
| `defaults.interval` | Auto-refresh cadence as a Go duration, from `10s` to `6h`. The generated default is `10m`. |
| `prSections` | Pull request sub-tabs. Each entry needs `title` and `filters`; optional `limit` from 1 to 100 overrides the default. |
| `issueSections` | Issue sub-tabs with the same `title`, `filters`, and optional `limit` fields. |

Section filters use GitHub search syntax and apply to both the account-wide
view and sidebar shortcuts. All repos adds the active account and every
organization returned by that host’s authenticated GitHub account, with
pagination. Shortcut queries add their configured repository scope. Section
filters cannot contain `repo:`, `user:`, or `org:` qualifiers: GitHub's classic
search unions those qualifiers rather than narrowing the monitor's scope. GitHub.com
repositories use the compact `OWNER/REPO` form.
Enterprise repositories keep their host prefix and are queried through that
host's authenticated `gh` account. If no shortcuts are configured, the monitor
uses `GH_HOST` or `github.com`. Owner discovery failures report an error and
retain the last account-wide snapshot while successful shortcuts remain
available. Failed shortcut queries also retain their own last snapshot and
show an unavailable state. All repos never falls back to a configured selection.
Each section still obeys its row limit; the footer shows loaded and matching
counts. If either section list is empty or omitted,
the monitor restores its generated default sections.

Press `s` to edit repositories, the default row limit, and the refresh interval
inside the dashboard. Press `e` to edit the complete YAML file with `$VISUAL`,
then `$EDITOR`, or the platform default editor. The monitor reloads the file
when the editor exits. A separate `state.json` in the same directory remembers
the selected tab, section, and repository; it is not a configuration source.

`--print-query` performs authenticated owner discovery before printing the
account-wide search documents. It does not fetch PRs or issues. Owner batches
contain at most 16 user and organization qualifiers, and multi-document output
labels each host and operation.

### GitHub request timeouts

Every one-shot command bounds its complete GitHub operation, including account
discovery and fallback attempts, to 30 seconds. Set `GH_X_GITHUB_TIMEOUT` to a
positive [Go duration](https://pkg.go.dev/time#ParseDuration) to override it:

```powershell
$env:GH_X_GITHUB_TIMEOUT = "45s"
gh x issue list --repo hemsoft-dev/gh-x
```

Each monitor refresh has a separate 45-second total deadline controlled by
`GH_X_MONITOR_REFRESH_TIMEOUT`. Host queries run concurrently within that
single budget. If one host times out, data from successful hosts remains visible
with a short host-qualified warning. If the whole refresh times out, the monitor
clears its refreshing state, retains the last successful snapshot, and reports
the timeout. Quitting the monitor cancels and waits for the in-flight `gh` child
instead of leaving it running. Invalid, zero, or negative duration settings fail
with a configuration error; they never disable the deadline.

## What `gh x pr me` adds

Your open PRs, authored or assigned, plus all open Dependabot PRs across every
repo in the selected organization or personal account. Dependabot PRs appear
even when they are unassigned. The combined results show each PR once, ordered
by most recent update, with `--limit` applied to the whole list.

```text
#   Title                                      Repo   Author State Rev AI   Appv Checks Cmts Upd
#42 feat: add repo governance (CI lint, Cop... my-app jdoe   open  •   fail 0    fail   0/1  2d
#15 fix: update auth token refresh logic       api    bsmith open  •   -    0    pass   3/3  5d
```

Works with both organizations and personal accounts.

### `me` flags

| Flag | Description |
|------|-------------|
| `-o, --org ORG` | Organization or user to search (default: inferred from current repo) |
| `-L, --limit N` | Maximum PRs to show (default: 30) |
| `--json` | Output as JSON |

### `me` examples

```bash
gh x pr me                           # my PRs and Dependabot PRs across current owner
gh x pr me --org AcmeCorp            # the same selection in a specific org
gh x pr me --org octocat             # the same selection in a personal account
gh x pr me --limit 10                # combined list capped at 10
gh x pr me --json                    # the same enriched results as JSON
```

## What `gh x pr atm` adds

An org-wide view of PRs that need your attention — no more checking each repo individually.

```text
#   Title                                      Repo   Author State Rev AI   Appv Checks Cmts  Upd
#42 feat: add repo governance (CI lint, Cop... my-app jdoe   open  •   fail 0    fail   0/1   2d
#41 feat: add contract-testing for PactNet...  my-app jdoe   open  •   pass 0    pass   12/12 2d
```

By default, shows open PRs you authored across the org.
Use `--review-required` to see PRs awaiting your review.

### `atm` flags

| Flag | Description |
|------|-------------|
| `-o, --org ORG` | Organization to search (default: inferred from current repo) |
| `-L, --limit N` | Maximum PRs to show (default: 30) |
| `-r, --review-required` | Show PRs where your review is requested |
| `--json` | Output as JSON |

### `atm` examples

```bash
gh x pr atm                              # my PRs across current org
gh x pr atm --org AcmeCorp                # my PRs in a specific org
gh x pr atm --review-required            # PRs awaiting my review
gh x pr atm --org AcmeCorp -r --limit 10   # review requests, capped
gh x pr atm --json                       # machine-readable output
```

## What `gh x pr review` adds

Runs a PR review through an agentic CLI. The command resolves PR metadata with
`gh pr view`, builds a review prompt, and delegates analysis to a provider
preset. By default it prints the agent's read-only review locally. With
`--post`, it captures structured findings and posts one GitHub PR review with a
formal Markdown body and validated inline review comments.

Default provider is `codex` with model `gpt-5.5`, high reasoning effort, and
`strict` review mode. These are configurable with flags or environment
variables.

Approvals are opt-in. `--allow-approve` implies posting a review, and approval
is only selected in `strict` mode when the structured result is explicitly
approval-eligible and has no critical, medium, or nitpick findings. Critical
findings post as `REQUEST_CHANGES`; other findings post as `COMMENT`.

### `review` flags

| Flag | Description |
| --- | --- |
| `-R, --repo OWNER/REPO` | Target a specific repository |
| `-a, --agent AGENT` | Provider preset: `codex`, `claude`, `copilot`, `gemini`, `opencode`, or `custom` |
| `--command COMMAND` | Custom command template for `--agent custom` |
| `-m, --model MODEL` | Model passed through to supported providers |
| `--effort EFFORT` | Reasoning effort for supported providers: `low`, `medium`, or `high` |
| `--mode MODE` | Review mode: `strict`, `medium`, or `fast-lane` |
| `--preset MODE` | Alias for `--mode` |
| `-B, --base BRANCH` | Override the base branch in the review prompt |
| `-i, --instructions TEXT` | Additional review instructions |
| `--instructions-file FILE` | Read additional instructions from a file |
| `--reviewer NAME` | Reviewer identity used in posted review reports |
| `--dry-run` | Print the resolved command and prompt without running the agent |
| `--post` | Post a GitHub PR review with inline comments |
| `--allow-approve` | Allow strict-mode approval when the review has no findings |

### `review` examples

```bash
gh x pr review
gh x pr review 42 --mode strict
gh x pr review 42 --mode medium
gh x pr review 42 --mode fast-lane
gh x pr review 42 --post
gh x pr review 42 --post --allow-approve
gh x pr review 42 --agent claude --model sonnet
gh x pr review 42 --agent copilot
gh x pr review 42 --dry-run
GH_X_PR_REVIEW_AGENT=claude GH_X_PR_REVIEW_MODE=medium gh x pr review 42
GH_X_PR_REVIEW_POST=true GH_X_PR_REVIEW_ALLOW_APPROVE=true gh x pr review 42
gh x pr review 42 --agent custom --command 'my-reviewer --prompt "{prompt}"'
```

## What `gh x run list` adds

Workflow run listing with clickable run IDs — ctrl-click any ID to open
the run in your browser.

```text
   Title                                     Workflow             Branch                    Event         ID           Elapsed  Age
✓  feat: add pr subcommand group for mul...  Copilot Setup Steps  v0.4.0                    push          25714589506  10s      19m
✓  feat: add pr subcommand group for mul...  Auto Release         main                      push          25714553634  45s      20m
X  feat: comprehensive quality improvem...   CI Quality Gates     feature/quality-imp...     pull_request  25708704123  1m43s    3h
```

| Column     | Description |
|------------|-------------|
| **Status** | `✓` success, `X` failure, `*` in progress, `○` queued, `!` cancelled |
| **Title**  | Commit or PR title (truncated) |
| **Workflow** | Workflow name |
| **Branch** | Head branch |
| **Event**  | Trigger: `push`, `pull_request`, `dynamic`, etc. |
| **ID**     | Run ID — clickable link to the run on GitHub (OSC 8) |
| **Elapsed** | Run duration: `10s`, `1m56s`, `2h15m` |
| **Age**    | Time since created: `5m`, `2h`, `3d` |

### `run list` flags

| Flag | Description |
|------|-------------|
| `-R, --repo OWNER/REPO` | Target a specific repository |
| `-L, --limit N` | Maximum runs to show (default: 20) |
| `-s, --status STATUS` | Filter: `queued`, `in_progress`, `completed`, `success`, `failure`, etc. |
| `-w, --workflow NAME` | Filter by workflow name |
| `-b, --branch BRANCH` | Filter by branch |
| `-e, --event EVENT` | Filter by event type |
| `-u, --user USER` | Filter by user who triggered the run |

### `run list` examples

```bash
gh x run list                                 # recent runs
gh x run list --status failure                # failed runs only
gh x run list --workflow "CI Quality Gates"   # specific workflow
gh x run list --branch main --limit 10        # main branch, last 10
gh x run list --event pull_request            # PR-triggered runs
```

## Changelog

View release notes directly from the CLI:

```bash
gh x pr changelog                     # last 5 releases
gh x pr changelog --limit 10          # last 10 releases
gh x pr changelog --version 0.3.0     # specific version
```

The currently installed version is marked with `← installed`.

## Checking for updates

```bash
gh x version
```

```text
gh-x v0.1.2 © 2026 HemSoft Developments · gh extension install hemsoft-dev/gh-x
✓ Up to date
```

If a newer release exists:

```text
gh-x v0.1.0 © 2026 HemSoft Developments · gh extension install hemsoft-dev/gh-x
↑ v0.1.2 available · gh extension upgrade gh-x
```

## T3 Code project icon

[t3.json](t3.json) selects [the gh-x SVG](assets/gh-x.svg) as this repository's
project icon in T3 Code. The image is self-contained and uses a path relative to
the checkout, so the configuration also works in worktrees. Leave the project's
icon selection on Automatic to use it.

## Local development

Requires Go 1.26.7 or newer. CI reads the exact patched version from
`go.mod`; local builds with the default `GOTOOLCHAIN=auto` setting download it
when the installed Go toolchain is older.

```bash
# Build and install locally (one-time symlink setup)
go build -o gh-x.exe ./src   # Windows
go build -o gh-x ./src       # macOS/Linux
gh extension install .

# After code changes, just rebuild — no reinstall needed
go build -o gh-x.exe ./src
gh x pr list
```

A convenience script is provided for Windows:

```powershell
.\build.ps1   # runs vet → test → build
```

### Performance measurements

Run `node benchmarks/run.mjs` for offline synthetic rendering, monitor and Codex
snapshot benchmarks. The [performance guide](benchmarks/README.md) documents
the fixtures, versioned budgets, sampling policy and retained CI artifacts.

### Dependency updates

Dependabot checks Go modules and GitHub Actions every Monday at 09:00 Eastern.
Minor and patch updates are grouped by ecosystem, with at most two open version
update pull requests per ecosystem. Major-version updates are intentionally
deferred to dedicated migration pull requests. Compatibility-sensitive `v0`
minor updates receive the same manual review as a major update; dependency pull
requests are never auto-merged.

### Local usage dashboards

The repository includes launchers for the local Copilot CLI and Codex CLI
usage dashboards:

```powershell
.\run.ps1        # open the Copilot dashboard in the active Copilot Canvas
.\run-codex.ps1  # open the Codex dashboard in a standalone window
.\run-dashboard-hub.ps1  # open the combined local dashboard hub
```

The Codex dashboard reads `~\.codex\state_5.sqlite` and local rollout JSONL
files in read-only mode. It shows recent sessions, token and cache totals,
requests, tool calls, context usage, runtimes, the latest weekly rate-limit
snapshot, and projected usage at reset based on the current pace. Changed
session rows glow after each refresh. The server listens only on `127.0.0.1`
behind a random URL token. The Codex launcher starts that server in the
background and opens it in a dedicated Microsoft Edge or Google Chrome app
window. Later launches reuse the running local server. Both dashboard servers
reject malformed HTTP request targets with a 400 response and keep serving
healthy requests.

The Codex Canvas, standalone server and dashboard hub run snapshot readers in
child processes, with at most two readers per provider or Canvas instance.
Healthy readers stay warm to reuse their caches. A disconnected request or 30-second read deadline stops its
reader process; capacity is released only after that process exits. New
refreshes can then start even if an older reader ignored cancellation or
blocked its event loop. Server shutdown also stops active and idle readers. Each process keeps its IPC
control loop separate from the computation thread. If its parent dies, that
control loop terminates the whole reader process even when computation blocks.
Injected readers used by tests must honor the supplied `AbortSignal`; merely
abandoning their promise does not free capacity.

For phone access over Tailscale, install the persistent loopback-only hub once:

```powershell
.\install-dashboard-hub.ps1
```

The installer checks the current device's DNS name, IPv4 address and Serve
configuration before starting the hub or changing its logon task. It registers
`HemSoft CLI Dashboard Hub`, mounts `/dashboards/` over HTTPS and adds a raw
TCP forward on tailnet port 80. It prints each URL only after that operation
succeeds. Use the printed HTTPS URL from another tailnet device, or the printed
HTTP fallback when that client cannot resolve MagicDNS. Neither route is
exposed to the LAN or public internet.

Each Tailscale operation must succeed within 30 seconds. A failed command names
the operation and exit code. A timeout reports unverified completion and keeps
captured stderr for diagnosis. If HTTPS succeeds but TCP fails, installation
returns a nonzero result and reports that HTTPS and the logon task remain.
Correct the named failure and rerun the installer. It never resets Serve or
deletes scheduled tasks during recovery. Unrelated mounts remain; conflicting
`/dashboards`, port 80 or non-HTTPS port 443 targets cause preflight to fail.
Active foreground sessions on those ports or this HTTPS host also fail
preflight. A matching TCP destination is accepted only as a plain forward;
TLS termination, PROXY protocol and unknown handler settings are not overwritten.
Extra settings on the requested HTTPS mount, including application-capability
forwarding, fail preflight rather than being lost. Unrelated mounts are untouched.
Port 443 with public Funnel access also fails preflight; the installer neither
publishes private usage through it nor disables unrelated public routes.
A same-named task is updated only when its sole action runs this exact launcher
under the current user. Use `-TaskName` to avoid an unrelated task's name.
Do not change Serve configuration concurrently with installation; preflight and
route writes are separate native operations, not an atomic transaction.

The hub reads Codex state directly and uses the installed `copilot-spend`
extension's read-only session-store adapter and current UI. Installer regressions
run offline in CI and in the local audit:

```powershell
pwsh -NoProfile -File tests/installer/test-dashboard-hub.ps1
```

These tests use a native command double and scheduler mocks. They never contact
Tailscale services or register real tasks. Required CI checks run PowerShell 7
on Ubuntu and Windows PowerShell 5.1 on Windows. On Windows, the same fixtures can
also exercise the Windows PowerShell 5.1 installer launcher:

```powershell
pwsh -NoProfile -File tests/installer/test-dashboard-hub.ps1 `
  -InstallerPowerShell "$env:WINDIR/System32/WindowsPowerShell/v1.0/powershell.exe"
```

The same required Windows job installs Node from `.node-version` and runs the
standalone Codex dashboard launcher test under PowerShell 7:

```powershell
node --test src/codex-dashboard/launcher.test.mjs
```

Both launcher tests must pass with zero skips on Windows. The integration test
uses a temporary Codex home and a synthetic browser command, starts and reuses
its test-owned server, then stops it and removes the temporary files. It never
opens a real browser or reads personal session data. Linux retains the Windows
lifecycle skip; local audit success on Linux cannot qualify this hosted check.
The existing Windows PowerShell 5.1 installer checks remain required.

## How it works

- Wraps `gh pr list --json` for core PR data and authentication
- Makes a single GraphQL call for supplemental data (review threads, AI reviewer detection, comment counts)
- Fetches required status check contexts from repo rulesets to detect pending-but-unreported CI checks
- Uses [termenv](https://github.com/muesli/termenv) for color output, respecting `NO_COLOR` and `CLICOLOR` conventions
- SSH host aliases (e.g., `github-work:org/repo`) are handled gracefully via `gh repo view` fallback

## Quality limits

CI and the local audit enforce the same checked-in measurement limits.
Run the complete local check set from a disposable checkout with PowerShell 7:

```powershell
pwsh -NoProfile -File .agents/skills/perfection/scripts/perfection-audit.ps1
```

The runner reports each gate as `pass`, `fail` or `blocked` and continues
independent checks after failures. A missing tool or unavailable GitHub access
blocks the affected gate, not unrelated work. Failed coverage collection blocks
coverage and CRAP instead of reusing an old profile. Any failed or blocked local
gate makes the audit exit nonzero.

All maintained production Go packages, including the hidden `.github/scripts/...` release
and review helpers, participate in build, vet, static analysis, complexity and
fresh coverage gates. The `./tests/behavior` package runs separately without a
coverage profile. CRAP joins use full relative
file paths and function declaration lines, so helper files and methods with the
same names cannot borrow coverage from one another. No generated Go source is
tracked. `*_test.go` files are excluded from production complexity; no maintained
helper is excluded. Mutation testing retains its separately declared `./src` scope.

The local set includes release-helper tests, all 12 release-target builds,
CLI behavior, dashboard tests, ruleset and changelog validation, mutation
fixtures, and performance tests and budgets. Local performance qualification
always measures the full workload rather than applying CI's changed-path filter.
The runner tests and `.github/local-quality-gates.json` inventory are checked in
CI; an unclassified new or removed CI step fails the inventory test.

Install Go from `go.mod`, Node from `.node-version`, PowerShell 7, Git, GitHub CLI, Bash with its
standard Unix utilities, and the pinned analyzers from `.github/quality-tools.env`.
Race tests require Go's supported C compiler setup. Git Bash supplies the shell
utilities on Windows; CI runs these checks on Ubuntu. Changelog validation needs
read access to this repository's releases through `gh`. The runner does not
install Go analyzers or modify GitHub. Markdown lint uses the pinned `npx` command.
Install the locked JavaScript development tools with `node .github/scripts/dashboard-quality/install.mjs`
before running the audit.

A JSON report and performance evidence are retained in a unique system temporary
directory, printed at completion. Use `-ReportPath <existing-directory>/audit.json`
to save the report elsewhere. Build outputs and the coverage profile are removed
from that temporary directory; no generated output is placed in the checkout.

Local success does not replace GitHub qualification. The report separately lists
CodeQL setup/analysis, PR dependency review, current-head Codex review and the
required aggregate `Quality Gate` as hosted checks not run locally. The audit
never uploads source, requests reviews, posts comments or dispatches workflows.

Dashboard JavaScript also has an all-source coverage and per-function complexity
gate, with reviewed aggregate/per-module coverage floors and worst-function CRAP
ceilings. CRAP above 30 fails; scores from 15 through 30 form the review queue.
The [dashboard quality policy](docs/dashboard-quality.md) documents exact tools,
scope, source-range joins, zero-coverage entry modules, baselines and retained
failure reports. These JavaScript limits are separate from the Go limits below.

| Gate | Limit |
| --- | ---: |
| Statement coverage | at least 70% |
| Cyclomatic complexity | at most 10 per function |
| Cognitive complexity | at most 15 per function |
| CRAP score | below 30 per function |
| Mutation efficacy | at least 90% |
| Mutator coverage | at least 90% |

The mutation gate runs Gremlins v0.6.0 against `./src` and keeps the killed,
lived, not-covered, efficacy, and mutator-coverage metrics in its output:

```powershell
gremlins unleash --timeout-coefficient 10 --threshold-efficacy 90 `
  --threshold-mcover 90 ./src
```

The tool version, thresholds, and package scope live in
`.github/quality-tools.env`. Raise a mutation floor only after the complete
Perfection audit establishes a stable higher baseline. Update that file, the
mutation fixture expectations, this table, and the Perfection examples in the
same change. Never lower a floor to make a failing change pass.

## Releases

Every qualifying push to `main` runs the CI quality workflow. After all gates
pass, the release workflow examines every commit since the latest reachable
semantic-version tag and applies the highest required bump. The next version
starts from the highest semantic-version tag anywhere in the repository, so a
tag on another history cannot be reused:

- Subjects using `type!:` or `type(scope)!:`, or commit bodies with a
  `BREAKING CHANGE:` or `BREAKING-CHANGE:` footer, produce a major release.
- `feat:` and scoped `feat(scope):` subjects produce a minor release.
- Every other subject produces a patch release.

The workflow skips a commit that already has a semantic-version tag. Changes
limited to Markdown, `.agents/**`, or `LICENSE` do not start a release. GitHub
Releases is the authoritative version history. After publishing, the workflow
opens a changelog update and enables squash auto-merge for its exact head.
Only same-repository `github-actions[bot]` PRs targeting `main`, using a
`chore/changelog-X.Y.Z` branch, and modifying only `CHANGELOG.md` qualify.
Every pull request to `main`, including Dependabot updates and generated
changelogs, must pass the read-only `Current-head Codex Review` job before the
required `Quality Gate` succeeds. Ordinary pull requests are bound by event
number, branch, full head SHA, and ordered pull-request history. The job runs
the trusted default-branch helper and accepts only clean evidence attributable
to that exact head. Missing, stale, truncated, ambiguous, refused, superseded,
or unresolved evidence cannot pass.

### Supported release targets

`.github/release-targets.json` is the canonical release matrix. Pull-request CI
compiles every entry into a disposable directory before `Build & Test` can pass;
it does not upload those files. Auto Release uses the same manifest and builder
to create the published assets.

| Operating system | Architectures | Assets |
| --- | --- | --- |
| Darwin | amd64, arm64 | `darwin-amd64`, `darwin-arm64` |
| FreeBSD | 386, amd64, arm64 | `freebsd-386`, `freebsd-amd64`, `freebsd-arm64` |
| Linux | 386, amd64, arm, arm64 | `linux-386`, `linux-amd64`, `linux-arm`, `linux-arm64` |
| Windows | 386, amd64, arm64 | `windows-386.exe`, `windows-amd64.exe`, `windows-arm64.exe` |

Reproduce the cross-build locally from the repository root:

```powershell
$env:RELEASE_VERSION = "dev"
$env:RELEASE_BUILD_DATE = git show -s --format=%cs HEAD
$env:RELEASE_OUTPUT_DIR = Join-Path $env:TEMP "gh-x-release-targets"
go run ./.github/scripts/release-targets build
```

Each log line identifies the exact `GOOS/GOARCH` target and output asset. A
failed compilation names that target and stops the quality gate. To change the
matrix, edit only `.github/release-targets.json`, update the expected list in
`.github/scripts/release-targets/main_test.go`, and run that package's tests.
Validation rejects empty, duplicate, malformed, unknown-field, and
Go-toolchain-unsupported entries. A tagged retry for a commit that predates the
canonical manifest uses the frozen `.github/release-targets-legacy.json`
snapshot; current CI and releases never use that compatibility file.

Every published binary carries GitHub build provenance created from the same
trusted release job before asset publication. The attestation binds its SHA-256
digest to this repository, the release workflow, and the validated source
commit. The initial attestation covers the complete build before any upload, so
an interrupted publication can upload a missing, already-attested asset without
signing it under a later workflow context. A retry verifies every rebuilt asset
against the repository, trusted workflow, original source commit, and SLSA
predicate before continuing. It runs that reconciliation with the helper from
the immutable trusted workflow revision, never the older released checkout.
Existing release assets are never replaced: a retry reuses bytes with a matching
digest, uploads only missing attested assets, and fails closed on a digest
mismatch or unexpected asset.

After downloading an asset produced after the organization transfer, verify it
with this command:

```powershell
$releaseTag = '<post-transfer-release-tag>'
$sourceCommit = gh release view $releaseTag --repo hemsoft-dev/gh-x `
  --json targetCommitish --jq .targetCommitish
gh attestation verify .\windows-amd64.exe `
  --repo hemsoft-dev/gh-x `
  --signer-workflow hemsoft-dev/gh-x/.github/workflows/auto-release.yml `
  --source-digest $sourceCommit `
  --predicate-type https://slsa.dev/provenance/v1
```

Replace the release tag and asset path with the downloaded release. The command
must exit successfully and name `hemsoft-dev/gh-x`, the trusted release workflow,
and the expected source commit. To verify all downloaded assets, run
the same command once per file. Pre-transfer assets keep their original signing
identity: use `HemSoft/gh-x` for `--repo` and
`HemSoft/gh-x/.github/workflows/auto-release.yml` for `--signer-workflow`
when verifying those historical assets. Keep release download commands
canonical. See the exact options and output fields on the
[`gh attestation verify` manual page](https://cli.github.com/manual/gh_attestation_verify).

Release retries verify the canonical identity first. A pre-transfer certificate
may use the original `HemSoft/gh-x` signer, but is accepted only when its verified
certificate identifies the same immutable repository ID, `1262580000`. An
unrelated repository that later occupies the old name cannot satisfy that check.

Generated changelog reviews retain stricter bot-author, same-repository,
branch-name, and CHANGELOG-only checks on both pull-request and manually
dispatched CI runs. The trusted release workflow requests connected Codex only.
Cubic and other optional products are not requested or awaited; their actionable
findings and unresolved conversations still require assessment.

### Connected requester setup and recovery

Connect the `HemSoft` GitHub account to Codex and enable repository code review,
as described in [OpenAI's GitHub guide](https://learn.chatgpt.com/docs/third-party/github).
Configure the repository Actions secret `CODEX_REVIEW_TOKEN` with that user's
GitHub token, granting pull-request write and contents read/write access. A
fine-grained token also needs repository metadata read access. The helper
requires the repository push permission before allowing a new request.
The helper checks `GET /user` and repository access before posting. This proves
GitHub identity and access; Codex's response proves the account connection.
The default `GITHUB_TOKEN` and installation tokens do not inherit that connection.
Missing credentials or an unsupported identity produce an actionable error.
The user token is supplied only to the review-request command. Release creation
and guarded merge retain the workflow's bot token.

`Auto Release` is the sole automated request owner for generated changelogs,
serialized by its existing non-canceling concurrency group. Request comments
persist the head, author, URL, and GitHub creation time. Verification jobs never
post requests. Ordinary pull requests reuse automatic or manual Codex activity;
a later `HemSoft` `@codex review` comment is bound to the full head through the
ordered pull-request timeline. Duplicate pull-request events replace an older
verifier for the same pull request/head and reuse the original 30-minute
review deadline. Auto Release's manually dispatched verifier uses a run-specific
concurrency lane, so pull-request, edited, and replacement-head events cannot
cancel the exact run that the release job awaits. Before any
current-head activity exists, the ten-minute setup window starts from the later of
the head commit and the triggering pull-request update, so old branches and
newly ready drafts receive one fresh window without a check rerun resetting it.
Refusal comments persist terminal access, quota, plan, or configuration failures;
reruns report the same failure immediately with the response URL and correction.
A timeout derives from the original request time, so reruns cannot reset it.
Auto Release also preserves the remote changelog head when its content is
unchanged, including after main advances. Only changed notes create a new head.

After correcting a refused bot request, a connected `HemSoft` session can run the
helper from a reviewed checkout. Do not run a manual request concurrently with
`Auto Release`; it owns automated requests. In PowerShell:

```powershell
$env:GITHUB_REPOSITORY = 'hemsoft-dev/gh-x'
$env:CHANGELOG_BRANCH = 'chore/changelog-0.11.8'
$env:EXPECTED_HEAD = '<exact 40-character PR head>'
gh api user --jq .login
go run ./.github/scripts/changelog-merge request
go run ./.github/scripts/changelog-merge review
```

The request command reuses existing current-head evidence. It permits one human
replacement of an explicitly refused bot request. Re-requesting after a human
request's refusal or timeout requires an explicit access repair and manual review
decision; automatic reruns never retrigger it. A timeout ends waiting, not the
validity of subsequently available review evidence. A genuine later clean receipt
for the same head can pass verification without another trigger or wait, including
after an old bot refusal. Silence, timeout, or refusal alone can never pass.
Never remove comments or create empty commits to reset a wait.
For an ordinary pull request, rerun the failed checks from the pull request after
clean evidence appears; this reuses the unchanged head without another request.
For a generated changelog, rerun CI with
`gh workflow run ci.yml --ref <branch> -f changelog_branch=<branch> -f changelog_head=<exact-40-character-head>`
after clean evidence appears.
The `enable` command waits for that CI gate and revalidates evidence before it can
queue a merge; `request` and `review` never enable auto-merge.
Auto Release captures the dispatched CI run ID using the versioned GitHub API,
waits for that exact run to succeed, verifies its head, and requires exactly one
successful `Quality Gate` job in that run before calling `enable`. Failure output
identifies the pull request, expected head, authoritative run ID, and a clickable
URL for that exact Actions run. An older failed check or competing pull-request
event cannot cancel or satisfy that run.

To verify a candidate helper revision against an existing bot PR before deployment,
dispatch CI from the reviewed candidate branch with `changelog_branch` and
`changelog_head` inputs. It checks that live PR using a read-only token and the
explicit workflow revision. Normal PR and changelog-branch CI use the trusted
default-branch helper. A changelog-branch run cannot override its own target.
The candidate run's check belongs to the candidate SHA, so it does not replace
the changelog PR's required check. Its final `Quality Gate` deliberately fails
when the candidate ref is neither `main` nor a generated changelog branch;
inspect `Current-head Codex Review` for candidate-helper evidence. Repeat the
dispatch to verify evidence reuse without another comment or review charge.

Repository auto-merge must be enabled. The release workflow rechecks reviews
immediately before queueing and every 30 seconds while queued. Changed or
unavailable evidence, a changed head, or an expired monitoring deadline causes
it to withdraw auto-merge. A withdrawal failure is reported explicitly.
The release workflow waits for the PR
to actually merge before deleting its exact head branch and dispatching CI
on main. It does not bypass branch protection. A failed release run resumes that update
from the existing GitHub Release and current `main`, even when `main` has
advanced; a tag without a release remains a skip. CI also rejects stale release
links in `CHANGELOG.md`. After the bot merge, the workflow dispatches CI on
`main` so intervening product changes remain eligible for release. Each
created release includes prebuilt binaries for the supported operating systems
and architectures.

## License

[MIT](LICENSE)
