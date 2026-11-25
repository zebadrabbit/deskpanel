#!/bin/sh
# Test, then cross-compile the binary for the GPU hosts into dist/. Needs only Docker.
set -e
cd "$(dirname "$0")"
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.26-alpine sh -ec '
  go vet ./... && go test ./...
  for t in linux/amd64 windows/amd64; do
    os=${t%/*}; arch=${t#*/}; ext=; [ "$os" = windows ] && ext=.exe
    GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/deskpanel-$os-$arch$ext .
  done'
ls -l dist
