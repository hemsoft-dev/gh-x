# Dashboard JavaScript quality

Install the development tools with `node .github/scripts/dashboard-quality/install.mjs`, then run:

```bash
node .github/scripts/dashboard-quality/install.mjs
node --test .github/scripts/dashboard-quality/*.test.mjs
node .github/scripts/dashboard-quality/run.mjs /tmp/dashboard-quality
```

The installer runs locked `npm ci` in a temporary directory identified by the
lockfile digest. It keeps dependency source outside the checkout so recursive Go
commands retain their repository scope. `GH_X_DASHBOARD_QUALITY_TOOLS` may select
another external tools directory.
Canonical destination and checkout paths are checked before directory creation
and again before copying. Installation uses the resolved path so a symlink or
junction alias cannot direct dependency writes into the checkout. A failed npm
install retains its actual exit status and reports incomplete tools without a
success message.

The same commands run in the required CI Quality Gate and the local Perfection
audit. Node is pinned in `.node-version`; c8 and ESLint have exact development
dependency versions and a committed npm lock. Runtime dashboards remain free
of npm dependencies. The runner checks the Node version, installed direct tool
versions and the complete npm dependency graph before measuring. A missing
secondary dependency fails with an instruction to rerun the isolated installer.
Missing direct tools also name that installer command.

`package.json` overrides `minimatch` to `^11.0.5` for
[GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249).
c8 12.0.0 reaches minimatch through `test-exclude` 8.0.0 and `glob` 13.0.6,
which still ask for `^10.2.2`, so Dependabot cannot update it alone. ESLint
10.12.0 and `@eslint/config-array` 0.23.5 also ask for minimatch 10. Remove the
override only once c8's chain and ESLint's both accept minimatch 11.0.1 or later.

## Scope and measurement

Every `.mjs` module under `src/codex-dashboard` and `src/dashboard-hub` is
inventoried recursively. The measurement selects tests with recursive `**/*.test.mjs` patterns under both
source roots. Tests and the explicit process-liveness fixture
`src/codex-dashboard/test-process-liveness.mjs` are excluded. There are no
generated modules in these source roots and no generated-code exclusions.
The filesystem inventory must match the reviewed policy inventory, and every
production module must appear in coverage. New modules require an explicit
baseline review and tests rather than silently disappearing from the report.

[c8's all-source mode](https://github.com/bcoe/c8#checking-for-full-source-coverage-using---all)
includes modules which no test loads. The browser entry module and dashboard hub
entry module currently have zero coverage; they remain visible in the report.
Their zero-coverage baseline is a disclosed test gap, not a claim that they are
tested. New untested modules fail the inventory gate.

Lines, statements and branches come from c8's V8-to-Istanbul mapping. Its
statement entries represent source lines, so the line and statement percentages
are equal here; these are not AST statement instrumentation. V8 block branches
are not identical to semantic true/false branches for each JavaScript conditional.
The JSON reports retain the counts and source ranges needed to inspect that basis.

Function coverage inventories source functions using ESLint's AST and joins raw
V8 execution ranges by full module path and source offsets, including anonymous
callbacks. c8's named-function map alone omits anonymous callbacks and cannot
qualify this inventory. Nested functions have separate execution and complexity
records. Function identity never depends on a name or basename alone.
Script-root execution is excluded even when its offsets equal an uncalled
function's offsets. Implicit class fields and static blocks use V8's grouped
initializer range for their owning class. A callback created by a field has
its own execution record. Grouped initializer execution cannot distinguish
individual sequential initializers interrupted by an exception; each such
record explicitly labels this coverage basis.

Cyclomatic complexity uses the pinned ESLint
[classic complexity rule](https://eslint.org/docs/latest/rules/complexity),
including defaults, optional chaining, logical assignments and switch cases.
For each function, CRAP is `CC² × (1 − coverage)³ + CC`. Coverage uses the V8
block branches contained in that function, assigning a nested function's branches
to the nested function. When no branches belong to a function, its own execution
count supplies a binary coverage basis. Unexecuted functions receive zero
coverage; repository averages are never substituted. Every record states its basis.

## Policy and failure evidence

`.github/dashboard-quality-policy.json` declares the reviewed aggregate and
per-module coverage floors and per-module worst-function CRAP ceilings. Changes
may improve these baselines; lowering a floor or raising a ceiling needs explicit
review and justification. No function may exceed CRAP 30, even if a baseline
would otherwise allow it. Scores from 15 through 30 are published as the review
queue; higher scores fail with the exact source location and an instruction to
simplify or add branch tests. Existing Go thresholds remain unchanged.

Reports include `coverage-final.json`, raw V8 data, `quality.json` and
`quality.md`. Measurement failures also write `failure.json`. Failed tests cannot
qualify the metrics they produced. Local audits retain this output in their
artifact directory after failures; CI uploads it with `if: always()` for 14 days. Installation and
gate-fixture steps create that directory before executing and retain stdout/stderr with Bash pipe failure
semantics. Empty artifact uploads fail instead of reporting success.
The runner clears only its own prior report files before a new measurement so a
failed rerun cannot leave a stale successful report.
Raw V8 files are cleared before checking tools, including when a missing tool
prevents c8 from starting.

Regression fixtures reject missing or newly unreviewed modules, coverage loss,
worst-function CRAP growth and incomplete secondary tooling. Additional behavior
tests exercise invalid inputs, default configuration, unavailable callbacks,
incomplete usage records and rate-projection boundaries identified by the new
per-function report.
