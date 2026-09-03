[CmdletBinding()]
param(
    [ValidateSet("Auto", "Docker", "Local")]
    [string]$Mode = "Auto"
)

$ErrorActionPreference = "Stop"
$RepositoryRoot = Split-Path -Parent $PSScriptRoot
$ComposeFile = Join-Path $PSScriptRoot "docker-compose.yml"
$MigrationDirectory = Join-Path $PSScriptRoot "migrations"
$EnvironmentFile = Join-Path $RepositoryRoot ".env"

function Get-MysqlClientPath {
    $command = Get-Command mysql -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }
    $candidates = @(
        (Join-Path $env:ProgramFiles "MySQL\MySQL Server 8.4\bin\mysql.exe"),
        (Join-Path $env:ProgramFiles "MySQL\MySQL Server 8.0\bin\mysql.exe"),
        (Join-Path $env:ProgramFiles "MySQL\MySQL Server 9.0\bin\mysql.exe")
    )
    foreach ($candidate in $candidates) {
        if (Test-Path $candidate -PathType Leaf) {
            return $candidate
        }
    }
    throw "mysql client was not found on PATH or under Program Files\MySQL"
}

function Invoke-DockerMigrations {
    param(
        [string[]]$ComposeArguments,
        $MigrationFiles
    )

    foreach ($migration in $MigrationFiles) {
        Write-Host "Applying $($migration.Name) via Docker"
        Get-Content -Raw $migration.FullName |
            & docker @ComposeArguments exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" exec mysql -u"$MYSQL_USER" "$MYSQL_DATABASE"'
        if ($LASTEXITCODE -ne 0) {
            throw "Migration $($migration.Name) failed"
        }
    }
}

function Invoke-LocalMigrations {
    param($MigrationFiles)

    . (Join-Path $RepositoryRoot "tests\e2e\_mysql-env.ps1")
    $connection = Resolve-MySQLConnection -IgnoreProcessDSN
    $mysql = Get-MysqlClientPath
    $previousPassword = $env:MYSQL_PWD
    $env:MYSQL_PWD = $connection.PlainPassword
    try {
        $mysqlArgs = @(
            "-h", $connection.HostName,
            "-P", "$($connection.Port)",
            "-u", $connection.User,
            "--default-character-set=utf8mb4",
            $connection.Database
        )
        foreach ($migration in $MigrationFiles) {
            if ($migration.Name -notmatch "^(\d+)_") {
                throw "Migration file name is not versioned: $($migration.Name)"
            }
            $version = [int]$Matches[1]
            $applied = & $mysql @mysqlArgs -N -e "SELECT COUNT(*) FROM schema_migrations WHERE version=$version" 2>$null
            if ($LASTEXITCODE -eq 0 -and "$applied".Trim() -eq "1") {
                Write-Host "Skipping $($migration.Name) (already applied)"
                continue
            }
            Write-Host "Applying $($migration.Name) via local mysql"
            Get-Content -Raw -Encoding UTF8 $migration.FullName | & $mysql @mysqlArgs
            if ($LASTEXITCODE -ne 0) {
                throw "Migration $($migration.Name) failed"
            }
        }
    }
    finally {
        if ($null -eq $previousPassword) {
            Remove-Item Env:MYSQL_PWD -ErrorAction SilentlyContinue
        }
        else {
            $env:MYSQL_PWD = $previousPassword
        }
    }
}

$migrationFiles = Get-ChildItem $MigrationDirectory -Filter "*.up.sql" | Sort-Object Name
if (-not $migrationFiles) {
    throw "No *.up.sql files found in $MigrationDirectory"
}

$composeArguments = @("compose")
if (Test-Path $EnvironmentFile) {
    $composeArguments += @("--env-file", $EnvironmentFile)
}
$composeArguments += @("-f", $ComposeFile)

$useDocker = $Mode -eq "Docker"
if ($Mode -eq "Auto" -and (Get-Command docker -ErrorAction SilentlyContinue)) {
    $runningServices = & docker @composeArguments ps --status running --services 2>$null
    $useDocker = $LASTEXITCODE -eq 0 -and $runningServices -contains "mysql"
}

if ($useDocker) {
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "docker was not found on PATH; use -Mode Local or install Docker"
    }
    Invoke-DockerMigrations -ComposeArguments $composeArguments -MigrationFiles $migrationFiles
}
else {
    Write-Host "Applying migrations with the local mysql client"
    Invoke-LocalMigrations -MigrationFiles $migrationFiles
}

Write-Host "Migrations completed."
