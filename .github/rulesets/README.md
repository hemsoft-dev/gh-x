# Repository rulesets

[`main.json`](main.json) is the source configuration for the active
`main-quality-gate` repository ruleset in `hemsoft-dev/gh-x`.

The ruleset targets only `refs/heads/main`. Changes must arrive through a pull
request, the `Quality Gate` check from GitHub Actions must pass, and every
review conversation must be resolved. `Quality Gate` includes the required
clean current-head Codex review for every pull request to `main`; it accepts a
skipped review job only on non-pull-request runs. Integration ID `15368` is the
GitHub Actions app that publishes this repository's check.

The ruleset has no bypass actors and requires zero approving reviews. It does
not dismiss stale approvals after a push, require code-owner review, require
approval of the last push, or require a branch to be current with `main`.

## Deletion and force-push policy

`main` is the only source for releases, so the ruleset also carries two
type-only rules:

- `deletion` (Restrict deletions): nobody can delete `main`.
- `non_fast_forward` (Block force pushes): nobody can rewrite `main`'s history.

Neither rule takes parameters, and with no bypass actors they apply to every
actor, including administrators and automation tokens. Changelog cleanup in
Auto Release deletes only its own `chore/changelog-*` branch, which this ruleset
does not target. Allowing deletion or force pushes on `main` would need a
recorded decision and a validator change; it is not an accepted exception.

The validator requires exactly one `required_status_checks`, one
`pull_request`, one `deletion` and one `non_fast_forward` rule, and nothing
else. It fails when either protection is missing, duplicated or given a
parameters object, and when any other rule type is added.

The source also records the existing live PR settings: all three merge methods
remain allowed, and extra approval for unattributed changes remains enabled
when a nonzero approval count applies. The approval count remains zero. These
values are explicit so applying this file preserves the existing policy.

## Validate and apply

Run the local consistency check before applying the configuration:

```bash
go test ./.github/scripts/...
go run .github/scripts/validate-main-ruleset.go
```

Create the ruleset when it does not exist:

```powershell
gh api --method POST repos/hemsoft-dev/gh-x/rulesets `
  --input .github/rulesets/main.json
```

Apply a change only after its pull request has merged, and only from the
reviewed merged source. Start from a clean checkout of `main` at the merge
commit, then confirm that the file is the reviewed one and still validates:

```powershell
git fetch origin main
git switch --detach origin/main
git status --porcelain   # must print nothing
git log -1 --format=%H -- .github/rulesets/main.json   # the reviewed merge
go run .github/scripts/validate-main-ruleset.go
```

Save a GET of the existing ruleset before applying it. Compare the complete
stored settings and effective rules after the PUT: existing PR and status-check
parameters must be unchanged, and only the two protections should be added.
Preserve the snapshot as evidence; stop and investigate any other policy drift.

Update the existing ruleset by resolving its ID from its stable name:

```powershell
$rulesetId = gh api repos/hemsoft-dev/gh-x/rulesets `
  --jq '.[] | select(.name == "main-quality-gate") | .id'
gh api --method PUT "repos/hemsoft-dev/gh-x/rulesets/$rulesetId" `
  --input .github/rulesets/main.json
```

Read back the stored configuration and the rules effective on `main`. The
stored ruleset must list the four rule types with an empty bypass list, and
`main` must show the same four types from this ruleset:

```powershell
gh api "repos/hemsoft-dev/gh-x/rulesets/$rulesetId" `
  --jq '{enforcement, bypass_actors, include: .conditions.ref_name.include, rules: [.rules[].type]}'
gh api repos/hemsoft-dev/gh-x/rules/branches/main `
  --jq '[.[] | {type, ruleset_id}]'
```

Expected rule types: `required_status_checks`, `pull_request`, `deletion` and
`non_fast_forward`. Compare server-returned fields with the saved pre-apply
snapshot as well as the versioned source, including the PR merge methods and
unattributed-change approval setting. Don't test the protections by deleting
or force-pushing `main`; the readback is the check.

The [GitHub REST ruleset reference](https://docs.github.com/en/rest/repos/rules#update-a-repository-ruleset)
defines the type-only protections and the PR parameters.

Keep the final job name in `.github/workflows/ci.yml` equal to `Quality Gate`.
Changing that name without updating the ruleset would leave pull requests
waiting for a check that never reports.
