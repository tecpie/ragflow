---
name: deploy-test-server
description: Deploy RAGFlow to the internal test server 172.16.0.31. Debug uses Go package build (remote build.sh --go + replace binary). Stable/prod use image pull. Use when the user asks to deploy, sync, or push to the test environment (测试环境, 测试服务器, 发到31, sync to test server, 部署到debug, 部署到stable, debug打包, debug全量).
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
| **debug** (default) | Daily Go binary rebuild on host mount | http://172.16.0.31:8080 | :9390 | `ragflow-debug` |
| **stable** | Production-like, image + compose bind files | http://172.16.0.31 | :9380 | `ragflow` |

Both share MySQL/ES/MinIO/Redis; debug uses DB `ragflow_debug` and Redis db 2 (`REDIS_DB=2`).

**Image policy** (built by `docker/build_amd64_image.sh`):

| Target | Tag | Example |
|--------|-----|---------|
| **debug** base image | `amd64-dev` (floating; refreshed via `--full`) | `registry.cn-hangzhou.aliyuncs.com/tecpie/ragflow:amd64-dev` |
| **stable** | `amd64-<branch>-<commit>` (pinned release) | `.../ragflow:amd64-dev-9421a03c7` |

## Why debug is package deploy (not file sync)

Backend is **Go**. Replacing `.go` / Python sources under the bind mount does **not** change the running server; `entrypoint` runs `bin/ragflow_server`. Debug daily deploy must:

1. Pack Go build inputs
2. Build on 31 with the runner image (`./build.sh --go`, reuse cpp artifacts when present)
3. Stop container → replace `/data/docker/ragflow-debug/ragflow/bin/ragflow_server` → start
4. Ping `http://127.0.0.1:9390/api/v1/system/ping`

## Docker directory sync（`--full` / `--release-stable`）

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
  - debug：`RAGFLOW_IMAGE=...:amd64-dev`、`REDIS_DB=2`、`GO_HTTP_PORT=9394`、`GO_ADMIN_PORT=9393`、`COMPOSE_PROFILES=...ragflow-go`
  - stable：`RAGFLOW_IMAGE=...:amd64-<branch>-<commit>`（可被 `STABLE_RAGFLOW_IMAGE` 覆盖）

### `docker-compose.yml` 目标补丁

| | debug | stable |
|--|--------|--------|
| `container_name` | `ragflow-debug` | `ragflow` |
| `depends_on` mysql/es | 注释掉 | 注释掉 |
| `--init-model-provider-tables` | 去掉 | 去掉 |
| 代码挂载 | `./ragflow:/ragflow`（二进制落在挂载内） | **无** |
| `entrypoint` / `service_conf` | 挂载 | 挂载 |

## Debug update modes

### Package（打包，**默认**，日常 Go 改动）

```powershell
# Default for debug — same as --package
.\scripts\sync-to-test-server.ps1
.\scripts\sync-to-test-server.ps1 --package
.\scripts\sync-to-test-server.ps1 --package --build-web
```

What it does:

1. Pack `go.mod` / `go.sum` / `build.sh` / `cmd` / `internal` / `locales` / `ragflow_deps/download_deps.py`
2. Upload to `/tmp/ragflow-go-package.tar.gz`
3. Refresh `/data/src/ragflow-build` (keeps `internal/binding/cpp/cmake-build-release`)
4. `docker run` runner → `./build.sh --go` (builds cpp first only if static lib missing)
5. `docker-compose stop` → copy `bin/ragflow_server` into debug mount → `start`
6. Health check + recent logs

Remote build dir override: `SYNC_REMOTE_BUILD_DIR` (default `/data/src/ragflow-build`).  
Runner image override: `SYNC_GO_RUNNER_IMAGE`.

### Full（镜像基底刷新，换镜像 / 大版本后）

1. Sync local `docker/` + force debug image / `REDIS_DB=2`
2. `docker-compose pull`
3. Temporarily remove `./ragflow:/ragflow`, recreate from image
4. `docker cp` `/ragflow` → host `./ragflow/`
5. Restore bind mount and recreate

```powershell
.\scripts\sync-to-test-server.ps1 --full
.\scripts\sync-to-test-server.ps1 --full --migrate
```

After `--full`, daily Go work still uses `--package`. Frontend ahead of the image: `--build-web`.

### Frontend

```powershell
.\scripts\sync-to-test-server.ps1 --build-web
```

Local `npm run build` → zip `web/dist` → extract on debug. Do **not** build web inside the container.

## Stable release

```powershell
.\scripts\sync-to-test-server.ps1 --release-stable --target stable
```

Override pinned tag: `STABLE_RAGFLOW_IMAGE` in `test-server.env`.

**Do not SFTP-sync application code into stable.** Stable has no live code mount.

## First-time setup

```powershell
.\scripts\sync-to-test-server.ps1 --setup-ssh
```

Needs `SYNC_PASS` in `.agents/skills/deploy-test-server/test-server.env` for the first SSH key install.

## Agent workflow

When the user asks to deploy to debug:

1. Confirm `.agents/skills/deploy-test-server/test-server.env` exists.
2. **Default: `--package`** (Go rebuild on 31, replace `bin/ragflow_server`).
3. **`--full`** when refreshing the base `amd64-dev` image / resetting host `ragflow/` from image.
4. After full upgrade across major versions, use `--migrate` with `--full`.
5. Frontend-only: `--build-web`.
6. Before modifying the server, list the non-local change set (build workspace / binary path / restart).
7. Report steps and suggest verification:

```bash
docker logs ragflow-debug 2>&1 | tail -30
curl http://172.16.0.31:9390/api/v1/system/ping
ls -lh /data/docker/ragflow-debug/ragflow/bin/ragflow_server
```

## Script flags

| Flag | Description |
|------|-------------|
| *(no flags)* / `--package` | **Debug default:** pack Go sources, remote `build.sh --go`, replace `bin/ragflow_server` |
| `--full` | Debug: pull image + refresh host `ragflow/` via docker cp |
| `--migrate` | Run `run_migrations.sh` after `--full` |
| `--target debug\|stable` | Deploy target (default: debug) |
| `--build-web` | Local `npm run build`, zip `web/dist`, upload + extract on debug |
| `--release-stable` | Sync `docker/` + pull pinned image + recreate stable |
| `--setup-ssh` | One-time SSH key install |
| `--all` / explicit files | Rare config-only upload into debug bind mount (not for Go) |
| `--no-restart` | With file upload only: skip container restart |

## Server layout

| Item | Debug | Stable |
|------|-------|--------|
| Compose dir | `/data/docker/ragflow-debug` | `/data/docker/ragflow` |
| Code / binary | Host mount; **binary** at `ragflow/bin/ragflow_server` | Docker image only |
| Go build workspace | `/data/src/ragflow-build` | n/a |
| Frontend | Local build zip → `web/dist` | Image baked-in |

## Config

- Template: `.agents/skills/deploy-test-server/test-server.env.example`
- Local override (gitignored): `.agents/skills/deploy-test-server/test-server.env`
- Optional local overlay (gitignored): `.agents/skills/deploy-test-server/test-server.local.env`
- SSH key: `~/.ssh/id_ed25519_ragflow`
- Optional: `SYNC_REMOTE_BUILD_DIR`, `SYNC_GO_RUNNER_IMAGE`

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
