# Release changelog preparation

Auto Release creates a real draft changelog PR before attesting binaries or
creating a release, tag or release asset. Its body describes planned notes.
Only successful publication makes the candidate ready for review and changes
its body to describe published notes. Build and attestation still use the
validated upstream CI source SHA, after restoring that checkout.

Creation uses the job's existing `GITHUB_TOKEN`, with `pull-requests: write`.
This permission does not override the repository or organization Actions
PR-creation policy. The workflow does not change policy or use the connected
reviewer's `CODEX_REVIEW_TOKEN` to create PRs.

The [workflow-permissions API][permissions] requires Administration read.
The standard job token has no such permission. GitHub documents no nonmutating
PR-creation capability probe usable with this job's permissions. Reading
unrelated PR metadata cannot prove creation is allowed. Preparing the actual
candidate is therefore the capability test, rather than an administrative API
preflight that would reject otherwise permitted jobs.

If creation is denied, the failed attempt creates no PR and publication stops.
The candidate branch may already exist. API failures, malformed results,
ambiguous PR matches and identity or head mismatches also stop publication.
Errors retain the GitHub diagnostic and identify the blocked operation.

A retry can reuse one same-repository candidate by `github-actions[bot]`, on
the expected branch and exact SHA, modifying only `CHANGELOG.md`. An unpublished
candidate must remain a draft. Reuse preserves its commit and review evidence.
Conflicting retained notes or other changed files require inspection; the
workflow never force-pushes a replacement candidate. A published release whose
changelog is already recorded needs no candidate PR.

After publication, the existing connected HemSoft reviewer identity, current-head
review, authoritative dispatched CI run, exact-head verifier and guarded merge
remain required. A prepared draft alone supplies none of those gates.

## Recovery and authority

For [issue #250][issue], repository and organization reads both reported
`can_approve_pull_request_reviews: false` on October 25, 2026. The inheritance
source and approved correction remain unresolved. An authorized owner must
record the effective [Actions PR-creation policy][policy] and approve its
correction or a separate identity design. This source change does not enable
the policy.

Inspect retained candidate notes and the failed operation before authorizing
recovery. A failed publication leaves a draft with planned notes, not proof of a
published release. If publication already happened, reconcile that release's
notes and attested assets under separate authority. Do not blindly rerun the
historical release, create a duplicate version, borrow the review token or
enable repository auto-merge as a workaround.

Feature pushes and PR CI do not qualify for Auto Release. A main merge can
publish because workflow and helper changes count as release-worthy. Preparing
this patch does not authorize merging or publishing it. Historical recovery,
policy approval and release authority remain separate gates on [issue #250][issue].

[permissions]: https://docs.github.com/en/rest/actions/permissions#get-default-workflow-permissions-for-a-repository
[policy]: https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository#preventing-github-actions-from-creating-or-approving-pull-requests
[issue]: https://github.com/hemsoft-dev/gh-x/issues/250
