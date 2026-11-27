# Pending

What is open: decisions to take, work to build or fix, things to run, and ideas under consideration. Fixed findings, with their tests, are in FINDINGS.md; this list is for what is not done yet. Remove an item once it is done, and add new ones at the end of their section.

The list started on 12 November 2026 as what remained before a first pilot, from the last section of the KISS review of that day (`docs/reviews/KISS-2026-11-12.md`, in the private notes), which keeps the findings. Item names such as S1 or C6 are that review's; R-26 and the like are REVIEW-FABLE5.2's. Item numbers are kept when items are added or removed, since other documents refer to them.

Olav's answers on 12 and 17 November are marked **Answered**. The plan for closing what is listed here is `docs/plans/2026-11-17-pending-items.md` in the private notes.

Removed as done on 17 November: item 5 (CI on branch pushes, T1 and T2), item 8 (request logging, C6), item 9 (the release key, made on 16 November) and item 29 (the hub ID in the Facility Hub app's hub list, a "Show hub ID" box since 3 November). Removed on 19 November: item 12 (the live suite on Sierra Leone and Laos, 2.42 and 2.43: all four pass and their R-03 baselines are in `internal/live/testdata/oracle`; the adversarial suite on all four, its failures read against each profile's metadata: mostly test assumptions, now fixed; the hub findings, fixed, are FINDINGS.md #46 to #48; results in the private notes, `docs/sessions/PROFILES-2026-11-19.md`). Removed on 26 November: item 10 (the release: v0.2.0 was published on 23 November, with the fixes made from this list, the Pi image and the Facility Hub app; the app's job ran in a release for the first time).

Reviewed on 26 November 2026 against the release, the Windows VM sessions of 19 to 22 November (private notes, `docs/sessions/WINDOWS-VM-2026-11-22.md` and `WINDOWS-RETEST-2026-11-22.md`) and the reviews: items 1, 4, 11, 13, 15 and 17 updated, items 37 to 44 added.

The list below is what remains, from the KISS review, REVIEW-FABLE5.2 section 13, FINDINGS.md and the hardware sessions. "Decide" items belong to the project. The others are work.

**Decide**

1. **Which platforms and network set-ups the pilot uses.** Pi kits on a router (cases A and D) have the most hardware testing. The setup Wi-Fi, MiFi and USB modems (cases B and C) have none. Windows has run on VMs only (S1, S5): since 19 to 22 November also on a Windows 11 x64 VM, with the installer, the service, a virtual TPM, upgrades and Edge (item 15).

   **Answered:** not known yet, but at least one Pi and one Windows PC, probably more Windows, since a PC needs no new hardware. So S1 stays in, and Windows on a real facility PC moves to "Run" (R-27, the service start timeout).
2. **Web-only facilities.** The stock Android Capture app does not trust the hub's certificate, and Android is on hold (design/deployment-options.md, option B, and DEPLOYMENT.md, "New devices"). Pilot facilities must work in the browser: Capture web, Data Entry, eRegisters.

   **Answered:** web only.
3. **An offline edit overwrites a newer value on central** without anyone being told (FINDINGS, session A). Per AGENTS.md, first find out what Android Capture does on its own, then decide whether a pilot accepts it.

   **Not Android only.** The hub pushes the whole stored record, whichever client made the edit: every attribute of a person and every value of an event (adversarial C1). Central imports with `CREATE_AND_UPDATE` and does not check versions. Android Capture on its own does the same.
   - In SDK `3ff01d8` (14 October 2026), `NewTrackerImporterTrackedEntityPostPayloadGenerator` sends every person, enrollment and event that is not `SYNCED`.
   - It sends them with all their attribute values (`queryTrackedEntityAttributeValueToPost`) and all their data values.
   - It uses `CREATE_AND_UPDATE` and makes no check against central's version.

   So a tablet offline for a day overwrites a colleague's newer change on central in the same way. Within the facility the hub adds nothing new: every device saves to the hub's copy, and the later save wins, as online. **Documented** in OPERATIONS.md, "Offline edits and newer changes on central", with what staff should do and two options, both unbuilt, if the pilot shows it is common. This is read from the SDK, not run on a tablet without the hub. The hub adds no new risk, but it makes the case more common: more people work offline, for longer.

   **Answered 17 November:** detect and report, as telemetry. The push goes ahead as today; the hub counts the cases and the Facility Hub app shows them. It must not load central noticeably with hundreds of hubs, and it must be easy to remove again. **Built** the same day, checking one save in ten (OPERATIONS.md, same section, with the measured cost). Open: after some months of use, read the counts and decide whether to keep the count, remove it, or hold such saves.
4. **TPM or not.** Without it, a stolen hub opens to an offline password guess. With it, the code has not run on a board (S4).

   **On Windows** the hub uses the PC's own TPM through TPM Base Services (`devkey/open_windows.go`), with no board to fit. Windows 11 requires TPM 2.0, so a laptop or PC sold with Windows 11 (most since late 2021) has one, built into the processor (Intel PTT or AMD fTPM). Many Windows 10 PCs from 2016 on have one too, sometimes switched off in the firmware. TESTING.md step 10 was run on the Windows 11 VM with a virtual TPM (19 to 22 November): sealed, removed, restored, none, and added later all behave as designed. It has not been run on a PC's own TPM.

   **Answered 17 November:** a TPM is recommended and used where there is one, but not required: Pi kits without a board and PCs without a TPM are supported. A real account obtained by softer means is the more likely way to the data. OPERATIONS.md says so, and the Facility Hub app shows per hub whether its keys are sealed. Open: the runs on a board (HARDWARE-TESTING.md, step 7, on a Pi 4 and a Pi 5, with a drill that removes the board from a set-up hub and restores it from its stick) and on a real PC's TPM (item 15).

37. **A code-signing certificate for Windows.** There is none, so v0.2.0 has no Windows installer attached, and a country cannot install the hub on a PC from a release: `windows/build.sh` signs with a self-signed test certificate, which SmartScreen warns about (`windows/README.md`). Who buys and holds the certificate, and through which signing service CI uses it, is open (OPERATIONS.md, "Signing releases": the key must sit in hardware or a cloud signing service, about USD 10 a month for the cheapest). With at least one Windows PC in the pilot (item 1), this comes before the kits (item 23).
38. **The hub's page has one tier for all facility staff** (6 November; FINDINGS.md, "A slow uplink and search filters", "Open"). Anyone assigned to the facility can reset or move the hub and choose the USB stick; only set-up and take-over need the hub owners group. Whether reset and move need a stricter tier is not decided.

**Build or fix**

6. **The Pi image items** of R-26 and R-31. Commit `23f39d2` fixed the `hub-diag` root path and the file modes. **Built**, **not yet run on a Pi**:
   - R-26: the unit has `MemorySwapMax=0`, `ProtectSystem=strict` and writes only its data folder, its program folder and the stick. Polkit allows the `hub` user only the four NetworkManager actions its nmcli calls need. The hub no longer writes NetworkManager's dnsmasq folder: it sets a flag in `/run/hub`, and a root unit writes the captive line.
   - R-31: unattended upgrades include Raspberry Pi's archive (the origin string is to be confirmed on a Pi). The Pi takes central's time zone (`timedatectl`, one polkit action), so the 03:00 reboot is in the facility's night; a central on UTC leaves it at 03:00 UTC. The boot partition is `nofail` and `hub-diag` writes it safely. The stick's udev rule skips the image's own disk. The clock save of design/2026-11-04-no-rtc.md runs every 15 minutes and at shutdown. Load-test mode is only in images built with `LOADTEST=1`.

   Run the 14 checks in `image/README.md` ("Checks on a Pi after an image change") before this counts as done.
7. **"Needs connection" in Capture web** shows as "An unknown error occurred (503)". Staff need to know what it means: fix it in the hub's answer or cover it in training. **Answered:** accepted for the pilot; cover it in training. After a successful pilot, raise it with the Capture team.

**Run**

11. **A soak on the v0.2.0 image on a Pi**, with `hubdrive check` (HARDWARE-TESTING.md, step 8). Much changed since the last long run: signed instructions, input checks at the boundary, the throttle and the pull overlap (10 and 11 November), then the fixes of 17 to 23 November.
13. **The Facility Hub app on the demo central** with the new groups, signed instructions and Revoke, as a hub manager who is not a superuser. Since v0.2.0 also the overwrite count (item 3) and whether each hub's keys are sealed (item 4).
14. **The unexplained hardware findings:**
    - the Pi 4 restarted on its own on 30 October;
    - the Pi 4 did no sync cycle on the night of 9 November;
    - the Pi 5 reported "online" under a firewall block without pushing, and the Windows hub showed "online" for a few seconds after a start while the internet was blocked (session B, once item 33). The hub starts offline (`central.NewMonitor`) and goes online after one probe that succeeds, so a probe got through in both;
    - two USB sticks in at once.

    The journal now stays on disk, so the next occurrence can be read. Since 17 November the hub logs why it skips a cycle and why its connectivity changed, and `hub-diag.txt` keeps a day of those lines, power and swap (HARDWARE-TESTING.md, "When something unexplained happens"). A cycle has no overall time limit, so one request that hangs would stop every later cycle; a limit waits until the log shows that was the cause.
15. **Windows on a real facility PC.** The Windows 11 x64 VM sessions of 19 to 22 November ran most of TESTING.md's Windows steps (its list says which) and the installer cases: an upgrade over v0.1.5, a keep-data uninstall and a lower-version install (the kept version in `install.log` and the Apps list), no elevated run of the installed `hub.exe` (R-27), restarts and a power cut with no event 7009 and nothing lost. What they found is fixed (FINDINGS.md #49 to #54). The VM emulated x64 on Apple Silicon, was slow, and crashed PowerShell and Defender now and then, so a real x64 PC is still needed. On it:
    - TESTING.md's Windows steps, including the ones the VM could not run: a tablet on the LAN (step 2), two USB sticks (step 5), Bonjour (step 6) and the PC's own TPM (step 10).
    - The two fixes made after the retest, not yet run on Windows: `hub init` before the program is replaced with `install.log` appended (FINDINGS #53), and no firewall prompt at a fresh install (#54).
    - The hub reached from another device on a network marked Public, after a fresh install. The first VM run could not reach it; the cause (Block rules Windows made for `bin\hub.exe` at v0.1.5's prompt) is gone, but reachability was not measured again.
    - The service start timeout after a cut (event 7009, FINDINGS session B). It did not show on the x64 VM. If it shows on a real PC: a task at boot that starts the service when it is not running, not `ServicesPipeTimeout`, which changes every service on the PC.
    - The rest of R-27: the upgrade's DPAPI handover is a file read and then unlinked, not passed in memory.
16. **Two hubs on neighbouring facilities** (TESTING.md phase 6), if the pilot district has neighbours that share patients.
17. **The DHIS2 bugs** from REVIEW-FABLE5.2 section 13, and J1, C2, I8c and J4. **Answered 17 November:** C2 (an event accepted into a deleted person's enrollment), J1 (a value accepted into an approved form), the 2.43 event read by ID without program sharing, and filters and orders on attributes of programs the user cannot read are being fixed in the next DHIS2 release; check them on it when it comes. J4 settled on 2.43.2.1: central's completion endpoints do not validate (FINDINGS.md), to report. I8c was the hub's offline check and is fixed (FINDINGS.md, item 42). Also to report: central's `dataEntry/metadata` ETag does not cover which data sets are assigned to the facility (FINDINGS.md, item 43); and two found on 2.43.2.1 on 18 November, both checked without the hub: `GET /api/tracker/events?enrollments=<uid>` without the program answers a bare HTTP 400 where 2.42 answers 200, and marking a form incomplete runs the compulsory-field check (409 E7605). The second has not been tried through the hub: a user who clears a compulsory value and un-completes the form offline would probably get a refusal at the push. Not narrowed down, so not ready to report: a person with no attribute values refused with misleading errors (FINDINGS.md, "A slow uplink and search filters", "Open"). The event read by ID without program sharing still has no reproduction; make one before it is checked on the next release. The drafts are in the private notes, `docs/plans/2026-11-17-upstream-reports.md`. The reports to the Capture and Android teams (items 7, 25, 27, and Android's trust in the hub's certificate) wait for feedback from in-country testing.

**Prepare** (Rollout, "At national level")

18. The pilot district and its facilities, chosen by the criteria.
19. Central's time zone: DHIS2's dates and, on a Pi, the time of the nightly update reboot follow it.
20. The hub groups named in the app.
21. Retention set.
22. A password policy.
23. The installation card, the kits with their USB sticks, training and the support line. Training covers the hub's page, where staff see their own refused changes (item 27), and what "An unknown error occurred (503)" means in Capture (item 7).

39. **Agree what staff details reach central** (DEPLOYMENT.md, "On central, before the first hub", step 5; from the PR review of 23 November). Hubs write the record IDs and DHIS2 usernames of refused and overwritten saves to central, for the Facility Hub app. The country's data protection lead signs off on this, and on who may read those entries, before a national rollout.
40. **Whether central compresses its answers** (FINDINGS.md, "A slow uplink and search filters", "Open"). The demo central did not; uncompressed, a weekly full pull at 31 KB/s took 6 minutes where 2 compressed. Check it per country, at central's proxy.

**From the Pi 4 test of 16 November** (image `0.1.5-rc.1`, Capture 107.3.1 on 2.43.2.1, setup over Wi-Fi). Set-up, offline login after a power cut offline, registration and the push afterwards worked. Open:

25. **Report to the Capture team: the enrollment dashboard crashes when its layout read fails** ("TypeError: _t is undefined" in `useWidgetColumns`; it ignores `isError` in `useEnrollmentPageLayout`). Seen offline when nobody had opened a dashboard online through the hub. The hub now loads the layout entry at every browser login (FINDINGS.md, "Found by adversarial testing", item 35), so this no longer happens through it.
26. **Swap on the Pi 4** (R-26). `hub-diag.txt` shows `memory.swap.max: No such file`: the Raspberry Pi kernel gives the hub's cgroup no swap control, so `MemorySwapMax=0` does not hold. Raspberry Pi OS's `rpi-swap` runs zram swap (905 MB) and a 905 MB swap device on `loop0` labelled `origin:rpi-swap`, which looks like zram's writeback to a file on the disk. zram alone stays in memory; a writeback would put pages of the hub's memory, the data key among them, on the disk unencrypted. Check what `loop0` is backed by and whether writeback is on (`/sys/block/zram0/backing_dev`), on the Pi 5 too; then turn writeback off in the image (or swap off altogether on boards with memory to spare) and add it to `check.sh`.

**Open findings from testing** (moved from FINDINGS.md on 16 November; the sessions' details are in the private notes, `docs/sessions/FINDINGS-SESSIONS.md`). Also from testing, and listed above: an offline edit overwriting a newer value on central (item 3), the stock Android app and the hub's certificate (item 2), "needs connection" shown as an unknown error (item 7), the unexplained hardware findings (item 14) and the Windows service start timeout (item 15).

27. **Refused changes: the author is not told in Capture** (session A). Since 4 November the hub closes "already deleted" refusals itself, a hub manager can Resend or Discard in the Facility Hub app, and a user sees their own refusals on the hub's page. **Answered 17 November:** no new mechanism in the hub; the hub's page goes into the installation card and training (item 23), and the app teams are asked for a way to show a hub's refusals (item 17). Audited the same day: every code the hub has met has a way to close (the table in `docs/design/2026-11-04-refused-changes.md`, "Refusals by who can fix them"). One gap remains: Discard reads central's version as the change's author, so a record central no longer shows that author (moved to another facility, access removed) stays refused, and a manager's only way out is Resend as another user, which sends the change. A Discard as another user would close it; not built until the pilot shows it.
28. **F4 again on the tablet**: an enrollment dated after central's today, offline, now refused at the save by the hub. Run on Android Capture to see what the app shows (`TestFutureEnrollmentDates` covers the hub).
36. **A sync cycle that gets past the push before the clock is set** (F1, after a start without network): untested on hardware. design/2026-11-04-no-rtc.md predicts minor effects (a token five weeks short, a heartbeat dated at the image's date), corrected by later cycles.

**From the Windows VM sessions of 19 to 22 November** (private notes, `docs/sessions/WINDOWS-VM-2026-11-22.md` and `WINDOWS-RETEST-2026-11-22.md`). Fixed: FINDINGS.md #49 to #54. To run on a real PC: item 15. Small, and open:

41. **What the Windows installer and service leave unclear.** None of these loses data.
    - `install.ini` keeps `tpm=none` after the hub seals its keys at a later start, until the next install. The heartbeat and the Facility Hub app show the right state.
    - The registry entry on central shows the version a hub was set up with, not the one it runs, on the "Replace it" page.
    - When the PC's TPM is taken away, Windows' event log says only "Incorrect function" (event 7024). The hub's start page gives the reason.
    - Component names are cut off in the installer's and the uninstaller's lists.
    - The browser tests (`HUB_CLIENTS=1`) fit the synthetic profile only, so they were not run against the Sierra Leone database on Windows; Edge showed no failure of its own.

**Measure first** (REVIEW-FABLE5.2 section 13). Not planned: per AGENTS.md each needs a measured problem first.

42. **The data-value filter reads every event of the program** (R-20). Paging and the uncapped reads are bounded. Measure at 150 000 events (`internal/perf`) before adding anything.
43. **Pushes go one object per request** (R-21.1). Not measured as a problem; central's import time sets the push rate (PERFORMANCE.md, "Central's import time").
44. **TypeScript's `strict` is off in the Facility Hub app** (R-53c).
