[CmdletBinding()]
param([ValidateSet('install','test')][string]$Action='install')
$ErrorActionPreference='Stop'
Push-Location (Join-Path $PSScriptRoot 'server')
try {
    if ($Action -eq 'install') { & go mod download } else { & go test ./... }
    if ($LASTEXITCODE -ne 0) { throw 'Go command failed' }
} finally { Pop-Location }
Push-Location (Join-Path $PSScriptRoot 'web')
try {
    if ($Action -eq 'install') { & npm.cmd ci } else { & npm.cmd run build }
    if ($LASTEXITCODE -ne 0) { throw 'Frontend command failed' }
} finally { Pop-Location }