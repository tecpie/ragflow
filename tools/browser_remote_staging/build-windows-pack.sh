#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACK_DIR="$ROOT_DIR/windows-pack"
DIST_DIR="$ROOT_DIR/dist"
OUTPUT_ZIP="$DIST_DIR/ragflow-browser-gateway-windows-amd64.zip"

mkdir -p "$DIST_DIR"

echo "==> Download Go module dependencies"
(
  cd "$ROOT_DIR"
  go mod tidy
)

echo "==> Cross compile Windows amd64 binary"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w" \
  -o "$PACK_DIR/ragflow-browser-gateway.exe" \
  "$ROOT_DIR/cmd/ragflow-browser-gateway"

echo "==> Create zip package"
rm -f "$OUTPUT_ZIP"
(
  cd "$PACK_DIR"
  zip -r "$OUTPUT_ZIP" \
    ragflow-browser-gateway.exe \
    start.bat \
    config.env \
    README.md
)

echo "Done: $OUTPUT_ZIP"
ls -lh "$OUTPUT_ZIP"
