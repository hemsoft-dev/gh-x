# Credential scanning

The required Quality Gate depends on the `Credential Scan` job. It runs
[Gitleaks 8.30.1](https://github.com/gitleaks/gitleaks/releases/tag/v8.30.1)
against current source and reachable history. The same fixture and scan gates
run in the local audit.

## Install and run

The version is pinned in `.github/quality-tools.env`. The installer verifies
the downloaded archive against `.github/gitleaks-checksums.txt`, copied from
the upstream 8.30.1 release. CI and the development container's existing
`install-quality-tools.sh all` command install this scanner.

```bash
bash .github/scripts/install-quality-tools.sh credentials
go run ./.github/scripts/credential-scan
go test -race -count=1 -timeout=90s ./.github/scripts/credential-scan \
  -run '^TestGitleaks' -gitleaks-integration
```

For a custom tools directory, including Windows:

```powershell
$tools = Join-Path ([IO.Path]::GetTempPath()) 'gh-x-tools'
go run ./.github/scripts/credential-scan -install $tools
$env:PATH = "$tools$([IO.Path]::PathSeparator)$env:PATH"
go run ./.github/scripts/credential-scan
```

Release archives support Linux, macOS and Windows on amd64 and arm64, plus
Linux/Windows 386. Other platforms need an independently installed scanner at
the exact pin; an unavailable or mismatched tool fails the gate.

## Scope and failures

Run at the repository root. Source acquisition includes tracked working files
and nonignored untracked files. A private temporary snapshot excludes Git
metadata, ignored build/cache output and deleted working files. Symbolic links
are scanned as link text; their targets are never followed outside the checkout.
Submodules and unsupported filesystem entries fail acquisition explicitly.

The history scan uses `--all HEAD --full-history --root -m`. It covers locally
reachable refs and the checked-out head, including root commits, merge-parent
diffs, and a PR merge head's ancestry. Before scanning, the helper verifies that
every head and tag currently advertised by `origin` exists at the matching
local remote-tracking ref or tag. Shallow clones, missing refs, changed remote
refs and unavailable remote metadata fail with an incomplete scope. CI uses
`fetch-depth: 0`; local audits do not fetch or mutate refs. Fetch missing history
and refs explicitly before rerunning:

```bash
# Only when git rev-parse --is-shallow-repository reports true:
git fetch --unshallow origin
git fetch --prune origin '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*'
```

Unreachable objects, reflogs and remote refs the server does not advertise are
outside this scope. The scan uses the pinned
[upstream rules and filters](https://github.com/gitleaks/gitleaks/blob/v8.30.1/config/gitleaks.toml),
including upstream exclusions for generated/vendor files and binary asset
extensions. A clean result means no matches within that declared scope and rule
set. It does not prove that every possible credential format was detected.

There are no project allowlists, baseline suppressions or credential-fixture
exceptions. Synthetic credentials are assembled only inside temporary fixture
repositories. Short placeholders need no exception. `.gitleaksignore` files at
the scan root are rejected because Gitleaks loads them even when a separate
ignore path is supplied. Inline `gitleaks:allow` directives are disabled, and
scanner configuration environment overrides are removed.

Each scanner invocation has a two-minute timeout; the wrapper has a five-minute
limit. Startup errors, malformed/missing reports, inconsistent exit status and
incomplete redaction fail. Raw stdout/stderr are discarded. Private reports use
100% redaction and are deleted with the temporary source snapshot. Public output
contains only scope metadata and finding counts. The
optional `-report FILE` writes that same metadata, never finding values or
matching lines. No scanner finding report is uploaded as a CI artifact.

Fixtures prove that a synthetic token fails in current source, remains detected
after removal from the tip, and cannot be hidden by inline or ignore-file
suppression. Additional tests cover missing refs, shallow history, external
symlinks, bad archives/checksums and unsafe scanner diagnostics. The checked-in
`gitleaks-8.30.1-redacted.json` is an actual pinned-producer response from the
temporary synthetic repository, with both `Secret` and `Match` redacted.

## Baseline and hosted protection

The initial redacted baseline on October 23, 2026 EDT covered
[561dda3](https://github.com/hemsoft-dev/gh-x/commit/561dda3590192fc43cc0c8e9a9a105b29f1eda14),
311 reachable commits and 106 advertised origin heads/tags, with nonshallow
history and zero findings. A working-source scan during implementation also
returned zero findings. PR validation records the final committed scan scope.
No genuine finding required a revocation assessment in this baseline.

GitHub secret scanning and secret-scanning push protection were enabled and
read back as `enabled` through repository REST metadata on October 23, 2026 EDT.
The repository is public and had zero open hosted secret-scanning alerts at that
check. GitHub documents these
[hosted controls](https://docs.github.com/en/rest/secret-scanning/secret-scanning).
They complement the pinned pre-merge scanner and its explicit history scope.

## Triage ownership

Repository maintainers own triage. A finding keeps the gate red. Investigate
locations privately with the same pinned scanner and full redaction; do not paste
token values, matched lines or raw reports into issues, PRs, Discord or artifacts.

For a genuine finding, revoke or rotate it at its issuer first. Privately record
the credential identifier, issuer confirmation, verification time and owner.
Deletion from the tip or history is not revocation evidence. A historical
finding remains a failure until verified remediation and an explicitly reviewed,
narrow exception are implemented. Do not add file-wide or commit-wide skips to
make a scan pass. False positives also require a documented, tested exception
whose negative fixtures prove nearby genuine credentials still fail.

When updating Gitleaks, refresh its pin and committed upstream archive checksums,
regenerate the real-producer capture in a synthetic repository, and run the full
local audit plus current-head CI and review.
