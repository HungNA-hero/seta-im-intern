[CmdletBinding()]
param(
  [ValidateSet("read-folder-tree", "cursor-search", "breaker-recovery", "fixed-request-count")]
  [string]$Scenario = "read-folder-tree",
  [string]$BaseUrl = "http://localhost:4000",
  [string]$K6BaseUrl = "http://host.docker.internal:4000",
  [string]$UserId = "00000000-0000-0000-0000-000000000001",
  [string]$OrgId = "00000000-0000-0000-0000-000000000010",
  [string]$FolderId = "10000000-0000-0000-0000-000000000002",
  [ValidateRange(1, 500)]
  [int]$MaxVUs = 25,
  [ValidateRange(0, 60)]
  [double]$SleepSeconds = 1,
  [string]$RampUp = "1m",
  [string]$HoldDuration = "3m",
  [string]$RampDown = "1m",
  [ValidateRange(1, 100)]
  [int]$PageSize = 100,
  [ValidateRange(1, 1000000000)]
  [int]$TotalRequests = 10000,
  [ValidateRange(1, 100000)]
  [int]$TargetRps = 50,
  [ValidateRange(1, 10000)]
  [int]$PreAllocatedVUs = 20
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$scenarioPath = "/scripts/scripts/load/k6/$Scenario.js"
$resultsDir = Join-Path $repoRoot ".cache\load"
New-Item -ItemType Directory -Force -Path $resultsDir | Out-Null

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
  throw "Docker Desktop is required because k6 runs in a disposable container."
}

try {
  $health = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 "$BaseUrl/health"
  if ($health.StatusCode -ne 200) {
    throw "Access Core health returned HTTP $($health.StatusCode)."
  }
} catch {
  throw "Access Core is not healthy at $BaseUrl/health. Start the local stack before load testing. $($_.Exception.Message)"
}

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$summaryFile = "/results/$timestamp-$Scenario-summary.json"
$dockerArgs = @(
  "run", "--rm", "-i",
  "-v", "${repoRoot}:/scripts:ro",
  "-v", "${resultsDir}:/results",
  "-e", "BASE_URL=$K6BaseUrl",
  "-e", "USER_ID=$UserId",
  "-e", "ORG_ID=$OrgId",
  "-e", "FOLDER_ID=$FolderId",
  "-e", "MAX_VUS=$MaxVUs",
  "-e", "SLEEP_SECONDS=$SleepSeconds",
  "-e", "RAMP_UP=$RampUp",
  "-e", "HOLD_DURATION=$HoldDuration",
  "-e", "RAMP_DOWN=$RampDown",
  "-e", "PAGE_SIZE=$PageSize",
  "-e", "TOTAL_REQUESTS=$TotalRequests",
  "-e", "TARGET_RPS=$TargetRps",
  "-e", "PRE_ALLOCATED_VUS=$PreAllocatedVUs",
  "grafana/k6:0.55.0",
  "run", "--summary-export", $summaryFile, $scenarioPath
)

Write-Host "Running $Scenario against $K6BaseUrl with at most $MaxVUs."
& docker @dockerArgs
if ($LASTEXITCODE -ne 0) {
  throw "k6 reported a failed threshold or runtime error. See the console and $resultsDir."
}

Write-Host "k6 summary saved under $resultsDir."
