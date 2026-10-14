#!/usr/bin/env bash
# Publishes what the integrations loop agent asked for
# (docs/design/integrations/loop-agent.md, "The outbox"). It runs in the
# Publish job, on a machine the agent never touched, from a checkout of main.
# $LOOP_IN holds the agent's outbox.json and branch.bundle, which are only
# data. Everything is checked before anything is done, so a bad outbox
# publishes nothing: only loop/* branches, never a force push, nothing under
# .github/, known labels, capped counts, every post signed. Nothing here can
# merge.
set -euo pipefail

in="${LOOP_IN:-.loop}"
outbox="$in/outbox.json"
bundle="$in/branch.bundle"
[ -f "$outbox" ] || { echo "no outbox: nothing to publish"; exit 0; }
jq -e 'type == "object"' "$outbox" >/dev/null

sign='[agent:integrations-loop]'
allowed_labels='^(loop|loop:ready|loop:in-progress|loop:blocked-owner|phase:P[0-6])$'
fail() { echo "::error::$1"; exit 1; }
check_labels() {
  local label
  while read -r label; do
    [ -z "$label" ] && continue
    [[ "$label" =~ $allowed_labels ]] || fail "label not allowed: $label"
  done
}
check_body() {
  [[ "$1" == "$sign"* ]] || fail "a post must start with $sign"
  [ "${#1}" -le 20000 ] || fail "a post is too long"
}
count() { jq "[$1] | length" "$outbox"; }

# --- Check everything first.
branch=$(jq -r '.branch // empty' "$outbox")
if [ -n "$branch" ]; then
  [[ "$branch" =~ ^loop/[0-9]+-[a-z0-9-]{1,60}$ ]] || fail "branch not allowed: $branch"
  [ -f "$bundle" ] || fail "no bundle for $branch"
  git bundle verify -q "$bundle"
  git fetch -q "$bundle" "+refs/heads/$branch:refs/loop/candidate"
  base=$(git merge-base origin/main refs/loop/candidate)
  if git diff --name-only "$base" refs/loop/candidate | grep -q '^\.github/'; then
    fail "the branch changes .github/, which only macbookpro may do"
  fi
fi
if jq -e '.pr' "$outbox" >/dev/null; then
  [ -n "$branch" ] || fail "a pr needs a branch"
  [ -n "$(jq -r '.pr.title // empty' "$outbox")" ] || fail "a pr needs a title"
  check_body "$(jq -r '.pr.body' "$outbox")"
  jq -r '.pr.labels[]?' "$outbox" | check_labels
fi
[ "$(count '.comments[]?')" -le 10 ] || fail "too many comments"
[ "$(count '.labels[]?')" -le 10 ] || fail "too many label changes"
[ "$(count '.issues[]?')" -le 3 ] || fail "too many issues"
jq -r '.comments[]?.number, .labels[]?.number' "$outbox" | while read -r number; do
  [[ "$number" =~ ^[0-9]+$ ]] || fail "bad number: $number"
done
n=$(count '.comments[]?'); for ((i = 0; i < n; i++)); do check_body "$(jq -r ".comments[$i].body" "$outbox")"; done
n=$(count '.issues[]?'); for ((i = 0; i < n; i++)); do
  [ -n "$(jq -r ".issues[$i].title // empty" "$outbox")" ] || fail "an issue needs a title"
  check_body "$(jq -r ".issues[$i].body" "$outbox")"
done
jq -r '.labels[]? | (.add[]?, .remove[]?)' "$outbox" | check_labels
jq -r '.issues[]?.labels[]?' "$outbox" | check_labels

# --- Then publish.
# 1. The branch, without force.
if [ -n "$branch" ]; then
  git push -q "https://x-access-token:${GH_TOKEN}@github.com/${GITHUB_REPOSITORY}.git" "refs/loop/candidate:refs/heads/$branch"
fi

# 2. The pull request: opened, or updated when it exists.
if jq -e '.pr' "$outbox" >/dev/null; then
  title=$(jq -r '.pr.title' "$outbox"); body=$(jq -r '.pr.body' "$outbox")
  labels=$(jq -r '[.pr.labels[]?] | join(",")' "$outbox")
  existing=$(gh pr list --head "$branch" --state open --json number -q '.[0].number // empty')
  if [ -n "$existing" ]; then
    gh pr edit "$existing" --title "$title" --body "$body" ${labels:+--add-label "$labels"}
  else
    gh pr create --base main --head "$branch" --title "$title" --body "$body" ${labels:+--label "$labels"}
  fi
fi

# 3. Comments on issues or pull requests.
n=$(count '.comments[]?'); for ((i = 0; i < n; i++)); do
  gh issue comment "$(jq -r ".comments[$i].number" "$outbox")" --body "$(jq -r ".comments[$i].body" "$outbox")"
done

# 4. Labels.
n=$(count '.labels[]?'); for ((i = 0; i < n; i++)); do
  add=$(jq -r "[.labels[$i].add[]?] | join(\",\")" "$outbox")
  remove=$(jq -r "[.labels[$i].remove[]?] | join(\",\")" "$outbox")
  gh issue edit "$(jq -r ".labels[$i].number" "$outbox")" ${add:+--add-label "$add"} ${remove:+--remove-label "$remove"}
done

# 5. Follow-up issues.
n=$(count '.issues[]?'); for ((i = 0; i < n; i++)); do
  labels=$(jq -r "[.issues[$i].labels[]?] | join(\",\")" "$outbox")
  gh issue create --title "$(jq -r ".issues[$i].title" "$outbox")" --body "$(jq -r ".issues[$i].body" "$outbox")" ${labels:+--label "$labels"}
done
echo "published"
