# Requires Python 3.10+ and aiohttp: pip install aiohttp
$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

if (-not $env:BROWSER_GATEWAY_PORT) { $env:BROWSER_GATEWAY_PORT = "8443" }
if (-not $env:BROWSER_STAGING_TOKEN) { $env:BROWSER_STAGING_TOKEN = "change-me" }
if (-not $env:BROWSER_CDP_UPSTREAM) { $env:BROWSER_CDP_UPSTREAM = "http://127.0.0.1:9222" }
if (-not $env:BROWSER_STAGING_DIR) { $env:BROWSER_STAGING_DIR = "$env:ProgramData\ragflow\browser-uploads" }

Write-Host "Starting RAGFlow browser gateway on port $($env:BROWSER_GATEWAY_PORT)"
Write-Host "Staging dir: $($env:BROWSER_STAGING_DIR)"
Write-Host "CDP upstream: $($env:BROWSER_CDP_UPSTREAM)"

python gateway.py
