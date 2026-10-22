# Power-cut test of the hub on this Windows PC (docs/HARDWARE-TESTING.md, "Power
# cuts"): saves the hub confirmed must survive a hard reset. A second hub beside the
# installed one, as in the stress kit (127.0.0.1:18090, fake central on :18091).
# Build the kit with perf/windows/kit.sh.
#
# Automatic, on a VM (the Mac kills it at random moments, powercut-host.sh):
#   powercut.ps1 install     once, as administrator: copies the kit to
#                            C:\hubstress\kit and runs "cycle" at every start-up
#                            (as SYSTEM: nobody logs in after a reset)
#   powercut.ps1 status      what the cycles found so far
#   powercut.ps1 uninstall   ends it: removes the start-up task, stops the test
# By hand:
#   powercut.ps1 load        takes saves offline, every user at once, logging each
#                            one the hub confirms; hard-reset the PC during it
#   powercut.ps1 verify      after the PC is back: every confirmed save must still
#                            be on the hub
#   powercut.ps1 cycle       verify, then load (what the start-up task runs)
#   powercut.ps1 reset       start over
#
# Everything stays on this PC's own disk (C:\hubstress\powercut): the log of
# confirmed saves is written through to disk with each save. A shared folder from the
# Mac is not safe for it: a killed VM loses what it last wrote there.
param(
  [Parameter(Mandatory = $true)] [ValidateSet("load", "verify", "cycle", "reset", "install", "uninstall", "status")] [string]$Step,
  [string]$Duration = "60m"
)
$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
$base = Join-Path $env:SystemDrive "hubstress"
$kit = Join-Path $base "kit"
$run = Join-Path $base "powercut"
$data = Join-Path $run "data"
$confirmedTsv = Join-Path $run "confirmed.tsv"
$log = Join-Path $run "powercut-log.txt"
$task = "Hub power-cut test"
$people = 10000; $users = 20

# Each line of the log is on disk before the next step: the next step may be the cut.
function Note($s) {
  $line = "$(Get-Date -Format s) $s"
  Write-Host $line
  New-Item -ItemType Directory -Force -Path $run | Out-Null
  $fs = [IO.FileStream]::new($log, [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::ReadWrite, 4096, [IO.FileOptions]::WriteThrough)
  $b = [Text.Encoding]::UTF8.GetBytes("$line`r`n"); $fs.Write($b, 0, $b.Length); $fs.Close()
}
function Confirmed { if (Test-Path $confirmedTsv) { (Get-Content $confirmedTsv | Measure-Object -Line).Lines } else { 0 } }
function StopTest {
  Get-Process hub, hubload, fakecentral -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -like "$base\*" -or $_.Path -like "$here\*" } | Stop-Process -Force -ErrorAction SilentlyContinue
}

switch ($Step) {
  "reset" {
    StopTest
    Remove-Item -Recurse -Force $run -ErrorAction SilentlyContinue
    Write-Host "the power-cut test starts over at the next load"
    exit 0
  }
  "status" {
    Write-Host "$(Confirmed) saves confirmed so far; the log:"
    if (Test-Path $log) { Get-Content $log | Select-Object -Last 40 } else { Write-Host "(no log yet)" }
    exit 0
  }
  "install" {
    New-Item -ItemType Directory -Force -Path $kit, $run | Out-Null
    foreach ($f in "hub.exe", "hubload.exe", "fakecentral.exe", "powercut.ps1") { Copy-Item (Join-Path $here $f) $kit -Force }
    # Saves confirmed by hand earlier, in the old place next to the script.
    $old = Join-Path $here "confirmed.tsv"
    if ((Test-Path $old) -and -not (Test-Path $confirmedTsv)) { Copy-Item $old $confirmedTsv }
    Add-MpPreference -ExclusionPath $base -ErrorAction SilentlyContinue
    $action = New-ScheduledTaskAction -Execute "powershell.exe" `
      -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$kit\powercut.ps1`" cycle -Duration $Duration"
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Hours 4) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName $task -Action $action -Trigger $trigger -Settings $settings -User "SYSTEM" -RunLevel Highest -Force | Out-Null
    Note "install: the cycle runs at every start-up from $kit; $(Confirmed) saves confirmed so far"
    Write-Host "Now start powercut-host.sh on the Mac. It restarts this VM, so save your work."
    exit 0
  }
  "uninstall" {
    Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
    StopTest
    Note "uninstall: start-up task removed, test stopped; $(Confirmed) saves confirmed in all"
    exit 0
  }
}

# load, verify, cycle: a fake central and the test hub.
$exe = if (Test-Path (Join-Path $kit "hub.exe")) { $kit } else { $here }
$first = -not (Test-Path (Join-Path $data "hub.db"))
New-Item -ItemType Directory -Force -Path $data | Out-Null
Add-MpPreference -ExclusionPath $base -ErrorAction SilentlyContinue
$dataFwd = $data -replace '\\', '/'
@"
central_url: http://127.0.0.1:18091
listen: "127.0.0.1:18090"
data_dir: "$dataFwd"
tls: false
sync_interval: 1h
log_requests: false
"@ | Set-Content -Encoding ascii (Join-Path $run "hub.yaml")

StopTest
$procs = @()
try {
  $central = Start-Process -PassThru -WindowStyle Hidden -FilePath (Join-Path $exe "fakecentral.exe") `
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
  $hub = Start-Process -PassThru -FilePath (Join-Path $exe "hub.exe") -ArgumentList "serve -config `"$(Join-Path $run 'hub.yaml')`""
  $procs += $hub

  $ErrorActionPreference = "Continue"  # hubload logs to stderr
  $load = Join-Path $exe "hubload.exe"
  $common = "-hub http://127.0.0.1:18090 -central http://127.0.0.1:18091 -users $users"

  if ($Step -in "verify", "cycle") {
    $n = Confirmed
    if ($n -eq 0) {
      Note "verify: nothing confirmed yet"
    } else {
      $out = Join-Path $run "verify-last.txt"
      & $load $common.Split(" ") -verify $confirmedTsv 2>&1 | ForEach-Object { "$_" } | Tee-Object -FilePath $out | Out-Host
      if ($LASTEXITCODE -eq 0) {
        Note "verify: all $n confirmed saves are on the hub"
      } else {
        Copy-Item $out (Join-Path $run ("verify-failed-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".txt"))
        Note "verify: SAVES MISSING of $n confirmed ($out lists them); no more loads until this is looked at"
        exit 1
      }
    }
  }
  if ($Step -in "load", "cycle") {
    if ($first) {
      Note "load: first cycle; setting the hub up and filling it ($people people), a few minutes"
      $extra = @("-setup", "-fill")
    } else {
      Note "load: saves offline for up to $Duration, $(Confirmed) confirmed so far"
      $extra = @()
    }
    Write-Host "When 'uplink cut' appears, saves are being confirmed: the power may be cut at any moment."
    & $load $common.Split(" ") $extra -duration $Duration -think 0 -reconnect=false -android-first none -confirmed $confirmedTsv 2>&1 |
      ForEach-Object { "$_" } | Out-Host
    Note "load: ran $Duration without a power cut; $(Confirmed) confirmed so far"
  }
} finally {
  foreach ($p in $procs) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
}
