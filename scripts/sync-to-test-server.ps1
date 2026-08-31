# Backward-compatible entry point. Implementation lives in the deploy skill.
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$ScriptArgs
)

$SkillPs1 = Join-Path $PSScriptRoot "..\.agents\skills\deploy-test-server\sync-to-test-server.ps1"
& $SkillPs1 @ScriptArgs
