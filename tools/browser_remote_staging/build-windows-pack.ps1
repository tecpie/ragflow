$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$PackDir = Join-Path $RootDir "windows-pack"
$DistDir = Join-Path $RootDir "dist"
$OutputZip = Join-Path $DistDir "ragflow-browser-gateway-windows-amd64.zip"

New-Item -ItemType Directory -Force -Path $DistDir | Out-Null

Write-Host "==> Download Go module dependencies"
Push-Location $RootDir
go mod tidy
Pop-Location

Write-Host "==> Build Windows amd64 binary"
$ExePath = Join-Path $PackDir "ragflow-browser-gateway.exe"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o $ExePath (Join-Path $RootDir "cmd/ragflow-browser-gateway")

Write-Host "==> Create zip package"
if (Test-Path $OutputZip) { Remove-Item $OutputZip -Force }
Compress-Archive -Path @(
    (Join-Path $PackDir "ragflow-browser-gateway.exe"),
    (Join-Path $PackDir "start.bat"),
    (Join-Path $PackDir "config.env"),
    (Join-Path $PackDir "README.md")
) -DestinationPath $OutputZip

Write-Host "Done: $OutputZip"
Get-Item $OutputZip | Format-List FullName, Length
