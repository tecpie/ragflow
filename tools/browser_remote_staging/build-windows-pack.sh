#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PACK_DIR="$ROOT_DIR/windows-pack"
DIST_DIR="$ROOT_DIR/dist"
OUTPUT_ZIP="$DIST_DIR/ragflow-browser-gateway-windows-amd64.zip"
EXE_PATH="$PACK_DIR/ragflow-browser-gateway.exe"

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
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Go/bin/go.exe" \
    "$HOME/go/bin/go"; do
    if [[ -n "$candidate" && -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  if compgen -G "$HOME/sdk/go"*/bin/go >/dev/null 2>&1; then
    for candidate in "$HOME/sdk/go"*/bin/go; do
      if [[ -x "$candidate" ]]; then
        echo "$candidate"
        return 0
      fi
    done
  fi
  return 1
}

require_go_version() {
  local version_line major minor
  version_line="$("$GO_BIN" version 2>/dev/null || true)"
  if [[ "$version_line" =~ go([0-9]+)\.([0-9]+) ]]; then
    major="${BASH_REMATCH[1]}"
    minor="${BASH_REMATCH[2]}"
    if (( major < 1 || (major == 1 && minor < 21) )); then
      cat <<EOF
Error: Go 1.21+ is required (found: $version_line).

Install or upgrade Go, then re-run:
  brew install go
EOF
      exit 1
    fi
  fi
}

download_modules() {
  local proxy="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}"
  export GOPROXY="$proxy"
  export GOSUMDB="${GOSUMDB:-sum.golang.org}"
  # Avoid unexpected auto-download of a newer Go toolchain during CI/offline builds.
  export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

  echo "==> Download Go module dependencies (GOPROXY=$GOPROXY, GOTOOLCHAIN=$GOTOOLCHAIN)"
  (
    cd "$ROOT_DIR"
    if [[ -f go.sum ]]; then
      if ! "$GO_BIN" mod download; then
        echo "Warning: go mod download failed, retrying with go mod tidy..."
        "$GO_BIN" mod tidy
      fi
    else
      "$GO_BIN" mod tidy
    fi
  )
}

create_zip_package() {
  mkdir -p "$DIST_DIR"
  rm -f "$OUTPUT_ZIP"

  if command -v zip >/dev/null 2>&1; then
    (
      cd "$PACK_DIR"
      zip -r "$OUTPUT_ZIP" \
        ragflow-browser-gateway.exe \
        start.bat \
        config.env \
        README.md
    )
    return 0
  fi

  if command -v ditto >/dev/null 2>&1; then
    ditto -c -k --sequesterRsrc --keepParent \
      "$PACK_DIR/ragflow-browser-gateway.exe" \
      "$PACK_DIR/start.bat" \
      "$PACK_DIR/config.env" \
      "$PACK_DIR/README.md" \
      "$OUTPUT_ZIP"
    return 0
  fi

  cat <<EOF
Warning: neither 'zip' nor 'ditto' found; skipped archive creation.
Windows binary is ready at:
  $EXE_PATH
EOF
}

GO_BIN="$(resolve_go || true)"
if [[ -z "${GO_BIN:-}" ]]; then
  cat <<'EOF'
Error: Go toolchain not found (go: command not found).

Install Go, then re-run this script:
  macOS:  brew install go
  Linux:  https://go.dev/dl/
  Windows: https://go.dev/dl/  (or: winget install GoLang.Go)

Or point GO to your go binary, e.g.:
  export GO=/usr/local/go/bin/go
  ./build-windows-pack.sh
EOF
  exit 1
fi

mkdir -p "$PACK_DIR" "$DIST_DIR"

echo "==> Using Go: $("$GO_BIN" version)"
require_go_version
download_modules

echo "==> Cross compile Windows amd64 binary"
(
  cd "$ROOT_DIR"
  GOOS=windows GOARCH=amd64 CGO_ENABLED=0 "$GO_BIN" build \
    -trimpath \
    -ldflags "-s -w" \
    -o "$EXE_PATH" \
    ./cmd/ragflow-browser-gateway
)

if [[ ! -f "$EXE_PATH" ]]; then
  echo "Error: build finished but exe not found: $EXE_PATH" >&2
  exit 1
fi

echo "==> Create zip package"
create_zip_package

if [[ -f "$OUTPUT_ZIP" ]]; then
  echo "Done: $OUTPUT_ZIP"
  ls -lh "$OUTPUT_ZIP"
else
  echo "Done: $EXE_PATH"
  ls -lh "$EXE_PATH"
fi
