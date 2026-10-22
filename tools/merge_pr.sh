#!/usr/bin/env bash
# Rebase-merge a pull request with signed commits, which GitHub's "Rebase and merge" button cannot do
# (it rewrites the commits without a signature).
#
#   tools/merge_pr.sh 231
#
# Needs: git commit signing set up (commit.gpgsign), and a main ruleset bypass for the person running it.
# Refuses unless the PR is open against main, carries human-approved, every check passed, and the branch
# already sits on the current main. The commits are re-signed onto the same base, the tree is checked to be
# exactly the one CI tested, the PR branch is moved to them, then main fast-forwards, so GitHub shows it merged.
set -euo pipefail
PR="${1:-}"
[[ "$PR" =~ ^[0-9]+$ ]] || { echo "usage: $0 <pr-number>" >&2; exit 2; }
REPO="${ARKISM_REPO:-FelixBaek/arkism}"
cd "$(dirname "$0")/.."
[[ "$(git config --get commit.gpgsign)" == true ]] || { echo "commit signing is off (git config commit.gpgsign true)" >&2; exit 1; }

info="$(gh pr view "$PR" -R "$REPO" --json state,baseRefName,headRefName,headRefOid,isCrossRepository,labels,statusCheckRollup)"
field() { local query="$1"; jq -r "$query" <<<"$info"; }
[[ "$(field .state)" == OPEN && "$(field .baseRefName)" == main && "$(field .isCrossRepository)" == false ]] \
  || { echo "PR #$PR is not an open same-repository PR against main" >&2; exit 1; }
field '.labels[].name' | grep -qx human-approved || { echo "PR #$PR has no human-approved label" >&2; exit 1; }
bad="$(field '[.statusCheckRollup[] | select(((.conclusion // .state) | IN("SUCCESS","NEUTRAL","SKIPPED")) | not) | (.name // .context) + "=" + ((.conclusion // .state) // "PENDING")] | join(", ")')"
[[ "$(field '.statusCheckRollup | length')" -gt 0 && -z "$bad" ]] || { echo "checks not all passed: ${bad:-none reported}" >&2; exit 1; }

head="$(field .headRefOid)" branch="$(field .headRefName)"
git fetch -q origin main "$branch"
[[ "$(git rev-parse "origin/$branch")" == "$head" ]] || { echo "the PR branch moved; run again" >&2; exit 1; }
git merge-base --is-ancestor origin/main "$head" \
  || { echo "branch is behind main: gh pr update-branch $PR --rebase, wait for checks, run again" >&2; exit 1; }

work="$(mktemp -d)"
trap 'git worktree remove --force "$work" >/dev/null 2>&1 || true' EXIT
git worktree add -q --detach "$work" "$head"
git -C "$work" rebase -q --force-rebase --gpg-sign origin/main
signed="$(git -C "$work" rev-parse HEAD)"
[[ "$(git rev-parse "$signed^{tree}")" == "$(git rev-parse "$head^{tree}")" ]] || { echo "re-signed tree differs from the tested one" >&2; exit 1; }
for c in $(git rev-list origin/main.."$signed"); do
  git cat-file commit "$c" | grep -q '^gpgsig' || { echo "commit $c is not signed" >&2; exit 1; }
done

count="$(git rev-list --count origin/main.."$signed")"
# Same tree as the one CI passed. Moving the PR branch first lets GitHub mark the PR merged.
git push -q --force-with-lease="$branch:$head" origin "$signed:refs/heads/$branch"
git push -q origin "$signed:refs/heads/main"
echo "merged #$PR as $count signed commit(s), main is now ${signed:0:7}"
