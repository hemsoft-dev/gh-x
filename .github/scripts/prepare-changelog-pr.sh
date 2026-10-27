#!/usr/bin/env bash
# Called after generating CHANGELOG.md on origin/main, before publication.
set -euo pipefail

blocked() {
  echo "::error::Cannot prepare changelog PR for ${RELEASE_TAG}: $* No release publication was attempted. An authorized owner must resolve the Actions PR-creation policy or API failure, then explicitly authorize recovery. Do not rerun an already-published release blindly or use CODEX_REVIEW_TOKEN for creation." >&2
  exit 1
}

[[ "$RELEASE_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || blocked "invalid release tag"
[[ "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || blocked "invalid repository"
[[ "$RELEASE_PUBLISHED" == true || "$RELEASE_PUBLISHED" == false ]] || blocked "unknown publication state"
branch="chore/changelog-${RELEASE_TAG#v}"

if git diff --quiet -- CHANGELOG.md; then
  [[ "$RELEASE_PUBLISHED" == true ]] || blocked "unpublished candidate has no changelog change"
  echo "CHANGELOG.md already records published ${RELEASE_TAG}."
  exit 0
fi

matches=$(gh pr list --repo "$GITHUB_REPOSITORY" --base main --head "$branch" --state open --json number) || blocked "cannot list candidate PRs"
number=$(jq -er 'if type == "array" and length == 0 then "new" elif type == "array" and length == 1 and (.[0].number | type == "number" and . > 0 and . == floor) then .[0].number else error("ambiguous candidate PRs") end' <<< "$matches") || blocked "invalid candidate PR lookup"

remote_sha=$(git ls-remote --heads origin "$branch" | awk '{print $1}') || blocked "cannot inspect candidate branch"
if [[ -n "$remote_sha" ]]; then
  [[ "$remote_sha" =~ ^[0-9a-f]{40}$ ]] || blocked "invalid candidate branch SHA"
  git fetch origin "$remote_sha"
  # Never replace a retained candidate or discard its exact-head review evidence.
  git diff --quiet "$remote_sha" -- CHANGELOG.md || blocked "retained candidate differs from these release notes; inspect it before recovery"
  [[ "$(git diff --name-only "origin/main...$remote_sha")" == CHANGELOG.md ]] || blocked "retained candidate changes files beyond CHANGELOG.md"
  head_sha="$remote_sha"
  # The generated file was proven identical to the retained commit above.
  # Restore the clean base before checking out that commit and its index.
  git restore -- CHANGELOG.md
  git switch --detach "$head_sha"
else
  [[ "$number" == new ]] || blocked "candidate PR exists without its branch"
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  git switch -c "$branch"
  git add CHANGELOG.md
  git commit -m "docs: record ${RELEASE_TAG} release"
  head_sha=$(git rev-parse HEAD)
  git push origin "HEAD:refs/heads/${branch}"
fi

if [[ "$number" == new ]]; then
  pr_url=$(gh pr create --repo "$GITHUB_REPOSITORY" --base main --head "$branch" --draft \
    --title "docs: record ${RELEASE_TAG} release" \
    --body "Planned ${RELEASE_TAG} notes. Publication has not been confirmed by this candidate PR. Auto Release keeps this PR in draft until publication succeeds, then requires exact-head review, authoritative CI and guarded merge.") || blocked "gh pr create denied or unavailable for $branch at $head_sha; retained branch can be reused"
  [[ "$pr_url" == "https://github.com/${GITHUB_REPOSITORY}/pull/"* ]] || blocked "invalid created PR URL"
  number="${pr_url##*/}"
fi
[[ "$number" =~ ^[1-9][0-9]*$ ]] || blocked "invalid candidate PR number"
pr_url="https://github.com/${GITHUB_REPOSITORY}/pull/${number}"

pr=$(gh api "repos/${GITHUB_REPOSITORY}/pulls/${number}") || blocked "cannot verify candidate PR"
jq -e --arg repo "$GITHUB_REPOSITORY" --arg branch "$branch" --arg head "$head_sha" --argjson published "$RELEASE_PUBLISHED" '
  .state == "open" and .merged == false and
  (.draft == true or ($published and .draft == false)) and
  .user.login == "github-actions[bot]" and .user.type == "Bot" and
  .base.ref == "main" and .base.repo.full_name == $repo and
  .head.ref == $branch and .head.sha == $head and .head.repo.full_name == $repo and
  .changed_files == 1
' <<< "$pr" >/dev/null || blocked "candidate PR identity, draft state or head does not match"
files=$(gh api "repos/${GITHUB_REPOSITORY}/pulls/${number}/files?per_page=100") || blocked "cannot verify candidate files"
jq -e 'type == "array" and length == 1 and .[0].filename == "CHANGELOG.md" and .[0].status == "modified"' <<< "$files" >/dev/null || blocked "candidate PR may only modify CHANGELOG.md"

echo "Prepared $pr_url at $head_sha; publication and merge gates remain pending."
{
  echo "pr_url=$pr_url"
  echo "head_sha=$head_sha"
  echo "branch=$branch"
} >> "$GITHUB_OUTPUT"
