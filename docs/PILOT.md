# Before a pilot

What remains before a first pilot, as of 12 November 2026. It started as the last section of the KISS review of that day (`docs/reviews/KISS-2026-11-12.md`, in the private notes), which keeps the findings; this list is the one kept up to date. Item names such as S1 or C6 are that review's; R-26 and the like are REVIEW-FABLE5.2's.

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
9. **A release key.** The repository has no `RELEASE_PUBLIC_KEY` variable and no `release` environment, and no release has been published (checked 12 November). Without them the release workflow cannot sign, and a hub built without the key never upgrades itself. Make the pair with `hub keygen` on a trusted machine, set the public key as the repository variable and the private key as the secret of the environment `release` with required reviewers (README, "Releases"), and keep an offline copy of the private key: a lost key means re-imaging every hub. Image tests until then use a throwaway key and a release served from a laptop (`image/test-build.sh`, and `image/README.md`, steps 1 and 5).

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
