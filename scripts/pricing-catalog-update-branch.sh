#!/usr/bin/env bash
set -euo pipefail

# Update a bot-owned catalog branch only after observing its current remote
# value. The explicit lease keeps a concurrent manual/bot update from being
# overwritten, while the fetch gives checkout(main) a tracking ref for an
# already-open automation PR.
remote="${1:?usage: pricing-catalog-update-branch.sh <remote> <branch>}"
branch="${2:?usage: pricing-catalog-update-branch.sh <remote> <branch>}"
remote_line="$(git ls-remote --heads "$remote" "refs/heads/$branch")"
remote_oid=""
if [ -n "$remote_line" ]; then
  read -r remote_oid _ <<<"$remote_line"
  git fetch "$remote" "refs/heads/$branch:refs/remotes/$remote/$branch"
fi

if [ -n "$remote_oid" ]; then
  git push --force-with-lease="refs/heads/$branch:$remote_oid" "$remote" "HEAD:refs/heads/$branch"
else
  git push --force-with-lease="refs/heads/$branch:" "$remote" "HEAD:refs/heads/$branch"
fi
