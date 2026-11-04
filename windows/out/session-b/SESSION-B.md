# Session B: clock and storage (4–7 November)

Physical session 3 in docs/ADVERSARIAL-TESTING.md ("Sessions with the physical hubs"): F1, F2, E7 and E9, with the power rows E1–E6 that session 2 did not reach on hardware. Each test below says what you do, what to note, what the plan expects and about how long it takes. Steps marked **Claude** are done from the sandbox: reading central, `hubdrive check`, and a browser on the Facility Hub app. Claude makes no change on central. Besides the driver's test records, the changes on central are yours: retiring Ngelehun's old hub (2.0), a few EPI Stock values (2.3) and the hub version choice (2.5).

## Plan

The order keeps re-flashing to one card and puts the waiting inside other work.

| Part | Device | Facility | When | Tests | Your time |
|---|---|---|---|---|---|
| 1 | Pi 5, SSD | Needy CHC, as set up in session A | any day before Friday | E9, E1, E2, E5, E6 | about 2 hours |
| 2 | Windows VM, USB HDD | Ngelehun CHC | any day before Friday | E3, F2, E7, E4 | about 3 hours |
| 3 | Pi 4, SD card | Needy CHC, restored from the Pi 5's stick | Friday 6 November, after the 72-hour run ends at about 14:00 | E5 and E9 (the restore), E1, E2, E6 | about 2½ hours |
| 4 | Pi 4, SD card | Needy CHC | Saturday 7 November, morning | F1, with F3 | about 1½ hours |

The Pi 4 is busy with its 72-hour run until Friday 6 November at about 14:00 (windows/out/pi4-week/RUN.md). F1 on the Pi 4 therefore comes last. The Pi 5 is not re-flashed: part 1 uses the hub that session A set up, and part 1 ends by leaving that hub's copy on its USB stick for the restore on Friday. Only the Pi 4's card is flashed, and it must be flashed after the 72-hour run anyway.

The VM is set up as Ngelehun CHC, not Needy. Needy's hub must stay active until the restore on Friday, and a second set-up for Needy would retire it. Ngelehun also has 20 841 people in Child Programme, which makes a first pull long enough to cut (E3). Needy's first pull takes seconds. Ngelehun's current hub (`hub-CInItHlvfgY`) is the soak's Pi 5 hub, whose disk was re-flashed for session A, so retiring it loses nothing.

### What this hardware cannot do

- **E7 on a Pi.** The image has no SSH and no console, so nothing can fill a Pi's disk. E7 runs on the VM only.
- **E4 on a Pi.** The Pi images in `windows/out/pi-image` are built without a release key, and a build without a key never upgrades (`internal/upgrade`, "A build without a key never upgrades"). A Pi upgrade would need a new image and another flash. E4 runs on the VM, with a test release key and a release served from the Mac.
- **E3 at Needy's size.** Needy's first pull is over before a cut can land in it. E3 runs on the VM at Ngelehun's size.
- **A cut or a pull exactly during a stick write (E5, E9).** The hub copies a few kilobytes to the stick once a minute, in milliseconds. A pull by hand lands in a write only by chance. The test shows that pulls and cuts at random moments leave a copy that restores; the restore on Friday is the check.
- **Periods in F1.** Friday evening to Saturday morning crosses no month and no week, so the Pi 4's clock behind central cannot show how it judges an aggregate period. F2 covers periods with the clock ahead, on the VM.
- **F4 with the hub's clock behind.** The Pi image is built from dad9c4d, before the future-date check. With `ready2`'s hub, every Child Programme registration the driver saves offline in part 4 would be refused with E1020 and E1021 (docs/NO-RTC.md, `TestNoRTCEnrollmentDateCheck`). This run cannot show it.
- **Android's incremental syncs with the clock behind.** No tablet in this session; docs/NO-RTC.md has what the code predicts.
- **E8, E10, E11, E12 and F4** are not part of this session.

## Before part 1 (15 minutes)

### The files

Everything for this session is in `windows/out/session-b/` in the repo on the Mac:

- `hubdrive-linux-amd64` and `hubdrive-darwin-arm64`: built from main at dad9c4d. They include the 2 November change that makes `check` retry central's 5xx answers.
- `installer/dhis2-hub-0.0.90-setup.exe`: the hub for the VM, built from main at dad9c4d with a test release key. Its version is set low on purpose, so any later build installs over it.
- `releases/v0.0.91/`: the same code as version 0.0.91, signed with that test key, for E4.
- `slowserve.py`: serves `releases/` from the Mac at a fixed rate, so the download in E4 takes about two minutes.

Results go into the same folder: ledgers, logs and your notes with times.

### The Ubuntu server

The Pis are driven from the server, because Firewalla blocks a Pi's internet and the server stays online. Linux tools cannot resolve `.local` names, so every command uses an IP address. The Pi 5 was at 10.0.1.213. If Firewalla shows another address for it, use that address everywhere below.

On the Mac, in the repo:

```sh
scp windows/out/session-b/hubdrive-linux-amd64 <your server's address>:
```

On the server:

```sh
mkdir -p ~/_tmp_sessionb
mv ~/hubdrive-linux-amd64 ~/_tmp_sessionb/
cd ~/_tmp_sessionb
chmod +x hubdrive-linux-amd64
curl -o needy-ca.crt http://10.0.1.213/hub/ca.crt
openssl x509 -in needy-ca.crt -noout -fingerprint -sha256
```

Compare the fingerprint with the one on the hub's page (`https://10.0.1.213/hub/ui`, "Devices").

**Claude:** before you start, Claude reads the Needy hub's heartbeat on central (`facility-hub-status/hub-b6Gb6hZidJv`), to confirm it is online, has nothing pending and has a working stick.

## Part 1. Pi 5, Needy CHC

### 1.1 Start the driver (5 minutes)

1. Check that the Pi 5's internet is not blocked in Firewalla, and that the USB stick from session A is in the Pi 5. Put a label on that stick: "Needy box".
2. On the Mac, open `https://10.0.1.213/hub/ui` and log in as `needy1`. Under "USB stick" the page should say "Working. Last copy …". Keep this page open for all of part 1.
3. On the server:

   ```sh
   cd ~/_tmp_sessionb
   tmux new -s sessionb
   export HUBDRIVE_PASSWORD='HubDemo-2026!'
   ./hubdrive-linux-amd64 run -hub https://10.0.1.213 -ca needy-ca.crt -users needy1,needy2,needy3 -orgunit UOJlcpPnBat -tag b5cuts -ledger needy-cuts.jsonl 2>&1 | tee needy-cuts.log
   ```

   `UOJlcpPnBat` is Needy CHC. Without `-orgunit`, hubdrive saves at Ngelehun CHC.
4. After two minutes the `minute` lines should show confirmed saves and `pending 0`. Leave the driver running until the end of 1.3.

### 1.2 E9: a different stick (15 minutes)

You need a second USB stick, FAT or exFAT, that has never been in a hub.

1. On the Mac, insert the second stick and record what it holds. Replace `STICK` with the stick's name in Finder:

   ```sh
   ls -laR /Volumes/STICK > ~/Desktop/stick-before.txt
   ```

   Eject it.
2. Pull the "Needy box" stick out of the Pi 5. Put the second stick in.
3. Reload the hub's page after a minute. Note what it says under "USB stick". Expected: "This is not the box's USB stick: the hub copies nothing to it." Do not press "Keep the hub's copy on this stick".
4. Wait 3 minutes. The driver keeps saving, so the hub would copy to a stick it trusted.
5. Pull the second stick, put it in the Mac, and compare:

   ```sh
   ls -laR /Volumes/STICK > ~/Desktop/stick-after.txt
   diff ~/Desktop/stick-before.txt ~/Desktop/stick-after.txt
   ```

   Ignore lines about `.fseventsd` and `.Spotlight-V100`: the Mac writes those. Anything else is a finding, especially a `facility-hub` folder.
6. Put the "Needy box" stick back in the Pi 5. Within 2 minutes the page should say "Working" again, with a new "Last copy" time. Note the time.

**Claude:** at the next hourly heartbeat, Claude reads the hub's `usb` fields on central (`unknown` while the second stick was in, then `present` and `writable`). Claude also opens the Facility Hub app's hub list in a browser and keeps a screenshot of the USB column.

Expected (E9): no secret leaks to the wrong stick; the copy resumes.

### 1.3 Ten power cuts in an hour, with the stick pulled (75 minutes)

This covers E1 (a cut during saves), E5 (a cut while the hub copies to the stick, by chance), E2 (a cut during a push), E6 (ten cuts in an hour) and E9 (the stick pulled during a copy, by chance). Write down the time of every pull and cut, and what the driver showed just before it.

**Offline, the stick pulled (25 minutes)**

1. In Firewalla, block the Pi 5's internet. Note the time. Within a minute the driver shows `hub: up, offline`. Saves stay confirmed, `by the hub alone`, and `pending` grows. The hub now copies the pending saves to the stick every minute.
2. Over the next 20 minutes, pull the "Needy box" stick five times, at random moments. Keep it out for 10 to 30 seconds each time, then put it back.
3. After each time, reload the hub's page. Note whether "Last copy" moves on within 2 minutes. Expected: it does, and the page never shows the stick as failing once it is back.

**Offline, five power cuts (25 minutes)**

4. Keep the internet blocked. Pull the Pi 5's power cord. Plug it back in after about 10 seconds.
5. Wait until the driver shows `hub: up, offline` and two more `minute` lines with confirmed saves. Then do the next cut. Make five cuts in all.
6. For each cut, note how long the hub was down. In session 2 the Pi 5 was back, locked, after 23 to 42 seconds, and the driver's logins unlocked it.

**Online, five cuts during the push (25 minutes)**

7. In Firewalla, unblock the Pi 5's internet. Note the time. The driver shows `hub: up, online`, and `pending` starts to fall as the hub pushes its outbox.
8. While `pending` is still above 0, pull the power cord. Plug it back in after about 10 seconds.
9. When the driver shows `hub: up, online` again, cut again while `pending` is above 0. If `pending` reaches 0 before you have made five cuts here, block the internet for 5 minutes, unblock it, and cut during that push.
10. Make five cuts here, so the hour has ten in all.
11. Wait until `pending` stays at 0 for two `minute` lines. Press Ctrl-C.

What the driver should show: saves not confirmed while the hub is down (`connection refused` or a timeout, which is correct: no client is told a save worked), then `saving again` for each client.

12. Copy the ledger and the log to the Mac:

    ```sh
    scp <your server's address>:_tmp_sessionb/needy-cuts.jsonl <your server's address>:_tmp_sessionb/needy-cuts.log ~/Repos/PoC/poc-data-proxy/windows/out/session-b/
    ```

**Claude:** runs `hubdrive check` on `needy-cuts.jsonl` against central. About 5 000 saves take about 10 minutes, so this runs while you do 1.4.

Expected: every confirmed save is on central once and whole: `lost 0`, no `DUPLICATE` (E2), no `PARTIAL` (E1). The data file is never corrupt (E6): the hub comes back every time. The stick's copy stays usable or is replaced (E5): Friday's restore checks it.

### 1.4 Leave saves on the stick for Friday (20 minutes)

The Pi 5 now stops until the Pi 4 has taken over its hub on Friday. It goes off holding saves that only it and its stick have.

1. On the server, start a new run:

   ```sh
   cd ~/_tmp_sessionb
   ./hubdrive-linux-amd64 run -hub https://10.0.1.213 -ca needy-ca.crt -users needy1,needy2,needy3 -orgunit UOJlcpPnBat -tag b5restore -ledger needy-restore.jsonl 2>&1 | tee needy-restore.log
   ```

2. In Firewalla, block the Pi 5's internet.
3. Let the driver save for 10 minutes. Then press Ctrl-C and note the time.
4. Wait 3 minutes. Reload the hub's page. "Last copy" must be later than the time you stopped the driver. Note the "pending objects" number.
5. Pull the Pi 5's power cord. Take out the "Needy box" stick and keep it with the Pi 4.
6. Do not plug the Pi 5 in again until it is re-flashed. After Friday the Pi 4 is this hub, and two boards must never run as one hub. Leave its internet blocked in Firewalla.
7. Copy the ledger and the log to the Mac:

    ```sh
    scp <your server's address>:_tmp_sessionb/needy-restore.jsonl <your server's address>:_tmp_sessionb/needy-restore.log ~/Repos/PoC/poc-data-proxy/windows/out/session-b/
    ```

**Claude:** reads the hub's last heartbeat on central, to have its state before the restore.

## Part 2. Windows VM, Ngelehun CHC

The VM's network is shared with the Mac, so the driver runs on the Mac, not on the server. Offline is made with a Windows firewall rule that blocks only central. The power cut is UTM's **Force stop** (in the VM window's toolbar, or Virtual Machine menu). Never use "Shut down" or Windows' own restart for a cut.

### 2.0 Install the hub (20 minutes)

1. In the VM, uninstall the old hub: Settings → Apps → Installed apps → DHIS2 Facility Hub → Uninstall. Tick **Remove the hub's data**. That hub is 0.1.3-test, set up as Kindoyal Hospital. Its store is older than the first release, so it is reset, not upgraded (docs/DEPLOYMENT.md, "Stores from before the first release").
2. On the Mac, copy `windows/out/session-b/installer/dhis2-hub-0.0.90-setup.exe` into the folder the VM shares with the Mac (`Z:\` in the VM).
3. In the VM, right-click the installer and choose "Run as administrator". SmartScreen warns, because the installer is signed with the test certificate: choose "More info", then "Run anyway". Install for the whole PC. Accept the Defender exclusion and keeping the PC awake.
4. In the VM's browser, open `https://dhis2.local`. Enter central `https://implement.im.dhis2.org/facility-hub` and log in as `ngelehun1`. Choose Ngelehun CHC. When it asks, retire the old hub, `hub-CInItHlvfgY`.
5. In the VM, open PowerShell as administrator. Run `ipconfig` and note the IPv4 address of the Ethernet adapter. This is the VM's address, written `VM` below.
6. On the Mac:

   ```sh
   cd ~/Repos/PoC/poc-data-proxy/windows/out/session-b
   VM=192.168.64.5            # the address from ipconfig
   curl -o vm-ca.crt http://$VM/hub/ca.crt
   chmod +x hubdrive-darwin-arm64
   xattr -d com.apple.quarantine hubdrive-darwin-arm64 2>/dev/null
   ```

   If the hub chose ports 8443 and 8080 (shown on its page), add them: `https://$VM:8443`, `http://$VM:8080`.

These two commands, in the VM's administrator PowerShell, cut central off and give it back. They block only central's address, so the Mac and the release server stay reachable:

```powershell
# offline: block central
New-NetFirewallRule -DisplayName "Hub test: block central" -Direction Outbound -Action Block -RemoteAddress (Resolve-DnsName implement.im.dhis2.org -Type A).IPAddress
# online again
Remove-NetFirewallRule -DisplayName "Hub test: block central"
```

### 2.1 E3: a cut during the first pull (45 minutes, half of it waiting)

1. Right after set-up the first pull starts. Open `https://dhis2.local/hub/ui` in the VM and log in as `ngelehun1`. Reload it every minute. "objects cached" grows.
2. When "objects cached" has passed 10 000 and still grows, force stop the VM in UTM. Note the number and the time.
3. Start the VM. Wait for Windows to boot. Open `https://dhis2.local/hub/ui` and log in as `ngelehun1`: the login unlocks the hub.
4. Note "objects cached" right after the login. Note whether it grows from there or starts again from 0.
5. Wait until "objects cached" stops growing and "last pull" shows a time. Note how long the whole pull took, with the cut.
6. In the VM, block central (the command above). On the Mac, count Ngelehun's Child Programme people on the hub, offline:

   ```sh
   curl -sk -u 'ngelehun1:HubDemo-2026!' "https://$VM/api/tracker/trackedEntities?program=IpHINAT79UW&orgUnits=DiszpKrYNg8&pageSize=1&totalPages=true&fields=trackedEntity"
   ```

   Note `"total"` in the `pager`. Tell Claude the number and the time.
7. In the VM, unblock central.

**Claude:** reads the same count on central at the same moment (20 841 on 3 November).

Expected (E3): the pull resumes; no half-stored people. The offline total equals central's.

### 2.2 Start the driver (5 minutes)

On the Mac, in a second Terminal window:

```sh
cd ~/Repos/PoC/poc-data-proxy/windows/out/session-b
VM=192.168.64.5            # the address from ipconfig
export HUBDRIVE_PASSWORD='HubDemo-2026!'
caffeinate -dims ./hubdrive-darwin-arm64 run -hub https://$VM -ca vm-ca.crt -tag vmb -ledger vm-b.jsonl 2>&1 | tee vm-b.log
```

The defaults save as `ngelehun1`–`ngelehun3` at Ngelehun CHC. If the certificate does not fit the address (`x509: certificate is valid for …`), replace `-ca vm-ca.crt` with `-insecure`. Leave the driver running until the end of 2.5.

### 2.3 F2: the clock ahead (50 minutes)

Central runs on UTC. Its certificate is valid until 19 December 2026, so a clock up to a month ahead still reaches it.

To read the hub's clock from the Mac at any time, compare these two lines (the `Date` header is the hub's own clock):

```sh
date -u
curl -sI http://$VM/hub/status | grep -i '^date'
```

**Three hours ahead (20 minutes)**

1. In the VM: Settings → Time & language → Date & time. Turn off "Set time automatically". Under "Set the date and time manually", choose Change, and set the time 3 hours ahead.
2. On the Mac, run the two commands above. Note the difference. Run them again after 5 minutes. If the VM's clock has gone back by itself, UTM is setting the guest's time: note it and tell Claude.
3. Watch the driver for 5 minutes online. `pending` should stay at 0.
4. In the VM, block central. Let the driver save offline for 5 minutes. Unblock central. `pending` should fall to 0.
5. On the hub's page, note the "PAT expires" column for `ngelehun1`–`ngelehun3`.

**Claude:** reads the run's people and events on central: their dates must be the driver's (the Mac's clock), not the hub's. Claude also reads the hub's heartbeat time and the expiry of the tokens it made for `ngelehun1`–`ngelehun3`. In the Facility Hub app, Claude notes what "Last heartbeat" shows for a hub 3 hours ahead.

**A month ahead (25 minutes)**

6. Set the VM's date to 1 December 2026, 10:00.
7. Reload the hub's page. Note "objects cached". If it falls in the next minutes, the hub may be applying retention by its own clock: note the numbers and times.
8. On the Mac, open `https://<the VM's address>/dhis-web-aggregate-data-entry/index.html` in a private window and log in as `ngelehun2`. The browser does not trust the VM's new authority: accept the warning, or add `vm-ca.crt` to Keychain Access and trust it for SSL. If that address does not open, open the Data Entry app from the apps menu at `https://<the VM's address>/`.
9. Choose the data set **EPI Stock** (monthly, no future periods) and the org unit Ngelehun CHC. Note which periods the period list offers. Note whether December 2026 is among them.
10. Online, enter a value for November 2026. If December 2026 is offered, enter a value there too. Note what the app shows for each.
11. In the VM, block central. Enter one more value in November 2026, and in December 2026 if offered. Note what the app shows. Unblock central.
12. After the push, reload the hub's page and the Data Entry app. Note what each shows for the December value.

**Claude:** reads both periods' values on central, and the hub's refused changes (`facility-hub-issues`). Central's clock says November, so it should refuse a December value as a future period, and the hub should keep it as a refused change. After the session, clear the November values you entered, online, so the demo's EPI Stock form is as before.

**Back to the right time (5 minutes)**

13. In the VM, turn "Set time automatically" back on. The clock jumps back about four weeks while the hub runs (F3, backwards).
14. Watch the driver for 5 minutes. Note any errors, and whether its clients have to log in again.

Expected (F2, as F1): saves still accepted by central; dates on records come from clients; periods and expiry judged correctly once synced.

### 2.4 E7: the disk fills up (30 minutes)

A Windows VM with a full disk can behave oddly. That is acceptable here: it is a VM.

1. In the VM's administrator PowerShell, note the free space:

   ```powershell
   fsutil volume diskfree C:
   ```

2. Fill the disk to 1.5 GB free:

   ```powershell
   fsutil file createnew C:\fill1.bin ((Get-PSDrive C).Free - 1.5GB)
   ```

3. Press "Sync now" on the hub's page. Note any warning on the page.
4. Block central, so that every save is written to the hub's disk.
5. Fill the rest, leaving almost nothing:

   ```powershell
   fsutil file createnew C:\fill2.bin ((Get-PSDrive C).Free - 20MB)
   fsutil file createnew C:\fill3.bin ((Get-PSDrive C).Free - 1MB)
   ```

6. Watch for 5 minutes. Note what the driver shows for saves (the exact error text), what the hub's page shows, and the last lines of the hub's log:

   ```powershell
   Get-Content "C:\ProgramData\DHIS2 Facility Hub\hub.log" -Tail 20
   ```

7. Free the space:

   ```powershell
   Remove-Item C:\fill1.bin, C:\fill2.bin, C:\fill3.bin
   ```

8. Note how long it takes until the driver confirms saves again, and whether the hub needed a restart.
9. Unblock central. `pending` should fall to 0.

**Claude:** reads the heartbeat's `diskFreeBytes` on central, and keeps a screenshot of the Facility Hub app's flags for this hub ("under 2 GB free"). The heartbeat is hourly, so this may come after the session.

Expected (E7): saves refused clearly; nothing corrupt; the hub warns; it recovers when space returns. A save the driver saw confirmed must be on central (`hubdrive check`, after 2.5).

### 2.5 E4: a cut during an upgrade (50 minutes)

The VM runs 0.0.90. It will upgrade to 0.0.91, which is the same code, from a server on the Mac. The download is slowed to about two minutes so a cut can land in it. Unlike in the sandbox, this upgrade has no migration to run, so a cut can only land in the download, the store copy, the swap or the restart.

1. On the Mac, in a third Terminal window:

   ```sh
   cd ~/Repos/PoC/poc-data-proxy/windows/out/session-b
   python3 slowserve.py releases 8099 200
   ```

   If macOS asks whether Python may accept incoming connections, allow it. The window prints each download's progress in steps of 10 %.
2. In the VM's administrator PowerShell, find the Mac's address. With UTM's shared network it is the "Default Gateway" in `ipconfig`, usually 192.168.64.1. Check that it serves the release:

   ```powershell
   Invoke-WebRequest -UseBasicParsing http://192.168.64.1:8099/v0.0.91/release.json
   ```

3. Give the release server the name the test build expects:

   ```powershell
   Add-Content C:\Windows\System32\drivers\etc\hosts "192.168.64.1 hub-releases.test"
   Invoke-WebRequest -UseBasicParsing http://hub-releases.test:8099/v0.0.91/release.json
   ```

4. In a second administrator PowerShell, follow the hub's log:

   ```powershell
   Get-Content "C:\ProgramData\DHIS2 Facility Hub\hub.log" -Wait -Tail 5 | Select-String upgrad
   ```

5. **Your change on central.** In the Facility Hub app on central, as `fh_national`, open "Hub version". Type the version `0.0.91`. Under "Pilot hubs" choose only Ngelehun CHC with the VM's hub ID (on the hub's page, top right). Press "Start with pilot hubs". Do not press "Next stage". No other hub on the demo central takes it: only this hub is a pilot, and only the VM's build trusts the test key.

**Claude:** reads `facility-hub-admin/release` on central to confirm it names only the VM's hub.

**Cut 1, during the download**

6. Press "Sync now" on the hub's page. Within a minute the Mac's server window shows `/v0.0.91/hub-windows-amd64.exe: 10 %`.
7. At about 50 %, force stop the VM. Note the time.
8. Start the VM. Log in on the hub's page as `ngelehun1`. Note the version at the top right. Expected: v0.0.90.
9. Press "Sync now". The server window should show the download starting again from 0 %. Let it reach 100 %. The hub's log then shows `upgrade downloaded`.

**Cut 2, during the swap**

10. Press "Sync now". When the log shows `upgrading`, force stop the VM at once. Note the time.
11. Start the VM. Log in on the hub's page as `ngelehun1`. Note the version. Expected: v0.0.90 or v0.0.91, never between. The page loads, and the driver confirms saves again.
12. If it is back on v0.0.90, press "Sync now" and let it finish without a cut: a version whose upgrade was cut short is tried once more. Expected after that: v0.0.91. If it came back on v0.0.91 already, the upgrade is done.
13. On the Hub version page, note how it counts the hub.

**Afterwards (10 minutes)**

14. On the Hub version page, press "Stop".
15. Press Ctrl-C in the driver's window and in the server window.
16. In the VM, remove the name:

    ```powershell
    (Get-Content C:\Windows\System32\drivers\etc\hosts) | Where-Object { $_ -notmatch 'hub-releases.test' } | Set-Content C:\Windows\System32\drivers\etc\hosts
    ```

17. Check that the firewall rule is gone:

    ```powershell
    Get-NetFirewallRule -DisplayName "Hub test: block central" -ErrorAction SilentlyContinue
    ```

    It should print nothing. If it prints the rule, remove it with the command in 2.0.

**Claude:** runs `hubdrive check` on `vm-b.jsonl`. It is in `windows/out/session-b/` already. Claude also reads the hub's `lastUpgrade` from its heartbeat.

Expected (E4): the hub comes back on the old or the new release, never in between.

## Part 3. Pi 4, Friday 6 November

### 3.1 End the 72-hour run (20 minutes)

The run ends at about 14:00. Its results come off the card before the card is flashed.

1. On the server, check that the run has finished:

   ```sh
   tmux attach -t pi4week
   ```

   hubload has printed its summary, and the fake central has stopped. Detach with Ctrl-b d.
2. Wait 5 minutes after the fake central stopped: the Pi copies its run to `loadtest/` on the card's boot partition. Then pull the Pi 4's power cord.
3. Remove the firewall rule on the server:

   ```sh
   sudo ufw delete allow from 10.0.1.0/24 to any port 8092 proto tcp
   ```

4. Put the Pi 4's card in the Mac. From its boot partition (`bootfs`), copy `loadtest/` and `soak-samples.csv` into `windows/out/pi4-week/`.
5. Copy the server's files:

   ```sh
   scp <your server's address>:_tmp_hubtest/pi4-sd-72h.txt <your server's address>:_tmp_hubtest/pi4-sd-72h.log <your server's address>:_tmp_hubtest/confirmed.tsv <your server's address>:_tmp_hubtest/central.log ~/Repos/PoC/poc-data-proxy/windows/out/pi4-week/
   ```

### 3.2 Flash the card (15 minutes)

1. In Raspberry Pi Imager: Choose OS → **Use custom** → `windows/out/pi-image/dhis2-hub-0.1.5-local.dad9c4d-arm64.img.xz`. Choose the Pi 4's card. Choose Next, then **No** to "apply OS customisation settings".
2. Do not create `hub-loadtest.txt` on the card. Without it the hub starts as a normal hub.

### 3.3 Restore the Needy hub from the stick (25 minutes)

This is the end of E5 and E9: the copy that went through five stick pulls and ten cuts in part 1 must restore.

1. Check that the Pi 5 is still unplugged.
2. Put the card and the "Needy box" stick in the Pi 4. Plug in Ethernet and power. Wait 3 minutes.
3. In Firewalla, find the Pi 4's address (reserved during the 72-hour run). On the Mac and on the server, the commands below call it `PI4`.
4. On the Mac, open `http://<the Pi 4's address>/hub/setup`. The page should say "The USB stick holds hub hub-b6Gb6hZidJv (copy of …)". Note the copy's time. Expected: later than the time you stopped the driver in 1.4.
5. Press "Restore hub hub-b6Gb6hZidJv" and log in as `needy1` with the password `HubDemo-2026!`.
6. The hub restarts. After a minute, open `https://<the Pi 4's address>/hub/ui` and log in as `needy1`.
7. Note "pending objects". Expected: about the number you noted in 1.4. It then falls to 0 as the hub pushes. Note how long that takes.
8. The hub ID at the top right must be `hub-b6Gb6hZidJv`.

**Claude:** runs `hubdrive check` on `needy-restore.jsonl`. These saves existed only on the Pi 5's SSD and its stick. Claude also reads the hub's heartbeat on central, from the Pi 4 now.

Expected (E5, and E9's "copy resumes"): the stick's copy stays usable; the hub carries on as itself; `lost 0`. The plan allows a restore to lose at most the last minute's saves; 1.4 waited 3 minutes so that none are in that minute.

### 3.4 Ten cuts in an hour on the SD card (75 minutes)

Session 2 and part 1 cut the power on an SSD. This is the same on the SD card.

1. On the server, set the address and fetch the restored hub's authority. It should be the same as `needy-ca.crt`:

   ```sh
   cd ~/_tmp_sessionb
   PI4=10.0.1.50              # the Pi 4's address from Firewalla
   curl -o needy-ca-pi4.crt http://$PI4/hub/ca.crt
   openssl x509 -in needy-ca-pi4.crt -noout -fingerprint -sha256
   openssl x509 -in needy-ca.crt -noout -fingerprint -sha256
   ```

2. Start the driver:

   ```sh
   export HUBDRIVE_PASSWORD='HubDemo-2026!'
   ./hubdrive-linux-amd64 run -hub https://$PI4 -ca needy-ca-pi4.crt -users needy1,needy2,needy3 -orgunit UOJlcpPnBat -tag b4cuts -ledger needy-sd-cuts.jsonl 2>&1 | tee needy-sd-cuts.log
   ```

   If the certificate does not fit the new address, replace `-ca needy-ca-pi4.crt` with `-insecure`, and tell Claude: the restored hub should cover its new address.
3. After two minutes of confirmed saves, block the Pi 4's internet in Firewalla.
4. Make five cuts offline: pull the power cord, plug it back after about 10 seconds, and wait for `hub: up, offline` and two `minute` lines with confirmed saves before the next. Note how long the Pi 4 takes to come back from the card, against the Pi 5's 23 to 42 seconds.
5. Unblock the Pi 4's internet. Make five cuts while `pending` is above 0. If it reaches 0 too soon, block for 5 minutes, unblock, and cut during that push.
6. Wait until `pending` stays at 0 for two `minute` lines. Press Ctrl-C.
7. Copy the ledger and log to the Mac:

   ```sh
   scp <your server's address>:_tmp_sessionb/needy-sd-cuts.jsonl <your server's address>:_tmp_sessionb/needy-sd-cuts.log ~/Repos/PoC/poc-data-proxy/windows/out/session-b/
   ```

**Claude:** runs `hubdrive check` on `needy-sd-cuts.jsonl`.

Expected (E1, E2, E6 on the card): confirmed saves survive; no duplicate on central; the data file never corrupt.

### 3.5 Prepare F1 (10 minutes, Friday evening)

The Pi 4 has no clock battery. Raspberry Pi OS normally starts such a board at the time it last saved (the `fake-hwclock` package). This image has no `fake-hwclock`, and `chrony` replaced `systemd-timesyncd`. Read from the image, the Pi 4 starts at a fixed date: 6 October 2026, 00:27 UTC, the base image's date (docs/NO-RTC.md, "Where the clock starts"). F1 checks this.

1. Leave the Pi 4 running online for at least an hour after 3.4, with no driver.
2. If Firewalla's NTP intercept is on for this network, turn it off until Saturday's test is over. Otherwise Firewalla would give the Pi the right time while its internet is blocked.
3. In Firewalla, block the Pi 4's internet. Note the time.
4. Pull the Pi 4's power cord. Note the time. Leave it unplugged overnight.

## Part 4. Pi 4, Saturday 7 November: F1 (90 minutes)

What the code predicts is in docs/NO-RTC.md. Each prediction is under the step that shows it. Note the time of each step in your notes.

1. Check that the Pi 4's internet is still blocked in Firewalla.
2. On the server, read the dates of the demo central's certificate:

   ```sh
   echo | openssl s_client -connect implement.im.dhis2.org:443 -servername implement.im.dhis2.org 2>/dev/null | openssl x509 -noout -dates
   ```

   Expected: `notBefore` before 6 October 2026 (about 20 September), `notAfter` 19 December 2026. Then the hub can reach central even before its clock is set. If `notBefore` is after 6 October, the certificate was renewed: the hub cannot reach central until chrony sets its clock. Note both dates.
3. On the server, in tmux, open a window that logs the hub's clock next to the server's every 30 seconds:

   ```sh
   cd ~/_tmp_sessionb
   PI4=10.0.1.50              # the Pi 4's address from Firewalla
   while true; do echo "$(date -u '+%a, %d %b %Y %H:%M:%S GMT') server | hub $(curl -sI -m 5 http://$PI4/hub/status | grep -i '^date' | cut -d' ' -f2- | tr -d '\r')"; sleep 30; done | tee f1-clock.log
   ```

4. Plug in the Pi 4. Note the time. When the hub answers, the log shows the hub's clock. Note the first hub time in full, and how long after power-on the hub answered.

   Expected: the hub's clock reads Tue, 06 Oct 2026 00:28 GMT, or a minute or two later: the image's date plus the time since power-on. If it reads Friday evening's time, the image keeps a saved clock that the code reading missed: tell Claude. Compare how long the Pi took to answer with the Pi 4's times in 3.4. A longer boot may come from the file system check, which sees its last mount time in the future.
5. On the Mac, open `https://<the Pi 4's address>/hub/ui` and log in as `needy1`. The hub is offline and locked until this login. Note what the page shows: mode, "last pull", "last push", and the "PAT expires" column.

   Expected: the login works (no check in it depends on the clock). "Last pull" and "last push" show Friday's times, a month after the hub's clock. "PAT expires" shows the dates from Friday.
6. In a second tmux window (Ctrl-b c), start the driver:

   ```sh
   cd ~/_tmp_sessionb
   PI4=10.0.1.50
   export HUBDRIVE_PASSWORD='HubDemo-2026!'
   ./hubdrive-linux-amd64 run -hub https://$PI4 -ca needy-ca-pi4.crt -users needy1,needy2,needy3 -orgunit UOJlcpPnBat -tag b4clock -ledger needy-clock.jsonl 2>&1 | tee needy-clock.log
   ```

   If you used `-insecure` in 3.4, use it here too.
7. Let it save offline for 20 minutes. Saves should be confirmed `by the hub alone`.

   Expected: all confirmed. The image is built from dad9c4d and has no future-date check. A hub built from `ready2` would refuse every registration here (E1020).
8. In Firewalla, unblock the Pi 4's internet. Note the time to the second.
9. Watch both windows for 15 minutes. Note:
   - when the hub's clock in `f1-clock.log` jumps to the right time (F3: the jump while the hub runs);
   - when the hub's page first shows it online, and any error on its page ("last sync error");
   - whether the driver's clients must log in again, or show errors, at the jump;
   - when `pending` reaches 0.

   Expected: the hub probes central every 15 seconds. With the certificate from step 2 it is online within about 30 seconds of the unblock, whatever its clock. chrony steps the clock at its first answer from `pool.ntp.org`, which may come later. If the hub's first sync cycle runs before the jump (it starts within a random 5 minutes of going online), the code predicts three things for Claude to check: the hub's token is renewed at once, with an expiry about 4 January 2027 (6 October plus the default 90 days) instead of about 5 February; the heartbeat's time is 6 October, so the Facility Hub app shows Needy CHC as silent until the next heartbeat, an hour later; the aggregate forms are pulled for September's window. The driver's clients do not have to log in again: sessions and timers use Go's monotonic clock, which the jump does not move.
10. Press Ctrl-C in the driver's window. Leave the clock window running.
11. Wait until 35 minutes after the unblock, so that `hub-diag` has run once after the jump. Then press Ctrl-C in the clock window.
12. Pull the Pi 4's power cord. Put its card in the Mac. In Terminal:

    ```sh
    cp /Volumes/bootfs/hub-diag.txt ~/Repos/PoC/poc-data-proxy/windows/out/session-b/f1-hub-diag.txt
    grep -n -E 'stepped|wrong by|central still unreachable|connectivity|x509|in the future' /Volumes/bootfs/hub-diag.txt
    ```

    Expected: a chrony line "System clock was stepped by" or "System clock wrong by", with about 2 800 000 seconds (32 days). If it shows hours instead, the clock started at a saved time. A line with `x509` means the hub's clock was behind central's certificate when it probed; note its time. Put the card back in the Pi 4, plug in its power, and check that `https://<the Pi 4's address>/hub/ui` answers.
13. Copy the files to the Mac:

    ```sh
    scp <your server's address>:_tmp_sessionb/needy-clock.jsonl <your server's address>:_tmp_sessionb/needy-clock.log <your server's address>:_tmp_sessionb/f1-clock.log ~/Repos/PoC/poc-data-proxy/windows/out/session-b/
    ```

14. If you turned off Firewalla's NTP intercept, turn it back on.

**Claude:** runs `hubdrive check` on `needy-clock.jsonl`. Claude reads on central the dates of the run's people and events (they must be the driver's, not the hub's), the hub's heartbeat times (`time`, `lastPush`, `hubTokenExpires`), and the expiry of the tokens the hub made for itself and for `needy1`–`needy3` around the jump. In the Facility Hub app, Claude notes whether Needy CHC is flagged silent after the unblock, and until when. Claude reads `f1-hub-diag.txt` for the order of the probe errors, the switch online and chrony's step.

Expected (F1): saves still accepted by central; dates on records come from clients; periods and expiry judged correctly once synced. Periods cannot be judged in this run (see "What this hardware cannot do"). F3, which this run also touches: no duplicate pulls, no lost watermarks, sessions survive.

## After the session

- The Pi 5's SSD still holds hub `hub-b6Gb6hZidJv`, which now runs on the Pi 4. Re-flash the SSD before the Pi 5 is used again, and then remove its block in Firewalla.
- Leave the Pi 4 as Needy CHC's hub, or retire it in the Facility Hub app before it is re-flashed.
- Put your notes, with the times, into `windows/out/session-b/notes.txt`. Claude records the results in docs/ADVERSARIAL-TESTING.md ("Progress") and any finding in docs/FINDINGS.md.
