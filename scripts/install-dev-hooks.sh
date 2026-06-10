#!/usr/bin/env bash
# Install local git hooks that keep ./slimemold (the binary the Claude Code
# Stop/UserPromptSubmit hooks invoke) in sync with the source after every
# commit and pull/merge.
#
# Why: .git/hooks/pre-push builds to /tmp and deletes it — it's a compile
# check, not an artifact refresh — so committing/pushing does NOT rebuild the
# binary the live hook runs. Without this, an edit can leave the hook executing
# stale logic (it ran a ~9h-old binary on 2026-06-10). The binary also self-
# reports staleness at runtime (staleBinaryCheck in main.go); this just makes
# the common case auto-heal so you rarely see the warning.
#
# Idempotent. Run once per clone:  ./scripts/install-dev-hooks.sh
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
hooks="$root/.git/hooks"

write_hook() {
  local name="$1"
  cat > "$hooks/$name" <<'HOOK'
#!/usr/bin/env bash
# Auto-rebuild the binary the Claude Code hooks invoke. Managed by
# scripts/install-dev-hooks.sh — re-run it to update.
root="$(git rev-parse --show-toplevel)" || exit 0
cd "$root" || exit 0
if go build -o slimemold . 2>/tmp/slimemold-hook-build.err; then
  echo "slimemold: rebuilt ./slimemold"
else
  echo "slimemold: REBUILD FAILED — run 'go build -o slimemold .' ($(cat /tmp/slimemold-hook-build.err 2>/dev/null | head -1))" >&2
fi
HOOK
  chmod +x "$hooks/$name"
  echo "installed $name"
}

write_hook post-commit
write_hook post-merge
echo "done — ./slimemold will now rebuild after each commit and pull/merge."
