<#
.SYNOPSIS
Fetches and prepares Open Images V7 metadata for import.

.DESCRIPTION
This script serves as an orchestration wrapper for the Go-based prepare-open-images command.
It ensures the output directory exists, delegates to the Go binary to perform secure HTTPS
downloads, caching, deterministic joining, and payload generation. If the Go tool fails,
this script exits with a non-zero code.

.PARAMETER Split
The dataset split to process (e.g., "validation").

.PARAMETER MaxItems
Maximum number of valid items to output.

.PARAMETER OutputDirectory
Absolute path to the output directory where CSV caches and JSON payloads will be saved.

.PARAMETER VerifyOnly
If set, only verifies the existing cache and payload against the manifest without network access or rewriting.
#>
[CmdletBinding()]
param(
    [string]$Split = "validation",
    [int]$MaxItems = 25,
    [string]$OutputDirectory = (Join-Path $env:TEMP "seta-open-images-v7"),
    [switch]$VerifyOnly
)

$ErrorActionPreference = "Stop"

if (-not (Test-Path -LiteralPath $OutputDirectory)) {
    if ($VerifyOnly) {
        throw "Output directory does not exist and VerifyOnly is set."
    }
    New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null
}

$WorkspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$AssetCoreDir = Join-Path $WorkspaceRoot "services\asset-core"

if (-not (Test-Path -LiteralPath $AssetCoreDir)) {
    throw "services\asset-core directory not found."
}

if ($VerifyOnly) {
    $ManifestPath = Join-Path $OutputDirectory "provenance-manifest.json"
    if (-not (Test-Path -LiteralPath $ManifestPath)) {
        throw "Manifest not found in $OutputDirectory for verification."
    }
    Write-Host "VerifyOnly: Reading manifest..."
    $ManifestContent = Get-Content -Raw -Path $ManifestPath | ConvertFrom-Json

    $ExpectedArtifacts = @{
        "oidv7-val-annotations-human-imagelabels.csv" = @{
            Url = "https://storage.googleapis.com/openimages/v7/oidv7-val-annotations-human-imagelabels.csv"
            Sha256 = "92ddbdfceb3626e044df5e89100b24f6c22a79c1888a4bddd00a6f231d86d56a"
        }
        "oidv7-class-descriptions.csv" = @{
            Url = "https://storage.googleapis.com/openimages/v7/oidv7-class-descriptions.csv"
            Sha256 = "84a4373a0efb7fd6d93fe19b0e7ceb6c1b855c233d13b9b78a9a33655c9fdce3"
        }
        "validation-images-with-rotation.csv" = @{
            Url = "https://storage.googleapis.com/openimages/2018_04/validation/validation-images-with-rotation.csv"
            Sha256 = "ed93a0e121fe345effdfc7359b848dbc64a1ff6778c8c73563157cb500b33a17"
        }
    }

    if ($ManifestContent.tool_version -ne "v1.0.0") { throw "Invalid manifest version" }
    if ($ManifestContent.artifacts.Count -ne 3) { throw "Expected exactly 3 artifacts" }
    if ($ManifestContent.output_ids_count -ne 25 -or $ManifestContent.output_ids.Count -ne 25) { throw "Expected exactly 25 output IDs" }
    $UniqueIds = @($ManifestContent.output_ids | Sort-Object -Unique)
    $SortedIds = @($ManifestContent.output_ids | Sort-Object)
    if ($UniqueIds.Count -ne 25 -or (Compare-Object $SortedIds @($ManifestContent.output_ids))) {
        throw "Output IDs must be unique and sorted"
    }

    $SeenArtifacts = @{}
    foreach ($Artifact in $ManifestContent.artifacts) {
        if (-not $ExpectedArtifacts.ContainsKey($Artifact.filename)) { throw "Unexpected artifact filename: $($Artifact.filename)" }
        if ($SeenArtifacts.ContainsKey($Artifact.filename)) { throw "Duplicate artifact filename: $($Artifact.filename)" }
        $SeenArtifacts[$Artifact.filename] = $true
        $Expected = $ExpectedArtifacts[$Artifact.filename]
        if ($Artifact.source_url -ne $Expected.Url) { throw "Unexpected source URL for $($Artifact.filename)" }
        $ResolvedUri = [uri]$Artifact.resolved_url
        if ($ResolvedUri.Scheme -ne "https" -or $ResolvedUri.Host -ne "storage.googleapis.com") { throw "Resolved URL not in allowed host: $($Artifact.resolved_url)" }
        if ($Artifact.sha_256 -ne $Expected.Sha256) { throw "Manifest checksum is not the trusted checksum for $($Artifact.filename)" }

        $ArtifactPath = Join-Path $OutputDirectory $Artifact.filename
        if (-not (Test-Path -LiteralPath $ArtifactPath)) {
            throw "Artifact not found: $($Artifact.filename)"
        }
        $FileInfo = Get-Item -LiteralPath $ArtifactPath
        if ($FileInfo.Length -ne $Artifact.bytes) {
            throw "Artifact size mismatch for $($Artifact.filename)"
        }
        $FileHash = (Get-FileHash -Path $ArtifactPath -Algorithm SHA256).Hash.ToLower()
        if ($FileHash -ne $Expected.Sha256) {
            throw "Artifact checksum mismatch for $($Artifact.filename)"
        }
    }
    if ($SeenArtifacts.Count -ne $ExpectedArtifacts.Count) { throw "Manifest does not contain the exact trusted artifact set" }

    $ForbiddenFiles = Get-ChildItem -Path $OutputDirectory -Include *.jpg,*.jpeg,*.png,*.gif,*.webp,*.bmp,*.partial -Recurse -File -ErrorAction SilentlyContinue
    if ($ForbiddenFiles) { throw "Forbidden image binaries or partial files found in cache" }

    $OutputPath = Join-Path $OutputDirectory "validation-sample.json"
    if (-not (Test-Path -LiteralPath $OutputPath)) {
        throw "Output validation-sample.json not found."
    }

    $OutputHash = (Get-FileHash -Path $OutputPath -Algorithm SHA256).Hash.ToLower()
    if ($OutputHash -ne $ManifestContent.output_checksum) {
        throw "Output checksum mismatch. Expected $($ManifestContent.output_checksum), got $OutputHash"
    }

    $Dataset = Get-Content -Raw -LiteralPath $OutputPath | ConvertFrom-Json
    if ($Dataset.version -ne 1 -or $Dataset.external_source -ne "open_images_v7") { throw "Invalid validation sample contract" }
    if ($Dataset.metadata.Count -ne 25) { throw "Validation sample must contain exactly 25 metadata records" }
    $DatasetIds = @($Dataset.metadata.external_id | Sort-Object)
    if (Compare-Object $SortedIds $DatasetIds) { throw "Validation sample IDs do not match the manifest" }

    Write-Host "VerifyOnly: Verification completed successfully."
    return
}

Push-Location -LiteralPath $AssetCoreDir
try {
    Write-Host "Running go prepare-open-images..."
    go run ./cmd/prepare-open-images/main.go -split $Split -max-items $MaxItems -output-dir $OutputDirectory
    if ($LASTEXITCODE -ne 0) {
        throw "Go prepare-open-images command failed with exit code $LASTEXITCODE"
    }
    Write-Host "Open Images preparation completed successfully."
} finally {
    Pop-Location
}
