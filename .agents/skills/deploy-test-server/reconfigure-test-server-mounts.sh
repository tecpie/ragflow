#!/bin/bash
# Legacy one-shot reconfigure for 172.16.0.31 (stable + debug).
# For debug-only full refresh from image, prefer:
#   uv run python .agents/skills/deploy-test-server/sync_to_test_server.py --full [--migrate]
set -euo pipefail

STABLE_DIR="/data/docker/ragflow"
DEBUG_DIR="/data/docker/ragflow-debug"
STABLE_COMPOSE="$STABLE_DIR/docker-compose.yml"
DEBUG_COMPOSE="$DEBUG_DIR/docker-compose.yml"

backup() {
  cp -a "$1" "$1.bak-$(date +%Y%m%d%H%M%S)"
}

ensure_stable_no_code_mount() {
  backup "$STABLE_COMPOSE"
  if grep -q './ragflow:/ragflow' "$STABLE_COMPOSE"; then
    sed -i '/^[[:space:]]*- \.\/ragflow:\/ragflow[[:space:]]*$/d' "$STABLE_COMPOSE"
    echo "removed stable ./ragflow mount"
  else
    echo "stable already has no ./ragflow mount"
  fi
}

ensure_debug_code_mount() {
  backup "$DEBUG_COMPOSE"
  if grep -q './ragflow:/ragflow' "$DEBUG_COMPOSE"; then
    echo "debug already mounts ./ragflow"
    return
  fi
  sed -i '/^[[:space:]]*volumes:/a\      - ./ragflow:/ragflow' "$DEBUG_COMPOSE"
  echo "added debug ./ragflow mount"
}

seed_debug_code_from_image() {
  mkdir -p "$DEBUG_DIR/ragflow"
  echo "seeding debug code dir from ragflow-debug container image ..."
  docker cp ragflow-debug:/ragflow/. "$DEBUG_DIR/ragflow/"
  # Keep host-managed runtime config out of the bind mount tree.
  rm -f "$DEBUG_DIR/ragflow/conf/service_conf.yaml"
}

pull_and_recreate() {
  local dir=$1
  local service=$2
  echo "=== pull + recreate in $dir ($service) ==="
  cd "$dir"
  docker-compose pull "$service"
  docker-compose --profile cpu up -d --force-recreate "$service"
}

ensure_stable_no_code_mount
ensure_debug_code_mount
seed_debug_code_from_image
pull_and_recreate "$STABLE_DIR" ragflow-cpu
pull_and_recreate "$DEBUG_DIR" ragflow-cpu

echo "=== verify mounts ==="
echo "stable:"
docker inspect ragflow --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}' | grep -E 'ragflow|deepdoc' || true
echo "debug:"
docker inspect ragflow-debug --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}' | grep -E 'ragflow|deepdoc' || true

echo "=== verify agent reference code (debug mount vs stable image) ==="
docker exec ragflow-debug grep -n 'conv.reference' /ragflow/api/db/services/canvas_service.py | tail -1
docker exec ragflow grep -n 'conv.reference' /ragflow/api/db/services/canvas_service.py | tail -1
