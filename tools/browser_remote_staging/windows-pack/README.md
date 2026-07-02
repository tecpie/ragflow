# RAGFlow Browser Gateway - Windows 内网一键包

本目录为 **Windows 内网可直接运行** 的程序包，无需安装 Python。

## 包含内容

- `ragflow-browser-gateway.exe`：单文件网关（CDP 代理 + 文件 staging）
- `start.bat`：一键启动
- `stop.bat`：停止服务
- `config.env`：配置文件

## 使用前准备

1. 在本机启动 Chrome（仅本机调试，9222 不对外）：

```powershell
& "C:\Program Files\Google\Chrome\Application\chrome.exe" `
  --remote-debugging-port=9222 `
  --user-data-dir="C:\ragflow\chrome-profile"
```

2. 编辑 `config.env`，至少修改 `BROWSER_STAGING_TOKEN`

3. 双击 `start.bat` 或命令行执行：

```bat
start.bat
```

4. Windows 防火墙放行 **19080**（或你在 config.env 里设置的端口）

## RAGFlow 配置

Browser 节点：

| 配置 | 值 |
|------|-----|
| 使用 CDP 连接 | 开启 |
| CDP 地址 | `http://<windows-ip>:19080` |
| 远程暂存地址 | `http://<windows-ip>:19080` |
| 远程暂存 Token | 与 `config.env` 中一致 |
| 上传来源 | file_id 或变量 |

## 健康检查

```bat
curl http://127.0.0.1:19080/health
```

## 端口说明

默认 **19080** 同时提供：

- `/health`：健康检查
- `/staging/upload`：RAGFlow 推送上传文件
- `/json/*`、`/devtools/*`：转发到本机 Chrome CDP

## 重新打包（开发者）

在有 Go 环境的机器上执行：

```bash
cd tools/browser_remote_staging
./build-windows-pack.sh
```

Windows 上也可执行：

```powershell
.\build-windows-pack.ps1
```

输出：`dist/ragflow-browser-gateway-windows-amd64.zip`
