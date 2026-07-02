# ================================================================
# Sprint 2 Review Demo — SETA DAM (PowerShell)
# Run from repo root: .\scripts\sprint2-demo.ps1
# ================================================================

$ROOT    = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$GQL     = "http://localhost:4000/graphql"
$ADMIN   = "00000000-0000-0000-0000-000000000001"
$VIEWER  = "00000000-0000-0000-0000-000000000002"
$ORG     = "c0000000-0000-0000-0000-000000000001"
$UNKNOWN = "ffffffff-ffff-ffff-ffff-ffffffffffff"

$DEMO_FOLDER_ID = ""
$DEMO_META_ID   = ""

# ================================================================
# STARTUP
# ================================================================

function Test-Port([int]$Port) {
  try {
    $tcp = New-Object System.Net.Sockets.TcpClient
    $tcp.Connect("127.0.0.1", $Port)
    $tcp.Close(); return $true
  } catch { return $false }
}

Write-Host ""
Write-Host "  [SETUP] Checking services..." -ForegroundColor Yellow

# 1) Docker DBs
Write-Host "  [1/4] Docker databases..." -ForegroundColor Cyan
docker compose -f "$ROOT\infra\docker-compose.yml" up -d asset-db access-db 2>$null
Start-Sleep -Seconds 2

# 2) Flyway
Write-Host "  [2/4] Flyway migrations..." -ForegroundColor Cyan
docker compose -f "$ROOT\infra\docker-compose.yml" --profile migration run --rm flyway-asset 2>$null
docker compose -f "$ROOT\infra\docker-compose.yml" --profile migration run --rm flyway-access 2>$null

# 3) Go :8080
if (Test-Port 8080) {
  Write-Host "  [3/4] Go Asset Core already running on :8080" -ForegroundColor Green
} else {
  Write-Host "  [3/4] Starting Go Asset Core on :8080 (new window)..." -ForegroundColor Cyan
  Start-Process powershell -ArgumentList "-NoExit", "-Command", "Set-Location '$ROOT\services\asset-core'; Write-Host 'Go Asset Core starting...' -ForegroundColor Cyan; go run ./cmd/server/main.go"
}

# 4) Node :4000
if (Test-Port 4000) {
  Write-Host "  [4/4] Node Access Core already running on :4000" -ForegroundColor Green
} else {
  Write-Host "  [4/4] Starting Node Access Core on :4000 (new window)..." -ForegroundColor Cyan
  Start-Process powershell -ArgumentList "-NoExit", "-Command", "Set-Location '$ROOT\services\access-core'; Write-Host 'Node Access Core starting...' -ForegroundColor Cyan; npm run dev"
}

# Wait for both
Write-Host ""
Write-Host "  Waiting for Go (:8080) and Node (:4000)..." -ForegroundColor Yellow

for ($i = 0; $i -lt 30; $i++) {
  $goOk   = Test-Port 8080
  $nodeOk = Test-Port 4000
  if ($goOk -and $nodeOk) { break }
  $status = "Go=$(if($goOk){'OK'}else{'...'}), Node=$(if($nodeOk){'OK'}else{'...'})"
  Write-Host "    $status ($($i*2)s)" -ForegroundColor DarkGray
  Start-Sleep -Seconds 2
}

if (-not (Test-Port 8080) -or -not (Test-Port 4000)) {
  Write-Host "  [ERROR] Services not ready after 60s." -ForegroundColor Red
  Write-Host "  Please start manually:" -ForegroundColor Red
  Write-Host "    Terminal 1: cd services/asset-core && go run ./cmd/server/main.go" -ForegroundColor DarkGray
  Write-Host "    Terminal 2: cd services/access-core && npm run dev" -ForegroundColor DarkGray
  exit 1
}

Write-Host "  [OK] All services ready!" -ForegroundColor Green
Write-Host ""

# ================================================================
# HELPER
# ================================================================

function Run-Demo {
  param(
    [string]$Title,
    [string]$User,
    [string]$Expected,
    [string]$QueryDisplay,
    [hashtable]$Headers,
    [string]$Body
  )

  Write-Host ""
  Write-Host "================================================================" -ForegroundColor White
  Write-Host "  $Title" -ForegroundColor Cyan
  Write-Host "================================================================" -ForegroundColor White
  Write-Host ""
  Write-Host "  User:     " -ForegroundColor Yellow -NoNewline; Write-Host $User
  $hdrStr = ($Headers.GetEnumerator() | Sort-Object Key | ForEach-Object { "$($_.Key): $($_.Value)" }) -join ", "
  Write-Host "  Headers:  " -ForegroundColor Yellow -NoNewline; Write-Host $hdrStr
  Write-Host "  Expected: " -ForegroundColor Yellow -NoNewline; Write-Host $Expected
  Write-Host ""
  Write-Host "  -- Query --" -ForegroundColor DarkGray
  Write-Host $QueryDisplay -ForegroundColor DarkGray
  Write-Host ""

  # Use Invoke-WebRequest to get raw JSON (Invoke-RestMethod auto-parses and loses structure)
  try {
    $raw = Invoke-WebRequest -Uri $GQL -Method Post -ContentType "application/json" `
      -Headers $Headers -Body ([System.Text.Encoding]::UTF8.GetBytes($Body)) `
      -UseBasicParsing -ErrorAction Stop
    $json = $raw.Content | ConvertFrom-Json | ConvertTo-Json -Depth 10
  } catch {
    if ($_.Exception.Response) {
      $reader = [System.IO.StreamReader]::new($_.Exception.Response.GetResponseStream())
      $json = $reader.ReadToEnd() | ConvertFrom-Json | ConvertTo-Json -Depth 10
    } else {
      $json = $_.Exception.Message
    }
  }

  Write-Host "  -- Response --" -ForegroundColor DarkGray
  Write-Host $json
  Write-Host ""
  Read-Host "  Press Enter for next demo"

  # Return parsed object for capturing IDs
  return ($json | ConvertFrom-Json)
}

# ================================================================
# BANNER
# ================================================================

Clear-Host
Write-Host ""
Write-Host "  +---------------------------------------------+" -ForegroundColor White
Write-Host "  |      SETA DAM - Sprint 2 Review Demo        |" -ForegroundColor White
Write-Host "  |      GraphQL API + RBAC Policy Engine        |" -ForegroundColor White
Write-Host "  +---------------------------------------------+" -ForegroundColor White
Write-Host ""
Write-Host "  Endpoint:  " -NoNewline; Write-Host $GQL -ForegroundColor Cyan
Write-Host "  Admin:     " -NoNewline; Write-Host "admin@seta.com (org_admin)" -ForegroundColor Green
Write-Host "  Viewer:    " -NoNewline; Write-Host "dungpd@seta.com (viewer, read only)" -ForegroundColor Green
Write-Host "  Org:       " -NoNewline; Write-Host "Seta" -ForegroundColor Green
Write-Host ""
Read-Host "  Press Enter to start"

# ================================================================
# DEMOS
# ================================================================

# ── DEMO 1: Admin creates folder ──
$r = Run-Demo `
  -Title "DEMO 1 - Admin creates a folder" `
  -User "admin@seta.com (org_admin)" `
  -Expected "ALLOW - returns new folder with id, path, name" `
  -Headers @{ "x-user-id" = $ADMIN; "x-org-id" = $ORG } `
  -Body '{"query":"mutation { createFolder(orgId: \"c0000000-0000-0000-0000-000000000001\", name: \"Sprint 2 Demo\") { id path name createdBy } }"}' `
  -QueryDisplay @"
  mutation {
    createFolder(orgId: "c0000000-...-000001", name: "Sprint 2 Demo")
    { id  path  name  createdBy }
  }
"@

$DEMO_FOLDER_ID = $r.data.createFolder.id
Write-Host "  [Captured] DEMO_FOLDER_ID = $DEMO_FOLDER_ID" -ForegroundColor Green

# ── DEMO 2: Viewer reads folder tree ──
Run-Demo `
  -Title "DEMO 2 - Viewer reads folder tree" `
  -User "dungpd@seta.com (viewer)" `
  -Expected "ALLOW - viewer has read permission, sees full tree" `
  -Headers @{ "x-user-id" = $VIEWER; "x-org-id" = $ORG } `
  -Body '{"query":"query { folderTree(orgId: \"c0000000-0000-0000-0000-000000000001\") { id path name children { id path name } } }"}' `
  -QueryDisplay @"
  query {
    folderTree(orgId: "c0000000-...-000001")
    { id  path  name  children { id  path  name } }
  }
"@ | Out-Null

# ── DEMO 3: Viewer tries to create folder (DENY) ──
Run-Demo `
  -Title "DEMO 3 - Viewer tries to create folder (WRITE)" `
  -User "dungpd@seta.com (viewer)" `
  -Expected "DENY - FORBIDDEN, viewer has no write permission" `
  -Headers @{ "x-user-id" = $VIEWER; "x-org-id" = $ORG } `
  -Body '{"query":"mutation { createFolder(orgId: \"c0000000-0000-0000-0000-000000000001\", name: \"Viewer Cannot Create\") { id } }"}' `
  -QueryDisplay @"
  mutation {
    createFolder(orgId: "c0000000-...-000001", name: "Viewer Cannot Create")
    { id }
  }
"@ | Out-Null

# ── DEMO 4: Admin creates metadata ──
$body4 = "{`"query`":`"mutation { createMetadata(orgId: \`"c0000000-0000-0000-0000-000000000001\`", input: { folderId: \`"$DEMO_FOLDER_ID\`", title: \`"Golden Retriever on grass\`", description: \`"A golden retriever in the park\`", labels: [\`"dog\`", \`"animal\`", \`"outdoor\`"], category: \`"Animal\`", metadataJson: \`"{\\\`"source\\\`": \\\`"open_images\\\`"}\`" }) { id folderId title description labels category } }`"}"

$r = Run-Demo `
  -Title "DEMO 4 - Admin creates metadata in demo folder" `
  -User "admin@seta.com (org_admin)" `
  -Expected "ALLOW - metadata item created with all fields" `
  -Headers @{ "x-user-id" = $ADMIN; "x-org-id" = $ORG } `
  -Body $body4 `
  -QueryDisplay @"
  mutation {
    createMetadata(orgId: "...", input: {
      folderId: "$DEMO_FOLDER_ID"
      title: "Golden Retriever on grass"
      labels: ["dog", "animal", "outdoor"]
      category: "Animal"
    }) { id  folderId  title  labels  category }
  }
"@

$DEMO_META_ID = $r.data.createMetadata.id
Write-Host "  [Captured] DEMO_META_ID = $DEMO_META_ID" -ForegroundColor Green

# ── DEMO 5: Admin lists metadata ──
$body5 = "{`"query`":`"query { metadataItems(orgId: \`"c0000000-0000-0000-0000-000000000001\`", folderId: \`"$DEMO_FOLDER_ID\`") { id title labels category createdAt } }`"}"

Run-Demo `
  -Title "DEMO 5 - Admin lists metadata in folder" `
  -User "admin@seta.com (org_admin)" `
  -Expected "ALLOW - returns array of metadata items" `
  -Headers @{ "x-user-id" = $ADMIN; "x-org-id" = $ORG } `
  -Body $body5 `
  -QueryDisplay @"
  query {
    metadataItems(orgId: "...", folderId: "$DEMO_FOLDER_ID")
    { id  title  labels  category  createdAt }
  }
"@ | Out-Null

# ── DEMO 6: Admin reads metadata detail ──
$body6 = "{`"query`":`"query { metadataItem(orgId: \`"c0000000-0000-0000-0000-000000000001\`", id: \`"$DEMO_META_ID\`") { id title description labels category metadataJson createdBy createdAt } }`"}"

Run-Demo `
  -Title "DEMO 6 - Admin reads metadata detail" `
  -User "admin@seta.com (org_admin)" `
  -Expected "ALLOW - returns full item with metadataJson" `
  -Headers @{ "x-user-id" = $ADMIN; "x-org-id" = $ORG } `
  -Body $body6 `
  -QueryDisplay @"
  query {
    metadataItem(orgId: "...", id: "$DEMO_META_ID")
    { id  title  labels  metadataJson  createdBy }
  }
"@ | Out-Null

# ── DEMO 7: Admin updates metadata ──
$body7 = "{`"query`":`"mutation { updateMetadata(orgId: \`"c0000000-0000-0000-0000-000000000001\`", id: \`"$DEMO_META_ID\`", input: { title: \`"Updated - Golden Retriever\`", labels: [\`"dog\`", \`"updated\`", \`"sprint2\`"] }) { id title labels updatedBy updatedAt } }`"}"

Run-Demo `
  -Title "DEMO 7 - Admin updates metadata" `
  -User "admin@seta.com (org_admin)" `
  -Expected "ALLOW - title and labels updated" `
  -Headers @{ "x-user-id" = $ADMIN; "x-org-id" = $ORG } `
  -Body $body7 `
  -QueryDisplay @"
  mutation {
    updateMetadata(orgId: "...", id: "$DEMO_META_ID", input: {
      title: "Updated - Golden Retriever"
      labels: ["dog", "updated", "sprint2"]
    }) { id  title  labels  updatedBy  updatedAt }
  }
"@ | Out-Null

# ── DEMO 8: canDo — Viewer read (ALLOW) ──
$body8 = "{`"query`":`"query { canDo(userId: \`"$VIEWER\`", action: read, resourceType: folder, resourceId: \`"$DEMO_FOLDER_ID\`") { allowed reason } }`"}"

Run-Demo `
  -Title "DEMO 8 - canDo: Viewer read folder" `
  -User "dungpd@seta.com (viewer)" `
  -Expected "ALLOW - { allowed: true, reason: null }" `
  -Headers @{ "x-user-id" = $VIEWER; "x-org-id" = $ORG } `
  -Body $body8 `
  -QueryDisplay @"
  query {
    canDo(userId: "$VIEWER", action: read,
          resourceType: folder, resourceId: "$DEMO_FOLDER_ID")
    { allowed  reason }
  }
"@ | Out-Null

# ── DEMO 9: canDo — Viewer write (DENY) ──
$body9 = "{`"query`":`"query { canDo(userId: \`"$VIEWER\`", action: write, resourceType: folder, resourceId: \`"$DEMO_FOLDER_ID\`") { allowed reason } }`"}"

Run-Demo `
  -Title "DEMO 9 - canDo: Viewer write folder" `
  -User "dungpd@seta.com (viewer)" `
  -Expected "DENY - { allowed: false, reason: 'no RBAC ceiling' }" `
  -Headers @{ "x-user-id" = $VIEWER; "x-org-id" = $ORG } `
  -Body $body9 `
  -QueryDisplay @"
  query {
    canDo(userId: "$VIEWER", action: write,
          resourceType: folder, resourceId: "$DEMO_FOLDER_ID")
    { allowed  reason }
  }
"@ | Out-Null

# ── DEMO 10: Unknown user ──
Run-Demo `
  -Title "DEMO 10 - Unknown user tries to read" `
  -User "ffffffff-...-ffff (not in DB)" `
  -Expected "DENY - UNAUTHENTICATED" `
  -Headers @{ "x-user-id" = $UNKNOWN; "x-org-id" = $ORG } `
  -Body '{"query":"query { folderTree(orgId: \"c0000000-0000-0000-0000-000000000001\") { id name } }"}' `
  -QueryDisplay @"
  query {
    folderTree(orgId: "c0000000-...-000001") { id  name }
  }
"@ | Out-Null

# ── DEMO 11: Missing org header ──
Run-Demo `
  -Title "DEMO 11 - Missing x-org-id header" `
  -User "admin@seta.com (org_admin)" `
  -Expected "DENY - FORBIDDEN (no org context)" `
  -Headers @{ "x-user-id" = $ADMIN } `
  -Body '{"query":"query { folderTree(orgId: \"c0000000-0000-0000-0000-000000000001\") { id name } }"}' `
  -QueryDisplay @"
  query {
    folderTree(orgId: "c0000000-...-000001") { id  name }
  }
"@ | Out-Null

# ── Summary ──
Write-Host ""
Write-Host "  +-------------------------------------------------------------+" -ForegroundColor White
Write-Host "  |                      DEMO SUMMARY                          |" -ForegroundColor White
Write-Host "  +-------------------------------------------------------------+" -ForegroundColor White
Write-Host "  |  DEMO 1   createFolder (admin)        " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 2   folderTree (viewer)         " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 3   createFolder (viewer)       " -NoNewline; Write-Host "DENY  FORBIDDEN" -ForegroundColor Red -NoNewline; Write-Host "     |" -ForegroundColor White
Write-Host "  |  DEMO 4   createMetadata (admin)      " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 5   metadataItems list (admin)  " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 6   metadataItem detail (admin) " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 7   updateMetadata (admin)      " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 8   canDo viewer read           " -NoNewline; Write-Host "ALLOW" -ForegroundColor Green -NoNewline; Write-Host "               |" -ForegroundColor White
Write-Host "  |  DEMO 9   canDo viewer write          " -NoNewline; Write-Host "DENY  no ceiling" -ForegroundColor Red -NoNewline; Write-Host "    |" -ForegroundColor White
Write-Host "  |  DEMO 10  unknown user                " -NoNewline; Write-Host "DENY  UNAUTHN" -ForegroundColor Red -NoNewline; Write-Host "       |" -ForegroundColor White
Write-Host "  |  DEMO 11  missing org header          " -NoNewline; Write-Host "DENY  FORBIDDEN" -ForegroundColor Red -NoNewline; Write-Host "     |" -ForegroundColor White
Write-Host "  +-------------------------------------------------------------+" -ForegroundColor White
Write-Host ""
Write-Host "  [CLEANUP] Tearing down Docker databases..." -ForegroundColor Yellow
docker compose -f "$ROOT\infra\docker-compose.yml" down -v

Write-Host ""
Write-Host "  [NOTE] Go/Node are still running in their own windows." -ForegroundColor DarkGray
Write-Host "  Close those windows manually when done." -ForegroundColor DarkGray
Write-Host ""
