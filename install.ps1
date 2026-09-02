# 2papi installer for Windows (PowerShell)
# Usage: irm https://raw.githubusercontent.com/Rethinger/2papi/main/install.ps1 | iex
$ErrorActionPreference = "Stop"
$Repo = "Rethinger/2papi"
$Bin = "2papi.exe"
$Version = $env:VERSION
if (-not $Version) { $Version = "latest" }

function Get-Platform { return "windows_amd64" }

function Get-LatestVersion {
  if ($Version -ne "latest") { return $Version }
  try {
    $latest = (Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest").tag_name
    if ($latest) { return $latest }
  } catch {}
  # No published release (or the API is unreachable). Don't guess a tag —
  # a made-up version just turns this into a confusing 404 later.
  Write-Host "No published release found for $Repo." -ForegroundColor Yellow
  Write-Host "Install from source instead:"
  Write-Host "  go install github.com/Rethinger/2papi/cmd/gateway@master"
  Write-Host "or run the full stack with Docker:"
  Write-Host "  docker compose up --build"
  Write-Host "To pin a specific tag once releases exist: -Version vX.Y.Z"
  exit 1
}

$Platform = Get-Platform
$Tag = Get-LatestVersion
Write-Host "→ Installing 2papi $Tag for $Platform ..."

$Tmp = Join-Path $env:TEMP "2papi-install"
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
$Url = "https://github.com/$Repo/releases/download/$Tag/2papi_${Platform}.zip"
$Zip = Join-Path $Tmp "2papi.zip"
try {
  Invoke-WebRequest -Uri $Url -OutFile $Zip -UseBasicParsing
  Expand-Archive -Path $Zip -DestinationPath $Tmp -Force
  $Binary = Join-Path $Tmp $Bin
} catch {
  Write-Host "→ Release not found, building from source (requires Go)..."
  if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw "Go not found and no prebuilt binary. Install Go or use Docker." }
  $Src = Join-Path $Tmp "src"
  git clone --depth 1 "https://github.com/$Repo.git" $Src 2>$null
  if (-not (Test-Path $Src)) { Copy-Item -Recurse -Path "." -Destination $Src }
  Push-Location $Src
  go build -ldflags="-s -w" -o (Join-Path $Tmp $Bin) ./cmd/gateway
  Pop-Location
  $Binary = Join-Path $Tmp $Bin
}

$InstallDir = "$env:USERPROFILE\.2papi\bin"
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
Copy-Item -Path $Binary -Destination (Join-Path $InstallDir $Bin) -Force
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
  [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
  $env:Path += ";$InstallDir"
}

Write-Host "✓ Installed to $InstallDir\$Bin"
$configDir = "$env:USERPROFILE\.2papi"
$configFile = Join-Path $configDir "config.yaml"
if (-not (Test-Path $configFile)) {
  New-Item -ItemType Directory -Path $configDir -Force | Out-Null
  if (Test-Path ".\config\example.yaml") { Copy-Item ".\config\example.yaml" $configFile }
  else { Set-Content -Path $configFile -Value "version: 1`nsecret: change-me`nserver:`n  addr: `":8080`"`n" }
  Write-Host "✓ Config at $configFile"
}
Write-Host ""
Write-Host "Run: $InstallDir\$Bin --config $configFile"
Write-Host "Dashboard: http://localhost:8080/dashboard/"

