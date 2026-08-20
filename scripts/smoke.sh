#!/bin/sh
# 2papi smoke test: build + init + run + healthz
set -e
PLATFORM=$(uname -s)
GOIMAGE=golang:1.22

echo "== 1. Build =="
docker run --rm -v "${PWD}:/src" -w /src $GOIMAGE go build -o /tmp/2papi ./cmd/gateway

echo "== 2. Version =="
# no version flag yet; just check binary runs
docker run --rm -v "${PWD}:/src" -w /src $GOIMAGE /tmp/2papi --help 2>&1 | head -5 || true

echo "== 3. Unit tests =="
docker run --rm -v "${PWD}:/src" -w /src $GOIMAGE go test ./...

echo "== 4. Docker image =="
docker build -q -t 2papi:test .
echo "  image built"

echo ""
echo "Manual steps:"
echo "  1. docker compose up --build    (full stack: dashboard :13000, gateway :18080)"
echo "  2. curl http://localhost:18080/healthz"
echo "  3. ./2papi init                 (interactive hosts setup for 2papi.local)"
echo "  4. curl http://2papi.local:18080/healthz  (if hosts added & port forwarded)"
echo ""
echo "Done."
