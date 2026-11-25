#!/bin/sh
# The screen editor page's model tests (node --test), in Docker: needs nothing installed but Docker.
set -e
cd "$(dirname "$0")"
docker run --rm -v "$PWD":/e:ro -w /e node:22-alpine node --test
