# Browser Remote Staging（CDP 远程上传）

当 RAGFlow 运行在 Docker、Chrome 运行在远程机器并通过 CDP 连接时，网页上传依赖 Chrome 本机可读的文件路径。该组件在 **Chrome 主机** 上提供 HTTP 暂存服务，RAGFlow 先把文件推送到远程磁盘，再让 browser-use 通过 CDP 上传。

## 架构

```
RAGFlow (Docker)
  └─ Browser 节点
       ├─ 1. 从 file_id / URL 准备本地临时文件
       ├─ 2. POST 到远程 staging server
       └─ 3. 使用返回的 remote_path 调用 browser-use upload_file
Remote Chrome Host
  ├─ browser_remote_staging/server.py  (8765)
  └─ Chrome --remote-debugging-port=9222
```

## 单端口方案（Windows 推荐）

如果远程 Windows 只能对外开放 **一个端口**（例如 8443），请使用 **gateway.py**，把 CDP 和文件暂存合并到同一端口：

```
对外只开 :8443
  ├─ /health, /staging/upload  -> 本地暂存目录
  ├─ /json/*                     -> 转发到本机 Chrome :9222
  └─ /devtools/* (WebSocket)     -> 转发到本机 Chrome :9222
```

Chrome 的 9222 **不需要**对 RAGFlow 暴露，只在本机监听即可。

## 内网 Windows 没有 Python 怎么办？

### 纯 CDP Blob 传文件（无需部署程序包）

RAGFlow Browser 节点在 **CDP 模式** 下，若未配置 `remote_staging_url`，会自动使用 **Blob+CDP** 将文件写入远程 Chrome 下载目录：

```bash
# 可选：强制使用 blob 模式
RAGFLOW_BROWSER_REMOTE_UPLOAD_MODE=blob_cdp
RAGFLOW_BROWSER_CDP_BLOB_DOWNLOAD_DIR=C:\ProgramData\ragflow\browser-uploads
```

要求：远程 Chrome 至少有一个已打开的标签页（`/json/list` 可发现 page target）。

**Windows 上必须先创建下载目录**（Chrome 不会自动创建）：

```powershell
mkdir C:\ProgramData\ragflow\browser-uploads
```

Docker 侧建议设置：

```bash
RAGFLOW_BROWSER_CDP_BLOB_DOWNLOAD_DIR=C:\ProgramData\ragflow\browser-uploads
```

`auto` 模式优先级：配置了 staging URL 时用 HTTP staging，否则用 Blob+CDP。

已提供 **Windows 单文件 exe 程序包**，解压后双击 `start.bat` 即可，默认端口 **19080**：

```
tools/browser_remote_staging/dist/ragflow-browser-gateway-windows-amd64.zip
```

包内包含：

| 文件 | 说明 |
|------|------|
| `ragflow-browser-gateway.exe` | CDP 代理 + staging（无需 Python） |
| `start.bat` | 一键启动 |
| `stop.bat` | 停止服务 |
| `config.env` | 配置文件 |
| `README.md` | 使用说明 |

重新打包（开发者）：

```bash
cd tools/browser_remote_staging
./build-windows-pack.sh
```

gateway 本身是 Python 程序，但 **Windows 宿主机不必安装 Python**。也可选以下方式：

### 方案 1：Docker 跑 gateway（最推荐）

前提：Windows 上已有 **Docker Desktop**（内网可离线导入镜像）。

1. 在有网络的机器构建并导出镜像：

```bash
cd tools/browser_remote_staging
docker build -t ragflow-browser-gateway:latest .
docker save ragflow-browser-gateway:latest -o ragflow-browser-gateway.tar
```

2. 拷贝 `ragflow-browser-gateway.tar` 和整个 `tools/browser_remote_staging` 目录到内网 Windows。

3. 内网 Windows 导入并启动：

```powershell
docker load -i ragflow-browser-gateway.tar
cd tools\browser_remote_staging
$env:BROWSER_STAGING_TOKEN="your-secret-token"
$env:BROWSER_STAGING_HOST_DIR="C:\ProgramData\ragflow\browser-uploads"
.\start-docker.ps1
```

**关键**：`BROWSER_STAGING_HOST_DIR` 必须挂载到 **Windows 宿主机目录**，Chrome 才能读到上传文件。  
Gateway 容器通过 `host.docker.internal:9222` 访问本机 Chrome，Chrome 的 9222 仍不需要对外暴露。

### 方案 2：便携 Python（免安装、免管理员）

Python **Embeddable Package** 是 zip 解压即用，不写注册表。

1. 在有网络的机器下载 [Python embeddable zip](https://www.python.org/downloads/windows/)（如 `python-3.12.x-embed-amd64.zip`）。
2. 解压到 `C:\ragflow\python-embed`，按官方说明启用 pip 并安装依赖：

```powershell
cd C:\ragflow\python-embed
# 编辑 python312._pth，取消注释 import site
.\python.exe -m pip install aiohttp
```

3. 拷贝 `tools\browser_remote_staging` 到内网，启动：

```powershell
cd C:\ragflow\browser_remote_staging
$env:BROWSER_STAGING_DIR="C:\ProgramData\ragflow\browser-uploads"
$env:BROWSER_STAGING_TOKEN="your-secret-token"
C:\ragflow\python-embed\python.exe gateway.py
```

整个 `python-embed` 文件夹可通过 U 盘/内网共享拷贝，**目标机无需联网**。

### 方案 3：nginx 不能替代 staging

纯 nginx **无法**单独完成「接收上传 + 落盘到 Chrome 可读路径」。  
nginx 只能做 HTTPS 反代，后面仍需 gateway（Docker 或便携 Python）。

```
RAGFlow → nginx:443 → gateway:8443 → 写 C:\ProgramData\ragflow\browser-uploads
browser-use → nginx:443 → gateway → 转发 CDP → 本机 Chrome:9222
```

### 方案对比

| 方案 | 需要 Python 安装 | 需要 Docker | 内网离线 |
|------|------------------|-------------|----------|
| Docker gateway | 否 | 是 | 可（先 load 镜像） |
| 便携 Python embed | 否（仅解压 zip） | 否 | 可（拷贝文件夹） |
| 本机 pip 安装 | 是 | 否 | 取决于 pip 源 |

### Windows 部署

1. 安装 Python 3.10+ 和 aiohttp：

```powershell
pip install aiohttp
```

2. 启动 Chrome（仅本机调试，不对外）：

```powershell
& "C:\Program Files\Google\Chrome\Application\chrome.exe" `
  --remote-debugging-port=9222 `
  --user-data-dir="C:\ragflow\chrome-profile"
```

3. 启动网关（对外唯一端口）：

```powershell
cd tools\browser_remote_staging
$env:BROWSER_GATEWAY_PORT="8443"
$env:BROWSER_STAGING_TOKEN="your-secret-token"
$env:BROWSER_CDP_UPSTREAM="http://127.0.0.1:9222"
$env:BROWSER_STAGING_DIR="C:\ProgramData\ragflow\browser-uploads"
# 若 RAGFlow 通过域名/NAT 访问，建议显式设置公网 Origin，用于重写 CDP WebSocket 地址
# $env:BROWSER_GATEWAY_PUBLIC_ORIGIN="http://windows-host:8443"
.\start-gateway.ps1
```

4. Windows 防火墙只放行 **8443** 入站。

### RAGFlow Browser 节点配置（单端口）

| 配置项 | 值 |
|--------|-----|
| 使用 CDP 连接 | 开启 |
| CDP 地址 | `http://windows-host:8443` |
| 远程暂存地址 | `http://windows-host:8443`（与 CDP **相同**） |
| 远程暂存 Token | 与 `BROWSER_STAGING_TOKEN` 一致 |

Docker 环境变量示例：

```bash
RAGFLOW_BROWSER_REMOTE_STAGING_URL=http://windows-host:8443
RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN=your-secret-token
```

## 双端口方案（Linux / 内网）

## 1. 在 Chrome 主机部署 staging server

```bash
export BROWSER_STAGING_DIR=/data/browser-uploads
export BROWSER_STAGING_TOKEN=your-secret-token
export BROWSER_STAGING_PORT=8765
python3 tools/browser_remote_staging/server.py
```

建议使用 systemd / supervisor 常驻运行，并限制防火墙仅允许 RAGFlow 容器访问 8765 端口。

## 2. 启动 Chrome（远程调试）

```bash
google-chrome \
  --remote-debugging-port=9222 \
  --remote-debugging-address=0.0.0.0 \
  --user-data-dir=/data/chrome-profile
```

## 3. 配置 RAGFlow Browser 节点

在 Agent 的 Browser 节点中：

| 配置项 | 示例 |
|--------|------|
| 使用 CDP 连接 | 开启 |
| CDP 地址 | `http://<chrome-host>:9222` |
| 远程暂存地址 | `http://<chrome-host>:8765` |
| 远程暂存 Token | 与 `BROWSER_STAGING_TOKEN` 一致 |
| 上传来源 | `file_id` 或 `{begin@files}` |

也可通过 Docker 环境变量全局配置：

```bash
RAGFLOW_BROWSER_REMOTE_STAGING_URL=http://chrome-host:8765
RAGFLOW_BROWSER_REMOTE_STAGING_TOKEN=your-secret-token
RAGFLOW_BROWSER_REMOTE_STAGING_MAX_BYTES=104857600
RAGFLOW_BROWSER_REMOTE_STAGING_TIMEOUT=120
```

## 4. 健康检查

```bash
curl http://chrome-host:8443/health
# {"status":"ok","staging_dir":"C:\\ProgramData\\ragflow\\browser-uploads"}
```

## 安全建议

- 务必设置 `BROWSER_STAGING_TOKEN`
- 对外只暴露 gateway 端口，Chrome 9222 保持本机访问
- 定期清理 staging 目录下的历史 session 目录
