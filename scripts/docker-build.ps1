# Build compose services, then drop unused build cache.
# Usage:
#   .\scripts\docker-build.ps1
#   .\scripts\docker-build.ps1 control-plane
param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$Services
)

$ErrorActionPreference = 'Stop'

if ($Services -and $Services.Count -gt 0) {
  docker compose build @Services
} else {
  docker compose build
}

docker builder prune -f | Out-Host
docker image prune -f | Out-Host
docker system df
