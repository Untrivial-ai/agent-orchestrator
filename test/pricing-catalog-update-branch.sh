#!/usr/bin/env bash
set -euo pipefail

# Break caught: a refresh job has an existing remote automation branch but no
# local origin/automation tracking ref, so an unobserved force-with-lease push
# rejects the required update instead of keeping the existing PR current.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

remote="$scratch/remote.git"
seed="$scratch/seed"
worker="$scratch/worker"

git init --bare -q "$remote"
git init -q "$seed"
git -C "$seed" config user.name test
git -C "$seed" config user.email test@example.invalid
printf 'base\n' >"$seed/catalog"
git -C "$seed" add catalog
git -C "$seed" commit -m base >/dev/null
git -C "$seed" branch -M main
git -C "$seed" remote add origin "$remote"
git -C "$seed" push origin main >/dev/null 2>&1
git -C "$remote" symbolic-ref HEAD refs/heads/main
git -C "$seed" switch -c automation/pricing-catalog >/dev/null 2>&1
printf 'previous refresh\n' >"$seed/catalog"
git -C "$seed" commit -am previous >/dev/null
git -C "$seed" push origin automation/pricing-catalog >/dev/null 2>&1

git clone -q "$remote" "$worker"
git -C "$worker" config user.name test
git -C "$worker" config user.email test@example.invalid
git -C "$worker" update-ref -d refs/remotes/origin/automation/pricing-catalog
if git -C "$worker" show-ref --verify --quiet refs/remotes/origin/automation/pricing-catalog; then
  echo "worker unexpectedly tracks the automation branch" >&2
  exit 1
fi
printf 'new refresh\n' >"$worker/catalog"
git -C "$worker" add catalog
git -C "$worker" commit -m refresh >/dev/null

(
  cd "$worker"
  "$repo_root/scripts/pricing-catalog-update-branch.sh" origin automation/pricing-catalog >/dev/null 2>&1
)
if ! git -C "$worker" show-ref --verify --quiet refs/remotes/origin/automation/pricing-catalog; then
  echo "helper did not fetch the observed automation branch" >&2
  exit 1
fi

expected="$(git -C "$worker" rev-parse HEAD)"
actual="$(git -C "$remote" rev-parse refs/heads/automation/pricing-catalog)"
if [ "$actual" != "$expected" ]; then
  echo "remote automation branch = $actual, want $expected" >&2
  exit 1
fi
