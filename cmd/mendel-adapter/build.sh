#!/bin/sh
# Cross-compile the adapter and stage it beside its Dockerfile.
#
# Run at Mendel's own release time, not at deploy time: what ships to a project
# is the binary, so nothing of Mendel's source is built inside someone else's
# GCP. See dev/claude_plans/20_datastore_adapters.md.
#
# CGO off and a static link because the image has no libc to link against --
# distroless/static is the point rather than an accident, and a dynamically
# linked binary would fail at start with an error that says nothing useful.
set -eu

out="${1:-$(dirname "$0")/build}"
mkdir -p "$out"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags "-s -w" \
    -o "$out/mendel-adapter" ./cmd/mendel-adapter

cp "$(dirname "$0")/Dockerfile" "$out/Dockerfile"
echo "staged $out/mendel-adapter and its Dockerfile"
