[CmdletBinding()]
param(
    [string]$HostName = "127.0.0.1",
    [ValidateRange(1, 65535)]
    [int]$Port = 3306,
    [string]$Database = "classicfarm",
    [string]$User = "classicfarm",
    [ValidateRange(0, 57450)]
    [int]$ServicePortOffset = 0
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "_mysql-env.ps1")

$previousDSN = $env:MYSQL_DSN
$previousPhase = $env:E2E_FRIEND_PHASE
$previousStatePath = $env:E2E_FRIEND_STATE_PATH
$connection = $null
$statePath = Join-Path $env:TEMP "classic-farm-friend-recovery-$PID.json"

try {
    $connection = Resolve-MySQLConnection `
        -HostName $HostName -Port $Port -Database $Database -User $User -AllowPrompt
    $env:MYSQL_DSN = $connection.Dsn
    $env:E2E_FRIEND_STATE_PATH = $statePath
    $env:E2E_FRIEND_PHASE = "mutate"

    & (Join-Path $PSScriptRoot "run-dual-zone-routing.ps1") `
        -FriendSlice -ServicePortOffset $ServicePortOffset
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }

    $env:E2E_FRIEND_PHASE = "recover"
    & (Join-Path $PSScriptRoot "run-dual-zone-routing.ps1") `
        -FriendSlice -ServicePortOffset $ServicePortOffset
    if ($LASTEXITCODE -ne 0) {
        exit $LASTEXITCODE
    }
    Write-Host "RESULT mysql_friend_slice_restart_recovery=PASS"
}
finally {
    if ($null -ne $connection -and $null -ne $connection.PlainPassword) {
        Remove-Variable -Name connection -Force -ErrorAction SilentlyContinue
    }
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    Restore-EnvVar -Name "MYSQL_DSN" -PreviousValue $previousDSN
    Restore-EnvVar -Name "E2E_FRIEND_PHASE" -PreviousValue $previousPhase
    Restore-EnvVar -Name "E2E_FRIEND_STATE_PATH" -PreviousValue $previousStatePath
}
