param(
    [string]$OpenImagesDirectory = "",
    [int]$ReadinessTimeoutSeconds = 60,
    [parameter(ValueFromRemainingArguments=$true)]
    [string[]]$SectionsToRun
)

$ErrorActionPreference = "Stop"

if (-not [string]::IsNullOrEmpty($OpenImagesDirectory) -and -not (Test-Path $OpenImagesDirectory)) {
    Write-Error "Directory $OpenImagesDirectory does not exist."
    exit 1
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Resolve-Path (Join-Path $ScriptDir "..") | Select-Object -ExpandProperty Path
$AccessCore = Join-Path $RepoRoot "services/access-core"
$AssetCore = Join-Path $RepoRoot "services/asset-core"

$RunId = "tdemo-$(Get-Date -UFormat '%Y%m%dT%H%M%SZ')-$PID"
$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) $RunId
$NodeStdout = Join-Path $TempDir "node.stdout.log"
$NodeStderr = Join-Path $TempDir "node.stderr.log"
$GoStdout = Join-Path $TempDir "go.stdout.log"
$GoStderr = Join-Path $TempDir "go.stderr.log"
$GoBinary = Join-Path $TempDir "asset-core.exe"
$ImportBinary = Join-Path $TempDir "import-sample.exe"

$script:NODE_PORT = 4000
$script:GO_PORT = 8080
$script:ADMIN_USER = "00000000-0000-0000-0000-000000000001"
$script:VIEWER_USER = "00000000-0000-0000-0000-000000000002"
$script:UNKNOWN_USER = "99999999-9999-9999-9999-999999999999"
$script:ORG_ID = "00000000-0000-0000-0000-000000000010"
$script:OTHER_ORG_ID = "00000000-0000-0000-0000-000000000020"

# ---------- helpers ----------

function Write-Log {
    param([string]$Message)
    Write-Host $Message
}

function Show-Scenario {
    param([string]$Id, [string]$Title, [string]$Desc)
    Write-Host ""
    Write-Host "=== $Id: $Title ===" -ForegroundColor Cyan
    Write-Host $Desc
    Write-Host ""
    Read-Host "Press Enter to continue"
    Write-Host ""
}

function Show-Data {
    param($Data)
    $Data | ConvertTo-Json -Depth 10 | Write-Host
}

function Assert-Check {
    param($Expected, $Actual, $Label)
    if ([string]$Expected -eq [string]$Actual) {
        Write-Host "[PASS] $Label (got: $Actual)" -ForegroundColor Green
    } else {
        Write-Host "[WARN] $Label (expected: $Expected, actual: $Actual)" -ForegroundColor Yellow
    }
}

function Test-PortOpen {
    param([int]$Port)
    $connection = $null
    try {
        $connection = New-Object System.Net.Sockets.TcpClient("127.0.0.1", $Port)
        return $true
    } catch {
        return $false
    } finally {
        if ($connection) { $connection.Dispose() }
    }
}

function Wait-Service {
    param([string]$Name, [string]$Url, $Process, [string[]]$Logs)
    $deadline = (Get-Date).AddSeconds($ReadinessTimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if ($Process -and $Process.HasExited) {
            foreach ($l in $Logs) { if (Test-Path $l) { Get-Content $l -Tail 100 | Write-Host } }
            Write-Error "$Name exited before readiness"
            return $false
        }
        try {
            $res = Invoke-WebRequest -Uri $Url -TimeoutSec 1 -ErrorAction Stop -UseBasicParsing
            return $true
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    foreach ($l in $Logs) { if (Test-Path $l) { Get-Content $l -Tail 100 | Write-Host } }
    Write-Error "$Name readiness timed out after $ReadinessTimeoutSeconds seconds"
    return $false
}

function Invoke-GraphQLRaw {
    param([string]$Query, [object]$Variables = @{}, [string]$UserId = "", [string]$OrgId = "")
    $headers = @{"Content-Type" = "application/json"}
    if ($UserId) { $headers["x-user-id"] = $UserId }
    if ($OrgId) { $headers["x-org-id"] = $OrgId }
    $body = @{ query = $Query; variables = $Variables } | ConvertTo-Json -Depth 10 -Compress
    
    try {
        $res = Invoke-RestMethod -Uri "http://127.0.0.1:$script:NODE_PORT/graphql" -Method Post -Headers $headers -Body $body
        return $res
    } catch {
        if ($_.Exception.Response) {
            $stream = $_.Exception.Response.GetResponseStream()
            $reader = New-Object System.IO.StreamReader($stream)
            $errBody = $reader.ReadToEnd()
            try {
                return $errBody | ConvertFrom-Json
            } catch {
                return $errBody
            }
        }
        throw $_
    }
}

function Get-GraphQLErrorCode {
    param([string]$Query, [object]$Variables = @{}, [string]$UserId = "", [string]$OrgId = "")
    $response = Invoke-GraphQLRaw -Query $Query -Variables $Variables -UserId $UserId -OrgId $OrgId
    if ($response.errors -and $response.errors.Count -gt 0 -and $response.errors[0].extensions -and $response.errors[0].extensions.code) {
        return $response.errors[0].extensions.code
    }
    return "SUCCESS"
}

function Invoke-Psql {
    param([string]$Container, [string]$User, [string]$Database, [string]$Sql)
    $output = (docker exec $Container psql -U $User -d $Database -Atc $Sql) -split "\r?\n" | Where-Object { $_ -match '\S' }
    if ($output -is [array] -and $output.Count -gt 0) {
        return ($output[-1]).Trim()
    } elseif ($output) {
        return $output.Trim()
    }
    return ""
}

function Set-Olp {
    param([int]$Enabled)
    $value = if ($Enabled -eq 1) { "true" } else { "false" }
    Invoke-Psql -Container "seta-access-db" -User "access_user" -Database "access_db" -Sql "UPDATE access.organizations SET olp_enabled=$value WHERE id='$script:ORG_ID';" | Out-Null
}

function Grant-Permission {
    param([string]$ResourceType, [string]$ResourceId, [string]$Action, [string]$GranteeUser, [string]$Actor = $script:ADMIN_USER)
    $mutation = 'mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'
    $vars = @{
        orgId = $script:ORG_ID
        resourceType = $ResourceType
        resourceId = $ResourceId
        action = $Action
        granteeUserId = $GranteeUser
    }
    $res = Invoke-GraphQLRaw -Query $mutation -Variables $vars -UserId $Actor -OrgId $script:ORG_ID
    return $res.data.grantObjectPermission.id
}

function Revoke-Permission {
    param([string]$PermissionId, [string]$Actor = $script:ADMIN_USER)
    $mutation = 'mutation($id: ID!) { revokeObjectPermission(id: $id) }'
    $vars = @{ id = $PermissionId }
    Invoke-GraphQLRaw -Query $mutation -Variables $vars -UserId $Actor -OrgId $script:ORG_ID | Out-Null
}

# ---------- sections ----------

function Invoke-Architecture {
    Show-Scenario "architecture" "Architecture recap" "This confirms both the Go asset-core and Node access-core services are running and healthy. The Node service acts as the RBAC/OLP boundary, while the Go service owns the actual asset data."
    
    $goHealth = (Invoke-RestMethod -Uri "http://127.0.0.1:$script:GO_PORT/healthz").status
    Assert-Check "ok" $goHealth "Go asset-core /healthz"

    $nodeHealth = (Invoke-RestMethod -Uri "http://127.0.0.1:$script:NODE_PORT/health").status
    Assert-Check "ok" $nodeHealth "Node access-core /health"
}

function Invoke-OrgIsolation {
    Show-Scenario "org-isolation" "Authentication and organization isolation" "Every request must carry a known user. We'll show that a missing or unknown user is UNAUTHENTICATED, that a user requesting a different org's folder tree is FORBIDDEN, and that none of these denied attempts touch the Asset DB."

    $folderCountBefore = Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;"
    
    $orgQuery = 'query { organizations { id } }'
    $noUserCode = Get-GraphQLErrorCode -Query $orgQuery -Variables @{} -UserId "" -OrgId ""
    Assert-Check "UNAUTHENTICATED" $noUserCode "No user header returns UNAUTHENTICATED"
    
    $unknownUserCode = Get-GraphQLErrorCode -Query $orgQuery -Variables @{} -UserId $script:UNKNOWN_USER -OrgId $script:ORG_ID
    Assert-Check "UNAUTHENTICATED" $unknownUserCode "Unknown user returns UNAUTHENTICATED"
    
    $treeQuery = 'query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
    $adminOtherOrgCode = Get-GraphQLErrorCode -Query $treeQuery -Variables @{orgId=$script:OTHER_ORG_ID} -UserId $script:ADMIN_USER -OrgId $script:OTHER_ORG_ID
    Assert-Check "FORBIDDEN" $adminOtherOrgCode "Admin requesting a different org's folder tree is FORBIDDEN"
    
    $viewerOtherOrgCode = Get-GraphQLErrorCode -Query $treeQuery -Variables @{orgId=$script:OTHER_ORG_ID} -UserId $script:VIEWER_USER -OrgId $script:OTHER_ORG_ID
    Assert-Check "FORBIDDEN" $viewerOtherOrgCode "Viewer requesting a different org's folder tree is FORBIDDEN"
    
    $allowedTree = Invoke-GraphQLRaw -Query $treeQuery -Variables @{orgId=$script:ORG_ID} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Admin's own org folder tree:"
    Show-Data $allowedTree
    $allowedCount = if ($allowedTree.data -and $allowedTree.data.folderTree) { @($allowedTree.data.folderTree).Count } else { 0 }
    if ($allowedCount -ge 1) {
        Assert-Check "True" "True" "Admin's own org folder tree is non-empty"
    } else {
        Assert-Check "True" "False" "Admin's own org folder tree is non-empty"
    }
    
    $folderCountAfter = Invoke-Psql "seta-asset-db" "asset_user" "asset_db" "SELECT COUNT(*) FROM folders;"
    Assert-Check $folderCountBefore $folderCountAfter "Denied cross-org attempts did not change the Asset DB"
}

function Invoke-Folders {
    Show-Scenario "folders" "Folder tree lifecycle" "We will show the seeded folder tree, create a new folder, rename it, move it, and attempt to delete a folder that still has children."
    
    $treeQuery = 'query($orgId: ID!) { folderTree(orgId: $orgId) { id name } }'
    $treeRes = Invoke-GraphQLRaw -Query $treeQuery -Variables @{orgId=$script:ORG_ID} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Initial folder tree:"
    Show-Data $treeRes
    
    $createFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    $rootRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-root"; parentPath=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Created root folder:"
    Show-Data $rootRes
    $rootId = $rootRes.data.createFolder.id
    $rootPath = $rootRes.data.createFolder.path
    
    $childRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-child"; parentPath=$rootPath} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Created child folder:"
    Show-Data $childRes
    $childId = $childRes.data.createFolder.id
    $childPath = $childRes.data.createFolder.path
    
    $deleteFolder = 'mutation($orgId: ID!, $id: ID!) { deleteFolder(orgId: $orgId, id: $id) }'
    $delErrCode = Get-GraphQLErrorCode -Query $deleteFolder -Variables @{orgId=$script:ORG_ID; id=$rootId} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Assert-Check "CONFLICT" $delErrCode "Delete folder with children returns CONFLICT"
    
    $updateFolder = 'mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    $renameRes = Invoke-GraphQLRaw -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$childId; name="$RunId-child-renamed"} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Renamed child folder:"
    Show-Data $renameRes
    $renamedPath = $renameRes.data.updateFolder.path
    Assert-Check $childPath $renamedPath "Rename does not change path"
    
    $moveFolder = 'mutation($orgId: ID!, $id: ID!, $destinationParentId: ID) { moveFolder(orgId: $orgId, id: $id, destinationParentId: $destinationParentId) { id path } }'
    $moveRes = Invoke-GraphQLRaw -Query $moveFolder -Variables @{orgId=$script:ORG_ID; id=$childId; destinationParentId=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Moved child folder to root level:"
    Show-Data $moveRes
}

function Invoke-Metadata {
    Show-Scenario "metadata" "Metadata lifecycle + search" "We'll create a metadata item under a new folder, update it, and search for it."
    
    $createFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    $rootRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-meta-root"; parentPath=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $rootId = $rootRes.data.createFolder.id
    
    $createMetadata = 'mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
    $metaRes = Invoke-GraphQLRaw -Query $createMetadata -Variables @{orgId=$script:ORG_ID; input=@{folderId=$rootId; title="$RunId-metadata"; metadataJson='{"demo":true}'}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Created metadata:"
    Show-Data $metaRes
    $metaId = $metaRes.data.createMetadata.id
    
    $updateMetadata = 'mutation($orgId: ID!, $id: ID!, $input: UpdateMetadataInput!) { updateMetadata(orgId: $orgId, id: $id, input: $input) { id title description } }'
    $metaUpdateRes = Invoke-GraphQLRaw -Query $updateMetadata -Variables @{orgId=$script:ORG_ID; id=$metaId; input=@{title="$RunId-metadata-updated"; description="Trainer Demo"}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Updated metadata:"
    Show-Data $metaUpdateRes
    
    $searchMetadata = 'query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    $searchRes = Invoke-GraphQLRaw -Query $searchMetadata -Variables @{orgId=$script:ORG_ID; input=@{query="$RunId-metadata-updated"}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    Write-Host "Search results:"
    Show-Data $searchRes
    $foundCount = 0
    if ($searchRes.data -and $searchRes.data.searchMetadata) {
        $foundItems = @($searchRes.data.searchMetadata) | Where-Object { $_.id -eq $metaId }
        $foundCount = $foundItems.Count
    }
    Assert-Check "1" $foundCount "Search returned the updated metadata item"
}

function Invoke-Rbac {
    Show-Scenario "rbac" "RBAC mode (org seta, olp_enabled = false)" "By default, the org is in RBAC mode. We'll use a viewer account to show that reading succeeds (based on org role) but writing is denied, without even checking object-level grants."
    
    $createFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    $rootRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-rbac-root"; parentPath=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $rootId = $rootRes.data.createFolder.id
    
    $searchMetadata = 'query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    $readErrCode = Get-GraphQLErrorCode -Query $searchMetadata -Variables @{orgId=$script:ORG_ID; input=@{query=$RunId}} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "SUCCESS" $readErrCode "Viewer can read metadata in RBAC mode"
    
    $updateFolder = 'mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    $writeErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$rootId; name="$RunId-rbac-denied"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "FORBIDDEN" $writeErrCode "Viewer cannot write in RBAC mode"
}

function Invoke-Olp {
    Show-Scenario "olp" "Switch to OLP mode" "We flip olp_enabled to true. We'll show how to grant permission directly, how a grant on a parent inherits to a child (but manage_permissions does not), and that a creator does not get implicit access without a grant."
    
    $createFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    $policyRootRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-policy-root"; parentPath=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $policyRootId = $policyRootRes.data.createFolder.id
    $policyRootPath = $policyRootRes.data.createFolder.path
    
    $policyChildRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-policy-child"; parentPath=$policyRootPath} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $policyChildId = $policyChildRes.data.createFolder.id
    
    Write-Host "Enabling OLP mode..."
    Set-Olp 1
    
    $updateFolder = 'mutation($orgId: ID!, $id: ID!, $name: String) { updateFolder(orgId: $orgId, id: $id, name: $name) { id name path } }'
    
    $olpWriteErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$policyRootId; name="$RunId-olp-denied"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "FORBIDDEN" $olpWriteErrCode "Viewer cannot write in OLP mode without grant"
    
    $grantId = Grant-Permission -ResourceType "folder" -ResourceId $policyRootId -Action "write" -GranteeUser $script:VIEWER_USER
    Write-Host "Granted write permission directly to viewer (grant ID: $grantId)"
    
    $olpWriteGrantedErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$policyRootId; name="$RunId-direct-allowed"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "SUCCESS" $olpWriteGrantedErrCode "Viewer can write after direct grant"
    
    Revoke-Permission -PermissionId $grantId
    Write-Host "Revoked direct write permission"
    
    $olpWriteRevokedErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$policyRootId; name="$RunId-revoked-denied"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "FORBIDDEN" $olpWriteRevokedErrCode "Viewer cannot write after revoke"
    
    Write-Host "`nTesting inheritance..."
    $inheritedGrantId = Grant-Permission -ResourceType "folder" -ResourceId $policyRootId -Action "write" -GranteeUser $script:VIEWER_USER
    $childWriteErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$policyChildId; name="$RunId-inherited-allowed"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "SUCCESS" $childWriteErrCode "Viewer can write to child folder via inherited grant"
    Revoke-Permission -PermissionId $inheritedGrantId
    
    $manageGrantId = Grant-Permission -ResourceType "folder" -ResourceId $policyRootId -Action "manage_permissions" -GranteeUser $script:VIEWER_USER
    $grantMutation = 'mutation($orgId: ID!, $resourceType: ResourceType!, $resourceId: ID!, $action: PermissionAction!, $granteeUserId: ID!) { grantObjectPermission(orgId: $orgId, resourceType: $resourceType, resourceId: $resourceId, action: $action, granteeUserId: $granteeUserId) { id } }'

    $exactGrantRes = Invoke-GraphQLRaw -Query $grantMutation -Variables @{orgId=$script:ORG_ID; resourceType="folder"; resourceId=$policyRootId; action="read"; granteeUserId=$script:ADMIN_USER} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    $exactGrantId = $null
    if ($exactGrantRes.data -and $exactGrantRes.data.grantObjectPermission) {
        $exactGrantId = $exactGrantRes.data.grantObjectPermission.id
    }
    if ([string]::IsNullOrEmpty($exactGrantId) -eq $false -and $exactGrantId -ne "null") {
        Assert-Check "True" "True" "Viewer with exact manage_permissions can grant on the exact resource"
    } else {
        Assert-Check "True" "False" "Viewer with exact manage_permissions can grant on the exact resource"
    }

    $childGrantErrCode = Get-GraphQLErrorCode -Query $grantMutation -Variables @{orgId=$script:ORG_ID; resourceType="folder"; resourceId=$policyChildId; action="read"; granteeUserId=$script:ADMIN_USER} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "FORBIDDEN" $childGrantErrCode "Manage permissions does not inherit to child"

    if ([string]::IsNullOrEmpty($exactGrantId) -eq $false -and $exactGrantId -ne "null") {
        Revoke-Permission -PermissionId $exactGrantId
    }
    Revoke-Permission -PermissionId $manageGrantId
    
    Write-Host "`nTesting creator implicit access..."
    $viewerCreatedRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-viewer-created"; parentPath=$policyRootPath} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $viewerCreatedId = $viewerCreatedRes.data.createFolder.id
    Invoke-Psql -Container "seta-asset-db" -User "asset_user" -Database "asset_db" -Sql "UPDATE folders SET created_by='$script:VIEWER_USER' WHERE id='$viewerCreatedId';" | Out-Null
    
    $creatorBypassErrCode = Get-GraphQLErrorCode -Query $updateFolder -Variables @{orgId=$script:ORG_ID; id=$viewerCreatedId; name="$RunId-creator-bypass"} -UserId $script:VIEWER_USER -OrgId $script:ORG_ID
    Assert-Check "FORBIDDEN" $creatorBypassErrCode "Creator has no implicit access without a grant"
    
    Set-Olp 0
    Write-Host "`nOLP mode disabled (restored to RBAC)."
}

function Invoke-SoftDelete {
    Show-Scenario "soft-delete" "Soft delete keeps grants" "When a resource is deleted, it disappears from searches, but its grants remain in the access DB."
    
    $createFolder = 'mutation($orgId: ID!, $name: String!, $parentPath: String) { createFolder(orgId: $orgId, name: $name, parentPath: $parentPath) { id name path } }'
    $rootRes = Invoke-GraphQLRaw -Query $createFolder -Variables @{orgId=$script:ORG_ID; name="$RunId-soft-root"; parentPath=$null} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $rootId = $rootRes.data.createFolder.id
    
    $createMetadata = 'mutation($orgId: ID!, $input: CreateMetadataInput!) { createMetadata(orgId: $orgId, input: $input) { id title } }'
    $softDelRes = Invoke-GraphQLRaw -Query $createMetadata -Variables @{orgId=$script:ORG_ID; input=@{folderId=$rootId; title="$RunId-soft-delete"; metadataJson="{}"}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $softDelId = $softDelRes.data.createMetadata.id
    
    $softDelGrantId = Grant-Permission -ResourceType "metadata_item" -ResourceId $softDelId -Action "read" -GranteeUser $script:VIEWER_USER
    
    $deleteMetadata = 'mutation($orgId: ID!, $id: ID!) { deleteMetadata(orgId: $orgId, id: $id) }'
    Invoke-GraphQLRaw -Query $deleteMetadata -Variables @{orgId=$script:ORG_ID; id=$softDelId} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID | Out-Null
    
    $searchMetadata = 'query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    $delSearchRes = Invoke-GraphQLRaw -Query $searchMetadata -Variables @{orgId=$script:ORG_ID; input=@{query="$RunId-soft-delete"}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $delSearchCount = if ($delSearchRes.data -and $delSearchRes.data.searchMetadata) { @($delSearchRes.data.searchMetadata).Count } else { 0 }
    Assert-Check "0" $delSearchCount "Deleted item disappears from search"
    
    $grantCount = Invoke-Psql -Container "seta-access-db" -User "access_user" -Database "access_db" -Sql "SELECT COUNT(*) FROM access.object_permissions WHERE id='$softDelGrantId';"
    Assert-Check "1" $grantCount "Grant remains in access_db after soft delete"
}

function Invoke-OpenImages {
    Show-Scenario "open-images" "Open Images import" "Imports a verified set of Open Images dataset and demonstrates idempotency."
    if ([string]::IsNullOrEmpty($OpenImagesDirectory)) {
        Write-Host "No -OpenImagesDirectory flag provided. Skipping Open Images import."
        return
    }
    
    $datasetPath = Join-Path $OpenImagesDirectory "validation-sample.json"
    $databaseUrl = "postgresql://asset_user:asset_password@127.0.0.1:5433/asset_db?sslmode=disable"
    
    Write-Host "Running fetch_open_images_metadata.sh --verify-only..."
    bash "$ScriptDir/fetch_open_images_metadata.sh" --verify-only --output-dir "$OpenImagesDirectory"
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to verify open images fixture"
        return
    }
    
    Write-Host "First import run:"
    $firstOutput = & $ImportBinary -file $datasetPath -org-id $script:ORG_ID -user-id $script:ADMIN_USER -database-url $databaseUrl 2>&1 | Out-String
    Write-Host $firstOutput
    if ($firstOutput -match '"metadata_created": 25') {
        Assert-Check "True" "True" "First import created 25 items"
    } else {
        Assert-Check "True" "False" "First import created 25 items"
    }
    
    Write-Host "Second import run (idempotent):"
    $secondOutput = & $ImportBinary -file $datasetPath -org-id $script:ORG_ID -user-id $script:ADMIN_USER -database-url $databaseUrl 2>&1 | Out-String
    Write-Host $secondOutput
    if ($secondOutput -match '"metadata_unchanged": 25') {
        Assert-Check "True" "True" "Second import left 25 items unchanged"
    } else {
        Assert-Check "True" "False" "Second import left 25 items unchanged"
    }
    
    $searchMetadata = 'query($orgId: ID!, $input: MetadataSearchInput!) { searchMetadata(orgId: $orgId, input: $input) { id title externalId } }'
    $openImagesRes = Invoke-GraphQLRaw -Query $searchMetadata -Variables @{orgId=$script:ORG_ID; input=@{externalSource="open_images_v7"; limit=25}} -UserId $script:ADMIN_USER -OrgId $script:ORG_ID
    $openImagesCount = if ($openImagesRes.data -and $openImagesRes.data.searchMetadata) { @($openImagesRes.data.searchMetadata).Count } else { 0 }
    Assert-Check "25" $openImagesCount "Queryable imported items"
}

function Invoke-Section {
    param([string]$Section)
    switch ($Section) {
        "architecture" { Invoke-Architecture }
        "org-isolation" { Invoke-OrgIsolation }
        "folders" { Invoke-Folders }
        "metadata" { Invoke-Metadata }
        "rbac" { Invoke-Rbac }
        "olp" { Invoke-Olp }
        "soft-delete" { Invoke-SoftDelete }
        "open-images" { Invoke-OpenImages }
        default { Write-Error "Unknown section: $Section" }
    }
}

function Show-Menu {
    Write-Host ""
    Write-Host "=== Trainer Demo Menu ==="
    Write-Host "1) architecture   - Service boundary recap, health checks"
    Write-Host "2) org-isolation  - Unauthenticated/unknown user and cross-org access are denied"
    Write-Host "3) folders        - Seeded tree, create/rename/move, delete-with-children CONFLICT"
    Write-Host "4) metadata       - Create/update/search a metadata item"
    Write-Host "5) rbac           - Viewer read-ok / write-FORBIDDEN under default RBAC mode"
    Write-Host "6) olp            - OLP direct grant/revoke, inheritance, manage exactness, creator-no-bypass"
    Write-Host "7) soft-delete    - Delete a metadata item, show its grant row survives"
    Write-Host "8) open-images    - Open Images import (requires -OpenImagesDirectory)"
    Write-Host "0) quit         - Exit the demo"
    Write-Host "all)            - Run all sections sequentially"
    Write-Host ""
}

function Invoke-BootServices {
    New-Item -ItemType Directory -Force -Path $TempDir | Out-Null
    Push-Location $RepoRoot
    try {
        if ((Test-PortOpen $script:GO_PORT) -and (Test-PortOpen $script:NODE_PORT)) {
            Write-Host "Services on ports $($script:GO_PORT) and $($script:NODE_PORT) are already responding. Skipping boot."
        } else {
            Write-Host "Services not running. Booting environment..."
            & npm.cmd run docker:up
            if ($LASTEXITCODE -ne 0) { throw "docker:up failed" }
            & npm.cmd run docker:migrate
            if ($LASTEXITCODE -ne 0) { throw "docker:migrate failed" }
            
            & npm.cmd --prefix services/access-core run build
            if ($LASTEXITCODE -ne 0) { throw "access-core build failed" }
            
            Push-Location $AssetCore
            try {
                & go build -o $GoBinary ./cmd/server/main.go
                if ($LASTEXITCODE -ne 0) { throw "asset-core build failed" }
            } finally {
                Pop-Location
            }
            
            $env:ASSET_DB_HOST = "127.0.0.1"
            $env:ASSET_DB_PORT = "5433"
            $env:ASSET_DB_NAME = "asset_db"
            $env:ASSET_DB_USER = "asset_user"
            $env:ASSET_DB_PASSWORD = "asset_password"
            $env:PORT = $script:GO_PORT.ToString()
            
            $script:GoProcess = Start-Process -FilePath $GoBinary -RedirectStandardOutput $GoStdout -RedirectStandardError $GoStderr -PassThru -WindowStyle Hidden
            
            $waitOk = Wait-Service -Name "Go Asset Core" -Url "http://127.0.0.1:$($script:GO_PORT)/healthz" -Process $script:GoProcess -Logs @($GoStdout, $GoStderr)
            if (-not $waitOk) { throw "Go Asset Core failed to start" }
            
            $env:ACCESS_DB_HOST = "127.0.0.1"
            $env:ACCESS_DB_PORT = "5434"
            $env:ACCESS_DB_NAME = "access_db"
            $env:ACCESS_DB_USER = "access_user"
            $env:ACCESS_DB_PASSWORD = "access_password"
            $env:DATABASE_URL = "postgresql://access_user:access_password@127.0.0.1:5434/access_db"
            $env:GO_ASSET_URL = "http://127.0.0.1:$($script:GO_PORT)"
            $env:PORT = $script:NODE_PORT.ToString()
            
            $nodeScript = Join-Path $AccessCore "dist/index.js"
            $script:NodeProcess = Start-Process -FilePath "node.exe" -ArgumentList "`"$nodeScript`"" -RedirectStandardOutput $NodeStdout -RedirectStandardError $NodeStderr -PassThru -WindowStyle Hidden
            
            $waitOk = Wait-Service -Name "Node Access Core" -Url "http://127.0.0.1:$($script:NODE_PORT)/health" -Process $script:NodeProcess -Logs @($NodeStdout, $NodeStderr)
            if (-not $waitOk) { throw "Node Access Core failed to start" }
        }
        
        if (-not [string]::IsNullOrEmpty($OpenImagesDirectory)) {
            if (-not (Test-Path $ImportBinary)) {
                Write-Host "Building import CLI..."
                Push-Location $AssetCore
                try {
                    & go build -o $ImportBinary ./cmd/import-sample/main.go
                    if ($LASTEXITCODE -ne 0) { throw "import-sample build failed" }
                } finally {
                    Pop-Location
                }
            }
        }
    } finally {
        Pop-Location
    }
}

try {
    Invoke-BootServices
    
    $AllSections = @("architecture", "org-isolation", "folders", "metadata", "rbac", "olp", "soft-delete", "open-images")
    
    if ($SectionsToRun -and $SectionsToRun.Count -gt 0) {
        if ($SectionsToRun[0] -eq "all") {
            foreach ($s in $AllSections) { Invoke-Section $s }
        } else {
            foreach ($s in $SectionsToRun) { Invoke-Section $s }
        }
    } else {
        while ($true) {
            Show-Menu
            $choice = Read-Host "Select a section to run"
            if ($null -ne $choice) {
                $choice = $choice.Trim()
            }
            switch ($choice) {
                { $_ -in @("1", "architecture") } { Invoke-Section "architecture" }
                { $_ -in @("2", "org-isolation") } { Invoke-Section "org-isolation" }
                { $_ -in @("3", "folders") } { Invoke-Section "folders" }
                { $_ -in @("4", "metadata") } { Invoke-Section "metadata" }
                { $_ -in @("5", "rbac") } { Invoke-Section "rbac" }
                { $_ -in @("6", "olp") } { Invoke-Section "olp" }
                { $_ -in @("7", "soft-delete") } { Invoke-Section "soft-delete" }
                { $_ -in @("8", "open-images") } { Invoke-Section "open-images" }
                "all" { foreach ($s in $AllSections) { Invoke-Section $s } }
                { $_ -in @("0", "quit", "q", "exit") } { Write-Host "Exiting..."; break }
                default { Write-Host "Invalid choice: $choice" }
            }
            if ($choice -in @("0", "quit", "q", "exit")) {
                break
            }
        }
    }
    
    Write-Host "`nGraphiQL URL: http://localhost:4000/graphql"
    Write-Host "You can keep poking at the endpoints."
} finally {
    if (Test-Path $TempDir) {
        # Suppress errors because the running .exe files can't be deleted on Windows easily while running
        Remove-Item -Recurse -Force $TempDir -ErrorAction SilentlyContinue
    }
}
