#!/usr/bin/env bash
# Derive the app's version string. Single source of truth: the Makefile and both
# deploy workflows call this, so a local build and a deployed build label
# themselves the same way.
#
#   version.sh production          -> 2026.07.29        (first of the day)
#                                  -> 2026.07.29.1      (second, .2 third, ...)
#   version.sh staging [branch]    -> staging-my-branch-abc1234
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
# and the commit it was built from.
#
# Requires full history and tags: CI must check out with fetch-depth: 0.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

kind="${1:-dev}"
sha7="$(git rev-parse --short=7 HEAD 2>/dev/null || echo unknown)"

tag_exists() { git rev-parse -q --verify "refs/tags/$1" >/dev/null 2>&1; }

# Slashes are legal in branch names but awkward everywhere else (tags, paths,
# filenames), so flatten them.
sanitize() { printf '%s' "$1" | tr '/' '-'; }

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
    echo "staging-$(sanitize "$branch")-${sha7}"
    ;;
  *)
    echo "dev-${sha7}"
    ;;
esac
