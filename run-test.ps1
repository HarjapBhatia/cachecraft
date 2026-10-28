# run-test.ps1
# PowerShell benchmark harness for CacheCraft load experiments on Windows.
#
# ENVIRONMENT CONFIGURATION:
# Automatically loads defaults from .env if present.
# You do NOT need to pass -Target on every run.
#
# USAGE EXAMPLES (Clean & Simple):
#   .\run-test.ps1 -Type baseline -Strat cache-aside -Delay 0 -TTL 300
#   .\run-test.ps1 -Type stampede -Strat no-cache -N 200 -Delay 250 -Node 1
#   .\run-test.ps1 -Type stampede -Strat cache-aside -N 600 -Delay 250
#   .\run-test.ps1 -Type stampede -Strat local-single-flight -N 600 -Delay 250
#   .\run-test.ps1 -Type stampede -Strat distributed-lock -N 600 -Delay 250
#   .\run-test.ps1 -Type avalanche -Strat cache-aside -TTL 5 -Delay 5 -Jitter 0.0
#   .\run-test.ps1 -Type avalanche -Strat cache-aside-jitter -TTL 5 -Delay 5 -Jitter 0.10

param (
    [string]$Type = "baseline",
    [string]$Strat = "no-cache",
    [int]$N = 600,
    [int]$TTL = 30,
    [int]$Delay = 0,
    [double]$Jitter = 0.10,
    [int]$Node = 0,
    [string]$Target = ""
)

# Load defaults from .env if present
$baseUrl = "http://localhost:8080"
if (Test-Path ".env") {
    Get-Content ".env" | ForEach-Object {
        $line = $_.Trim()
        if ($line -and -not $line.StartsWith("#") -and $line.Contains("=")) {
            $parts = $line.Split("=", 2)
            $varName = $parts[0].Trim()
            $varVal = $parts[1].Trim()
            if ($varName -eq "BASE_URL") { $baseUrl = $varVal }
            if ($varName -eq "TARGET_URL" -and -not $Target) { $Target = $varVal }
        }
    }
}

# Resolve target URL
if (-not $Target) {
    if ($Node -eq 1) {
        $Target = "http://localhost:8081/api/items/42"
    } elseif ($Node -eq 2) {
        $Target = "http://localhost:8082/api/items/42"
    } elseif ($Node -eq 3) {
        $Target = "http://localhost:8083/api/items/42"
    } else {
        $Target = "$baseUrl/api/items/42"
    }
}

$scriptPath = "k6-load-tests/$Type.js"
if (-not (Test-Path $scriptPath)) {
    Write-Error "Test script '$scriptPath' not found."
    exit 1
}

Write-Host "======================================================"
Write-Host "Running $Type test"
Write-Host "  Strategy:   $Strat"
Write-Host "  VUs:        $N"
Write-Host "  Target:     $Target"
Write-Host "  Delay:      ${Delay}ms"
Write-Host "  TTL:        ${TTL}s"
Write-Host "  Jitter:     $Jitter"
Write-Host "======================================================"

# Helper function to fetch query counter from an API node
function Get-QueryCount([int]$port) {
    try {
        $res = Invoke-RestMethod -Uri "http://localhost:$port/debug/db-queries" -Method Get -TimeoutSec 3 -ErrorAction Stop
        return [int64]$res.dbQueryCount
    } catch {
        return 0
    }
}

# 1. Snapshot query counters before test
$before1 = Get-QueryCount 8081
$before2 = Get-QueryCount 8082
$before3 = Get-QueryCount 8083

Write-Host "Before DB Queries - API 1: $before1, API 2: $before2, API 3: $before3"

# 2. Set environment variables for k6
$env:BASE_URL   = $baseUrl
$env:TARGET_URL = $Target
$env:STRATEGY   = $Strat
$env:DELAY      = [string]$Delay
$env:TTL        = [string]$TTL
$env:JITTER     = [string]$Jitter
$env:VUS        = [string]$N

# 3. Run k6
k6 run $scriptPath

# 4. Snapshot query counters after test
$after1 = Get-QueryCount 8081
$after2 = Get-QueryCount 8082
$after3 = Get-QueryCount 8083

Write-Host "After DB Queries  - API 1: $after1, API 2: $after2, API 3: $after3"

$delta1 = $after1 - $before1
$delta2 = $after2 - $before2
$delta3 = $after3 - $before3
$totalDelta = $delta1 + $delta2 + $delta3

Write-Host "======================================================"
Write-Host "Repository Query Evidence (Total Delta): $totalDelta"
Write-Host " (API 1: +$delta1 | API 2: +$delta2 | API 3: +$delta3)"
Write-Host "======================================================"

if ($Strat -eq "distributed-lock" -and $Type -eq "stampede" -and $totalDelta -gt 1) {
    Write-Warning "Distributed lock test expected exactly 1 query, but saw $totalDelta."
}
