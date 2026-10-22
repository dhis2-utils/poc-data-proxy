# Power-cut test of the hub on this Windows PC (docs/HARDWARE-TESTING.md, "Power
# cuts"): saves the hub confirmed must survive a hard reset. A second hub beside the
# installed one, as in the stress kit (127.0.0.1:18090, fake central on :18091).
#
# Each cycle has two steps:
#   1. .\powercut.ps1 load     the hub takes saves offline, every user at once; each
#                              save it confirms is logged in confirmed.tsv. After the
#                              "now cut the power" line, hard-reset the PC or VM at any
#                              moment (VM: Reset / Force power off, not Restart).
#   2. .\powercut.ps1 verify   after the PC is back: every save in confirmed.tsv must
#                              still be on the hub. Missing ones are listed.
# Then load again for the next cycle. 10 or more cycles say something; each verify
# checks every save confirmed so far.
#
# Run it from the Mac's shared folder (Z:\...): confirmed.tsv then lives off this PC,
# so the reset cannot touch the record of what the hub confirmed. The hub's data stays
# on this PC's own disk (C:\hubstress\powercut). Run as administrator (the Defender
# exclusion). Start over: .\powercut.ps1 reset. Build the kit with perf/windows/kit.sh.
param(
  [Parameter(Mandatory = $true)] [ValidateSet("load", "verify", "reset")] [string]$Step,
  [string]$Duration = "30m"
)
$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
$run = Join-Path $env:SystemDrive "hubstress\powercut"
$data = Join-Path $run "data"
$confirmedTsv = Join-Path $here "confirmed.tsv"
$log = Join-Path $here "powercut-log.txt"
$people = 10000; $users = 20
function Note($s) { $line = "$(Get-Date -Format s) $s"; Write-Host $line; Add-Content -Path $log -Value $line }

if ($Step -eq "reset") {
  Remove-Item -Recurse -Force $run -ErrorAction SilentlyContinue
  Remove-Item -Force $confirmedTsv, $log -ErrorAction SilentlyContinue
  Write-Host "the power-cut test starts over at the next load"
  exit 0
}

$first = -not (Test-Path (Join-Path $data "hub.db"))
New-Item -ItemType Directory -Force -Path $data | Out-Null
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($admin) { Add-MpPreference -ExclusionPath $run -ErrorAction SilentlyContinue }
$dataFwd = $data -replace '\\', '/'
@"
central_url: http://127.0.0.1:18091
listen: "127.0.0.1:18090"
data_dir: "$dataFwd"
tls: false
sync_interval: 1h
log_requests: false
"@ | Set-Content -Encoding ascii (Join-Path $run "hub.yaml")

$procs = @()
try {
  $central = Start-Process -PassThru -WindowStyle Hidden -FilePath (Join-Path $here "fakecentral.exe") `
    -ArgumentList "-listen 127.0.0.1:18091 -tes $people -enrollments $([int]($people * 5 / 3)) -events $($people * 4) -users $users" `
    -RedirectStandardError (Join-Path $run "central.log") -RedirectStandardOutput (Join-Path $run "central.out")
  $procs += $central
  for ($i = 0; $i -lt 120; $i++) {
    try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:18091/api/system/ping" | Out-Null; break } catch { Start-Sleep 1 }
  }
  if (-not $first) {
    # Central stays down from the start: this fake central is a new one, which does not
    # know the tokens the hub holds; online, the hub would take them for revoked.
    Invoke-WebRequest -UseBasicParsing -Method Post "http://127.0.0.1:18091/fake/down" | Out-Null
  }
  $hub = Start-Process -PassThru -FilePath (Join-Path $here "hub.exe") -ArgumentList "serve -config `"$(Join-Path $run 'hub.yaml')`""
  $procs += $hub

  $ErrorActionPreference = "Continue"  # hubload logs to stderr
  $load = Join-Path $here "hubload.exe"
  $common = "-hub http://127.0.0.1:18090 -central http://127.0.0.1:18091 -users $users"
  if ($Step -eq "verify") {
    if (-not (Test-Path $confirmedTsv)) { Note "nothing to verify yet: run load first"; exit 1 }
    $n = (Get-Content $confirmedTsv | Measure-Object -Line).Lines
    Note "verify: $n confirmed saves to find"
    & $load $common.Split(" ") -verify $confirmedTsv 2>&1 | ForEach-Object { "$_" } | Tee-Object -FilePath (Join-Path $here "verify-last.txt") | Out-Host
    if ($LASTEXITCODE -eq 0) { Note "verify: all $n confirmed saves are on the hub" } else { Note "verify: SAVES MISSING (verify-last.txt lists them)" }
  } else {
    if ($first) {
      Note "load: first cycle; setting the hub up and filling it ($people people), a few minutes"
      $extra = @("-setup", "-fill")
    } else {
      Note "load: next cycle"
      $extra = @()
    }
    Write-Host ""
    Write-Host "When 'uplink cut' appears, saves are being confirmed: hard-reset this PC or VM at any moment."
    Write-Host ""
    & $load $common.Split(" ") $extra -duration $Duration -think 0 -reconnect=false -android-first none -confirmed $confirmedTsv 2>&1 |
      ForEach-Object { "$_" } | Out-Host
    Note "load: ran $Duration without a power cut; run load again and cut the power this time"
  }
} finally {
  foreach ($p in $procs) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
}
