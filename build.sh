#!/bin/sh
# ./build.sh          the web app's engine, then braid and braid-hub for this PC, into dist/
# ./build.sh release  also every platform's braid into dist/update/ and the hub
#                     for Linux amd64/arm64 into dist/hub-linux-<arch>/ (what the
#                     container image is made from)
# VERSION=v1.2.3 sets the version builds report (default: dev, which never self-updates).
set -e
cd "$(dirname "$0")"
LD="-s -w -X github.com/Simplyph0/braid/internal/update.Version=${VERSION:-dev}"
export CGO_ENABLED=0
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o cmd/braid-hub/web/braid.wasm ./cmd/braid-web
go build -trimpath -ldflags="$LD" -o dist/ ./cmd/braid ./cmd/braid-hub
[ "$1" = release ] || exit 0
mkdir -p dist/update
for p in linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64; do
  os=${p%/*} arch=${p#*/} ext=""
  [ "$os" = windows ] && ext=.exe
  GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$LD" -o "dist/update/braid-$os-$arch$ext" ./cmd/braid
done
for arch in amd64 arm64; do
  GOOS=linux GOARCH=$arch go build -trimpath -ldflags="$LD" -o "dist/hub-linux-$arch/braid-hub" ./cmd/braid-hub
done
