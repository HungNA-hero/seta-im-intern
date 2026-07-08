<#
.SYNOPSIS
Runs the Sprint 4 demo with shared assertions for CI-like and interactive modes.

.DESCRIPTION
The script resets the two project databases, starts exact Node and Go binaries, executes
FD-00 through FD-10, and proves cleanup. Destructive volume reset is always explicit.
#>
[CmdletBinding()]
param(
    [switch]$NonInteractive,
    [switch]$Interactive,
    [Parameter(Mandatory = $true)]
    [string]$OpenImagesDirectory,
    [switch]$ApproveDestructiveReset,
    [switch]$KeepEnvironment,
    [ValidateSet("None", "AfterBoot")]
    [string]$FailureInjection = "None",
    [ValidateRange(10, 180)]
    [int]$ReadinessTimeoutSeconds = 60
)

$ErrorActionPreference = "Stop"
if ($NonInteractive -eq $Interactive) { throw "Specify exactly one of -NonInteractive or -Interactive" }
if (-not $ApproveDestructiveReset) { throw "-ApproveDestructiveReset is required because the demo deletes project database volumes" }
if ($KeepEnvironment -and $FailureInjection -ne "None") { throw "Failure injection cannot keep the environment" }

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$AccessCore = Join-Path $RepoRoot "services\access-core"
$AssetCore = Join-Path $RepoRoot "services\asset-core"
$RunId = "s4demo-$((Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssfffZ'))-$PID"
$TempDir = Join-Path $env:TEMP $RunId
$NodeStdout = Join-Path $TempDir "node.stdout.log"
$NodeStderr = Join-Path $TempDir "node.stderr.log"
$GoStdout = Join-Path $TempDir "go.stdout.log"
$GoStderr = Join-Path $TempDir "go.stderr.log"
$GoBinary = Join-Path $TempDir "asset-core.exe"
$ImportBinary = Join-Path $TempDir "import-sample.exe"
$NodeProcess = $null
$GoProcess = $null
$PrimaryError = $null
$CleanupError = $null
$DemoSucceeded = $false
$NodePort = 4000
$GoPort = 8080
$AdminUser = "00000000-0000-0000-0000-000000000001"
$ViewerUser = "00000000-0000-0000-0000-000000000002"
$UnknownUser = "99999999-9999-9999-9999-999999999999"
$OrgID = "00000000-0000-0000-0000-000000000010"
$OtherOrgID = "00000000-0000-0000-0000-000000000020"
$EnvironmentNames = @("PORT", "GO_ASSET_URL", "ASSET_DB_HOST", "ASSET_DB_PORT", "ASSET_DB_NAME", "ASSET_DB_USER", "ASSET_DB_PASSWORD", "ACCESS_DB_HOST", "ACCESS_DB_PORT", "ACCESS_DB_NAME", "ACCESS_DB_USER", "ACCESS_DB_PASSWORD", "DATABASE_URL")
$OriginalEnvironment = @{}

foreach ($Name in $EnvironmentNames) { $OriginalEnvironment[$Name] = [Environment]::GetEnvironmentVariable($Name, "Process") }

# Runs an external command and converts a non-zero native exit code into a terminating error.
function Invoke-Checked([scriptblock]$Command, [string]$Description) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$Description failed with exit code $LASTEXITCODE" }
}

# Writes a deterministic scenario marker and adds the only interactive-mode pause.
function Write-Scenario([string]$Id, [string]$Title) {
    Write-Host "`n=== ${Id}: $Title ===" -ForegroundColor Cyan
    if ($Interactive) { Read-Host "Press Enter to continue" | Out-Null }
}

# Compares scalar evidence and reports both expected and actual values on failure.
function Assert-Equal($Expected, $Actual, [string]$Message) {
    if ($Expected -ne $Actual) { throw "$Message. Expected: $Expected; actual: $Actual" }
}

# Fails before mutation when a required service port is already owned by another process.
function Assert-PortFree([int]$Port) {
    $Listeners = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
    if ($Listeners) { throw "Port $Port is occupied by PID $($Listeners.OwningProcess -join ', ')" }
}

# Polls health while also detecting an exact child-process exit and printing bounded diagnostics.
function Wait-Service([string]$Name, [uri]$Uri, [System.Diagnostics.Process]$Process, [string[]]$Logs) {
    $Deadline = (Get-Date).AddSeconds($ReadinessTimeoutSeconds)
    while ((Get-Date) -lt $Deadline) {
        if ($Process.HasExited) {
            foreach ($Log in $Logs) { if (Test-Path -LiteralPath $Log) { Get-Content -LiteralPath $Log -Tail 100 } }
            throw "$Name exited before readiness"
        }
        try {
            Invoke-RestMethod -Uri $Uri -TimeoutSec 1 | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    foreach ($Log in $Logs) { if (Test-Path -LiteralPath $Log) { Get-Content -LiteralPath $Log -Tail 100 } }
    throw "$Name readiness timed out after $ReadinessTimeoutSeconds seconds"
}

# Sends one public GraphQL request and returns the full data/errors envelope for assertions.
function Invoke-GraphQLRaw([string]$Query, [hashtable]$Variables = @{}, [string]$UserId = "", [string]$RequestOrgId = "") {
    $Headers = @{}
    if ($UserId) { $Headers["x-user-id"] = $UserId }
    if ($RequestOrgId) { $Headers["x-org-id"] = $RequestOrgId }
    $Body = @{ query = $Query; variables = $Variables } | ConvertTo-Json -Depth 12 -Compress
    return Invoke-RestMethod -Uri "http://127.0.0.1:$NodePort/graphql" -Method Post -Headers $Headers -Body $Body -ContentType "application/json"
}

# Requires a successful GraphQL envelope and returns only its data object.
function Invoke-GraphQL([string]$Query, [hashtable]$Variables = @{}, [string]$UserId, [string]$RequestOrgId) {
    $Response = Invoke-GraphQLRaw $Query $Variables $UserId $RequestOrgId
    if ($Response.errors) {
        $Code = $Response.errors[0].extensions.code
        throw "Unexpected GraphQL error [$Code]: $($Response.errors[0].message)"
    }
    return $Response.data
}

# Requires an exact GraphQL error code instead of treating HTTP 200 as success.
function Assert-GraphQLError([string]$ScenarioId, [string]$ExpectedCode, [string]$Query, [hashtable]$Variables = @{}, [string]$UserId = "", [string]$RequestOrgId = "") {
    $Response = Invoke-GraphQLRaw $Query $Variables $UserId $RequestOrgId
    if (-not $Response.errors) { throw "$ScenarioId expected GraphQL error $ExpectedCode but request succeeded" }
    Assert-Equal $ExpectedCode $Response.errors[0].extensions.code "$ScenarioId returned the wrong GraphQL code"
}

# Executes a read/setup SQL statement in a named demo container and checks the native exit code.
function Invoke-Psql([string]$Container, [string]$User, [string]$Database, [string]$Sql) {
    $Output = docker exec $Container psql -U $User -d $Database -Atc $Sql
    if ($LASTEXITCODE -ne 0) { throw "psql failed in $Container" }
    return ($Output | Select-Object -Last 1).Trim()
}

# Toggles the organization policy mode as deterministic demo setup, not as a business operation.
function Set-Olp([bool]$Enabled) {
    $Value = if ($Enabled) { "true" } else { "false" }
    Invoke-Psql "seta-access-db" "access_user" "access_db" "UPDATE access.organizations SET olp_enabled=$Value WHERE id='$OrgID'; SELECT olp_enabled FROM access.organizations WHERE id='$OrgID';" | Out-Null
}

# Captures clean-baseline counts before any run-owned resource is created.
function Get-NamespaceCounts {
    $FolderCount = Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders WHERE name LIKE '$RunId%';"
    $MetadataCount = Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM metadata_items WHERE title LIKE '$RunId%';"
    $PermissionCount = Invoke-Psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions;"
    return @{ Folders = [int]$FolderCount; Metadata = [int]$MetadataCount; Permissions = [int]$PermissionCount }
}

# Produces a stable database-state digest used to prove dry-run immutability.
function Get-MetadataHash {
    return Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT md5(COALESCE(string_agg(row_to_json(t)::text, '' ORDER BY id), '')) FROM (SELECT * FROM metadata_items) t;"
}

# Grants an object permission exclusively through the public GraphQL mutation.
function Grant-Permission([string]$ResourceType, [string]$ResourceId, [string]$Action, [string]$GranteeUser, [string]$Actor = $AdminUser) {
    $Mutation = 'mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
    $Data = Invoke-GraphQL $Mutation @{ orgId = $OrgID; resourceType = $ResourceType; resourceId = $ResourceId; action = $Action; granteeUserId = $GranteeUser } $Actor $OrgID
    return $Data.grantObjectPermission.id
}

# Revokes an object permission exclusively through the public GraphQL mutation.
function Revoke-Permission([string]$PermissionId, [string]$Actor = $AdminUser) {
    $Mutation = 'mutation($id: ID!) { revokeObjectPermission(id: $id) }'
    $Data = Invoke-GraphQL $Mutation @{ id = $PermissionId } $Actor $OrgID
    Assert-Equal $true $Data.revokeObjectPermission "Permission revoke failed"
}

New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
Push-Location $RepoRoot
try {
    try {
        Write-Scenario "FD-00" "Preflight and trusted fixture"
        Assert-PortFree $NodePort
        Assert-PortFree $GoPort
        if (-not (Test-Path -LiteralPath (Join-Path $OpenImagesDirectory "provenance-manifest.json"))) { throw "Open Images manifest is missing" }
        & "$PSScriptRoot\fetch_open_images_metadata.ps1" -VerifyOnly -OutputDirectory $OpenImagesDirectory
        $Baseline = @{ Folders = 0; Metadata = 0; Permissions = 0 }

        Write-Scenario "FD-01" "Clean migration and exact service boot"
        Invoke-Checked { npm run clean:all } "Initial volume reset"
        Invoke-Checked { npm run docker:up } "Database startup"
        Invoke-Checked { npm run docker:migrate } "Flyway migration"
        Assert-Equal "2" (Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT MAX(version) FROM flyway_schema_history;") "Asset Flyway version"
        Assert-Equal "2" (Invoke-Psql "seta-access-db" "access_user" "access_db" "SELECT MAX(version) FROM flyway_schema_history;") "Access Flyway version"
        $Baseline = Get-NamespaceCounts
        Assert-Equal 0 $Baseline.Folders "FD-01 folder namespace must start empty"
        Assert-Equal 0 $Baseline.Metadata "FD-01 metadata namespace must start empty"
        Assert-Equal 0 $Baseline.Permissions "FD-01 permission table must start empty"

        Invoke-Checked { npm --prefix services/access-core run build } "Node build"
        Push-Location $AssetCore
        try {
            Invoke-Checked { go build -o $GoBinary ./cmd/server/main.go } "Go server build"
            Invoke-Checked { go build -o $ImportBinary ./cmd/import-sample/main.go } "Import CLI build"
        } finally { Pop-Location }

        $env:ASSET_DB_HOST = "127.0.0.1"; $env:ASSET_DB_PORT = "5433"; $env:ASSET_DB_NAME = "asset_db"; $env:ASSET_DB_USER = "asset_user"; $env:ASSET_DB_PASSWORD = "asset_password"; $env:PORT = "$GoPort"
        $GoProcess = Start-Process -FilePath $GoBinary -WorkingDirectory $AssetCore -PassThru -WindowStyle Hidden -RedirectStandardOutput $GoStdout -RedirectStandardError $GoStderr
        Wait-Service "Go Asset Core" "http://127.0.0.1:$GoPort/healthz" $GoProcess @($GoStdout, $GoStderr)

        $env:ACCESS_DB_HOST = "127.0.0.1"; $env:ACCESS_DB_PORT = "5434"; $env:ACCESS_DB_NAME = "access_db"; $env:ACCESS_DB_USER = "access_user"; $env:ACCESS_DB_PASSWORD = "access_password"
        $env:DATABASE_URL = "postgresql://access_user:access_password@127.0.0.1:5434/access_db"; $env:GO_ASSET_URL = "http://127.0.0.1:$GoPort"; $env:PORT = "$NodePort"
        $NodeProcess = Start-Process -FilePath (Get-Command node -ErrorAction Stop).Source -ArgumentList "dist/index.js" -WorkingDirectory $AccessCore -PassThru -WindowStyle Hidden -RedirectStandardOutput $NodeStdout -RedirectStandardError $NodeStderr
        Wait-Service "Node Access Core" "http://127.0.0.1:$NodePort/health" $NodeProcess @($NodeStdout, $NodeStderr)
        $Schema = Invoke-GraphQLRaw '{ __schema { queryType { name } } }'
        Assert-Equal "Query" $Schema.data.__schema.queryType.name "GraphQL introspection"
        if ($FailureInjection -eq "AfterBoot") { throw "CONTROLLED_FAILURE_AFTER_BOOT" }

        Write-Scenario "FD-02" "Authentication and organization isolation"
        $FolderCountBeforeDeny = Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;"
        $OrganizationQuery = 'query { organizations { id } }'
        Assert-GraphQLError "AUTH-01" "UNAUTHENTICATED" $OrganizationQuery
        Assert-GraphQLError "AUTH-02" "UNAUTHENTICATED" $OrganizationQuery @{} $UnknownUser $OrgID
        $TreeQuery = 'query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
        Assert-GraphQLError "ORG-01" "FORBIDDEN" $TreeQuery @{ orgId = $OtherOrgID } $AdminUser $OtherOrgID
        Assert-GraphQLError "ORG-02" "FORBIDDEN" $TreeQuery @{ orgId = $OtherOrgID } $ViewerUser $OtherOrgID
        $AllowedTree = Invoke-GraphQL $TreeQuery @{ orgId = $OrgID } $AdminUser $OrgID
        if ($AllowedTree.folderTree.Count -lt 1) { throw "ORG-03 expected seeded folders" }
        Assert-Equal $FolderCountBeforeDeny (Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;") "FD-02 deny cases changed Asset DB"

        Write-Scenario "FD-03" "Folder lifecycle"
        $CreateFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
        $UpdateFolder = 'mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
        $MoveFolder = 'mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) { moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) { id path } }'
        $DeleteFolder = 'mutation($orgId: ID!, $id: ID!) { deleteFolder(orgId: $orgId, id: $id) }'
        $Root = (Invoke-GraphQL $CreateFolder @{ orgId = $OrgID; name = "$RunId-root" } $AdminUser $OrgID).createFolder
        $Child = (Invoke-GraphQL $CreateFolder @{ orgId = $OrgID; name = "$RunId-child"; parentPath = $Root.path } $AdminUser $OrgID).createFolder
        Assert-GraphQLError "FOLDER-NONEMPTY" "CONFLICT" $DeleteFolder @{ orgId = $OrgID; id = $Root.id } $AdminUser $OrgID
        $Renamed = (Invoke-GraphQL $UpdateFolder @{ orgId = $OrgID; id = $Child.id; name = "$RunId-child-renamed" } $AdminUser $OrgID).updateFolder
        Assert-Equal $Child.path $Renamed.path "Folder rename changed UUID path"
        $Moved = (Invoke-GraphQL $MoveFolder @{ orgId = $OrgID; id = $Child.id; destinationParentId = $null } $AdminUser $OrgID).moveFolder
        if ($Moved.path -eq $Child.path) { throw "Folder move did not change path" }

        Write-Scenario "FD-04" "Metadata lifecycle and search"
        $CreateMetadata = 'mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
        $UpdateMetadata = 'mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) { updateMetadata(orgId: $orgId, id: $id, input: $input) { id title description } }'
        $SearchMetadata = 'query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
        $DeleteMetadata = 'mutation($orgId: ID!, $id: ID!) { deleteMetadata(orgId: $orgId, id: $id) }'
        $Metadata = (Invoke-GraphQL $CreateMetadata @{ orgId = $OrgID; input = @{ folderId = $Root.id; title = "$RunId-metadata"; metadataJson = '{"demo":true}' } } $AdminUser $OrgID).createMetadata
        $UpdatedMetadata = (Invoke-GraphQL $UpdateMetadata @{ orgId = $OrgID; id = $Metadata.id; input = @{ title = "$RunId-metadata-updated"; description = "Sprint 4 demo" } } $AdminUser $OrgID).updateMetadata
        Assert-Equal "$RunId-metadata-updated" $UpdatedMetadata.title "Metadata update"
        $Found = (Invoke-GraphQL $SearchMetadata @{ orgId = $OrgID; input = @{ query = "$RunId-metadata-updated" } } $AdminUser $OrgID).searchMetadata
        if (@($Found | Where-Object { $_.id -eq $Metadata.id }).Count -ne 1) { throw "Metadata search did not return the updated item" }

        Write-Scenario "FD-05" "Verified Open Images V7 import"
        $DatasetPath = Join-Path $OpenImagesDirectory "validation-sample.json"
        $DatabaseUrl = "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db?sslmode=disable"
        $FirstImport = & $ImportBinary -file $DatasetPath -org-id $OrgID -user-id $AdminUser -database-url $DatabaseUrl 2>&1
        $FirstExit = $LASTEXITCODE; $FirstText = $FirstImport -join "`n"
        if ($FirstExit -ne 0 -or $FirstText -notmatch '"metadata_created": 25') { throw "First real import failed: $FirstText" }
        $SecondImport = & $ImportBinary -file $DatasetPath -org-id $OrgID -user-id $AdminUser -database-url $DatabaseUrl 2>&1
        $SecondExit = $LASTEXITCODE; $SecondText = $SecondImport -join "`n"
        if ($SecondExit -ne 0 -or $SecondText -notmatch '"metadata_unchanged": 25') { throw "Real import rerun was not idempotent: $SecondText" }
        $BeforeDryRun = Get-MetadataHash
        $DryRun = & $ImportBinary -file $DatasetPath -org-id $OrgID -user-id $AdminUser -database-url $DatabaseUrl -dry-run 2>&1
        $DryExit = $LASTEXITCODE; $DryText = $DryRun -join "`n"
        if ($DryExit -ne 0 -or $DryText -notmatch '"metadata_unchanged": 25') { throw "Real import dry run failed: $DryText" }
        Assert-Equal $BeforeDryRun (Get-MetadataHash) "Dry run changed metadata state"
        $OpenImages = (Invoke-GraphQL $SearchMetadata @{ orgId = $OrgID; input = @{ externalSource = "open_images_v7"; limit = 25 } } $AdminUser $OrgID).searchMetadata
        Assert-Equal 25 $OpenImages.Count "GraphQL did not expose 25 imported items"

        Write-Scenario "FD-06" "RBAC and OLP direct grant/revoke"
        $PolicyRoot = (Invoke-GraphQL $CreateFolder @{ orgId = $OrgID; name = "$RunId-policy-root" } $AdminUser $OrgID).createFolder
        Set-Olp $false
        Invoke-GraphQL 'query($orgId: ID!, $id: ID!) { folder(orgId: $orgId, id: $id) { id } }' @{ orgId = $OrgID; id = $PolicyRoot.id } $ViewerUser $OrgID | Out-Null
        Assert-GraphQLError "PM-04" "FORBIDDEN" $UpdateFolder @{ orgId = $OrgID; id = $PolicyRoot.id; name = "$RunId-rbac-denied" } $ViewerUser $OrgID
        Assert-Equal "$RunId-policy-root" (Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT name FROM folders WHERE id='$($PolicyRoot.id)';") "PM-04 deny changed folder"
        Set-Olp $true
        Assert-GraphQLError "PM-05" "FORBIDDEN" $UpdateFolder @{ orgId = $OrgID; id = $PolicyRoot.id; name = "$RunId-olp-denied" } $ViewerUser $OrgID
        Assert-Equal "$RunId-policy-root" (Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT name FROM folders WHERE id='$($PolicyRoot.id)';") "PM-05 deny changed folder"
        $DirectWrite = Grant-Permission "folder" $PolicyRoot.id "write" $ViewerUser
        Invoke-GraphQL $UpdateFolder @{ orgId = $OrgID; id = $PolicyRoot.id; name = "$RunId-direct-allowed" } $ViewerUser $OrgID | Out-Null
        Revoke-Permission $DirectWrite
        Assert-GraphQLError "PM-08" "FORBIDDEN" $UpdateFolder @{ orgId = $OrgID; id = $PolicyRoot.id; name = "$RunId-revoked" } $ViewerUser $OrgID

        Write-Scenario "FD-07" "Creator no-bypass, inheritance, and exact manage permission"
        $ViewerCreated = (Invoke-GraphQL $CreateFolder @{ orgId = $OrgID; name = "$RunId-viewer-created"; parentPath = $PolicyRoot.path } $AdminUser $OrgID).createFolder
        Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "UPDATE folders SET created_by='$ViewerUser' WHERE id='$($ViewerCreated.id)'; SELECT created_by FROM folders WHERE id='$($ViewerCreated.id)';" | Out-Null
        Set-Olp $false
        Assert-GraphQLError "PM-10-RBAC" "FORBIDDEN" $UpdateFolder @{ orgId = $OrgID; id = $ViewerCreated.id; name = "$RunId-creator-rbac-bypass" } $ViewerUser $OrgID
        Set-Olp $true
        Assert-GraphQLError "PM-10" "FORBIDDEN" $UpdateFolder @{ orgId = $OrgID; id = $ViewerCreated.id; name = "$RunId-creator-bypass" } $ViewerUser $OrgID
        $InheritedWrite = Grant-Permission "folder" $PolicyRoot.id "write" $ViewerUser
        Invoke-GraphQL $UpdateFolder @{ orgId = $OrgID; id = $ViewerCreated.id; name = "$RunId-inherited-allowed" } $ViewerUser $OrgID | Out-Null
        $ExactManage = Grant-Permission "folder" $PolicyRoot.id "manage_permissions" $ViewerUser
        $GrantMutation = 'mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
        $ViewerExactGrant = (Invoke-GraphQL $GrantMutation @{ orgId = $OrgID; resourceType = "folder"; resourceId = $PolicyRoot.id; action = "read"; granteeUserId = $AdminUser } $ViewerUser $OrgID).grantObjectPermission.id
        Assert-GraphQLError "PM-13" "FORBIDDEN" $GrantMutation @{ orgId = $OrgID; resourceType = "folder"; resourceId = $ViewerCreated.id; action = "read"; granteeUserId = $AdminUser } $ViewerUser $OrgID
        Revoke-Permission $ViewerExactGrant
        Revoke-Permission $ExactManage
        Revoke-Permission $InheritedWrite

        Write-Scenario "FD-08" "Soft delete hides resource and preserves grant"
        $SoftMetadata = (Invoke-GraphQL $CreateMetadata @{ orgId = $OrgID; input = @{ folderId = $PolicyRoot.id; title = "$RunId-soft-delete"; metadataJson = '{}' } } $AdminUser $OrgID).createMetadata
        $SoftGrant = Grant-Permission "metadata_item" $SoftMetadata.id "read" $ViewerUser
        $BeforeGrantCount = Invoke-Psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions WHERE id='$SoftGrant';"
        Invoke-GraphQL $DeleteMetadata @{ orgId = $OrgID; id = $SoftMetadata.id } $AdminUser $OrgID | Out-Null
        $AfterDeleteSearch = (Invoke-GraphQL $SearchMetadata @{ orgId = $OrgID; input = @{ query = "$RunId-soft-delete" } } $AdminUser $OrgID).searchMetadata
        Assert-Equal 0 $AfterDeleteSearch.Count "Soft-deleted metadata remained searchable"
        Assert-Equal $BeforeGrantCount (Invoke-Psql "seta-access-db" "access_user" "access_db" "SELECT COUNT(*) FROM access.object_permissions WHERE id='$SoftGrant';") "Soft delete removed permission history"

        Write-Scenario "FD-09" "Reset invariants"
        Set-Olp $false
        Write-Scenario "FD-10" "Rehearsal completion"
        Write-Host "All FD-00 through FD-10 assertions passed for $RunId" -ForegroundColor Green
        $DemoSucceeded = $true
    } catch {
        $PrimaryError = $_
    } finally {
        if (-not $KeepEnvironment) {
            try {
                foreach ($Process in @($NodeProcess, $GoProcess)) {
                    if ($Process -and -not $Process.HasExited) { Stop-Process -Id $Process.Id -Force -ErrorAction Stop; $Process.WaitForExit() }
                }
                Invoke-Checked { npm run clean:all } "Final volume cleanup"
                Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
                foreach ($Port in @($NodePort, $GoPort)) {
                    if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) { throw "Cleanup left a listener on port $Port" }
                }
                $Volumes = docker volume ls --format "{{.Name}}" | Where-Object { $_ -in @("seta-dam_asset_db_data", "seta-dam_access_db_data") }
                if ($Volumes) { throw "Cleanup left project volumes: $($Volumes -join ', ')" }
            } catch {
                $CleanupError = $_
            }
        }
        foreach ($Name in $EnvironmentNames) { [Environment]::SetEnvironmentVariable($Name, $OriginalEnvironment[$Name], "Process") }
    }
} finally {
    Pop-Location
}

if ($PrimaryError) {
    if ($CleanupError) { throw "$($PrimaryError.Exception.Message); cleanup also failed: $($CleanupError.Exception.Message)" }
    throw $PrimaryError
}
if ($CleanupError) { throw $CleanupError }
if (-not $DemoSucceeded) { throw "Demo ended without a success verdict" }
