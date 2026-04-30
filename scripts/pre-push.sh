#!/usr/bin/env bash
# Mirror of .github/workflows/ci.yml — run before pushing to catch CI failures locally.
# Install: ln -sf ../../scripts/pre-push.sh .git/hooks/pre-push
#
# What this script catches (i.e. classes of failure that will not reach CI
# if the hook is installed and passes locally):
#   - gofmt:        unformatted Go files
#   - go vet:       common Go correctness mistakes (printf, nil checks, ...)
#   - go build:     compile errors anywhere in the module
#   - go test:      every Test* function in every package; -count=1 disables
#                   the test result cache, -timeout 5m matches CI's budget
#   - golangci-lint (if installed locally): the lint set declared in
#                   .golangci.yml — currently govet, ineffassign, staticcheck,
#                   unused, misspell, bodyclose, durationcheck, nilerr,
#                   cyclop, gocognit, nestif, gosec, plus the gofmt and
#                   goimports formatters
#
# What this script does NOT catch:
#   - Runtime/integration regressions that no unit test exercises
#   - Real-DB migration issues against pre-production schemas not represented
#     in TestInventoryFlagMigration / TestSpeakerCheckMigration
#   - Extractor-prompt regressions that change LLM behavior without changing
#     test fixtures (offline structural coverage exists in
#     internal/analysis/inventory_fixtures_test.go; runtime accuracy needs
#     `slimemold calibrate` against real data — see the calibrate subcommand)
#   - Documentation drift (no automated check that README claims match code)
#   - golangci-lint version drift between local and CI (CI uses `latest` via
#     golangci-lint-action@v8; local uses whatever was last `go install`ed)
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
