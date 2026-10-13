# Sync local RAGFlow files to the test server and restart ragflow-cpu.
#
# Usage:
#   .\.agents\skills\deploy-test-server\sync-to-test-server.ps1 --incremental
#   .\.agents\skills\deploy-test-server\sync-to-test-server.ps1 --build-web
#   .\.agents\skills\deploy-test-server\sync-to-test-server.ps1 --full --migrate
#   .\.agents\skills\deploy-test-server\sync-to-test-server.ps1 --release-stable --target stable
#
# Windows shortcut (same script):
#   .\scripts\sync-to-test-server.ps1 --incremental
#
# First-time setup (install SSH key, needs SYNC_PASS in test-server.env):
#   .\scripts\sync-to-test-server.ps1 --setup-ssh
#
# See .agents/skills/deploy-test-server/SKILL.md for full documentation.
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ScriptArgs
)

$ErrorActionPreference = "Stop"
$SkillDir = $PSScriptRoot
$RepoRoot = (Resolve-Path (Join-Path $SkillDir "..\..\..")).Path
$EnvFile = Join-Path $SkillDir "test-server.env"
$PyScript = Join-Path $SkillDir "sync_to_test_server.py"

if (Test-Path $EnvFile) {
    Get-Content $EnvFile | ForEach-Object {
        $line = $_.Trim()
        if (-not $line -or $line.StartsWith("#")) { return }
        $idx = $line.IndexOf("=")
        if ($idx -lt 1) { return }
        $key = $line.Substring(0, $idx).Trim()
        $value = $line.Substring($idx + 1).Trim().Trim('"').Trim("'")
        if ($key -and $null -ne (Get-Item -Path "Env:$key" -ErrorAction SilentlyContinue)) { return }
        if ($key) { Set-Item -Path "Env:$key" -Value $value }
    }
}

Push-Location $RepoRoot
try {
    if (Get-Command py -ErrorAction SilentlyContinue) {
        py -3 $PyScript @ScriptArgs
    } elseif (Get-Command uv -ErrorAction SilentlyContinue) {
        uv run python $PyScript @ScriptArgs
    } else {
        python $PyScript @ScriptArgs
    }
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
finally {
    Pop-Location
}
