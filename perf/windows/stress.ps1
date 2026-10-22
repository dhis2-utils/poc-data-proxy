# Stress test of the hub on this Windows PC, beside the installed hub and without
# touching it: a second hub (its own folder, ports 18090/18091 on 127.0.0.1 only), a
# fake central with a facility of the chosen size, and the load generator from perf/.
# The same runs as on the Pis (docs/HARDWARE-TESTING.md, "The ladder"):
#   1. realistic: 20 staff, set up and first pull online, then 5 minutes offline at
#      a normal pace, then the reconnect and push;
#   2. hammer: every user at once, no pauses, 2 minutes offline, then the push.
# Memory and CPU of hub.exe are sampled every 10 seconds. The test's data goes to
# C:\hubstress on this PC's own disk; results-<size>-<time>.zip lands next to this
# script. Build the kit with perf/windows/kit.sh.
#
#   powershell -ExecutionPolicy Bypass -File .\stress.ps1 -Size 30k
#
# Sizes: 10k, 30k, 60k, 100k people. Run as administrator to exclude the test folder
# from Defender scanning, as the installer does for the real hub's folder.
param(
  [ValidateSet("10k", "30k", "60k", "100k")] [string]$Size = "30k",
  [int]$Users = 20,
  [string]$Realistic = "5m",
  [string]$Hammer = "2m"
)
$ErrorActionPreference = "Stop"
$here = $PSScriptRoot
$people = @{ "10k" = 10000; "30k" = 30000; "60k" = 60000; "100k" = 100000 }[$Size]
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
# prompt, nothing on the network), the first sync after the reconnect at once.
$dataFwd = $data -replace '\\', '/'
@"
central_url: http://127.0.0.1:18091
listen: "127.0.0.1:18090"
data_dir: "$dataFwd"
tls: false
sync_interval: 1h
reconnect_delay: 0s
log_requests: false
"@ | Set-Content -Encoding ascii (Join-Path $run "hub.yaml")

$procs = @()
try {
  $enr = [int]($people * 5 / 3); $ev = $people * 4
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

  # Memory and CPU of hub.exe every 10 seconds, until the runs end.
  $samples = Join-Path $run "samples.csv"
  "time,working_set_mb,private_mb,cpu_seconds" | Set-Content $samples
  $sampler = Start-Job -ArgumentList $hub.Id, $samples -ScriptBlock {
    param($id, $file)
    while ($true) {
      $p = Get-Process -Id $id -ErrorAction SilentlyContinue
      if (-not $p) { break }
      "{0},{1},{2},{3}" -f (Get-Date -Format "HH:mm:ss"), [math]::Round($p.WorkingSet64 / 1MB), [math]::Round($p.PrivateMemorySize64 / 1MB), [math]::Round($p.CPU, 1) |
        Add-Content $file
      Start-Sleep 10
    }
  }

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

  $rows = Import-Csv $samples
  if ($rows) {
    $peak = ($rows | Measure-Object -Property working_set_mb -Maximum).Maximum
    Note "`nhub.exe peak working set: $peak MB; data folder: $([math]::Round(((Get-ChildItem -Recurse $data | Measure-Object Length -Sum).Sum) / 1MB)) MB"
  }
} finally {
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
