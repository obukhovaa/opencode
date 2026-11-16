#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
while IFS= read -r name; do unset "$name"; done < <(git rev-parse --local-env-vars)
git init --bare -q "$WORK/origin.git"
git init -q "$WORK/repo"
git -C "$WORK/repo" config user.name 'Release test'
git -C "$WORK/repo" config user.email 'release@example.invalid'
git -C "$WORK/repo" config commit.gpgsign false
git -C "$WORK/repo" config tag.gpgsign false
git -C "$WORK/repo" config core.hooksPath /dev/null
git -C "$WORK/repo" remote add origin "$WORK/origin.git"
git -C "$WORK/repo" commit --allow-empty -qm initial
git -C "$WORK/repo" tag v1.0.0
git -C "$WORK/repo" push -q origin refs/tags/v1.0.0
git -C "$WORK/repo" tag v1.0.1
(cd "$WORK/repo" && bash "$ROOT/scripts/release" --minor)
test "$(git --git-dir="$WORK/origin.git" tag --list)" = $'v1.0.0\nv1.1.0'
echo 'PASS release publishes only the requested tag'
