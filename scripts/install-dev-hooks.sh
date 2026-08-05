#!/usr/bin/env bash
# Install local git hooks that keep the ~/go/bin/slimemold binary (the one the
# Claude Code Stop/UserPromptSubmit hooks invoke) in sync with the source after
# every commit and pull/merge.
#
# Why: .git/hooks/pre-push builds to /tmp and deletes it — it's a compile
# check, not an artifact refresh — so committing/pushing does NOT reinstall the
# binary the live hook runs. Without this, an edit can leave the hook executing
# stale logic (it ran a ~9h-old binary on 2026-06-10).
#
# The hook target lives in ~/go/bin (matching every sibling tool — hindcast,
# weir, bmg, …), NOT in the repo working tree. A working-tree binary invites a
# well-meaning sibling agent to "clean up" the stray artifact and break the
# hooks machine-wide (seen 2026-08-05). `go install` keeps the tracked location
# fresh; a runtime staleBinaryCheck no longer applies to it (no go.mod beside
# ~/go/bin), so this rebuild-on-commit IS the freshness mechanism.
#
# Idempotent. Run once per clone:  ./scripts/install-dev-hooks.sh
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
hooks="$root/.git/hooks"

write_hook() {
  local name="$1"
  cat > "$hooks/$name" <<'HOOK'
#!/usr/bin/env bash
# Reinstall the binary the Claude Code hooks invoke (~/go/bin/slimemold).
# Managed by scripts/install-dev-hooks.sh — re-run it to update.
root="$(git rev-parse --show-toplevel)" || exit 0
cd "$root" || exit 0
if go install . 2>/tmp/slimemold-hook-build.err; then
  echo "slimemold: reinstalled to $(go env GOPATH)/bin"
else
  echo "slimemold: INSTALL FAILED — run 'go install .' ($(cat /tmp/slimemold-hook-build.err 2>/dev/null | head -1))" >&2
fi
HOOK
  chmod +x "$hooks/$name"
  echo "installed $name"
}

write_hook post-commit
write_hook post-merge
echo "done — ~/go/bin/slimemold will now reinstall after each commit and pull/merge."
