$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
$AssetMigrations = Join-Path $RepoRoot "infra/db/asset/migrations"
$AccessMigrations = Join-Path $RepoRoot "infra/db/access/migrations"
$AssetSeed = Join-Path $RepoRoot "infra/db/asset/seed/demo_fixtures.sql"
$AccessSeed = Join-Path $RepoRoot "infra/db/access/seed/demo_fixtures.sql"
$AssetCore = Join-Path $RepoRoot "services/asset-core"
$AccessCore = Join-Path $RepoRoot "services/access-core"
$RunId = "metadata-e2e-$PID"
$AssetContainer = "$RunId-asset-db"
$AccessContainer = "$RunId-access-db"
$GoStdout = Join-Path $env:TEMP "$RunId-go.stdout.log"
$GoStderr = Join-Path $env:TEMP "$RunId-go.stderr.log"
$GoBinary = Join-Path $env:TEMP "$RunId-asset-core.exe"
$GoProcess = $null
$TestExitCode = 1

# Returns an available loopback TCP port for an isolated test process.
function Get-FreeTcpPort {
    $Listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $Listener.Start()
    try {
        return ([System.Net.IPEndPoint]$Listener.LocalEndpoint).Port
    } finally {
        $Listener.Stop()
    }
}

# Waits until PostgreSQL inside the disposable container accepts connections.
function Wait-Postgres([string]$Container, [string]$User, [string]$Database) {
    for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
        docker exec $Container pg_isready -U $User -d $Database *> $null
        if ($LASTEXITCODE -eq 0) { return }
        Start-Sleep -Milliseconds 500
    }
    throw "PostgreSQL container $Container did not become ready"
}

# Waits until the Go health endpoint returns success.
function Wait-GoHealth([int]$Port) {
    for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$Port/healthz" -TimeoutSec 1 | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    throw "Go Asset Core did not become healthy"
}

$AssetPort = Get-FreeTcpPort
$AccessPort = Get-FreeTcpPort
$GoPort = Get-FreeTcpPort

try {
    Write-Host "Starting disposable PostgreSQL 16 containers..."
    docker run --detach --rm --name $AssetContainer `
        --publish "127.0.0.1:${AssetPort}:5432" `
        --env POSTGRES_DB=asset_db `
        --env POSTGRES_USER=asset_user `
        --env POSTGRES_PASSWORD=asset_password `
        postgres:16-alpine | Out-Null
    docker run --detach --rm --name $AccessContainer `
        --publish "127.0.0.1:${AccessPort}:5432" `
        --env POSTGRES_DB=access_db `
        --env POSTGRES_USER=access_user `
        --env POSTGRES_PASSWORD=access_password `
        postgres:16-alpine | Out-Null
    Wait-Postgres $AssetContainer "asset_user" "asset_db"
    Wait-Postgres $AccessContainer "access_user" "access_db"

    Write-Host "Applying Flyway migrations to empty databases..."
    docker run --rm `
        --mount "type=bind,source=$AssetMigrations,target=/flyway/sql,readonly" `
        flyway/flyway:10-alpine `
        -url="jdbc:postgresql://host.docker.internal:$AssetPort/asset_db" `
        -user=asset_user -password=asset_password `
        -locations=filesystem:/flyway/sql -connectRetries=60 migrate
    if ($LASTEXITCODE -ne 0) { throw "Asset Flyway migration failed" }

    docker run --rm `
        --mount "type=bind,source=$AccessMigrations,target=/flyway/sql,readonly" `
        flyway/flyway:10-alpine `
        -url="jdbc:postgresql://host.docker.internal:$AccessPort/access_db" `
        -user=access_user -password=access_password `
        -locations=filesystem:/flyway/sql -connectRetries=60 migrate
    if ($LASTEXITCODE -ne 0) { throw "Access Flyway migration failed" }

    Write-Host "Applying explicit E2E fixtures..."
    Get-Content $AccessSeed | docker exec -i $AccessContainer psql -U access_user -d access_db
    if ($LASTEXITCODE -ne 0) { throw "Access E2E seed failed" }
    Get-Content $AssetSeed | docker exec -i $AssetContainer psql -U asset_user -d asset_db
    if ($LASTEXITCODE -ne 0) { throw "Asset E2E seed failed" }

    Write-Host "Starting Go Asset Core..."
    $env:ASSET_DB_HOST = "127.0.0.1"
    $env:ASSET_DB_PORT = "$AssetPort"
    $env:ASSET_DB_NAME = "asset_db"
    $env:ASSET_DB_USER = "asset_user"
    $env:ASSET_DB_PASSWORD = "asset_password"
    $env:ASSET_INTERNAL_API_TOKEN = "kan55-e2e-internal-token"
    $env:PORT = "$GoPort"
    Push-Location $AssetCore
    try {
        go build -o $GoBinary ./cmd/server/main.go
        if ($LASTEXITCODE -ne 0) { throw "Go Asset Core build failed" }
    } finally {
        Pop-Location
    }
    $GoProcess = Start-Process -FilePath $GoBinary `
        -WindowStyle Hidden `
        -PassThru `
        -RedirectStandardOutput $GoStdout `
        -RedirectStandardError $GoStderr
    Wait-GoHealth $GoPort

    Write-Host "Running GraphQL -> Node -> Go -> PostgreSQL E2E..."
    $env:GO_ASSET_URL = "http://127.0.0.1:$GoPort"
    $env:ASSET_DB_URL = "postgresql://asset_user:asset_password@127.0.0.1:$AssetPort/asset_db"
    $env:ACCESS_DB_HOST = "127.0.0.1"
    $env:ACCESS_DB_PORT = "$AccessPort"
    $env:ACCESS_DB_NAME = "access_db"
    $env:ACCESS_DB_USER = "access_user"
    $env:ACCESS_DB_PASSWORD = "access_password"
    $env:DATABASE_URL = "postgresql://access_user:access_password@127.0.0.1:$AccessPort/access_db"

    Push-Location $AccessCore
    try {
        npx vitest run --config vitest.e2e.config.ts
        $TestExitCode = $LASTEXITCODE
    } finally {
        Pop-Location
    }
} finally {
    if ($GoProcess -and -not $GoProcess.HasExited) {
        Stop-Process -Id $GoProcess.Id -Force
        $GoProcess.WaitForExit()
    }
    if ($TestExitCode -ne 0) {
        if (Test-Path -LiteralPath $GoStdout) { Get-Content -LiteralPath $GoStdout -Tail 100 }
        if (Test-Path -LiteralPath $GoStderr) { Get-Content -LiteralPath $GoStderr -Tail 100 }
    }
    docker stop $AssetContainer $AccessContainer 2>$null | Out-Null
    Remove-Item -LiteralPath $GoStdout, $GoStderr, $GoBinary -Force -ErrorAction SilentlyContinue
}

exit $TestExitCode
