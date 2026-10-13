#!/usr/bin/env bash
#
# Build an AMD64 RAGFlow Docker image for the current git branch and verify startup.
#
# Usage:
#   ./docker/build_amd64_image.sh              # build + verify
#   ./docker/build_amd64_image.sh --build-only # build only
#   ./docker/build_amd64_image.sh --no-cache   # rebuild without cache
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

IMAGE_REPO="${RAGFLOW_IMAGE_REPO:-registry.cn-hangzhou.aliyuncs.com/tecpie/ragflow}"
NEED_MIRROR="${NEED_MIRROR:-1}"
VERIFY_TIMEOUT="${VERIFY_TIMEOUT:-600}"
VERIFY_HTTP_PORT="${VERIFY_HTTP_PORT:-19380}"
VERIFY_WEB_PORT="${VERIFY_WEB_PORT:-9080}"

BUILD_ONLY=0
NO_CACHE=""
PUSH=0

usage() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Build linux/amd64 RAGFlow image tagged by current git branch, then verify startup.

Options:
  --build-only    Build image only, skip startup verification
  --no-cache      Build without Docker layer cache
  --push          Push image to registry after build
  -h, --help      Show this help
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --build-only) BUILD_ONLY=1 ;;
        --no-cache) NO_CACHE="--no-cache" ;;
        --push) PUSH=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
    esac
    shift
done

if ! command -v docker >/dev/null 2>&1; then
    echo "Error: docker is not installed or not in PATH" >&2
    exit 1
fi

if ! git -C "$PROJECT_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "Error: $PROJECT_ROOT is not a git repository" >&2
    exit 1
fi

BRANCH="$(git -C "$PROJECT_ROOT" branch --show-current)"
COMMIT="$(git -C "$PROJECT_ROOT" rev-parse --short HEAD)"
BRANCH_TAG="$(echo "$BRANCH" | sed 's/[^a-zA-Z0-9._-]/-/g')"
IMAGE_TAG="amd64-${BRANCH_TAG}"
IMAGE="${IMAGE_REPO}:${IMAGE_TAG}"
IMAGE_COMMIT="${IMAGE_REPO}:${IMAGE_TAG}-${COMMIT}"

echo "========================================"
echo " RAGFlow AMD64 Image Build"
echo "========================================"
echo "Branch : ${BRANCH}"
echo "Commit : ${COMMIT}"
echo "Image  : ${IMAGE}"
echo "Also   : ${IMAGE_COMMIT}"
echo "Mirror : NEED_MIRROR=${NEED_MIRROR}"
echo "========================================"

echo
echo ">>> Building image (platform=linux/amd64)..."
DOCKER_BUILDKIT=1 docker build \
    --platform linux/amd64 \
    --build-arg "NEED_MIRROR=${NEED_MIRROR}" \
    ${NO_CACHE} \
    -f "${PROJECT_ROOT}/Dockerfile" \
    -t "${IMAGE}" \
    -t "${IMAGE_COMMIT}" \
    "${PROJECT_ROOT}"

echo
echo "Build succeeded: ${IMAGE}"

if [[ "$PUSH" -eq 1 ]]; then
    echo ">>> Pushing image..."
    docker push "${IMAGE}"
    docker push "${IMAGE_COMMIT}"
fi

if [[ "$BUILD_ONLY" -eq 1 ]]; then
    echo "Skipping startup verification (--build-only)."
    exit 0
fi

echo
echo ">>> Verifying image startup..."

cd "$SCRIPT_DIR"

# Ensure dependency services are running.
docker compose up -d mysql redis minio es01

# Remove stale verify container if present.
docker compose --profile cpu rm -sf ragflow-cpu 2>/dev/null || true

echo "Starting ragflow-cpu with verify ports (HTTP=${VERIFY_HTTP_PORT}, WEB=${VERIFY_WEB_PORT})..."
RAGFLOW_IMAGE="${IMAGE}" \
SVR_HTTP_PORT="${VERIFY_HTTP_PORT}" \
SVR_WEB_HTTP_PORT="${VERIFY_WEB_PORT}" \
SVR_WEB_HTTPS_PORT=9443 \
ADMIN_SVR_HTTP_PORT=19381 \
SVR_MCP_PORT=19382 \
GO_HTTP_PORT=19384 \
GO_ADMIN_PORT=19383 \
docker compose --profile cpu up -d ragflow-cpu

PING_URL="http://127.0.0.1:${VERIFY_HTTP_PORT}/api/v1/system/ping"
echo "Waiting for ${PING_URL} (timeout=${VERIFY_TIMEOUT}s)..."

deadline=$((SECONDS + VERIFY_TIMEOUT))
while (( SECONDS < deadline )); do
    if curl -sf "${PING_URL}" >/dev/null 2>&1; then
        echo
        echo "Startup verification passed."
        curl -s "${PING_URL}" || true
        echo
        echo "RAGFlow is running:"
        echo "  API : http://127.0.0.1:${VERIFY_HTTP_PORT}"
        echo "  Web : http://127.0.0.1:${VERIFY_WEB_PORT}"
        echo "  Image: ${IMAGE}"
        exit 0
    fi
    sleep 5
    echo "  still waiting... ($(( deadline - SECONDS ))s remaining)"
done

echo
echo "Startup verification failed after ${VERIFY_TIMEOUT}s." >&2
docker compose --profile cpu logs --tail 80 ragflow-cpu >&2 || true
exit 1
