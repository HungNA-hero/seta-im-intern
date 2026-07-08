<#
.SYNOPSIS
Verifies an empty-volume setup, full regression, service boot, policy smoke, and real Open Images import.

.DESCRIPTION
The destructive path is opt-in. It removes the two project database volumes before and after the run.
Every external command is checked, services are launched as exact binaries, and cleanup is asserted.
#>
[CmdletBinding()]
param(
    [switch]$ApproveDestructiveReset
)

$ErrorActionPreference = "Stop"
$WorkspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$AssetCore = Join-Path $WorkspaceRoot "services\asset-core"
$AccessCore = Join-Path $WorkspaceRoot "services\access-core"
$RunId = "kan44-$PID"
$NodeStdout = Join-Path $env:TEMP "$RunId-node.stdout.log"
$NodeStderr = Join-Path $env:TEMP "$RunId-node.stderr.log"
$GoStdout = Join-Path $env:TEMP "$RunId-go.stdout.log"
$GoStderr = Join-Path $env:TEMP "$RunId-go.stderr.log"
$GoBinary = Join-Path $env:TEMP "$RunId-asset-core.exe"
$ImportBinary = Join-Path $env:TEMP "$RunId-import-sample.exe"
$NodeProcess = $null
$GoProcess = $null
$NodePort = 4000
$GoPort = 8080
$OriginalEnvironment = @{}
$EnvironmentNames = @("PORT", "GO_ASSET_URL", "ASSET_DB_HOST", "ASSET_DB_PORT", "ASSET_DB_NAME", "ASSET_DB_USER", "ASSET_DB_PASSWORD", "ACCESS_DB_HOST", "ACCESS_DB_PORT", "ACCESS_DB_NAME", "ACCESS_DB_USER", "ACCESS_DB_PASSWORD", "DATABASE_URL")

function Invoke-Checked([scriptblock]$Command, [string]$Description) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$Description failed with exit code $LASTEXITCODE" }
}

function Assert-PortFree([int]$Port) {
    $Listener = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    if ($Listener) { throw "TCP port $Port is already occupied by PID $($Listener.OwningProcess -join ', ')" }
}

function Wait-Health([string]$Name, [uri]$Uri, [System.Diagnostics.Process]$Process, [string[]]$Logs) {
    for ($Attempt = 0; $Attempt -lt 60; $Attempt++) {
        if ($Process.HasExited) {
            foreach ($Log in $Logs) { if (Test-Path -LiteralPath $Log) { Get-Content -LiteralPath $Log -Tail 100 } }
            throw "$Name exited before becoming healthy"
        }
        try {
            Invoke-RestMethod -Uri $Uri -TimeoutSec 1 | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    foreach ($Log in $Logs) { if (Test-Path -LiteralPath $Log) { Get-Content -LiteralPath $Log -Tail 100 } }
    throw "$Name did not become healthy"
}

function Invoke-GraphQL([string]$Query, [hashtable]$Headers = @{}) {
    $Body = @{ query = $Query } | ConvertTo-Json -Compress
    return Invoke-RestMethod -Uri "http://127.0.0.1:$NodePort/graphql" -Method Post -Headers $Headers -Body $Body -ContentType "application/json"
}

function Get-MetadataStateHash {
    $Sql = "SELECT md5(COALESCE(string_agg(row_to_json(t)::text, '' ORDER BY id), '')) FROM (SELECT * FROM metadata_items) t;"
    $Hash = docker exec seta-asset-db psql -U asset_user -d asset_db -Atc $Sql
    if ($LASTEXITCODE -ne 0) { throw "Failed to calculate metadata state hash" }
    return ($Hash | Select-Object -Last 1).Trim()
}

foreach ($Name in $EnvironmentNames) { $OriginalEnvironment[$Name] = [Environment]::GetEnvironmentVariable($Name, "Process") }

Push-Location $WorkspaceRoot
try {
    Assert-PortFree $NodePort
    Assert-PortFree $GoPort

    Write-Host "1. Preparing empty named-volume baseline"
    if ($ApproveDestructiveReset) {
        Invoke-Checked { npm run clean:all } "Initial destructive reset"
    } else {
        Write-Host "Destructive reset was not approved; existing project volumes will be reused."
    }
    Invoke-Checked { npm run docker:up } "Docker database startup"
    Invoke-Checked { npm run docker:migrate } "Flyway migration"

    Write-Host "2. Running disposable full E2E regression"
    & powershell -NoProfile -ExecutionPolicy Bypass -File "$PSScriptRoot\run_e2e.ps1"
    if ($LASTEXITCODE -ne 0) { throw "CS-13 E2E regression failed with exit code $LASTEXITCODE" }

    Write-Host "3. Building exact service binaries"
    Invoke-Checked { npm --prefix services/access-core run build } "Node build"
    Push-Location $AssetCore
    try {
        Invoke-Checked { go build -o $GoBinary ./cmd/server/main.go } "Go server build"
        Invoke-Checked { go build -o $ImportBinary ./cmd/import-sample/main.go } "Import CLI build"
    } finally {
        Pop-Location
    }

    Write-Host "4. Starting exact Go and Node processes"
    $env:ASSET_DB_HOST = "127.0.0.1"
    $env:ASSET_DB_PORT = "5433"
    $env:ASSET_DB_NAME = "asset_db"
    $env:ASSET_DB_USER = "asset_user"
    $env:ASSET_DB_PASSWORD = "asset_password"
    $env:PORT = "$GoPort"
    $GoProcess = Start-Process -FilePath $GoBinary -WorkingDirectory $AssetCore -PassThru -WindowStyle Hidden -RedirectStandardOutput $GoStdout -RedirectStandardError $GoStderr
    Wait-Health "Go Asset Core" "http://127.0.0.1:$GoPort/healthz" $GoProcess @($GoStdout, $GoStderr)

    $env:ACCESS_DB_HOST = "127.0.0.1"
    $env:ACCESS_DB_PORT = "5434"
    $env:ACCESS_DB_NAME = "access_db"
    $env:ACCESS_DB_USER = "access_user"
    $env:ACCESS_DB_PASSWORD = "access_password"
    $env:DATABASE_URL = "postgresql://access_user:access_password@127.0.0.1:5434/access_db"
    $env:GO_ASSET_URL = "http://127.0.0.1:$GoPort"
    $env:PORT = "$NodePort"
    $NodeExecutable = (Get-Command node -ErrorAction Stop).Source
    $NodeProcess = Start-Process -FilePath $NodeExecutable -ArgumentList "dist/index.js" -WorkingDirectory $AccessCore -PassThru -WindowStyle Hidden -RedirectStandardOutput $NodeStdout -RedirectStandardError $NodeStderr
    Wait-Health "Node Access Core" "http://127.0.0.1:$NodePort/health" $NodeProcess @($NodeStdout, $NodeStderr)

    Write-Host "5. Running GraphQL auth and cross-service smoke"
    $Introspection = Invoke-GraphQL "{ __schema { queryType { name } } }"
    if ($Introspection.data.__schema.queryType.name -ne "Query") { throw "GraphQL introspection failed" }

    $Denied = Invoke-GraphQL "query { organizations { id } }"
    if ($Denied.errors[0].extensions.code -ne "UNAUTHENTICATED") { throw "Missing-auth query did not return UNAUTHENTICATED" }

    $Headers = @{ "x-user-id" = "00000000-0000-0000-0000-000000000001"; "x-org-id" = "00000000-0000-0000-0000-000000000010" }
    $Allowed = Invoke-GraphQL "query { organizations { id } }" $Headers
    if ($Allowed.errors -or $Allowed.data.organizations.Count -lt 1) { throw "Seeded admin organization query failed" }
    $Tree = Invoke-GraphQL 'query { folderTree(orgId: "00000000-0000-0000-0000-000000000010") { id name path } }' $Headers
    if ($Tree.errors -or $Tree.data.folderTree.Count -lt 1) { throw "GraphQL -> Node -> Go -> PostgreSQL folder smoke failed" }

    Write-Host "6. Verifying trusted Open Images V7 cache and output"
    $OutputDir = Join-Path $env:TEMP "seta-open-images-v7"
    & "$PSScriptRoot\fetch_open_images_metadata.ps1" -Split validation -MaxItems 25 -OutputDirectory $OutputDir
    & "$PSScriptRoot\fetch_open_images_metadata.ps1" -VerifyOnly -OutputDirectory $OutputDir

    Write-Host "7. Importing real sample, rerunning, and proving dry-run immutability"
    $SamplePath = Join-Path $OutputDir "validation-sample.json"
    $OrgID = "00000000-0000-0000-0000-000000000010"
    $UserID = "00000000-0000-0000-0000-000000000001"
    $DbUrl = "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db?sslmode=disable"
    $First = & $ImportBinary -file $SamplePath -org-id $OrgID -user-id $UserID -database-url $DbUrl 2>&1
    if ($LASTEXITCODE -ne 0) { throw "First real import failed: $First" }
    $Rerun = & $ImportBinary -file $SamplePath -org-id $OrgID -user-id $UserID -database-url $DbUrl 2>&1
    $RerunText = $Rerun -join "`n"
    if ($LASTEXITCODE -ne 0 -or $RerunText -notmatch '"metadata_unchanged": 25') { throw "Exact rerun was not idempotent: $RerunText" }
    $BeforeDryRun = Get-MetadataStateHash
    $DryRun = & $ImportBinary -file $SamplePath -org-id $OrgID -user-id $UserID -database-url $DbUrl -dry-run 2>&1
    $DryRunText = $DryRun -join "`n"
    if ($LASTEXITCODE -ne 0 -or $DryRunText -notmatch '"metadata_unchanged": 25') { throw "Dry run failed: $DryRunText" }
    $AfterDryRun = Get-MetadataStateHash
    if ($BeforeDryRun -ne $AfterDryRun) { throw "Dry run changed metadata state" }

    Write-Host "KAN-44 clean setup verification passed."
} finally {
    Write-Host "8. Stopping exact service processes and cleaning resources"
    foreach ($Process in @($NodeProcess, $GoProcess)) {
        if ($Process -and -not $Process.HasExited) {
            Stop-Process -Id $Process.Id -Force -ErrorAction SilentlyContinue
            $Process.WaitForExit()
        }
    }
    Remove-Item -LiteralPath $NodeStdout, $NodeStderr, $GoStdout, $GoStderr, $GoBinary, $ImportBinary -Force -ErrorAction SilentlyContinue

    if ($ApproveDestructiveReset) {
        npm run clean:all
        if ($LASTEXITCODE -ne 0) { Write-Error "Final destructive cleanup failed" }
        $RemainingVolumes = docker volume ls --format "{{.Name}}" | Where-Object { $_ -in @("seta-dam_asset_db_data", "seta-dam_access_db_data") }
        if ($RemainingVolumes) { Write-Error "Project database volumes remain: $($RemainingVolumes -join ', ')" }
    } else {
        npm run docker:down
        if ($LASTEXITCODE -ne 0) { Write-Error "Docker shutdown failed" }
    }

    foreach ($Port in @($NodePort, $GoPort)) {
        if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { Write-Error "TCP port $Port still has a listener" }
    }
    foreach ($Name in $EnvironmentNames) { [Environment]::SetEnvironmentVariable($Name, $OriginalEnvironment[$Name], "Process") }
    Pop-Location
}
