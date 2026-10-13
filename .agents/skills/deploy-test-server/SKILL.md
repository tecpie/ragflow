---
name: deploy-test-server
description: Sync RAGFlow code to the internal test server 172.16.0.31 and restart ragflow-cpu. Use when the user asks to deploy, sync, or push to the test environment (测试环境, 测试服务器, 发到31, sync to test server, 部署到debug, 部署到stable, debug全量, debug增量).
---

# Deploy to RAGFlow Test Server

**Always use the project deploy script.** Do not hand-roll scp/ssh one-offs.

## Entry points

| Platform | Command |
|----------|---------|
| Windows (recommended) | `.\scripts\sync-to-test-server.ps1` |
| Direct skill path | `.\.agents\skills\deploy-test-server\sync-to-test-server.ps1` |
| Linux/macOS | `uv run python .agents/skills/deploy-test-server/sync_to_test_server.py` |

Thin wrappers in `scripts/` delegate to this skill directory.

## Environments

| Target | Purpose | Web | API | Container |
|--------|---------|-----|-----|-----------|
| **debug** (default) | Daily dev, host code mount | http://172.16.0.31:8080 | :9390 | `ragflow-debug` |
| **stable** | Production-like, image + compose bind files | http://172.16.0.31 | :9380 | `ragflow` |

Both share MySQL/ES/MinIO/Redis; debug uses DB `ragflow_debug` and Redis db 2 (`REDIS_DB=2`).

**Image policy** (built by `docker/build_amd64_image.sh`):

| Target | Tag | Example |
|--------|-----|---------|
| **debug** | `amd64-dev` (floating, tracks latest dev build) | `registry.cn-hangzhou.aliyuncs.com/tecpie/ragflow:amd64-dev` |
| **stable** | `amd64-<branch>-<commit>` (pinned release) | `.../ragflow:amd64-dev-9421a03c7` |

## Docker directory sync（debug / stable 共通）

`--full` 与 `--release-stable` 都会把本地 **`docker/`** 同步到对应 compose 目录：

| Target | Remote docker dir |
|--------|-------------------|
| debug | `/data/docker/ragflow-debug` |
| stable | `/data/docker/ragflow` |

同步内容包括：`docker-compose.yml`、`docker-compose-base.yml`、`.env`、`entrypoint.sh`、`service_conf.yaml.template`、`nginx/` 等（跳过 `build_amd64_image.sh`）。

### `.env` 合并规则

- 以本地 `docker/.env` 为模板扫键。
- **已有键：保留服务器上现有值**（端口、密码、MYSQL_HOST 等不覆盖）。
- **模板新增键：追加写入。**
- 服务器上多出的自定义键：保留在文件末尾。
- 部署动作仍会 **强制写入** 目标相关键：
  - debug：`RAGFLOW_IMAGE=...:amd64-dev`、`API_PROXY_SCHEME=python`、`REDIS_DB=2`、`GO_HTTP_PORT=9394`、`GO_ADMIN_PORT=9393`
  - stable：`RAGFLOW_IMAGE=...:amd64-<branch>-<commit>`（可被 `STABLE_RAGFLOW_IMAGE` 覆盖）

### `docker-compose.yml` 目标补丁

从仓库同步后按目标打补丁（测试机 MySQL/ES 为外部主机）：

| | debug | stable |
|--|--------|--------|
| `container_name` | `ragflow-debug` | `ragflow` |
| `depends_on` mysql/es | 注释掉 | 注释掉 |
| `--init-model-provider-tables` | 去掉（测试库 collation 混用会启动失败） | 同上 |
| 代码挂载 | `./ragflow:/ragflow` | **无**（禁止挂 debug 目录） |
| `entrypoint` / `service_conf` | 挂载 | 挂载 |
| docker.sock | 挂载 | 挂载 |

**Do not** leave stable mounting `../ragflow-debug/ragflow`. Stable 代码来自镜像；compose 只绑 entrypoint / service_conf / logs。

## Debug update modes

Debug uses a **host bind mount** (`./ragflow:/ragflow`). Updates fall into two modes:

### Incremental（增量，日常开发）

Upload changed files into the mounted directory on 31, then restart.

Paths under `docker/` upload into the compose dir (not `ragflow/`). `docker/.env` uses the merge rules above. `docker-compose.yml` is patched for debug.

If any uploaded path is under `web/`, or you pass `--build-web`, the script:

1. Runs **`npm run build` locally** in `web/`
2. Packs `web/dist` into `.tmp-deploy/web-dist.zip`
3. Uploads the zip to the server
4. Extracts into `/data/docker/ragflow-debug/ragflow/web/dist`

Do **not** run `npm run build` inside the container.

```powershell
# Default: git changed/untracked files
.\scripts\sync-to-test-server.ps1 --incremental

# Specific files (web paths auto-trigger local build + dist zip deploy)
.\scripts\sync-to-test-server.ps1 agent/canvas.py web/src/pages/agent/chat/use-send-agent-message.ts

# Frontend only: local build + zip upload (no source-file sync)
.\scripts\sync-to-test-server.ps1 --build-web

# Force frontend rebuild after uploading other files
.\scripts\sync-to-test-server.ps1 --incremental --build-web
```

### Full（全量，大版本/换镜像后）

1. Sync local `docker/` (compose / `.env` merge / entrypoint / nginx …) + force debug image / `REDIS_DB=2`
2. `docker-compose pull`
3. Temporarily remove `./ragflow:/ragflow`, recreate from image
4. `docker cp` `/ragflow` → host `./ragflow/`
5. Restore bind mount and recreate

```powershell
.\scripts\sync-to-test-server.ps1 --full
.\scripts\sync-to-test-server.ps1 --full --migrate
```

After `--full`, if you need a newer frontend than the image, run `--build-web` separately.

## Stable release

1. Sync local `docker/` (compose without code mount / `.env` merge)
2. Force pinned `RAGFLOW_IMAGE`
3. pull + recreate

```powershell
.\scripts\sync-to-test-server.ps1 --release-stable --target stable
```

Override pinned tag: set `STABLE_RAGFLOW_IMAGE` in `.agents/skills/deploy-test-server/test-server.env`.

**Do not SFTP-sync application code into stable.** Stable has no live code mount.

## First-time setup

```powershell
.\scripts\sync-to-test-server.ps1 --setup-ssh
```

Needs `SYNC_PASS` in `.agents/skills/deploy-test-server/test-server.env` for the first SSH key install.

Local frontend build requires Node.js/`npm` and `web/node_modules` (`cd web && npm install` once).

Linux/macOS:

```bash
uv run python .agents/skills/deploy-test-server/sync_to_test_server.py --incremental
uv run python .agents/skills/deploy-test-server/sync_to_test_server.py --build-web
uv run python .agents/skills/deploy-test-server/sync_to_test_server.py --full --migrate
uv run python .agents/skills/deploy-test-server/sync_to_test_server.py --release-stable --target stable
```

## Agent workflow

When the user asks to deploy to debug:

1. Confirm `.agents/skills/deploy-test-server/test-server.env` exists (copy from `test-server.env.example`).
2. **Incremental** unless they say 全量 / 换镜像 / 大版本 / full.
3. **Full** when merging main, publishing a new image, or resetting debug code to match image.
4. After full upgrade across v0.27+, use `--migrate`.
5. For frontend-only fixes, prefer `--build-web` (local build + zip).
6. Before modifying the server, list the non-local change set (compose / `.env` merge / mounts).
7. Report steps and suggest verification:

```bash
docker logs ragflow-debug 2>&1 | tail -30
curl http://172.16.0.31:9390/api/v1/system/ping
docker inspect ragflow --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}'
```

## Script flags

| Flag | Description |
|------|-------------|
| `--incremental` / `--changed` | Upload changed/untracked files to debug mount (default) |
| `--full` | Sync `docker/` + refresh host `ragflow/` from image |
| `--migrate` | Run `run_migrations.sh` after `--full` |
| `--target debug\|stable` | Deploy target (default: debug) |
| `--no-restart` | Incremental only: skip container restart |
| `--build-web` | Local `npm run build`, zip `web/dist`, upload + extract on debug |
| `--release-stable` | Sync `docker/` + pull pinned image + recreate stable |
| `--setup-ssh` | One-time SSH key install |
| `--all` | Incremental: all git-tracked files (default excludes `web/`) |

## Server layout

| Item | Debug | Stable |
|------|-------|--------|
| Compose dir | `/data/docker/ragflow-debug` | `/data/docker/ragflow` |
| Code source | Host mount `./ragflow:/ragflow` | Docker image only |
| Compose bind | entrypoint + service_conf (+ code) | entrypoint + service_conf |
| Frontend | Local build zip → `web/dist` | Image baked-in |

## Config

- Template: `.agents/skills/deploy-test-server/test-server.env.example`
- Local override (gitignored): `.agents/skills/deploy-test-server/test-server.env`
- Optional local overlay (gitignored): `.agents/skills/deploy-test-server/test-server.local.env`
- SSH key: `~/.ssh/id_ed25519_ragflow`

## Skill directory layout

```
.agents/skills/deploy-test-server/
├── SKILL.md                          # this file
├── sync_to_test_server.py            # core deploy logic
├── sync-to-test-server.ps1           # Windows entry
├── test-server.env.example           # config template
├── test-server.env                   # local secrets (gitignored)
├── test-server.local.env             # optional local overrides (gitignored)
└── reconfigure-test-server-mounts.sh # legacy one-shot mount fix (server-side)
```

Backward-compatible wrappers: `scripts/sync-to-test-server.ps1`, `scripts/sync_to_test_server.py`.
