$ErrorActionPreference = "Stop"

function Get-FreeTcpPort {
    $Listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $Listener.Start()
    try {
        return ([System.Net.IPEndPoint]$Listener.LocalEndpoint).Port
    } finally {
        $Listener.Stop()
    }
}

$AssetPort = Get-FreeTcpPort
$RunId = "test-asset-$PID"
$AssetContainer = "$RunId-db"
$AssetMigrations = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) "infra\db\asset\migrations"

try {
    Write-Host "Starting disposable PostgreSQL container on port $AssetPort..."
    docker run --detach --rm --name $AssetContainer `
        --publish "127.0.0.1:${AssetPort}:5432" `
        --env POSTGRES_DB=asset_db `
        --env POSTGRES_USER=asset_user `
        --env POSTGRES_PASSWORD=asset_password `
        postgres:16-alpine | Out-Null

    for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
        docker exec $AssetContainer pg_isready -U asset_user -d asset_db *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    }

    Write-Host "Applying Flyway migrations..."
    docker run --rm `
        --mount "type=bind,source=$AssetMigrations,target=/flyway/sql,readonly" `
        flyway/flyway:10-alpine `
        -url="jdbc:postgresql://host.docker.internal:$AssetPort/asset_db" `
        -user=asset_user -password=asset_password `
        -locations=filesystem:/flyway/sql -connectRetries=60 migrate

    $env:ASSET_TEST_DATABASE_URL = "postgresql://asset_user:asset_password@127.0.0.1:$AssetPort/asset_db"

    Write-Host "Running go tests..."
    go test ./internal/repository -v -count=1

} finally {
    docker stop $AssetContainer 2>$null | Out-Null
}
