$ErrorActionPreference = "Stop"

# Returns an available loopback port for the disposable PostgreSQL container.
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
$RunId = "test-cli-$PID"
$AssetContainer = "$RunId-db"
$AssetMigrations = Join-Path (Split-Path -Parent $PSScriptRoot) "infra\db\asset\migrations"

try {
    Write-Host "Starting PostgreSQL container on port $AssetPort..."
    docker run --detach --rm --name $AssetContainer --publish "127.0.0.1:${AssetPort}:5432" --env POSTGRES_DB=asset_db --env POSTGRES_USER=asset_user --env POSTGRES_PASSWORD=asset_password postgres:16-alpine | Out-Null

    for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
        docker exec $AssetContainer pg_isready -U asset_user -d asset_db *> $null
        if ($LASTEXITCODE -eq 0) { break }
        Start-Sleep -Milliseconds 500
    }

    Write-Host "Applying Flyway migrations..."
    docker run --rm --mount "type=bind,source=$AssetMigrations,target=/flyway/sql,readonly" flyway/flyway:10-alpine -url="jdbc:postgresql://host.docker.internal:$AssetPort/asset_db" -user=asset_user -password=asset_password -locations=filesystem:/flyway/sql -connectRetries=60 migrate

    $env:ASSET_TEST_DATABASE_URL = "postgresql://asset_user:asset_password@127.0.0.1:$AssetPort/asset_db"
    $env:ASSET_DATABASE_URL = $env:ASSET_TEST_DATABASE_URL

    Write-Host "Running Go integration tests..."
    Push-Location services/asset-core
    go test ./... -v
    $testExit = $LASTEXITCODE
    Pop-Location
    if ($testExit -ne 0) { throw "Go tests failed" }

    Write-Host "Building import-sample CLI..."
    Push-Location services/asset-core
    go build -o ../../import-sample.exe ./cmd/import-sample/main.go
    $buildExit = $LASTEXITCODE
    Pop-Location
    if ($buildExit -ne 0) { throw "Build failed" }

    $OrgID = "00000000-0000-0000-0000-000000000001"
    $UserID = "00000000-0000-0000-0000-000000000003"

    $ValidPayload = "sample_valid.json"
    Set-Content $ValidPayload '{"version": 1, "external_source": "open_images_v7", "folders": [{"key": "root", "name": "Root"}], "metadata": [{"folder_key": "root", "external_id": "item1", "title": "Test Title", "metadata_json": {}}]}'

    $ErrorActionPreference = "Continue"
    Write-Host "Testing valid import..."
    $out1 = ./import-sample.exe --file $ValidPayload --org-id $OrgID --user-id $UserID --database-url $env:ASSET_TEST_DATABASE_URL 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Import failed: $out1" }

    Write-Host "Testing idempotency (rerun)..."
    $out2 = ./import-sample.exe --file $ValidPayload --org-id $OrgID --user-id $UserID --database-url $env:ASSET_TEST_DATABASE_URL 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Idempotency test failed: $out2" }
    if (-not ($out2 -match '"metadata_unchanged": 1')) { throw "Idempotency test did not detect unchanged metadata: $out2" }

    Write-Host "Testing dry run..."
    $out3 = ./import-sample.exe --file $ValidPayload --org-id $OrgID --user-id $UserID --database-url $env:ASSET_TEST_DATABASE_URL --dry-run 2>&1
    if ($LASTEXITCODE -ne 0) { throw "Dry run failed: $out3" }
    if (-not ($out3 -match '"dry_run": true')) { throw "Dry run did not output dry_run=true: $out3" }

    $TrailingGarbage = "sample_garbage.json"
    Set-Content $TrailingGarbage '{"version": 1, "external_source": "open_images_v7", "folders": [{"key": "root", "name": "Root"}], "metadata": [{"folder_key": "root", "external_id": "item1", "title": "Test Title", "metadata_json": {}}]} {"garbage": true}'

    Write-Host "Testing trailing garbage rejection..."
    $out4 = ./import-sample.exe --file $TrailingGarbage --org-id $OrgID --user-id $UserID --database-url $env:ASSET_TEST_DATABASE_URL 2>&1
    if ($LASTEXITCODE -eq 0) { throw "Trailing garbage was NOT rejected!" }

    $OversizedPayload = "sample_oversized.json"
    # Create an 11MB file
    $dummyData = 'x' * 11MB
    Set-Content $OversizedPayload $dummyData

    Write-Host "Testing oversized file rejection..."
    $out5 = ./import-sample.exe --file $OversizedPayload --org-id $OrgID --user-id $UserID --database-url $env:ASSET_TEST_DATABASE_URL 2>&1
    if ($LASTEXITCODE -eq 0) { throw "Oversized file was NOT rejected!" }
    if (-not ($out5 -match "10 MiB")) { throw "Oversized error not matched: $out5" }

    Write-Host "All CLI tests passed."

} finally {
    docker stop $AssetContainer 2>$null | Out-Null
    Remove-Item -Path import-sample.exe, sample_valid.json, sample_garbage.json, sample_oversized.json -ErrorAction SilentlyContinue
}
