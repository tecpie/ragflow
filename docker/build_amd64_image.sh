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
RESTART_PROD="${RESTART_PROD:-1}"

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

# Explicit --profile overrides COMPOSE_PROFILES from .env; include doc engine + device.
DOC_ENGINE="${DOC_ENGINE:-elasticsearch}"
DEVICE="${DEVICE:-cpu}"
COMPOSE_PROFILE_ARGS=(--profile "${DOC_ENGINE}" --profile "${DEVICE}")

# Ensure dependency services are running.
docker compose "${COMPOSE_PROFILE_ARGS[@]}" up -d mysql redis minio es01

# Remove stale verify container if present.
docker compose "${COMPOSE_PROFILE_ARGS[@]}" rm -sf ragflow-cpu 2>/dev/null || true

echo "Starting ragflow-cpu with verify ports (HTTP=${VERIFY_HTTP_PORT}, WEB=${VERIFY_WEB_PORT})..."
RAGFLOW_IMAGE="${IMAGE}" \
SVR_HTTP_PORT="${VERIFY_HTTP_PORT}" \
SVR_WEB_HTTP_PORT="${VERIFY_WEB_PORT}" \
SVR_WEB_HTTPS_PORT=9443 \
ADMIN_SVR_HTTP_PORT=19381 \
SVR_MCP_PORT=19382 \
GO_HTTP_PORT=19384 \
GO_ADMIN_PORT=19383 \
docker compose "${COMPOSE_PROFILE_ARGS[@]}" up -d ragflow-cpu

VERIFY_CONTAINER="$(docker compose "${COMPOSE_PROFILE_ARGS[@]}" ps -q ragflow-cpu)"

verify_builtin_ocr() {
    local container_id="$1"
    if [[ -z "$container_id" ]]; then
        echo "OCR verification skipped: ragflow-cpu container not found." >&2
        return 1
    fi

    echo
    echo ">>> Verifying built-in OCR (offline / intranet, no HuggingFace download)..."
    docker exec "$container_id" ls -la /ragflow/rag/res/deepdoc/ || true

    docker exec "$container_id" bash -c 'source /ragflow/.venv/bin/activate && python - <<'"'"'PY'"'"'
import os
import sys

model_dir = "/ragflow/rag/res/deepdoc"
required = ["det.onnx", "rec.onnx", "ocr.res"]
missing = [f for f in required if not os.path.exists(os.path.join(model_dir, f))]
if missing:
    print("OCR model files missing:", ", ".join(missing), file=sys.stderr)
    sys.exit(1)
for f in required:
    print(f"OK: {os.path.join(model_dir, f)}")

import numpy as np
import cv2
from deepdoc.vision.ocr import OCR

ocr = OCR()
img = np.ones((100, 300, 3), dtype=np.uint8) * 255
cv2.putText(img, "TEST", (50, 60), cv2.FONT_HERSHEY_SIMPLEX, 1, (0, 0, 0), 2)
result = ocr(img)
print("OCR inference result:", result)

mods = [
    "onnxruntime", "cv2", "deepdoc", "pdfplumber", "openpyxl",
    "pptx", "numpy", "pandas", "sklearn", "xgboost",
]
failed = []
for m in mods:
    try:
        __import__(m)
        print(f"import OK: {m}")
    except Exception as e:
        failed.append((m, str(e)))
        print(f"import FAIL: {m} -> {e}", file=sys.stderr)

if failed:
    sys.exit(2)
print("OCR verification passed.")
PY'
}

PING_URL="http://127.0.0.1:${VERIFY_HTTP_PORT}/api/v1/system/ping"
echo "Waiting for ${PING_URL} (timeout=${VERIFY_TIMEOUT}s)..."

deadline=$((SECONDS + VERIFY_TIMEOUT))
while (( SECONDS < deadline )); do
    if curl -sf "${PING_URL}" >/dev/null 2>&1; then
        echo
        echo "Startup verification passed."
        curl -s "${PING_URL}" || true
        echo

        if ! verify_builtin_ocr "${VERIFY_CONTAINER}"; then
            echo "OCR verification failed." >&2
            docker compose "${COMPOSE_PROFILE_ARGS[@]}" logs --tail 80 ragflow-cpu >&2 || true
            exit 1
        fi

        if [[ "$RESTART_PROD" == "1" ]]; then
            echo
            echo ">>> Restarting ragflow-cpu on production ports from .env..."
            docker compose "${COMPOSE_PROFILE_ARGS[@]}" rm -sf ragflow-cpu 2>/dev/null || true
            RAGFLOW_IMAGE="${IMAGE}" \
            docker compose "${COMPOSE_PROFILE_ARGS[@]}" up -d --force-recreate ragflow-cpu

            PROD_PING_URL="http://127.0.0.1:9380/api/v1/system/ping"
            echo "Waiting for production ${PROD_PING_URL}..."
            prod_deadline=$((SECONDS + VERIFY_TIMEOUT))
            while (( SECONDS < prod_deadline )); do
                if curl -sf "${PROD_PING_URL}" >/dev/null 2>&1; then
                    echo "Production startup verification passed."
                    curl -s "${PROD_PING_URL}" || true
                    echo
                    break
                fi
                sleep 5
            done
            if ! curl -sf "${PROD_PING_URL}" >/dev/null 2>&1; then
                echo "Production startup verification failed." >&2
                docker compose "${COMPOSE_PROFILE_ARGS[@]}" logs --tail 80 ragflow-cpu >&2 || true
                exit 1
            fi

            PROD_CONTAINER="$(docker compose "${COMPOSE_PROFILE_ARGS[@]}" ps -q ragflow-cpu)"
            if ! verify_builtin_ocr "${PROD_CONTAINER}"; then
                echo "Production OCR verification failed." >&2
                exit 1
            fi
        fi

        echo "RAGFlow is running:"
        if [[ "$RESTART_PROD" == "1" ]]; then
            echo "  API : http://127.0.0.1:9380"
            echo "  Web : http://127.0.0.1:80"
        else
            echo "  API : http://127.0.0.1:${VERIFY_HTTP_PORT}"
            echo "  Web : http://127.0.0.1:${VERIFY_WEB_PORT}"
        fi
        echo "  Image: ${IMAGE}"
        exit 0
    fi
    sleep 5
    echo "  still waiting... ($(( deadline - SECONDS ))s remaining)"
done

echo
echo "Startup verification failed after ${VERIFY_TIMEOUT}s." >&2
docker compose "${COMPOSE_PROFILE_ARGS[@]}" logs --tail 80 ragflow-cpu >&2 || true
exit 1
