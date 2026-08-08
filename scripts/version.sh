#!/usr/bin/env bash
# Derive the app's version string. Single source of truth: the Makefile and both
# deploy workflows call this, so a local build and a deployed build label
# themselves the same way.
#
#   version.sh production          -> 2026.07.29        (first of the day)
#                                  -> 2026.07.29.1      (second, .2 third, ...)
#   version.sh staging [branch]    -> staging-my-branch-abc123
#   version.sh                     -> dev-abc1234
#
# Production uses CalVer: the date it shipped, with a counter only when more
# than one production build goes out the same day. The next free suffix is found
# by probing existing git tags, and the production workflow tags each build with
# exactly this string — so the tag, the GitHub release and the version the app
# reports are all the same identifier, and every production build stays as a
# rollback point.
#
# Staging is not a release, so it is named after what it actually is: the branch
# and the commit it was built from — SHORTENED, because a staging version is a
# label people read in a UI header and a workflow log, not an identifier anything
# resolves. Real branch names ("claude/staging-deploy-fixes-i3uvj8") make a
# 45-character version nobody can scan. So the leading namespace is dropped, the
# rest is capped, and only the first few characters of the commit are kept:
#
#   claude/staging-deploy-fixes-i3uvj8 @ 5b1e235c -> staging-staging-deploy-5b1e23
#
# That makes the staging version a LABEL, not a key: two branches sharing a
# trimmed name produce the same prefix and differ only by commit. Nothing
# resolves a build by this string — the server compares deploys by full commit
# hash (updater.IsCurrent), the release asset is tagged deploy-staging-<full
# branch> by the workflow, and the exact commit is built into the binary
# separately (main.CommitHash) and shown next to the version in the UI.
#
# Override the budgets below if a longer label reads better.
#
# Requires full history and tags: CI must check out with fetch-depth: 0.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

kind="${1:-dev}"
sha7="$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"

# How much of the branch name and of the commit hash a staging version keeps.
STAGING_BRANCH_MAXLEN="${STAGING_BRANCH_MAXLEN:-20}"
STAGING_SHA_LEN="${STAGING_SHA_LEN:-6}"

tag_exists() { git rev-parse -q --verify "refs/tags/$1" >/dev/null 2>&1; }

# The readable part of a branch name, within STAGING_BRANCH_MAXLEN characters:
#
#   - everything before the last "/" goes first — "claude/", "feature/",
#     "users/vlad/" say who opened the branch, not what is in it;
#   - anything that isn't [a-z0-9] becomes a dash, and runs of dashes collapse;
#   - then it's cut to the budget, on a dash boundary when one is close enough,
#     so the label ends on a whole word instead of mid-syllable.
short_branch() {
  local b="${1##*/}"
  b="$(printf '%s' "$b" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9' '-' | tr -s '-')"
  b="${b#-}"
  b="${b%-}"
  if [ "${#b}" -gt "$STAGING_BRANCH_MAXLEN" ]; then
    b="${b:0:$STAGING_BRANCH_MAXLEN}"
    # Prefer the last word boundary, but only when it isn't throwing away
    # most of the budget (a branch like "verylongsinglewordname" has none).
    local cut="${b%-*}"
    if [ "$cut" != "$b" ] && [ "${#cut}" -ge $((STAGING_BRANCH_MAXLEN / 2)) ]; then
      b="$cut"
    fi
    b="${b%-}"
  fi
  # A branch of nothing but separators ("---") would leave an empty label.
  printf '%s' "${b:-unknown}"
}

case "$kind" in
  production|prod)
    base="$(date -u +%Y.%m.%d)"
    if ! tag_exists "$base"; then
      echo "$base"
      exit 0
    fi
    # Go past the HIGHEST suffix in use, not into the first gap: if 2026.07.29.1
    # was deleted while .2 exists, reissuing .1 would name a build that already
    # shipped under that number. Version numbers only ever move forward.
    highest=0
    while read -r n; do
      [ -n "$n" ] && [ "$n" -gt "$highest" ] && highest="$n"
    done < <(git tag --list "$base.*" | sed -n "s/^${base}\.\([0-9]\{1,\}\)$/\1/p")
    echo "$base.$((highest + 1))"
    ;;
  staging)
    # The checkout is a detached HEAD at the PR's commit, so the branch cannot
    # be read from git here — the caller passes it.
    branch="${2:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)}"
    echo "staging-$(short_branch "$branch")-${sha7:0:$STAGING_SHA_LEN}"
    ;;
  *)
    echo "dev-${sha7}"
    ;;
esac
