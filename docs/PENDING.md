# Pending

What is open: decisions to take, work to build or fix, things to run, and ideas under consideration. Fixed findings, with their tests, are in FINDINGS.md; this list is for what is not done yet. Remove an item once it is done, and add new ones at the end of their section.

The list started on 12 November 2026 as what remained before a first pilot, from the last section of the KISS review of that day (`docs/reviews/KISS-2026-11-12.md`, in the private notes), which keeps the findings. Item names such as S1 or C6 are that review's; R-26 and the like are REVIEW-FABLE5.2's. Item numbers are kept when items are added or removed, since other documents refer to them.

Olav's answers on 12 and 17 November are marked **Answered**. The plan for closing what is listed here is `docs/plans/2026-11-17-pending-items.md` in the private notes.

Removed as done on 17 November: item 5 (CI on branch pushes, T1 and T2), item 8 (request logging, C6), item 9 (the release key, made on 16 November) and item 29 (the hub ID in the Facility Hub app's hub list, a "Show hub ID" box since 3 November).

The list below is what remains, from the KISS review, REVIEW-FABLE5.2 section 13, FINDINGS.md and the hardware sessions. "Decide" items belong to the project. The others are work.

**Decide**

1. **Which platforms and network set-ups the pilot uses.** Pi kits on a router (cases A and D) have the most hardware testing. The setup Wi-Fi, MiFi and USB modems (cases B and C) have none. Windows has run on a VM only (S1, S5).

   **Answered:** not known yet, but at least one Pi and one Windows PC, probably more Windows, since a PC needs no new hardware. So S1 stays in, and Windows on a real facility PC moves to "Run" (R-27, the service start timeout).
2. **Web-only facilities.** The stock Android Capture app does not trust the hub's certificate, and Android is on hold (DEPLOYMENT.md, "Reaching the hub securely", option B, and "New devices"). Pilot facilities must work in the browser: Capture web, Data Entry, eRegisters.

   **Answered:** web only.
3. **An offline edit overwrites a newer value on central** without anyone being told (FINDINGS, session A). Per AGENTS.md, first find out what Android Capture does on its own, then decide whether a pilot accepts it.

   **Not Android only.** The hub pushes the whole stored record, whichever client made the edit: every attribute of a person and every value of an event (adversarial C1). Central imports with `CREATE_AND_UPDATE` and does not check versions. Android Capture on its own does the same.
   - In SDK `3ff01d8` (14 October 2026), `NewTrackerImporterTrackedEntityPostPayloadGenerator` sends every person, enrollment and event that is not `SYNCED`.
   - It sends them with all their attribute values (`queryTrackedEntityAttributeValueToPost`) and all their data values.
   - It uses `CREATE_AND_UPDATE` and makes no check against central's version.

   So a tablet offline for a day overwrites a colleague's newer change on central in the same way. Within the facility the hub adds nothing new: every device saves to the hub's copy, and the later save wins, as online. **Documented** in DEPLOYMENT.md, "Offline edits and newer changes on central", with what staff should do and two options, both unbuilt, if the pilot shows it is common. This is read from the SDK, not run on a tablet without the hub. The hub adds no new risk, but it makes the case more common: more people work offline, for longer.

   **Answered 17 November:** detect and report, as telemetry. The push goes ahead as today; the hub counts the cases and the Facility Hub app shows them. It must not load central noticeably with hundreds of hubs, and it must be easy to remove again. **Built** the same day, checking one save in ten (DEPLOYMENT.md, same section, with the measured cost). Open: after some months of use, read the counts and decide whether to keep the count, remove it, or hold such saves.
4. **TPM or not.** Without it, a stolen hub opens to an offline password guess. With it, the code has not run on a board (S4).

   **On Windows** the hub uses the PC's own TPM through TPM Base Services (`devkey/open_windows.go`), with no board to fit. Windows 11 requires TPM 2.0, so a laptop or PC sold with Windows 11 (most since late 2021) has one, built into the processor (Intel PTT or AMD fTPM). Many Windows 10 PCs from 2016 on have one too, sometimes switched off in the firmware. TESTING.md step 10 for a PC with a TPM has not been run yet.

   **Answered 17 November:** a TPM is recommended and used where there is one, never required: Pi kits without a board and PCs without a TPM are supported. A real account obtained by softer means is the more likely way to the data. DEPLOYMENT.md says so, and the Facility Hub app shows per hub whether its keys are sealed. Open: the runs on a board (HARDWARE-TESTING.md, step 7, on a Pi 4 and a Pi 5, with a drill that removes the board from a set-up hub and restores it from its stick) and on a PC (item 15).

**Build or fix**

6. **The Pi image items** of R-26 and R-31. Commit `23f39d2` fixed the `hub-diag` root path and the file modes. **Built**, **not yet run on a Pi**:
   - R-26: the unit has `MemorySwapMax=0`, `ProtectSystem=strict` and writes only its data folder, its program folder and the stick. Polkit allows the `hub` user only the four NetworkManager actions its nmcli calls need. The hub no longer writes NetworkManager's dnsmasq folder: it sets a flag in `/run/hub`, and a root unit writes the captive line.
   - R-31: unattended upgrades include Raspberry Pi's archive (the origin string is to be confirmed on a Pi). The Pi takes central's time zone (`timedatectl`, one polkit action), so the 03:00 reboot is in the facility's night; a central on UTC leaves it at 03:00 UTC. The boot partition is `nofail` and `hub-diag` writes it safely. The stick's udev rule skips the image's own disk. The DEPLOYMENT.md, appendix A clock save runs every 15 minutes and at shutdown. Load-test mode is only in images built with `LOADTEST=1`.

   Run the 14 checks in `image/README.md` ("Checks on a Pi after an image change") before this counts as done.
7. **"Needs connection" in Capture web** shows as "An unknown error occurred (503)". Staff need to know what it means: fix it in the hub's answer or cover it in training. **Answered:** accepted for the pilot; cover it in training. After a successful pilot, raise it with the Capture team.

**Run**

10. **A release from `main`**: v0.1.6. v0.1.5 was published on 16 November with the review fixes, and no Go code has changed since, so v0.1.6 carries the fixes made from this list. Every step of the workflow is run by hand first (tags are protected); the app's job is new since v0.1.5 and has not run in a release.
11. **A soak on that image on a Pi**, with `hubdrive check`. Much changed on 10 and 11 November: signed instructions, input checks at the boundary, the throttle, the pull overlap.
12. **The live suite on Sierra Leone and Laos** for the R-03 baselines. The adversarial suite was not run in review FABLE5.2.
13. **The Facility Hub app on the demo central** with the new groups, signed instructions and Revoke, as a hub manager who is not a superuser.
14. **The unexplained hardware findings:**
    - the Pi 4 restarted on its own on 30 October;
    - the Pi 4 did no sync cycle on the night of 9 November;
    - the Pi 5 reported "online" under a firewall block without pushing, and the Windows hub showed "online" for a few seconds after a start while the internet was blocked (session B, once item 33). The hub starts offline (`central.NewMonitor`) and goes online after one probe that succeeds, so a probe got through in both;
    - two USB sticks in at once.

    The journal now stays on disk, so the next occurrence can be read. Since 17 November the hub logs why it skips a cycle and why its connectivity changed, and `hub-diag.txt` keeps a day of those lines, power and swap (HARDWARE-TESTING.md, "When something unexplained happens"). A cycle has no overall time limit, so one request that hangs would stop every later cycle; a limit waits until the log shows that was the cause.
15. **Windows on a real facility PC.** Run TESTING.md's Windows steps, including step 10 (the PC's TPM). Check the installer on the PC: a keep-data uninstall and an install of a lower version (the kept version in `install.log` and the Apps list), and that no elevated step runs the installed `hub.exe` (R-27, changed 17 November, not yet run on Windows). The rest of R-27 is open: the upgrade's DPAPI handover is a file read and then unlinked, not passed in memory. And the service start timeout after a cut (event 7009, FINDINGS session B): nothing in the hub is slow before it reports running, so the 30 seconds went to loading the program; if it shows on a real PC, a task at boot that starts the service when it is not running, not `ServicesPipeTimeout`, which changes every service on the PC.
16. **Two hubs on neighbouring facilities** (TESTING.md phase 6), if the pilot district has neighbours that share patients.
17. **Report the two DHIS2 bugs** from REVIEW-FABLE5.2 section 13, and J1, C2, I8c and J4. Each needs a reproduction without the hub first. FINDINGS.md and the private ADVERSARIAL-TESTING.md disagree on J4 (refused at push, or completed on central with a rule broken). I8c is the hub's offline check, not a DHIS2 bug: central's refusal is right.

**Prepare** (Rollout, "At national level")

18. The pilot district and its facilities, chosen by the criteria.
19. Central's time zone: DHIS2's dates and, on a Pi, the time of the nightly update reboot follow it.
20. The hub groups named in the app.
21. Retention set.
22. A password policy.
23. The installation card, the kits with their USB sticks, training and the support line. Training covers the hub's page, where staff see their own refused changes (item 27), and what "An unknown error occurred (503)" means in Capture (item 7).

**From the Pi 4 test of 16 November** (image `0.1.5-rc.1`, Capture 107.3.1 on 2.43.2.1, setup over Wi-Fi). Set-up, offline login after a power cut offline, registration and the push afterwards worked. Open:

24. **Notes on an existing event or enrollment offline.** Capture saves them with `POST /api/tracker/events/{uid}/note` and `…/enrollments/{uid}/note`, which the hub refuses offline ("Could not save event note"). A note typed in the event form when the event is first saved works, since it travels in `POST /api/tracker`. Documented in SUPPORTED.md and docs/user/SETUP-PI.md. A fix would take the note offline as an update of the stored event or enrollment (the store already merges notes by their `note` key, `tracker/model.go`) and push it in the normal tracker import. **Before building it**, check on 2.42 and 2.43 that central treats a note arriving that way as Capture's own request does: a new note added to the existing ones, not replacing them; the note's author and `storedAt`; what central does with a note UID the hub generated; and a note on an event or enrollment central has meanwhile changed or deleted. Per AGENTS.md, also find out what Android Capture does with notes made offline.
25. **Report to the Capture team: the enrollment dashboard crashes when its layout read fails** ("TypeError: _t is undefined" in `useWidgetColumns`; it ignores `isError` in `useEnrollmentPageLayout`). Seen offline when nobody had opened a dashboard online through the hub. The hub now loads the layout entry at every browser login (FINDINGS.md, "Found by adversarial testing", item 35), so this no longer happens through it.
26. **Swap on the Pi 4** (R-26). `hub-diag.txt` shows `memory.swap.max: No such file`: the Raspberry Pi kernel gives the hub's cgroup no swap control, so `MemorySwapMax=0` does not hold. Raspberry Pi OS's `rpi-swap` runs zram swap (905 MB) and a 905 MB swap device on `loop0` labelled `origin:rpi-swap`, which looks like zram's writeback to a file on the disk. zram alone stays in memory; a writeback would put pages of the hub's memory, the data key among them, on the disk unencrypted. Check what `loop0` is backed by and whether writeback is on (`/sys/block/zram0/backing_dev`), on the Pi 5 too; then turn writeback off in the image (or swap off altogether on boards with memory to spare) and add it to `check.sh`.

**Open findings from testing** (moved from FINDINGS.md on 16 November; the sessions' details are in the private notes, `docs/sessions/FINDINGS-SESSIONS.md`). Also from testing, and listed above: an offline edit overwriting a newer value on central (item 3), the stock Android app and the hub's certificate (item 2), "needs connection" shown as an unknown error (item 7), the unexplained hardware findings (item 14) and the Windows service start timeout (item 15).

27. **Refused changes: the author is not told in Capture** (session A). Since 4 November the hub closes "already deleted" refusals itself, a hub manager can Resend or Discard in the Facility Hub app, and a user sees their own refusals on the hub's page. **Answered 17 November:** no new mechanism in the hub; the hub's page goes into the installation card and training (item 23), and the app teams are asked for a way to show a hub's refusals (item 17). Audited the same day: every code the hub has met has a way to close (the table in `docs/design/2026-11-04-refused-changes.md`, "Refusals by who can fix them"). One gap remains: Discard reads central's version as the change's author, so a record central no longer shows that author (moved to another facility, access removed) stays refused, and a manager's only way out is Resend as another user, which sends the change. A Discard as another user would close it; not built until the pilot shows it.
28. **F4 again on the tablet**: an enrollment dated after central's today, offline, now refused at the save by the hub. Run on Android Capture to see what the app shows (`TestFutureEnrollmentDates` covers the hub).
36. **A sync cycle that gets past the push before the clock is set** (F1, after a start without network): untested on hardware. DEPLOYMENT.md, appendix A predicts minor effects (a token five weeks short, a heartbeat dated at the image's date), corrected by later cycles.
