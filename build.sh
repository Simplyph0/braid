#!/bin/sh
# Builds the web app's engine, then the programs, into dist/.
set -e
cd "$(dirname "$0")"
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cmd/braid-hub/web/braid.wasm ./cmd/braid-web
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/ ./cmd/braid ./cmd/braid-hub
