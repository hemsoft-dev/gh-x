#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
gate="$repo_root/.github/scripts/run-mutation-gate.sh"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
# Every gate run below warms the same private build cache.
export MUTATION_GOCACHE="$temp_dir/gocache"

write_module() {
  local root=$1 body=$2 tests=$3
  mkdir -p "$root/src"
  printf 'module example.com/fixture\n\ngo 1.22\n' > "$root/go.mod"
  printf 'package main\n\n%s\n\nfunc main() {}\n' "$body" > "$root/src/main.go"
  printf 'package main\n\nimport "testing"\n\n%s\n' "$tests" > "$root/src/main_test.go"
}

# ── Report fixtures: a fake Gremlins prints a summary and the test-run
# records the shim would have written for it.
fake="$temp_dir/gremlins"
cat > "$fake" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
expected=(unleash --workers 3 --timeout-coefficient 10 --threshold-efficacy 90 --threshold-mcover 90 ./src)
actual=("$@")
[[ ${#actual[@]} -eq ${#expected[@]} ]]
for index in "${!expected[@]}"; do
  [[ "${actual[index]}" == "${expected[index]}" ]]
done
[[ -n "${MUTATION_TEST_LOG:-}" && -n "${MUTATION_REAL_GO:-}" && -s "${MUTATION_WARM_CACHE:-}" ]]
cat "$GREMLINS_FIXTURE.runs" >> "$MUTATION_TEST_LOG"
cat "$GREMLINS_FIXTURE"
exit "${GREMLINS_FIXTURE_EXIT:-0}"
FAKE
chmod +x "$fake"
fake_module="$temp_dir/fake-module"
write_module "$fake_module" 'func one() int { return 1 }' 'func TestOne(t *testing.T) { _ = one() }'

write_runs() {
  local path=$1 outcome=$2 count=$3 index
  for ((index = 0; index < count; index++)); do
    printf '{"outcome":"%s","elapsed_ms":%d,"requested":"github.com/HemSoft/gh-x","packages":["github.com/HemSoft/gh-x/src"],"tests_run":3}\n' "$outcome" $((1000 + index)) >> "$path"
  done
}

write_report() {
  local path=$1 killed=$2 lived=$3 uncovered=$4 efficacy=$5 coverage=$6
  cat > "$path" <<REPORT
Mutation testing completed in 1 millisecond
Killed: $killed, Lived: $lived, Not covered: $uncovered
Timed out: 0, Not viable: 0, Skipped: 0
Test efficacy: $efficacy%
Mutator coverage: $coverage%
REPORT
  : > "$path.runs"
  write_runs "$path.runs" killed "$killed"
  write_runs "$path.runs" lived "$lived"
}

expect_failure_output() {
  local expected=$1 output=$2
  if ! grep -Fq "$expected" <<< "$output"; then
    printf 'missing diagnostic %q in:\n%s\n' "$expected" "$output" >&2
    exit 1
  fi
}

expect_failure() {
  local fixture=$1 expected=$2 fixture_exit=${3:-0} output
  if output=$(MUTATION_WORKERS=3 MUTATION_MODULE_ROOT="$fake_module" GREMLINS_BIN="$fake" GREMLINS_FIXTURE="$fixture" GREMLINS_FIXTURE_EXIT="$fixture_exit" bash "$gate" 2>&1); then
    echo "expected mutation gate failure for $fixture" >&2
    exit 1
  fi
  expect_failure_output "$expected" "$output"
}

empty="$temp_dir/empty.txt"
write_report "$empty" 0 0 0 0.00 0.00
expect_failure "$empty" "Mutation testing produced no scored mutants"

uncovered="$temp_dir/uncovered.txt"
write_report "$uncovered" 90 0 11 100.00 89.11
expect_failure "$uncovered" "Mutator coverage from 90 covered and 11 not-covered mutants is below 90%"

live="$temp_dir/live.txt"
write_report "$live" 8 1 0 88.89 100.00
expect_failure "$live" "Mutation efficacy from 8 killed and 1 lived mutants is below 90%" 10

rounded="$temp_dir/rounded.txt"
write_report "$rounded" 1808 0 201 100.00 90.00
expect_failure "$rounded" "Mutator coverage from 1808 covered and 201 not-covered mutants is below 90%" 11

# The #264 defect: every "kill" was a go test setup failure.
setup="$temp_dir/setup.txt"
write_report "$setup" 3 0 0 100.00 100.00
: > "$setup.runs"
write_runs "$setup.runs" unproven 3
expect_failure "$setup" "3 mutant test run(s) did not prove a package test execution"

# A restore that failed leaves a mutation behind for later mutants.
unrestored="$temp_dir/unrestored.txt"
write_report "$unrestored" 9 0 0 100.00 100.00
printf 'ERROR: failed to restore mutation at run.go:260:7 - LIVED\n' >> "$unrestored"
expect_failure "$unrestored" "Gremlins could not apply or restore a mutation"

# Kills that no test run produced.
unbacked="$temp_dir/unbacked.txt"
write_report "$unbacked" 5 0 0 100.00 100.00
: > "$unbacked.runs"
write_runs "$unbacked.runs" killed 4
expect_failure "$unbacked" "Gremlins counts (killed 5, lived 0, not viable 0) do not match executed test runs (killed 4, lived 0, not viable 0)"

passing="$temp_dir/passing.txt"
write_report "$passing" 90 0 10 100.00 90.00
# Equality satisfies the documented floor even though Gremlins v0.6.0 returns
# its threshold code for an exact match.
output=$(MUTATION_WORKERS=3 MUTATION_MODULE_ROOT="$fake_module" GREMLINS_BIN="$fake" GREMLINS_FIXTURE="$passing" GREMLINS_FIXTURE_EXIT=11 bash "$gate" 2>&1)
expect_failure_output "Per-mutant go test time: mean 1044 ms, median 1044 ms, max 1089 ms" "$output"

echo "Mutation report fixtures passed"

# ── Real Gremlins fixtures: a package main under src, the layout that hid
# every mutant from its tests.
real_gremlins=$(command -v "${GREMLINS_REAL_BIN:-gremlins}" || true)
if [[ -z "$real_gremlins" ]]; then
  echo "::error::gremlins is not installed; run .github/scripts/install-quality-tools.sh mutation"
  exit 1
fi

run_real_gate() {
  MUTATION_MODULE_ROOT=$1 GREMLINS_BIN="$real_gremlins" bash "$gate" ./src 2>&1
}

# Both mutants of `n > 10` change a tested result, so both are killed by
# tests that actually ran.
strong="$temp_dir/strong"
write_module "$strong" 'func over(n int) bool { return n > 10 }' \
  'func TestOver(t *testing.T) {
	if over(10) || !over(11) || over(5) {
		t.Fatal("over")
	}
}'
if ! output=$(run_real_gate "$strong"); then
  printf 'expected the detectable fixture to pass:\n%s\n' "$output" >&2
  exit 1
fi
expect_failure_output "Killed: 2, Lived: 0, Not covered: 0" "$output"
expect_failure_output "Mutant test runs: 2 (killed 2, lived 0, not viable 0, unproven 0)" "$output"
expect_failure_output "Per-mutant go test time:" "$output"

# `n >= 10` returns 10 for n == 10 either way: no test can detect it, so it
# must live and pull efficacy below the floor.
weak="$temp_dir/weak"
write_module "$weak" 'func clamp(n int) int {
	if n > 10 {
		return 10
	}
	return n
}' \
  'func TestClamp(t *testing.T) {
	if clamp(11) != 10 || clamp(10) != 10 || clamp(5) != 5 {
		t.Fatal("clamp")
	}
}'
if output=$(run_real_gate "$weak"); then
  printf 'expected the undetectable mutant to fail the gate:\n%s\n' "$output" >&2
  exit 1
fi
expect_failure_output "Killed: 1, Lived: 1, Not covered: 0" "$output"
expect_failure_output "Mutant test runs: 2 (killed 1, lived 1, not viable 0, unproven 0)" "$output"
expect_failure_output "Mutation efficacy from 1 killed and 1 lived mutants is below 90%" "$output"

echo "Mutation threshold fixtures passed"
