# Status freshness and performance

Interactive status presents local Git results before cache identity checks or
GitHub fetches complete. Each completed remote section appears in the preview
while later requests are still running. The rotating cursor names the active
stage. Cleanup eligibility remains pending until PR evidence and local worktree
inspection finish. The final snapshot keeps the existing section order and
health checks. Redirected output remains a single unanimated snapshot.

The live preview fits within the terminal height, retains the header and newest
results, and notes omitted lines. Completion removes the preview and prints all
rows once. Ctrl-C cancels Git and GitHub subprocesses, removes the indicator,
restores the cursor, and exits with status 130. Available partial results remain
visible. GitHub fetches remain serial and keep their existing timeouts.
Terminal dimensions refresh before repainting; resize handling accounts for
previously displayed rows wrapping at the new width. Account fallback notices
are serialized with the preview and remain above it. Windows virtual-terminal
processing is enabled only for the animation lifetime and restored afterward.
If console mode setup fails, status falls back to the unanimated snapshot.

`gh x status` always reads local Git state. Working changes, branches, worktrees,
stashes and cleanup eligibility are never taken from the remote-data cache.
When the current worktree holds the default branch, its initial status result
also supplies the default-branch summary.

GitHub sections share a private cache in the repository's Git common directory.
Linked worktrees share it. Each repository/authentication/options/color key has
one atomic snapshot file, so a hit reads one file instead of scanning every
snapshot. Schema v5 replaces v4 without reading incompatible snapshots.

Each successful section is fresh for 60 seconds after its fetch completes.
Failures retain their existing unknown/error output and retry after five seconds.
A failed section does not invalidate successful sections or extend their original
freshness deadlines. Rate-limit errors wait at least 60 seconds; numeric
`Retry-After` and `X-RateLimit-Reset` hints in the reported error extend that wait.
Healthy sections still refresh when they expire during a longer cooldown.
Writers take a nonblocking operating system file lock before merging and replacing
the snapshot. Each section keeps its newest fetch timestamp, data, errors and
retry deadline, including expired results. Pruning retains snapshots until their
latest section retry deadline, even across options or color changes. A contended writer skips publication;
readers continue using atomic snapshots without taking the lock.

`gh x status --refresh` explicitly bypasses the status snapshot and section
cooldowns. API quota cooldowns remain enforced as described below.

The authentication fingerprint includes configuration, token overrides and
credential-store identities. GitHub CLI prefers a configured active token over
its keyring, so status does the same. Fallback-account keyring entries remain
fingerprinted because `gh auth token --user` prefers secure storage. Unknown
identities disable caching. No credential is written to either cache.

Automatic update checks cache successful release tags for one hour, separately
from repository data and under an authentication/host key. It includes the CLI
default API host and repository-derived fallback-account context. Run `gh x version`
to force a fresh release check. Cache failures never prevent the command from
running. `GH_X_CACHE_DIR` overrides the automatic-update cache root for isolated
fixtures; otherwise it uses the operating system's user cache directory.

A `GH_REPO` override keeps its explicit host, or lets GitHub CLI select the host
when unqualified. Otherwise status respects the locally selected repository host.

Set `GH_X_STATUS_DEBUG=1` to report `hit`, `miss`, `refresh`, `section-refresh`
or `identity-unavailable` on stderr. It reports no credential values.

Status shares one repository target and one branch-rules lookup across its PR
sections. The target retains the selected repository's host, including
`gh repo set-default`; unresolved local targets keep GitHub CLI's contextual
selection. It fetches issue relationships and hierarchy together while retaining
field-specific incomplete-data handling. It still makes requests serially.
If an older Enterprise schema rejects the hierarchy fields, status retries
relationships separately and keeps hierarchy data visibly unavailable.

Merged discovery queries the repository connection with only `number`,
`updatedAt` and `mergedAt`, ordered by update time. Candidate windows expand from
25 to at most 1,000 rows while retaining every page and its continuation cursor.
Selection uses actual merge time. It stops only after exhausting the connection
or proving that the update-time boundary cannot hide a newer merge. An
unprovable result at the cap fails visibly.

Only selected rows fetch metadata, reviews and checks. The status/list path
combines those fields with its existing supplemental query, in batches of twenty
aliases, so it does not add a request to the populated fixture. Check pagination
runs only for those selected rows, and retains the older Enterprise fallback for
`CheckSuite.workflowRun`. Repository-connection discovery preserves behavior after
repository transfers. Query ordering and pagination use the
[GitHub GraphQL schema](https://docs.github.com/en/graphql/reference/objects#repository).

The deterministic fixtures assert these GraphQL operation and row budgets.
They count operations at the injected acquisition boundary, separately from the
subprocess counts recorded by the status benchmark. They are not measurements
of live GitHub HTTP traffic or point cost. Additional selected check pages and an
Enterprise fallback retry are separate operations.

| Fixture | Candidate rows before/after | Rows fetching reviews/checks before/after | List-path operations before/after |
| --- | --- | --- | --- |
| Populated, one merge | 1 / 1 | 1 / 1 | 2 / 2 |
| Expanding, two selected | 52 / 27 | 52 / 2 | 3 / 3 |
| Large, one selected from 125 | 300 / 125 | 300 / 1 | 6 / 5 |
| Unproven at 1,000 cap | 2,575 / 1,000 | 2,575 / 0 | 27 / 12 |

`benchmarks/run.mjs` runs these acquisition regressions and saves their request
counts and query-shape assertions in `merged-selection.stdout.txt`. Existing
populated subprocess budgets remain unchanged at nine cold and eight expired or
forced GitHub commands, with no warm commands. The selected query includes both
metadata and supplemental connections. A synthetic partial supplemental failure
retains metadata; a check-field failure fails closed.

`src/testdata/merged/github-pr240-2026-10-22.json` records one live query through
GitHub CLI 2.101.0 on October 22, 2026 EDT. It contains a one-row candidate page
and selected PR 240, used to qualify the candidate, check and supplemental
parsers against the actual GitHub transport. It contains public PR data and no
credentials.

## Progressive output measurements

On Mini, Linux amd64, the populated acquisition fixture at
`d33df597fb4bbc1e58658a1b5c6d929638084991` produced these seven-sample
medians. Each GitHub transport call has a fixed 50 ms delay. First content
excludes the version banner and spinner. Total duration still includes the
automatic update check.

| Mode | First content ms | Total ms |
| --- | ---: | ---: |
| Cold | 8.081 | 427.785 |
| Warm | 7.654 | 16.767 |
| Expired | 7.538 | 425.975 |
| Forced refresh | 7.411 | 425.977 |
| Partial failure within cooldown | 7.412 | 16.555 |

Separate native PTY captures, including process startup, show first content in
12 to 23 ms with a controlled five-second remote delay. The v0.20.0 baseline
waited 5.20 seconds before any status content. These are fixture measurements,
not live GitHub timings. All existing performance budgets pass.

## Acquisition comparison

The acquisition fixture also records `first_content_ms` separately from total
`ms`. It exercises terminal presentation with a deterministic 120-column,
40-row output sink. First content means the local repository header, excluding
the version banner and spinner. Real terminal captures measure process startup
separately. Existing total-duration and subprocess-count budgets still apply.

On Mini (Linux amd64, AMD Ryzen 5 7640HS, Go 1.26.7), the same populated fixture
at `a088800d9f560f15b883cf52719acb07689c4863` and this change produced these
seven-sample medians. Each GitHub transport call and keyring probe has a fixed
50 ms delay; local Git commands execute against a real temporary repository
with a linked worktree. The production dispatcher includes the automatic updater.

| Mode | Before ms | After ms | gh calls before/after | Git calls before/after | Keyring before/after | New cache outcome |
| --- | ---: | ---: | --- | --- | --- | --- |
| Cold | 728.568 | 425.355 | 11 / 9 | 16 / 15 | 4 / 0 | miss |
| Warm | 119.313 | 16.842 | 1 / 0 | 12 / 11 | 2 / 0 | hit |
| Expired | 728.951 | 424.647 | 11 / 8 | 16 / 15 | 4 / 0 | miss |
| Forced refresh | 728.210 | 424.425 | 11 / 8 | 16 / 15 | 4 / 0 | refresh |
| Partial failure, within cooldown | 121.101 | 15.909 | 1 / 0 | 12 / 11 | 2 / 0 | hit |

Forced refresh improves by 41.7% on this fixture. Git counts cover the status
command seam; gh counts cover every transport invocation, including the updater,
and do not claim to count underlying HTTP pagination requests. Cold samples
empty both fixture-owned caches. Baseline partial-failure samples could reuse
an older successful snapshot; current tests require the failed section's error
to remain visible instead. These controlled timings are separate from real API
latency. See [benchmark instructions](../benchmarks/README.md) to reproduce them.

### API quota cooldowns

Raw `gh api` acquisitions request response headers and strip them before JSON
parsing. Primary exhaustion records `x-ratelimit-reset`; secondary limits honor
`Retry-After`, with exponential fallback from one minute to a fifteen-minute cap.
HTTP-200 GraphQL rate-limit error envelopes follow the same path, following
[GitHub quota guidance](https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api#exceeding-the-rate-limit).
A successful
response consuming the final point still supplies its data.

Cooldowns live in the user's cache directory under `gh-x/api-quota-v1`. Each
filename hashes the host, API resource and authentication context. Files contain
only a deadline and retry count, without credentials, response bodies or repository
names. Persistent lock sidecars serialize updates so concurrent responses retain
the longest deadline. A successful response only clears an expired record that
matches its original observation. Later GraphQL acquisitions, including subsequent status sections and other
repositories, stop locally until the deadline. REST core, REST search, code search
and GraphQL requests use independent buckets. Workflow listings honor the core
bucket. Browser-only issue and PR lists remain available during cooldown.
Changing account or host selects a separate cooldown. The base authentication
fingerprint is read once per CLI command; each invocation still mixes in its
credential override. Unavailable identity or
unwritable cache disables persistence rather than reusing another identity's state.

`status --refresh` bypasses the status snapshot, but preserves API cooldowns.
Suppressed sections report unavailable evidence and the reset deadline. Existing
cached sections remain available through the usual cache path. Local Git inspection
continues. No extra rate-limit query is issued.

The transport fixtures cover a thirty-minute primary reset, secondary backoff,
resource/account/host separation and resumption. `src/testdata/quota/gh-2.101.0-http.txt`
was captured from installed GitHub CLI 2.101.0 using a synthetic loopback HTTP
server on October 22, 2026 EDT. It contains no real credentials or GitHub data.
Formatted and paginated CLI calls cannot expose headers through this adapter;
they honor known resource cooldowns and use bounded fallback when their stderr
reports a rate limit without reset metadata.
