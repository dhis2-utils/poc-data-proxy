# Pending

What is open: decisions to take, work to build or fix, things to run, and ideas under consideration. Fixed findings, with their tests, are in FINDINGS.md; this list is for what is not done yet. Remove an item once it is done, and add new ones at the end of their section.

The list started on 12 November 2026 as what remained before a first pilot, from the last section of the KISS review of that day (`docs/reviews/KISS-2026-11-12.md`, in the private notes), which keeps the findings. Item names such as S1 or C6 are that review's; R-26 and the like are REVIEW-FABLE5.2's. Item numbers are kept when items are added or removed, since other documents refer to them.

Olav's answers on 12 November are marked **Answered**. On the same day the branch `pilot-prep` turned on CI for branch pushes that change code (T1, T2), fixed C6, documented item 3 in DEPLOYMENT.md and fixed the Pi image items of item 6, pending the checks on a Pi.

The list below is what remains, from the KISS review, REVIEW-FABLE5.2 section 13, FINDINGS.md and the hardware sessions. "Decide" items belong to the project. The others are work.

**Decide**

1. **Which platforms and network set-ups the pilot uses.** Pi kits on a router (cases A and D) have the most hardware testing. The setup Wi-Fi, MiFi and USB modems (cases B and C) have none. Windows has run on a VM only (S1, S5).

   **Answered:** not known yet, but at least one Pi and one Windows PC, probably more Windows, since a PC needs no new hardware. So S1 stays in, and Windows on a real facility PC moves to "Run" (R-27, the service start timeout).
2. **Web-only facilities.** The stock Android Capture app does not trust the hub's certificate, and Android is on hold (Rollout, "Open decisions"). Pilot facilities must work in the browser: Capture web, Data Entry, eRegisters.

   **Answered:** web only.
3. **An offline edit overwrites a newer value on central** without anyone being told (FINDINGS, session A). Per AGENTS.md, first find out what Android Capture does on its own, then decide whether a pilot accepts it.

   **Not Android only.** The hub pushes the whole stored record, whichever client made the edit: every attribute of a person and every value of an event (adversarial C1). Central imports with `CREATE_AND_UPDATE` and does not check versions. Android Capture on its own does the same.
   - In SDK `3ff01d8` (14 October 2026), `NewTrackerImporterTrackedEntityPostPayloadGenerator` sends every person, enrollment and event that is not `SYNCED`.
   - It sends them with all their attribute values (`queryTrackedEntityAttributeValueToPost`) and all their data values.
   - It uses `CREATE_AND_UPDATE` and makes no check against central's version.

   So a tablet offline for a day overwrites a colleague's newer change on central in the same way. Within the facility the hub adds nothing new: every device saves to the hub's copy, and the later save wins, as online. **Documented** in DEPLOYMENT.md, "Offline edits and newer changes on central", with what staff should do and two options, both unbuilt, if the pilot shows it is common. This is read from the SDK, not run on a tablet without the hub. The hub adds no new risk, but it makes the case more common: more people work offline, for longer.
4. **TPM or not.** Without it, a stolen hub opens to an offline password guess. With it, the code has not run on a board (S4).

   **On Windows** the hub uses the PC's own TPM through TPM Base Services (`devkey/open_windows.go`), with no board to fit. Windows 11 requires TPM 2.0, so a laptop or PC sold with Windows 11 (most since late 2021) has one, built into the processor (Intel PTT or AMD fTPM). Many Windows 10 PCs from 2016 on have one too, sometimes switched off in the firmware. TESTING.md step 10 for a PC with a TPM has not been run yet.

**Build or fix**

5. **CI on push** for Go and the app (T1, T2). **Done** on `pilot-prep`: on a push to any branch but main that changes Go code, test data or the app, so each change runs once. The race detector, the image, Windows and macOS stay by hand. `pnpm lint` failed on `main` (Prettier on `RefusalsPage.tsx`), which is fixed in the same commit.
6. **The Pi image items** of R-26 and R-31. Commit `23f39d2` fixed the `hub-diag` root path and the file modes. **Built** on `pilot-prep`, **not yet run on a Pi**:
   - R-26: the unit has `MemorySwapMax=0`, `ProtectSystem=strict` and writes only its data folder, its program folder and the stick. Polkit allows the `hub` user only the four NetworkManager actions its nmcli calls need. The hub no longer writes NetworkManager's dnsmasq folder: it sets a flag in `/run/hub`, and a root unit writes the captive line.
   - R-31: unattended upgrades include Raspberry Pi's archive (the origin string is to be confirmed on a Pi). The Pi takes central's time zone (`timedatectl`, one polkit action), so the 03:00 reboot is in the facility's night; a central on UTC leaves it at 03:00 UTC. The boot partition is `nofail` and `hub-diag` writes it safely. The stick's udev rule skips the image's own disk. The NO-RTC.md clock save runs every 15 minutes and at shutdown. Load-test mode is only in images built with `LOADTEST=1`.

   Run the 14 checks in `image/README.md` ("Checks on a Pi after an image change") before this counts as done.
7. **"Needs connection" in Capture web** shows as "An unknown error occurred (503)". Staff need to know what it means: fix it in the hub's answer or cover it in training. **Answered:** accepted for the pilot; cover it in training. After a successful pilot, raise it with the Capture team.
8. **Request logging** (C6) before anyone turns `log_requests` on at a facility. **Done** on `pilot-prep`: `hub.log` gets parameter names only (`TestRequestLogKeepsNoValues`).
9. **A release key.** **Done 16 November:** the pair exists, the variable and the environment (tags `v*` only) are set; with the repository public, the environment can also require a reviewer. As it stood on 12 November: The repository has no `RELEASE_PUBLIC_KEY` variable and no `release` environment, and no release has been published (checked 12 November). Without them the release workflow cannot sign, and a hub built without the key never upgrades itself. Make the pair with `hub keygen` on a trusted machine, set the public key as the repository variable and the private key as the secret of the environment `release` (docs/RELEASES.md), and keep an offline copy of the private key: a lost key means re-imaging every hub. Image tests until then use a throwaway key and a release served from a laptop (`image/test-build.sh`, and `image/README.md`, steps 1 and 5).

**Run**

10. **A release from `main`**: v0.1.6 with the review fixes, the image built by the release workflow, and every step of the workflow run by hand first (tags are protected).
11. **A soak on that image on a Pi**, with `hubdrive check`. Much changed on 10 and 11 November: signed instructions, input checks at the boundary, the throttle, the pull overlap.
12. **The live suite on Sierra Leone and Laos** for the R-03 baselines. The adversarial suite was not run in review FABLE5.2.
13. **The Facility Hub app on the demo central** with the new groups, signed instructions and Revoke, as a hub manager who is not a superuser.
14. **The unexplained hardware findings:**
    - the Pi 4 restarted on its own on 30 October;
    - the Pi 4 did no sync cycle on the night of 9 November;
    - the Pi 5 reported "online" under a firewall block without pushing;
    - two USB sticks in at once.

    The journal now stays on disk, so the next occurrence can be read.
15. **Windows on a real facility PC.** Run TESTING.md's Windows steps, including step 10 (the PC's TPM). Check the installer's privilege boundaries (R-27) and the service start timeout after a cut (event 7009, FINDINGS session B).
16. **Two hubs on neighbouring facilities** (TESTING.md phase 6), if the pilot district has neighbours that share patients.
17. **Report the two DHIS2 bugs** from REVIEW-FABLE5.2 section 13, and J1, C2, I8c and J4.

**Prepare** (Rollout, "At national level")

18. The pilot district and its facilities, chosen by the criteria.
19. Central's time zone: DHIS2's dates and, on a Pi, the time of the nightly update reboot follow it.
20. The hub groups named in the app.
21. Retention set.
22. A password policy.
23. The installation card, the kits with their USB sticks, training and the support line.

**From the Pi 4 test of 16 November** (image `0.1.5-rc.1`, Capture 107.3.1 on 2.43.2.1, setup over Wi-Fi). Set-up, offline login after a power cut offline, registration and the push afterwards worked. Open:

24. **Notes on an existing event or enrollment offline.** Capture saves them with `POST /api/tracker/events/{uid}/note` and `…/enrollments/{uid}/note`, which the hub refuses offline ("Could not save event note"). A note typed in the event form when the event is first saved works, since it travels in `POST /api/tracker`. Documented in SUPPORTED.md and docs/user/SETUP-PI.md. A fix would take the note offline as an update of the stored event or enrollment (the store already merges notes by their `note` key, `tracker/model.go`) and push it in the normal tracker import. **Before building it**, check on 2.42 and 2.43 that central treats a note arriving that way as Capture's own request does: a new note added to the existing ones, not replacing them; the note's author and `storedAt`; what central does with a note UID the hub generated; and a note on an event or enrollment central has meanwhile changed or deleted. Per AGENTS.md, also find out what Android Capture does with notes made offline.
25. **Capture's enrollment dashboard offline crashes** ("TypeError: _t is undefined" in `useWidgetColumns`) when nobody at the facility has opened a dashboard online through the hub: the dashboard's page layout comes from `GET /api/dataStore/capture?fields=.`, which only the dashboard asks for, and Capture renders without a layout when that read fails instead of showing the error. Documented in SETUP-PI (open a person's record in each program online). Two follow-ups: the hub could load that entry at each online login whether or not it was seen (one small request per user per day); and report to the Capture team that the dashboard should handle the failed read (it ignores `isError` in `useEnrollmentPageLayout`).
26. **Swap on the Pi 4** (R-26). `hub-diag.txt` shows `memory.swap.max: No such file`: the Raspberry Pi kernel gives the hub's cgroup no swap control, so `MemorySwapMax=0` does not hold. Raspberry Pi OS's `rpi-swap` runs zram swap (905 MB) and a 905 MB swap device on `loop0` labelled `origin:rpi-swap`, which looks like zram's writeback to a file on the disk. zram alone stays in memory; a writeback would put pages of the hub's memory, the data key among them, on the disk unencrypted. Check what `loop0` is backed by and whether writeback is on (`/sys/block/zram0/backing_dev`), on the Pi 5 too; then turn writeback off in the image (or swap off altogether on boards with memory to spare) and add it to `check.sh`.

**Open findings from testing** (moved from FINDINGS.md on 16 November; the sessions' details are in the private notes, `docs/sessions/FINDINGS-SESSIONS.md`). Also from testing, and listed above: an offline edit overwriting a newer value on central (item 3), the stock Android app and the hub's certificate (item 2), "needs connection" shown as an unknown error (item 7), the unexplained hardware findings (item 14) and the Windows service start timeout (item 15).

27. **Refused changes nobody can resolve** (session A). The hub closes "already deleted" refusals itself since 4 November. For the rest, nobody can close an entry Resend cannot fix, and the author is not told. Options: a Discard action in the Facility Hub app, next to Resend; telling the author (FINDINGS.md, "How should rejected data be shown to the user who entered it?").
28. **F4 again on the tablet**: an enrollment dated after central's today, offline, now refused at the save by the hub. Run on Android Capture to see what the app shows (`TestFutureEnrollmentDates` covers the hub).
29. **The Facility Hub app's hub list should show each hub's ID**, as a column that can be shown or hidden: the ID is what the hub's page, `hub.txt` and the issue keys show.
30. **After a push, an author's offline registrations are hidden from her until her own pull lists them** (M2; `TestM2AuthorKeepsOwnRecordsAfterPush`, `HUB_FINDINGS=1`). Proposed fix: mark pushed objects visible to the entry's author, but only where the hub's own visibility check allows it (B1 after the token cache's hour, and a one-time referral in a protected program, C10, would otherwise show her what central no longer does).
31. **No generated values on a user's first login offline**: their programs join the catalog at login, but the pool fills only at the next cycle.
32. **A misleading message on a first Android login offline** (T3): "The URL provided is not a DHIS2 server", because the cached `/api/system/info` is per user. It holds nothing per user (checked on 2.42.7.1), so the hub can serve its copy to any user offline, as for `loginConfig`. Decided 30 October: fix later.
33. **"Online" for a few seconds after a start while the internet is blocked** (session B): the mode probably starts as online until the first probe fails. Check in the code.
34. **Windows: an uninstall that keeps the data keeps the old `hub.exe`**, in the data folder, and the next install of a lower version keeps it and reports success, with the Apps list showing the installer's version. Decide: the program outside the data folder, or the installer saying what it kept and writing that version.
35. **"Last push" on the hub's page moves on whenever the outbox is empty**, without asking central, so it can show a recent time while the hub cannot reach central.
36. **A sync cycle that gets past the push before the clock is set** (F1, after a start without network): untested on hardware. NO-RTC.md predicts minor effects (a token five weeks short, a heartbeat dated at the image's date), corrected by later cycles.
