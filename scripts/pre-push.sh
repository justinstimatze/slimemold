#!/usr/bin/env bash
# Mirror of .github/workflows/ci.yml — run before pushing to catch CI failures locally.
# Install: ln -sf ../../scripts/pre-push.sh .git/hooks/pre-push
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

echo "→ go build"
go build -o /tmp/slimemold-prepush . >/dev/null
rm -f /tmp/slimemold-prepush

echo "→ go test"
go test ./... -count=1 -timeout 5m >/dev/null

if command -v golangci-lint >/dev/null 2>&1; then
    echo "→ golangci-lint"
    golangci-lint run ./...
else
    echo "⚠ golangci-lint not installed — CI will run it; install with:"
    echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"
fi

echo "✓ pre-push checks passed"
