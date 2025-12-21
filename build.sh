#!/usr/bin/env bash
set -euo pipefail

out="${1:-chase-code}"

if ! command -v dlv >/dev/null 2>&1; then
  echo "dlv not found, installing via go install" >&2
  go install github.com/go-delve/delve/cmd/dlv@latest
  if ! command -v dlv >/dev/null 2>&1; then
    gopath="$(go env GOPATH)"
    echo "dlv installed but not in PATH. Please add ${gopath%/}/bin to PATH." >&2
    exit 1
  fi
fi

go build -gcflags "all=-N -l" -o "$out" .

dlv --listen=:2346 --headless=true --api-version=2 --accept-multiclient exec "./$out"
