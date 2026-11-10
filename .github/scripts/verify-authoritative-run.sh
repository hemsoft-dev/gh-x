#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 || ! "$4" =~ ^[0-9]+$ ]]; then
  echo "usage: verify-authoritative-run.sh <owner/repo> <pull-request-url> <expected-head> <run-id>" >&2
  exit 2
fi

repo=$1
pr_url=$2
expected_head=$3
run_id=$4
run_url="https://github.com/${repo}/actions/runs/${run_id}"

fail() {
  echo "::error::PR $pr_url expected head $expected_head authoritative run $run_id $1. Inspect $run_url."
  exit 1
}

# Outlive the slowest gate (Mutation Testing's timeout-minutes) plus queueing.
if ! timeout 240m gh run watch "$run_id" --repo "$repo" --exit-status --interval 30; then
  fail "stopped before a successful Quality Gate"
fi
if ! actual_head=$(gh api "repos/${repo}/actions/runs/${run_id}" --jq '.head_sha'); then
  fail "could not report its head"
fi
if [[ "$actual_head" != "$expected_head" ]]; then
  fail "reported head $actual_head"
fi
if ! gate_conclusion=$(gh api --paginate "repos/${repo}/actions/runs/${run_id}/jobs?per_page=100" |
  jq -sr '[.[].jobs[] | select(.name == "Quality Gate")] | if length == 1 then .[0].conclusion else "ambiguous" end'); then
  fail "could not inspect Quality Gate jobs"
fi
if [[ "$gate_conclusion" != "success" ]]; then
  fail "has Quality Gate conclusion '$gate_conclusion'"
fi
