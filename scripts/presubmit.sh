#!/bin/sh
# All presubmit checks for muster: 7-bit ASCII, gofmt, go vet, tests.
# Run before committing; scripts/install-hooks.sh wires it as the
# pre-commit hook.  CI runs the same script (.github/workflows/ci.yml).
set -eu
cd "$(git rev-parse --show-toplevel)"

sh scripts/check-ascii.sh

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt: these files need formatting:" >&2
  echo "$unformatted" >&2
  exit 1
fi
echo "gofmt: OK"

go vet ./...
echo "go vet: OK"

go test ./...
echo "presubmit: OK"
