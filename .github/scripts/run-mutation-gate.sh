#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
# shellcheck source=/dev/null
source "$repo_root/.github/quality-tools.env"

: "${MUTATION_EFFICACY_THRESHOLD:?missing mutation efficacy threshold}"
: "${MUTATION_COVERAGE_THRESHOLD:?missing mutator coverage threshold}"
: "${MUTATION_PACKAGE_SCOPE:?missing mutation package scope}"

gremlins_bin=${GREMLINS_BIN:-gremlins}
# Fixtures point the gate at a throwaway module; CI scores the repository.
module_root=${MUTATION_MODULE_ROOT:-$repo_root}
package_scope=${1:-$MUTATION_PACKAGE_SCOPE}
# Gremlins copies the module once per worker under TMPDIR, so TMPDIR needs
# room for several copies plus test binaries.
work=$(mktemp -d)
results="$work/gremlins.txt"
test_log="$work/mutant-tests.jsonl"
# MUTATION_RECORDS_OUT keeps the per-mutant test records for inspection.
trap '[[ -z "${MUTATION_RECORDS_OUT:-}" ]] || cp "$test_log" "$MUTATION_RECORDS_OUT"; rm -rf "$work"' EXIT
: > "$test_log"

# Gremlins v0.6.0 resolves a package main under src to the module root and
# counts that setup failure as a kill. Put the shim first on PATH so each
# mutant runs the scope's real tests and leaves a record of the run.
native_path() {
  if command -v cygpath > /dev/null; then cygpath -m "$1"; else printf '%s' "$1"; fi
}
go_exe=$(cd "$repo_root" && go env GOEXE)
real_go="$(cd "$repo_root" && go env GOROOT)/bin/go$go_exe"
mkdir "$work/bin"
(cd "$repo_root" && go build -o "$work/bin/go$go_exe" ./.github/scripts/mutation-go-shim)

# Mutants build into a private cache warmed with the scope's dependencies.
# The shim then removes each mutant's unique output, so the run stays at the
# warm size, and Gremlins times its coverage run against warm builds.
# MUTATION_GOCACHE lets repeated runs (the fixtures) share one such cache.
gocache=${MUTATION_GOCACHE:-$work/gocache}
mkdir -p "$gocache"
GOCACHE=$(native_path "$gocache")
export GOCACHE
cd "$module_root"
"$real_go" test -count=1 -vet=off -run '^$' "$package_scope/..." > /dev/null
"$real_go" test -count=1 -vet=off -cover -run '^$' "$package_scope/..." > /dev/null
find "$gocache" -type f | sed 's|.*/||' > "$work/warm-cache.txt"
echo "Warm mutation build cache: $(wc -l < "$work/warm-cache.txt" | tr -d ' ') entries, $(du -sh "$gocache" | cut -f1)"

MUTATION_REAL_GO=$real_go
MUTATION_TEST_LOG=$(native_path "$test_log")
MUTATION_WARM_CACHE=$(native_path "$work/warm-cache.txt")
export MUTATION_REAL_GO MUTATION_TEST_LOG MUTATION_WARM_CACHE
export PATH="$work/bin:$PATH"

# Gremlins' deadline is ten times one uncontended coverage run, and each
# mutant's build keeps about two cores busy. One worker per CPU pushed real
# runs past that deadline, and every false timeout hides a mutant that might
# have lived. Half as many workers keeps the same throughput.
cpus=$(getconf _NPROCESSORS_ONLN 2> /dev/null || nproc 2> /dev/null || echo 2)
workers=${MUTATION_WORKERS:-$(( cpus > 3 ? cpus / 2 : 1 ))}
echo "Mutation workers: ${workers} on ${cpus} CPUs"

set +e
"$gremlins_bin" unleash \
  --workers "$workers" \
  --timeout-coefficient 10 \
  --threshold-efficacy "$MUTATION_EFFICACY_THRESHOLD" \
  --threshold-mcover "$MUTATION_COVERAGE_THRESHOLD" \
  "$package_scope" 2>&1 | tee "$results"
gremlins_status=${PIPESTATUS[0]}
set -e
echo "Mutation build cache after the run: $(du -sh "$gocache" | cut -f1)"

if grep -Fq "No results to report" "$results"; then
  echo "::error::Mutation testing produced no results"
  exit 1
fi
# A mutation that was not applied is never tested, and one that was not
# restored stays in that worker's copy for every later mutant.
if grep -Eq 'failed to (apply|restore) mutation' "$results"; then
  echo "::error::Gremlins could not apply or restore a mutation, so its results are unreliable (check free space in TMPDIR)"
  grep -E 'failed to (apply|restore) mutation' "$results" | head -5
  exit 1
fi

summary=$(grep -E '^Killed: [0-9]+, Lived: [0-9]+, Not covered: [0-9]+$' "$results" | tail -1 || true)
secondary=$(grep -E '^Timed out: [0-9]+, Not viable: [0-9]+, Skipped: [0-9]+$' "$results" | tail -1 || true)
efficacy=$(sed -n 's/^Test efficacy: \([0-9][0-9.]*\)%$/\1/p' "$results" | tail -1)
mutator_coverage=$(sed -n 's/^Mutator coverage: \([0-9][0-9.]*\)%$/\1/p' "$results" | tail -1)
if [[ ! "$secondary" =~ ^Timed\ out:\ ([0-9]+),\ Not\ viable:\ ([0-9]+), ]]; then
  echo "::error::Mutation output did not contain timed-out and not-viable counts"
  exit 1
fi
timed_out=${BASH_REMATCH[1]}
not_viable=${BASH_REMATCH[2]}
if [[ ! "$summary" =~ ^Killed:\ ([0-9]+),\ Lived:\ ([0-9]+),\ Not\ covered:\ ([0-9]+)$ || -z "$efficacy" || -z "$mutator_coverage" ]]; then
  echo "::error::Mutation output did not contain killed, lived, not-covered, efficacy, and mutator-coverage metrics"
  exit 1
fi
killed=${BASH_REMATCH[1]}
lived=${BASH_REMATCH[2]}
not_covered=${BASH_REMATCH[3]}
covered=$((killed + lived))
total=$((covered + not_covered))
if (( total == 0 )); then
  echo "::error::Mutation testing produced no scored mutants"
  exit 1
fi

# Every scored status must match a classified test run of the scope.
count_runs() { grep -c "\"outcome\":\"$1\"" "$test_log" || true; }
runs_killed=$(count_runs killed)
runs_lived=$(count_runs lived)
runs_not_viable=$(count_runs not-viable)
runs_unproven=$(count_runs unproven)
runs_total=$(grep -c . "$test_log" || true)
echo "Mutant test runs: ${runs_total} (killed ${runs_killed}, lived ${runs_lived}, not viable ${runs_not_viable}, unproven ${runs_unproven}); Gremlins timed out ${timed_out}"
if (( runs_total > 0 )); then
  sed -n 's/.*"elapsed_ms":\([0-9]*\).*/\1/p' "$test_log" | sort -n | awk '
    { ms[NR] = $1; sum += $1 }
    END { printf "Per-mutant go test time: mean %d ms, median %d ms, max %d ms\n", sum / NR, ms[int((NR + 1) / 2)], ms[NR] }'
fi
if (( runs_unproven > 0 )); then
  echo "::error::${runs_unproven} mutant test run(s) did not prove a package test execution"
  grep -F '"outcome":"unproven"' "$test_log" | head -5
  exit 1
fi
if (( runs_killed != killed || runs_lived != lived || runs_not_viable != not_viable )); then
  echo "::error::Gremlins counts (killed ${killed}, lived ${lived}, not viable ${not_viable}) do not match executed test runs (killed ${runs_killed}, lived ${runs_lived}, not viable ${runs_not_viable})"
  exit 1
fi

efficacy_left=$((killed * 100))
efficacy_right=$((MUTATION_EFFICACY_THRESHOLD * covered))
coverage_left=$((covered * 100))
coverage_right=$((MUTATION_COVERAGE_THRESHOLD * total))

failed=false
if (( efficacy_left < efficacy_right )); then
  echo "::error::Mutation efficacy from ${killed} killed and ${lived} lived mutants is below ${MUTATION_EFFICACY_THRESHOLD}% (reported ${efficacy}%)"
  failed=true
fi
if (( coverage_left < coverage_right )); then
  echo "::error::Mutator coverage from ${covered} covered and ${not_covered} not-covered mutants is below ${MUTATION_COVERAGE_THRESHOLD}% (reported ${mutator_coverage}%)"
  failed=true
fi
if [[ "$failed" == true ]]; then
  exit 1
fi

# Gremlins v0.6.0 reserves 10 and 11 for its <= threshold checks. Suppress one
# only when the corresponding integer ratio exactly equals the configured floor.
threshold_equality=false
if (( gremlins_status == 10 && efficacy_left == efficacy_right )); then
  threshold_equality=true
elif (( gremlins_status == 11 && coverage_left == coverage_right )); then
  threshold_equality=true
fi
if (( gremlins_status != 0 )) && [[ "$threshold_equality" != true ]]; then
  echo "::error::Gremlins failed with exit code ${gremlins_status}"
  exit "$gremlins_status"
fi
