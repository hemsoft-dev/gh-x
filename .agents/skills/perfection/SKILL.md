---
name: perfection
description: "V1.3 - Commands: audit, coverage, crap, mutate, simplify, harden. Comprehensive Go code quality enforcement for gh-x: coverage, CRAP, mutation, static analysis and pinned source/history credential scanning. Use when improving code quality, writing tests, or before merging."
compatibility: Requires go 1.23+, git
hooks:
  PostToolUse:
    - matcher: "Read|Write|Edit"
      hooks:
        - type: prompt
          prompt: |
            If a file was read, written, or edited in the perfection directory (path contains 'perfection'), verify that history logging occurred.
            
            Check if History/{YYYY-MM-DD}.md exists and contains an entry for this interaction with:
            - Format: "## HH:MM - {Action Taken}"
            - One-line summary
            - Accurate timestamp (obtained via `Get-Date -Format "HH:mm"` command, never guessed)
            
            If history entry is missing or incomplete, provide specific feedback on what needs to be added.
            If history entry exists and is properly formatted, acknowledge completion.
  Stop:
    - matcher: "*"
      hooks:
        - type: prompt
          prompt: |
            Before stopping, if perfection was used (check if any files in perfection directory were modified), verify that the interaction was logged:
            
            1. Check if History/{YYYY-MM-DD}.md exists in perfection directory
            2. Verify it contains an entry with format "## HH:MM - {Action Taken}" where HH:MM was obtained via `Get-Date -Format "HH:mm"` (never guessed)
            3. Ensure the entry includes a one-line summary of what was done
            
            If history entry is missing:
            - Return {"decision": "block", "reason": "History entry missing. Please log this interaction to History/{YYYY-MM-DD}.md with format: ## HH:MM - {Action Taken}\n{One-line summary}\n\nCRITICAL: Get the current time using `Get-Date -Format \"HH:mm\"` command - never guess the timestamp."}
            
            If history entry exists:
            - Return {"decision": "approve"}
            
            Include a systemMessage with details about the history entry status.
---

# Perfection — Go Code Quality Enforcement

Comprehensive quality enforcement for this Go CLI extension. Every command operates from the repo root and assumes `go.mod` is present.

## Project Context

- **Module**: `github.com/HemSoft/gh-x`
- **Package**: `main` (single package)
- **Source files**: `src/*.go`
- **Test files**: `src/*_test.go`
- **Build**: `go build ./...`
- **Test**: `go test -race -count=1 ./...`
- **Vet**: `go vet ./...`
- **Coverage**: `go test -race -count=1 "-coverprofile=<temp-path>" ./...` then `go tool cover "-func=<temp-path>"`
- **Dashboard tests**: `node --test src/codex-dashboard/*.test.mjs src/dashboard-hub/*.test.mjs`
- **CI workflow**: `.github/workflows/ci.yml` — single source of truth for all quality gates

## Required Tools

CI and the development container use the exact versions in
`.github/quality-tools.env`. Update that file deliberately and run the full
audit before committing a version change. Install the current pins locally
with:

```powershell
# Static analysis
go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
go install github.com/go-critic/go-critic/cmd/gocritic@v0.14.4
go install github.com/kisielk/errcheck@v1.20.0

# Cyclomatic complexity
go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0

# Cognitive complexity
go install github.com/uudashr/gocognit/cmd/gocognit@v1.2.1

# Mutation testing
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Dead code detection
go install golang.org/x/tools/cmd/deadcode@v0.49.0

# Vulnerability scanning
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
```

Check if tools are installed: `Get-Command staticcheck, gocritic, errcheck, gocyclo, gocognit, gremlins, deadcode, govulncheck -ErrorAction SilentlyContinue | Select-Object Name`

Markdown linting runs through the version in `.github/quality-tools.env`; the
current command is `npx --yes markdownlint-cli2@0.20.0 '**/*.md' '#node_modules' '#.agents' '#.github/agents'`.

Install pinned Gitleaks with `bash .github/scripts/install-quality-tools.sh credentials`
or the cross-platform Go installer documented in
[credential scanning](../../../docs/credential-scanning.md). Its upstream archive
checksum is committed alongside the version pin. Credential fixtures and scans
require that exact binary; the local audit does not install missing tools.

## Commands

### `audit` — Full Quality Report

Run `.agents/skills/perfection/scripts/perfection-audit.ps1` with PowerShell 7
from a disposable checkout. It records `pass`, `fail` or `blocked` per local
gate and continues independent work after failures. Missing tools and unavailable
GitHub access block affected gates. Coverage and CRAP require successful fresh
coverage collection. Any failed or blocked local gate exits nonzero.

The authoritative local inventory is `.github/local-quality-gates.json`.
CI tests its step mapping against `.github/workflows/ci.yml` and runs the
PowerShell executor regression tests. The local runner executes:

1. Build locally and cross-build every canonical release target into temporary output.
2. Vet, race-test production Go packages with fresh coverage, explicitly test
   `.github/scripts/...`, and run CLI behavior with `-trimpath -race -count=1`.
3. Run Node dashboard tests and the dependency-free PowerShell runner tests.
4. Check formatting and module tidiness without rewriting source or module files.
5. Validate the ruleset and changelog release links. Changelog access is read-only.
6. Run staticcheck, gocritic, errcheck, deadcode and govulncheck, verifying each
   analyzer's exact `.github/quality-tools.env` pin before its own gate.
7. Scan all Go files for forbidden suppressions and production `fmt.Print*` calls.
8. Run pinned Markdown lint with the same CI exclusions.
9. Enforce statement coverage >=70%, cyclomatic complexity <=10, cognitive
   complexity <=15 and CRAP <30 with both `./...` and the explicit hidden `./.github/scripts/...` scopes.
    Production complexity scans include `.github/scripts`; CRAP joins use full
    relative paths and declaration lines rather than basenames.
10. Run mutation fixtures and Gremlins against the package scope and 90% floors
    in `.github/quality-tools.env`, including integer-ratio boundary checks.
11. Run performance gate tests and all versioned benchmark budgets. Local audits
    always measure; only CI applies changed-path selection.
12. Run dashboard JavaScript quality fixtures and the same all-source c8/ESLint
    measurement as CI. The reviewed policy prevents aggregate/per-module coverage
    loss and worst-function CRAP growth; any function above CRAP 30 fails.
    Scores from 15 through 30 remain visible as the review queue. See
    `docs/dashboard-quality.md` for the per-function V8 branch basis and explicit
    zero-coverage entry-module gaps. Failure reports remain in the audit's
    artifact directory.
13. Exercise real pinned-scanner fixtures, then scan nonignored working source
    and all reachable refs plus HEAD. Require complete origin heads/tags and
    nonshallow history. Publish only scope and counts; a credential finding,
    unavailable scanner or incomplete scan fails qualification.

Prerequisites: PowerShell 7, the Go version in `go.mod`, a supported race-test C
compiler, Node from `.node-version` with npm/npx, Git, authenticated read-only GitHub CLI access, Bash
and its standard Unix utilities. Git Bash supplies those utilities on Windows.
Pinned analyzers must already be installed; the audit never installs them.
Run `node .github/scripts/dashboard-quality/install.mjs` for the locked JavaScript development tools first;
the measurement checks direct pins and the complete secondary dependency graph.

The default JSON report and performance evidence live in a unique system temp
directory. `-ReportPath <existing-directory>/audit.json` selects another report
path. Build and coverage artifacts are cleaned up after all gates. The source
checkout receives no generated audit output.

The Windows standalone launcher lifecycle is a hosted check that a Linux audit
cannot qualify. The local dashboard suite also executes it on Windows; Linux
reports its platform skip.

CodeQL setup/upload, PR dependency review, current-head Codex review and the
aggregate GitHub Quality Gate are listed as hosted-only, not run locally. A
successful local audit never claims those checks passed and performs no GitHub
writes. Test the executor with
`pwsh -NoProfile -File .agents/skills/perfection/scripts/test-audit.ps1`.

Present results as a scorecard table:

```
## Quality Scorecard

| Metric              | Value   | Target  | Status |
|---------------------|---------|---------|--------|
| Build               | pass    | pass    | ✅     |
| Vet                 | pass    | pass    | ✅     |
| Formatting (gofmt)  | pass    | pass    | ✅     |
| Module tidy         | pass    | pass    | ✅     |
| Staticcheck         | pass    | pass    | ✅     |
| Gocritic            | pass    | pass    | ✅     |
| Errcheck            | pass    | pass    | ✅     |
| Dead Code           | 0 funcs | 0       | ✅     |
| Vulnerability Scan  | 0 vulns | 0       | ✅     |
| Lint Suppressions   | 0 found | 0       | ✅     |
| Anti-Patterns       | 0 found | 0       | ✅     |
| Race Detection      | pass    | pass    | ✅     |
| Test Coverage       | 77.8%   | ≥70%    | ✅     |
| Max Cyclomatic      | 4       | ≤10     | ✅     |
| Max Cognitive       | 8       | ≤15     | ✅     |
| CRAP Score          | 4.2     | <30     | ✅     |
| Mutation efficacy   | 100%    | ≥90%    | ✅     |
| Mutator coverage    | 90.17%  | ≥90%    | ✅     |
| Markdown Lint       | pass    | pass    | ✅     |
```

After the table, list the top 3 actionable improvements ranked by impact.

### `coverage` — Detailed Coverage Analysis

Generate and analyze test coverage:

```powershell
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

For each function with <80% coverage:
1. List the function name, file, and current coverage
2. Identify which branches/paths are untested
3. Write the missing test cases using table-driven tests (this repo's established pattern)

**Coverage targets:**
- 🟢 ≥80% — excellent
- 🟡 60-79% — acceptable, improve opportunistically
- 🔴 <60% — requires immediate attention

After analysis, **write the tests** — don't just list what's missing. Follow existing patterns in `main_test.go`:
- Table-driven tests with `t.Run`
- Descriptive test case names
- `t.Fatalf` for assertions (not `t.Errorf` — fail fast)
- Test edge cases: nil inputs, empty slices, boundary values, error paths

Always clean up: `Remove-Item coverage.out -ErrorAction SilentlyContinue`

### `crap` — CRAP Score Analysis

CRAP = Complexity² × (1 - Coverage)³ + Complexity

For each exported and significant unexported function:

1. Get cyclomatic complexity: `gocyclo -over 0 .`
2. Get per-function coverage: `go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out`
3. Calculate CRAP score per function

**CRAP score interpretation:**
- 🟢 ≤5 — clean code, well tested
- 🟡 5-30 — manageable, consider simplifying or adding tests
- 🔴 >30 — high risk: too complex + undertested

Present as table sorted by CRAP score descending:

```
| Function              | Complexity | Coverage | CRAP  | Risk |
|-----------------------|------------|----------|-------|------|
| normalizeCheckState   | 8          | 85%      | 8.0   | 🟡   |
| executeList           | 12         | 0%       | 156   | 🔴   |
```

For any 🔴 function, recommend: simplify first (extract helpers, reduce nesting), then add tests.

Always clean up: `Remove-Item coverage.out -ErrorAction SilentlyContinue`

### `mutate` — Mutation Testing

Mutation testing validates test quality by injecting faults and checking if tests catch them.

**Primary tool**: [gremlins](https://github.com/go-gremlins/gremlins) — the actively maintained Go mutation testing framework used in CI.

```powershell
gremlins unleash --timeout-coefficient 10 --threshold-efficacy 90 --threshold-mcover 90 ./src
```

**Timeout**: Mutation testing is CPU-intensive. CI allows 30 minutes. Locally, expect similar.

**Understanding results**:
- **Killed**: Test suite detected the mutation (good)
- **Survived**: Mutation wasn't caught — test gap exists
- **Not covered**: Mutated code has no test coverage at all
- **Timed out**: Mutation exceeded its test timeout; Gremlins v0.6.0
  reports it separately and excludes it from both ratios

Report format:

```
## Mutation Testing Results

| Metric          | Value |
|-----------------|-------|
| Total mutations | 120   |
| Killed          | 72    |
| Survived        | 18    |
| Not covered     | 30    |
| Test efficacy   | 60%   |
| Mutator coverage | 80%  |
| Efficacy floor  | ≥90%  |
| Coverage floor  | ≥90%  |
| Status          | ❌    |

### Surviving Mutations (tests need strengthening)
1. `normalizeCheckState`: swapping `hasFail` check order — no test catches this
   → Add test: single FAILURE item should return "fail" even with passing items
```

**Targets**: at least 90% mutation efficacy and at least 90% mutator coverage. CI and the local audit enforce both gates.

#### Architectural Boundary: Untested I/O Functions

The following functions are intentionally excluded from mutation testing coverage.
They are thin wrappers around `gh` CLI calls with no branching logic — the parsing
logic they delegate to IS fully tested. Testing them would require mocking
`exec.Command` through the entire call chain, a significant refactor with poor ROI.

**Excluded functions** (0% coverage, ~53 mutations marked "not covered"):

- `executeAtm`, `fetchAtmNodes`, `fetchAtmSingleSearch`, `fetchAtmMultiSearch`, `resolveCurrentUser` (`src/atm.go`)
- `fetchReleases`, `fetchReleaseByTag` (`src/changelog.go`)
- `main`, `runList`, `fetchLatestRelease` (`src/main.go`)
- `executeList`, `fetchSupplementalData`, `fetchRequiredChecks`, `renderTable`, `resolveRepoLabel`, `fetchRequiredCheckContexts`, `fetchPRSupplemental` (`src/prlist.go`)
- `buildMeQueries`, `resolveOwnerQualifier`, `executeMe`, `fetchMeNodes` (`src/me.go`)

**When to revisit**: If any of these functions gain retry logic, caching, error
recovery, or conditional branching beyond "call CLI → check error → parse result",
they become testable logic and should be covered.

### `simplify` — Simplification Patterns

Scan for Go-specific simplification opportunities:

1. **`gofmt -s -d .`** — mechanical simplifications (slice expressions, range loops, composite literals)
2. **Complexity reduction** — functions with cyclomatic complexity >10:
   - Extract switch cases into helper functions
   - Replace nested if/else with early returns
   - Use lookup tables (maps) instead of long switch statements
3. **Duplication detection** — look for repeated patterns across functions:
   - Similar switch/case structures
   - Repeated error handling patterns
   - Copy-paste code with minor variations
4. **Interface extraction** — identify groups of methods on the same type that could be interfaces for testability
5. **Dead code** — `deadcode .` to find unreachable functions

For each finding:
- Show the current code
- Show the simplified version
- Explain the improvement (readability, testability, maintainability)
- **Apply the change** if it's safe (preserves behavior)

Run `go vet ./... && go test ./...` after each batch of simplifications to verify nothing broke.

### `harden` — Test Hardening

Improve test quality and robustness:

1. **Analyze existing tests** in `main_test.go`:
   - Are edge cases covered? (nil, empty, zero, negative, max values)
   - Are error paths tested?
   - Do table-driven tests have sufficient case variety?
   - Are assertions checking the right things?

2. **Identify missing test categories**:
   - Functions with 0% coverage
   - Functions tested only for happy path
   - Boundary conditions (off-by-one, empty input, single element)
   - Error injection (malformed JSON, nil pointers, empty strings)

3. **Write hardened tests** following repo conventions:
   - Table-driven with `t.Run`
   - Descriptive case names that explain the scenario
   - `t.Fatalf` for assertions
   - Test both the function output AND side effects
   - Add `t.Helper()` to test helper functions

4. **Verify test independence**:
   - No test should depend on another test's state
   - No test should depend on external services (use test doubles)
   - Tests should be deterministic (inject `time.Now`, avoid random)

After writing tests, run:
```powershell
go test -v -count=1 ./...
go test -race ./...
```

## Quality Gates

These thresholds are enforced in CI (`.github/workflows/ci.yml`). Every gate is a hard blocker — no advisory-only gates, no suppressions allowed.

| Gate | Threshold | CI Job |
|------|-----------|--------|
| `go build` | 0 errors | Build & Test |
| `go vet` | 0 errors | Build & Test |
| `go test -race` | 0 failures, 0 races | Build & Test |
| `gofmt` | 0 unformatted files | Lint |
| `go mod tidy` | no diff | Lint |
| `staticcheck` | 0 findings | Lint |
| `gocritic` | 0 findings | Lint |
| `errcheck` | 0 unchecked errors | Lint |
| Dead code (`deadcode`) | 0 unreachable funcs | Lint |
| `govulncheck` | 0 vulnerabilities | Lint |
| Lint suppressions | 0 `//nolint` etc. | Lint |
| Anti-patterns | 0 `fmt.Print*` in prod code | Lint |
| Markdown lint | 0 errors | Lint |
| Node dashboard tests | 0 failures | Build & Test |
| Test coverage | ≥70% | Quality Gates |
| Cyclomatic complexity | ≤10 per function | Quality Gates |
| Cognitive complexity | ≤15 per function | Quality Gates |
| CRAP score | <30 per function | Quality Gates |
| Mutation efficacy | ≥90% | Mutation Testing |
| Mutator coverage | ≥90% | Mutation Testing |

All CI jobs feed into a single **Quality Gate** status check required by branch
protection. A local audit pass qualifies only its local gates; hosted checks
remain required in GitHub.

## CI Integration

The CI workflow (`.github/workflows/ci.yml`) is the single source of truth for
enforcement. This skill document describes the same gates for local use. When
running `audit`, `coverage`, `crap`, `mutate`, `simplify`, or `harden`, use the
same thresholds and tools as CI.

Mutation thresholds and the package scope live in `.github/quality-tools.env`.
Raise a floor only after a complete Perfection audit establishes a stable higher
baseline. Update the checked-in value, CI fixture expectations, this table, and
the example commands together. Never lower a floor to make a failing change
pass.

### Main ruleset

- Changes to `main` must arrive through a pull request.
- The `Quality Gate` status check must pass.
- All review conversations must be resolved before merge.
- No approving reviews are required by the ruleset.
- Stale approvals are not dismissed after new pushes.
- Code-owner review and approval of the last push are not required.
- No actor can bypass the ruleset.

### AI code review

Connected Codex is the required AI reviewer for every pull request to `main`,
including Dependabot updates. The required `Quality Gate` waits for a clean,
current-head Codex receipt or full-SHA review and fails closed on missing,
stale, ambiguous, refused, or unresolved evidence. Codex reviews newly opened
pull requests; after pushing fixes, request one fresh review for the new head
with `@codex review`. Verification is read-only and never exposes or requires
a reviewer credential. Resolve all addressed review conversations before
merging.

Cubic reviews pull requests automatically when available. Its green check on a
merge-only commit can mean that review was skipped; inspect the check output
before counting it as a completed AI review. Report quota or availability
limits explicitly.

## Patterns Established in This Repo

Follow these when writing code or tests:

- **Table-driven tests**: Always use `[]struct` + `t.Run` (see `TestFormatRelativeTime`, `TestCountApprovals`)
- **Fail-fast assertions**: Use `t.Fatalf`, not `t.Errorf`
- **Time injection**: Pass `now time.Time` as parameter, don't call `time.Now()` inside testable functions
- **Nil safety**: Check pointer fields before access (see `Author` nil check in `buildDisplayPullRequest`)
- **Error wrapping**: Use `fmt.Errorf("context: %w", err)` for wrapped errors
- **Sentinel errors**: Define as `var errX = errors.New(...)` at package level (see `errHelpDisplayed`)
- **Graceful degradation**: External calls (GraphQL) use best-effort pattern — failures produce fallback values, not crashes
- **IO abstraction**: Functions accept `io.Writer` for testability (see `run`, `executeList`, `renderTableWithStyle`)
- **Flag parsing**: Use `flag.NewFlagSet` with `ContinueOnError` for subcommand flags

## Anti-Patterns to Flag

When running any command, also flag these if found. Items marked **[CI]** are enforced in the CI workflow:

- **[CI]** `fmt.Println` / `fmt.Print` / `fmt.Printf` in production code (use `fmt.Fprintln(w, ...)` with injected writer)
- **[CI]** `//nolint`, `//lint:ignore`, `//nosec`, `#nosec` suppression comments (zero tolerance policy)
- Naked `panic` or `log.Fatal` in non-main functions
- `time.Now()` called directly in testable logic
- Unchecked type assertions
- `interface{}` or `any` without type switches
- Goroutines without synchronization
- `os.Exit` outside of `main()`
- Ignoring returned errors (use `errcheck` if available)
