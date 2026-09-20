#!/usr/bin/env bash
# Fast pre-commit gate — catches the cheap-to-find class of failure at
# commit time so it doesn't surface at pre-push (or, worse, in CI) on
# already-shipped commits.
#
# Install: ln -sf ../../scripts/pre-commit.sh .git/hooks/pre-commit
#
# Runs gofmt + go vet only. Deliberately does NOT run go build / go test /
# golangci-lint — those live in scripts/pre-push.sh, which is the gate for
# anything slow enough that a contributor would skip the commit checks if
# they ran every time. The hierarchy is: pre-commit catches formatting and
# trivial correctness in <2s; pre-push catches the rest before it leaves
# the laptop; CI is the final backstop.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

echo "→ gofmt"
fmt_out=$(gofmt -l . 2>&1 || true)
if [ -n "$fmt_out" ]; then
    echo "gofmt found unformatted files:"
    echo "$fmt_out"
    echo "fix with: gofmt -w ."
    exit 1
fi

echo "→ go vet"
go vet ./...

echo "✓ pre-commit checks passed"

# >>> codescene-precommit (managed) >>>
# CodeScene delta analysis on staged changes. Non-blocking: findings are
# printed, the commit proceeds. --error-on-warnings is required for cs to
# signal at all; without it cs delta exits 0 unconditionally and the warning
# below is unreachable.
if command -v cs >/dev/null 2>&1; then
  if ! cs delta --staged --error-on-warnings; then
    echo ""
    echo "CodeScene flagged code-health findings above (warning only, commit proceeds)."
  fi
fi
# <<< codescene-precommit (managed) <<<
