#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACK_DIR="$ROOT_DIR/windows-pack"
DIST_DIR="$ROOT_DIR/dist"
OUTPUT_ZIP="$DIST_DIR/ragflow-browser-gateway-windows-amd64.zip"

resolve_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return 0
  fi
  for candidate in \
    "${GO:-}" \
    "/opt/homebrew/bin/go" \
    "/usr/local/go/bin/go" \
    "/tmp/go/bin/go" \
    "$HOME/go/bin/go"; do
    if [[ -n "$candidate" && -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

GO_BIN="$(resolve_go || true)"
if [[ -z "${GO_BIN:-}" ]]; then
  cat <<'EOF'
Error: Go toolchain not found (go: command not found).

Install Go, then re-run this script:
  brew install go

Or set GO to your go binary, e.g.:
  export GO=/usr/local/go/bin/go
  ./build-windows-pack.sh
EOF
  exit 1
fi

mkdir -p "$DIST_DIR"

echo "==> Using Go: $("$GO_BIN" version)"
echo "==> Download Go module dependencies"
(
  cd "$ROOT_DIR"
  "$GO_BIN" mod tidy
)

echo "==> Cross compile Windows amd64 binary"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build \
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
