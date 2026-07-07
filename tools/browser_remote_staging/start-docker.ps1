# 内网 Windows 无 Python：用 Docker 启动 gateway（推荐）
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

if (-not $env:BROWSER_STAGING_TOKEN) {
    Write-Host "请设置环境变量 BROWSER_STAGING_TOKEN"
    exit 1
}

if (-not $env:BROWSER_STAGING_HOST_DIR) {
    $env:BROWSER_STAGING_HOST_DIR = "$env:ProgramData\ragflow\browser-uploads"
}

New-Item -ItemType Directory -Force -Path $env:BROWSER_STAGING_HOST_DIR | Out-Null

Write-Host "Staging host dir: $($env:BROWSER_STAGING_HOST_DIR)"
$port = if ($env:BROWSER_GATEWAY_PORT) { $env:BROWSER_GATEWAY_PORT } else { "8443" }
Write-Host "Gateway port: $port"

docker compose up -d --build

Write-Host ""
Write-Host "Health check:"
Write-Host "  curl http://localhost:$port/health"
