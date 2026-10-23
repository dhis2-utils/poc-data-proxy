# Stress test of the hub on this Windows PC, beside the installed hub and without
# touching it: a second hub (its own folder, ports 18090/18091 on 127.0.0.1 only), a
# fake central with a facility of the chosen size, and the load generator from perf/.
# The same runs as on the Pis (docs/HARDWARE-TESTING.md, "The ladder"):
#   1. realistic: 20 staff, set up and first pull online, then 5 minutes offline at
#      a normal pace, then the reconnect and push;
#   2. hammer: every user at once, no pauses, 2 minutes offline, then the push.
# Memory and CPU of hub.exe, and the disk's latency and queue, are sampled every 10
# seconds. The test's data goes to C:\hubstress on this PC's own disk;
# results-<size>-<time>.zip lands next to this script. Build the kit with
# perf/windows/kit.sh.
#
#   powershell -ExecutionPolicy Bypass -File .\stress.ps1 -Size 30k
#
# With -Central, only the hub runs here: the fake central and the users run on
# another machine (perf/remote-load.sh on the Mac), so they take nothing from this
# PC. The hub listens on all addresses at port 18090, behind a firewall rule for the
# test, and uses the given central; the size is the one the fake central was started
# with, given here for the record. Press Enter when the other machine is done.
#
#   powershell -ExecutionPolicy Bypass -File .\stress.ps1 -Size 300k -Central http://192.168.64.1:8090
#
# Sizes: 10k to 700k people (enrollments 5/3 and events 4 per person: 300k is the HC
# IV size). Run as administrator to exclude the test folder from Defender scanning, as
# the installer does for the real hub's folder (and, with -Central, to open the port).
param(
  [ValidateSet("10k", "30k", "60k", "100k", "300k", "500k", "700k")] [string]$Size = "30k",
  [int]$Users = 20,
  [string]$Realistic = "5m",
  [string]$Hammer = "2m",
  [string]$Central = ""
)
$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
$people = @{ "10k" = 10000; "30k" = 30000; "60k" = 60000; "100k" = 100000; "300k" = 300000; "500k" = 500000; "700k" = 700000 }[$Size]
# The test's data on this PC's own disk, never next to the script: run from a shared
# folder (Z:\ from the Mac), the database would sit on the network share.
$run = Join-Path $env:SystemDrive ("hubstress\run-$Size-" + (Get-Date -Format "yyyyMMdd-HHmmss"))
$data = Join-Path $run "data"
New-Item -ItemType Directory -Force -Path $data | Out-Null
$report = Join-Path $run "summary.txt"
function Note($s) { $s | Tee-Object -FilePath $report -Append }

# The PC, for the record. hub.exe has no console (a Windows GUI program): its version
# is in the reports, from its status.
$os = Get-CimInstance Win32_OperatingSystem
$cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
$disk = Get-PhysicalDisk -ErrorAction SilentlyContinue | Select-Object -First 1
Note "test data in $run"
Note "stress test $Size ($people people), $Users users, $(Get-Date -Format s)"
Note "PC: $($cpu.Name), $($cpu.NumberOfLogicalProcessors) logical CPUs, $([math]::Round($os.TotalVisibleMemorySize/1MB,1)) GB memory, $($os.Caption) $($os.Version)"
if ($disk) { Note "disk: $($disk.FriendlyName) $($disk.MediaType) $($disk.BusType)" }
try {
  $tpm = Get-Tpm
  Note "TPM: present=$($tpm.TpmPresent) ready=$($tpm.TpmReady)"
} catch {
  Note "TPM: not usable ($($_.Exception.Message.Trim()))"
}

$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if ($admin) {
  Add-MpPreference -ExclusionPath $run -ErrorAction SilentlyContinue
  Note "Defender: test folder excluded (as the installer does)"
} else {
  Note "Defender: test folder NOT excluded (not administrator): scanning slows the hub"
}

# The hub's settings: a central on this PC, plain HTTP on 127.0.0.1 (no firewall
# prompt, nothing on the network), the first sync after the reconnect at once. With
# -Central: that central, on all addresses, and the deployed sync interval, so pulls
# run during the load as they would at a facility.
$dataFwd = $data -replace '\\', '/'
if ($Central) {
@"
central_url: $Central
listen: "0.0.0.0:18090"
data_dir: "$dataFwd"
tls: false
reconnect_delay: 0s
log_requests: false
"@ | Set-Content -Encoding ascii (Join-Path $run "hub.yaml")
} else {
@"
central_url: http://127.0.0.1:18091
listen: "127.0.0.1:18090"
data_dir: "$dataFwd"
tls: false
sync_interval: 1h
reconnect_delay: 0s
log_requests: false
"@ | Set-Content -Encoding ascii (Join-Path $run "hub.yaml")
}

# Memory and CPU of hub.exe, the disk's read and write latency and queue, and free
# memory, every 10 seconds, until the hub stops. Counter names are those of an
# English Windows; elsewhere the disk columns stay empty.
$samples = Join-Path $run "samples.csv"
$sampler = $null
function Sample($id) {
  "time,working_set_mb,private_mb,cpu_seconds,disk_read_ms,disk_write_ms,disk_queue,available_mb" | Set-Content $samples
  $script:sampler = Start-Job -ArgumentList $id, $samples -ScriptBlock {
    param($id, $file)
    $names = '\PhysicalDisk(_Total)\Avg. Disk sec/Read', '\PhysicalDisk(_Total)\Avg. Disk sec/Write',
      '\PhysicalDisk(_Total)\Current Disk Queue Length', '\Memory\Available MBytes'
    while ($true) {
      $p = Get-Process -Id $id -ErrorAction SilentlyContinue
      if (-not $p) { break }
      $c = @("", "", "", "")
      try {
        $v = (Get-Counter $names -ErrorAction Stop).CounterSamples.CookedValue
        $c = @([math]::Round($v[0] * 1000, 1), [math]::Round($v[1] * 1000, 1), [math]::Round($v[2], 1), [math]::Round($v[3]))
      } catch {}
      "{0},{1},{2},{3},{4},{5},{6},{7}" -f (Get-Date -Format "HH:mm:ss"), [math]::Round($p.WorkingSet64 / 1MB), [math]::Round($p.PrivateMemorySize64 / 1MB), [math]::Round($p.CPU, 1), $c[0], $c[1], $c[2], $c[3] |
        Add-Content $file
      Start-Sleep 10
    }
  }
}
function Summary {
  $rows = Import-Csv $samples
  if ($rows) {
    $peak = ($rows | Measure-Object -Property working_set_mb -Maximum).Maximum
    Note "`nhub.exe peak working set: $peak MB; data folder: $([math]::Round(((Get-ChildItem -Recurse $data | Measure-Object Length -Sum).Sum) / 1MB)) MB"
    $q = $rows | Where-Object { $_.disk_queue -ne "" }
    if ($q) {
      $r = ($q | ForEach-Object { [double]$_.disk_read_ms } | Measure-Object -Average -Maximum)
      $dq = ($q | ForEach-Object { [double]$_.disk_queue } | Measure-Object -Average -Maximum)
      $am = ($q | ForEach-Object { [double]$_.available_mb } | Measure-Object -Minimum)
      Note ("disk read latency: average {0:N1} ms, max {1:N1} ms; disk queue: average {2:N1}, max {3:N1}; least free memory: {4} MB" -f $r.Average, $r.Maximum, $dq.Average, $dq.Maximum, $am.Minimum)
    }
  }
}

$procs = @()
$rule = "Hub stress test (port 18090)"
try {
  $enr = [int]($people * 5 / 3); $ev = $people * 4
  if ($Central) {
    if (-not $admin) { throw "-Central needs an administrator PowerShell, to open port 18090" }
    New-NetFirewallRule -DisplayName $rule -Direction Inbound -Protocol TCP -LocalPort 18090 -Action Allow | Out-Null
    $hub = Start-Process -PassThru -FilePath (Join-Path $here "hub.exe") -ArgumentList "serve -config `"$(Join-Path $run 'hub.yaml')`""
    $procs += $hub
    $ips = (Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notlike "127.*" -and $_.IPAddress -notlike "169.254.*" }).IPAddress -join ", "
    Note "hub only, central $Central; this PC: $ips"
    Note "on the Mac: perf/remote-load.sh http://<one of those addresses>:18090 $Size ..."
    Sample $hub.Id
    Read-Host "`nRun the load from the Mac now. Press Enter here when it is done"
    Summary
    return
  }
  Note "fake central: $people people, $enr enrollments, $ev events"
  $central = Start-Process -PassThru -WindowStyle Hidden -FilePath (Join-Path $here "fakecentral.exe") `
    -ArgumentList "-listen 127.0.0.1:18091 -tes $people -enrollments $enr -events $ev -users $Users" `
    -RedirectStandardError (Join-Path $run "central.log") -RedirectStandardOutput (Join-Path $run "central.out")
  $procs += $central
  for ($i = 0; $i -lt 600; $i++) {
    try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:18091/api/system/ping" | Out-Null; break } catch { Start-Sleep 1 }
  }
  Note "fake central ready after ${i}s"

  $hub = Start-Process -PassThru -FilePath (Join-Path $here "hub.exe") `
    -ArgumentList "serve -config `"$(Join-Path $run 'hub.yaml')`""
  $procs += $hub
  for ($i = 0; $i -lt 60; $i++) {
    try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "http://127.0.0.1:18090/hub/status" | Out-Null; break } catch { Start-Sleep 1 }
  }

  Sample $hub.Id

  # hubload logs to stderr: Windows PowerShell 5.1 shows such lines as errors, so
  # they must not stop the script, and are turned into plain text.
  $ErrorActionPreference = "Continue"
  $load = Join-Path $here "hubload.exe"
  $common = "-hub http://127.0.0.1:18090 -central http://127.0.0.1:18091 -users $Users"
  Note "`n--- realistic: set up, first pull, $Realistic offline, reconnect"
  $t = Measure-Command { & $load $common.Split(" ") -setup -fill -duration $Realistic -android-first none -out (Join-Path $run "realistic.txt") 2>&1 | ForEach-Object { "$_" } | Tee-Object -FilePath (Join-Path $run "realistic.log") | Out-Host }
  Note "realistic run: $([math]::Round($t.TotalMinutes,1)) min, exit $LASTEXITCODE"
  Note "`n--- hammer: every user at once, $Hammer offline, reconnect"
  $t = Measure-Command { & $load $common.Split(" ") -duration $Hammer -think 0 -out (Join-Path $run "hammer.txt") 2>&1 | ForEach-Object { "$_" } | Tee-Object -FilePath (Join-Path $run "hammer.log") | Out-Host }
  Note "hammer run: $([math]::Round($t.TotalMinutes,1)) min, exit $LASTEXITCODE"

  Summary
} finally {
  if ($Central) { Remove-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue }
  if ($sampler) { Stop-Job $sampler -ErrorAction SilentlyContinue; Remove-Job $sampler -Force -ErrorAction SilentlyContinue }
  foreach ($p in $procs) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
  Start-Sleep 2
  Copy-Item (Join-Path $data "hub.log") $run -ErrorAction SilentlyContinue
  $zip = Join-Path $here ("results-$Size-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".zip")
  Get-ChildItem $run -File | Compress-Archive -DestinationPath $zip -Force
  if ($admin) { Remove-MpPreference -ExclusionPath $run -ErrorAction SilentlyContinue }
  Write-Host "`nresults: $zip"
  Write-Host "the test's data stays in $run; delete that folder when done"
}
