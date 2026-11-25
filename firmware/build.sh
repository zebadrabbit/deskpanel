#!/bin/sh
# Build with the official ESP-IDF image; output in build/. Needs only Docker.
# Run it on the machine that holds the files: the IDF component manager can't unpack onto a
# network share (Windows/SMB refuses to delete just-emptied folders).
set -e
cd "$(dirname "$0")"
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/project -w /project \
    espressif/idf:v5.5.3 idf.py "${@:-build}"
