#!/usr/bin/env bash
# Verify that the release hook cannot redirect a test's Git sandbox into the
# repository being pushed. Use a fake build and one probe, not recursive E2Es.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
while IFS= read -r git_local_var; do
    unset "$git_local_var"
done < <(git rev-parse --local-env-vars)

mkdir -p "$WORK/repo/.githooks" "$WORK/repo/scripts/test" "$WORK/bin"
cp "$ROOT/.githooks/pre-push" "$WORK/repo/.githooks/pre-push"
git -C "$WORK/repo" init -q
git -C "$WORK/repo" remote add origin https://example.invalid/parent.git

cat > "$WORK/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == build && "$2" == -o ]]
printf '#!/usr/bin/env bash\nexit 0\n' > "$3"
chmod +x "$3"
SH
cat > "$WORK/repo/scripts/test/probe.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
test -z "${GIT_DIR:-}"
test -z "${GIT_WORK_TREE:-}"
test -z "${GIT_COMMON_DIR:-}"
test -z "${GIT_INDEX_FILE:-}"
sandbox="$(mktemp -d)"
trap 'rm -rf "$sandbox"' EXIT
git -C "$sandbox" init -q
git -C "$sandbox" remote add origin https://example.invalid/sandbox.git
test "$(git -C "$sandbox" remote get-url origin)" = https://example.invalid/sandbox.git
SH
chmod +x "$WORK/bin/go" "$WORK/repo/scripts/test/probe.sh"

(cd "$WORK/repo" &&
    printf 'refs/tags/v9.9.9 %040d refs/tags/v9.9.9 %040d\n' 1 0 |
    env PATH="$WORK/bin:$PATH" GIT_DIR="$WORK/repo/.git" \
        GIT_WORK_TREE="$WORK/repo" GIT_COMMON_DIR="$WORK/repo/.git" \
        GIT_INDEX_FILE="$WORK/repo/.git/index" \
        bash .githooks/pre-push origin https://example.invalid/parent.git)
test "$(git -C "$WORK/repo" remote get-url origin)" = https://example.invalid/parent.git
echo 'PASS release hook isolates Git sandboxes from the pushed repository'
