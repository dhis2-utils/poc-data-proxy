# Facility hub: backup and recovery options

Draft, companion to the facility hub POC spec. Options and considerations only; nothing here is decided. The last section notes how each option relates to the hub as built today.

## The problem

A facility is offline for a week. During that week the hub's outbox holds the only complete copy of what staff entered.

- Web users: the browser keeps nothing. The hub has the only copy.
- Android users: the device keeps its records, but marks them as synced once the hub accepts them. If the hub dies, the device will not send them again on its own.

If the SD card or the device fails, or no one who can unlock the hub is available, a week of patient data can be lost or stuck.

## Constraints

1. The data is important. Losing a week of clinical records is not acceptable as a normal failure mode.
2. The setting is not always well organised. Staff turn over, passwords are forgotten, devices get moved or unplugged, and procedures that need someone to remember a step will be skipped. Anything that depends on a key kept safe for years by named people is fragile.
3. District users (who set up and support hubs) usually do not have access to tracker data. Recovery must not give them a way to read patient records they cannot read in DHIS2.

Two principles follow:

- Prefer automatic, redundant copies over procedures that a person has to carry out.
- Separate "unlock the hub so data can flow" from "read the data". The person who unlocks should not need, or get, access to patient records.

## What already helps

The hub already stores a personal access token (PAT) for each user who has logged in, encrypted with the hub's data key. Once the hub is unlocked by anyone, it can push queued data as the original authors, with correct attribution. The person who unlocked it needs no tracker access at all.

This only works while those PATs are valid. PAT expiry must be longer than any plausible offline period plus recovery time (for example one year, not 90 days). If an author's PAT has expired or the user is disabled, their entries wait until they log in online again, or go through a fallback (option F).

## Options

### A. Sync whenever there is any connection

Push the outbox in small batches the moment central is reachable, even on a weak or brief connection. Retry often. This shortens the window in which the hub holds the only copy.

- Covers: most "offline for a week" cases where there is occasional signal.
- Does not cover: no signal at all.
- Cost: low. Mostly sync worker tuning.

### B. SMS fallback for the outbox

DHIS2 already supports compressed tracker sync over SMS (used by the Android app). The hub could send the outbox over SMS through a USB GSM modem when there is mobile signal but no data.

- Covers: areas with 2G voice/SMS coverage but no usable data.
- Does not cover: no signal at all.
- Cost: medium to high. Needs a modem, SIM and airtime per hub, an SMS gateway set up on central, and the SMS compression format in the hub. Worth it only if field data shows many facilities have SMS but no data.

### C. Second copy on the hub (USB mirror)

Every outbox write is also written to a USB stick plugged into the hub, encrypted the same way as the main store.

- Covers: SD card failure.
- Does not cover: theft or loss of the whole device; the stick goes with it.
- Cost: low. One cheap USB stick per hub.
- Fit with the setting: automatic, no one has to remember anything. A dead card is fixed by moving the stick and card (or a new card) into a working hub.

### C2. Disk plus USB, restorable (extends C)

Chosen for the kit (DEPLOYMENT.md, section 1; built 2026-10-17, `internal/stick`, tested in `internal/e2e/stick_test.go`): one USB stick per box, which before set-up carries the hub's setup code and after set-up is this mirror. Its state (present, last copy, failing, full) shows on the hub's page and in the heartbeat, so central sees a hub without a working stick. The facility keeps the stick in the box, and keeps it when a disk is replaced.

The hub keeps a copy on a USB stick in two parts, so the stick is not worn out by constant writes:

- **Every minute, if anything is new:** the offline saves accepted since the last copy, plus changed key slots, tokens, the hub's identity and keys, and sync state. A few kilobytes per save; nothing is written when nobody saves. Copying runs on its own schedule, so a slow or failing stick never delays a client's save.
- **Nightly or weekly:** a full snapshot of the store (the hub can already write a consistent encrypted copy of itself), so a restored hub can work offline at once instead of waiting to pull its cache again from central.

Both stay encrypted as on the disk: the stick is as safe as the disk, opened only with a user's password.

| Failure | What happens | Lost |
|---|---|---|
| Board | Move the disk and the stick to a new board; it boots as the same hub. | Nothing: saves are synced to the disk before the hub confirms them. |
| Disk | New disk with the image; at first boot the hub finds the stick, restores the last snapshot and replays the newer saves, with the same hub ID. | At most the last minute's saves (Android devices still hold theirs). Records central changed since the snapshot return with the next pull. |
| Stick | The hub keeps working and shows a warning on the dashboard and in the heartbeat, so central and the district see it. | Nothing while the disk lives. |

- Health checks: stick missing, unwritable or full, a periodic write-and-read-back failing, or the snapshot older than its schedule.
- As built:
  - **What the stick holds.** The per-minute copy holds the saves still pending, each sealed whole with the data key, so who saved it, its flags and its order cannot be changed on the stick. A save is rewritten when it changes (a corrected aggregate value). It leaves the stick once pushed, or refused and reported.
  - **Key file and snapshot.** The key file is copied whenever it changes, with the highest save number the stick has seen. The snapshot is written beside the previous one and swapped in; after a power cut between the two renames, the previous one (`snapshot.old`) is offered.
  - **A full stick.** A snapshot that does not fit, or fails, leaves nothing behind and is tried again an hour later; the per-minute copies go on.
  - **Mount check:** the hub uses `usb_dir` only when it is a mount point, so a missing stick never fills the hub's own disk.
  - **Restore is explicit.** A new disk starts as a new hub. Its setup page offers each hub copy on the stick, and a staff member of that hub restores it with their own password, checked offline against the copy's key slots. A stick from another box, or a crafted one, therefore does nothing on its own, and a new box never clones a hub that is still running.
  - **What a restore does.** Everything it needs is first copied to the disk, so pulling the stick out does not matter. The hub restarts as the same hub, locked. The first staff login puts back the saves made after the snapshot and marks as sent the ones the stick saw pushed since, so nothing central already has is sent again. Copying to the stick resumes after that.
- The stick: an industrial USB stick or a pSLC card in a USB reader, formatted ext4 (journaled), not exFAT.
- Interactions: keys bound to the board (option I) would not move with the disk, so a moved disk would need a login before pushing; the central registry's replaced-hub check must accept a restored hub with the same ID.
- Tests (conditions phase): remove the stick during copying, fail the disk, restore on a fresh store, and check nothing is missing or duplicated against central.
- Cost: medium. The per-minute copy and the warning first; snapshot and restore at first boot next.

### D. Keep data recoverable on the clients (Android)

Android devices keep a "sent to hub, not yet confirmed by central" state. If the hub disappears, or reports it lost data, the device sends those records again (to a new hub or to central).

- Covers: hub loss, for data entered on Android.
- Does not cover: data entered on the web.
- Cost: an Android SDK change, owned by the Android team. Small in principle, but outside the hub project.

### E. Unlock without reading: an "unlock and sync" role

Members of a configured user group (for example district HMIS officers) can unlock the hub and start a sync or an export. The hub UI gives them only counts and status; it never shows patient data, to them or anyone else. Pushes go out as the original authors (see "What already helps").

- Covers: no registered facility user available, for example after staff turnover.
- Does not cover: the case where the unlocker has never logged in to this hub while it was online (the hub cannot check an unknown password offline).
- Cost: low. One more key slot type and a UI permission.
- Limitation to be honest about: the unlock is still cryptographic. Someone with the card, an unlock password and the right tools could decrypt the data outside the hub software. The protection against district users reading records is the hub software plus physical custody, not cryptography. Whether that is acceptable is a question for the MoH and its data protection rules.

To reduce that gap: give the "unlock and sync" key slot access only to the outbox and the PATs, not to the full cache. The cache can be rebuilt from central; the outbox is what matters. A district officer's slot then exposes at most the unsynced entries, not every patient at the facility.

### F. Per-hub recovery key held on central

At setup, the hub generates a recovery key pair. The private key is stored in the central DHIS2 data store, shared only with central system administrators. The hub keeps only the public key.

The hub writes every outbox entry a second time, encrypted to that public key. These copies are unreadable on the hub, even when it is unlocked. They can be exported to a USB stick or phone at any time by anyone, carried to a place with internet, and imported by a central administrator with a recovery tool.

- Covers: dead hub with no one able to unlock it; long offline periods where someone can travel; card or USB copies that leave the facility.
- Does not cover: compromise of the central server, but then all patient data is exposed anyway.
- Cost: medium. Asymmetric encryption on the hub is small. The recovery tool and import procedure on central are the larger part.
- Fit with the constraints:
  - No national master key. Each key opens one facility's unsynced data only.
  - The keys live where the patient data already lives, guarded by the people who already guard it. No new safe, no new key holders.
  - District users carry bundles they cannot read.
- Attribution: the recovery tool pushes as the original authors where their PATs are still valid (the bundle can carry the PATs, encrypted to the same key). Otherwise it imports under a dedicated recovery account, sets `storedBy` to the original user, and logs that the data came through recovery.

### G. Make the risk visible at the facility

A physical indicator on the hub (an LED, or a small display) and the hub status page show how many records are waiting and for how long. After a set time (for example three days), the status turns to a warning that tells staff what to do: plug in the USB stick, call the district, or take an export bundle to the district office.

- Covers: the "nobody noticed" failure, which is likely in a disorganised setting.
- Cost: low.

### H. Instruction card with the hub

A printed card attached to the device: what the lights mean, who to call, and the three steps for an export bundle. Not technology, but in this setting it may matter as much as the rest.

### I. Push after a reboot without anyone logging in

(Added from the review against the code; see "Notes against the current hub".) Keep what is needed to push (the outbox entries and the users' tokens) under a key available at boot without a person, for example derived from a device-unique key; Raspberry Pi 4 and 5 are believed to have one in OTP memory, to be checked. The cache of central's data stays under the data key, as now.

- Covers: power cuts and restarts, the most common failure, which today stop all pushing until a known user logs in.
- Does not cover: a dead device or card (C and F do).
- Cost: medium. It needs the outbox and tokens split from the rest of the data file (see E).
- Trade-off: stealing the running device exposes the unsynced entries and tokens, not the cache; tokens can be revoked on central.
- On a Windows PC (DEPLOYMENT.md, options under consideration), DPAPI or the TPM holds such a key for a service at boot, usable only on that machine.

## A possible combination

| Layer | Option | Why |
|---|---|---|
| Reduce the window | A, and G | Cheap; most offline periods have some signal; staff know when something is wrong. |
| Survive card failure | C2 (or C) | Automatic, no human step; a new disk restores from the stick. |
| Survive power cuts and restarts | I | No one has to log in for data to flow. |
| Survive staff turnover | E, outbox-only | District can unlock and sync without reading the cache. |
| Survive total loss of unlock ability | F | No new key holders; bundles can be carried by anyone. |
| Android | D | Ask the Android team; outside the hub. |
| Later, if field data supports it | B | Only where SMS works and data does not. |

## Open questions

- Is the "unlock and sync" role acceptable to the MoH, given that the protection against reading is software and physical custody rather than cryptography?
- How long can PATs live under the country's security policy? This sets how long data can wait for recovery and still keep correct attribution.
- Can the data store hold the per-hub recovery keys with sharing limited to system administrators, and is that acceptable to the central server's administrators?
- Who runs the recovery tool on central, and how often would they need to?
- Is attribution through a recovery account, with `storedBy` set to the author, acceptable for clinical records in this country?
- What share of facilities have SMS coverage but no usable data? This decides whether option B is worth building.
- Is a key available at boot without a person (option I) acceptable, given that it trades theft exposure of unsynced entries for automatic recovery?

## Notes against the current hub

How the options relate to the hub as built (October 2026). Considerations, not decisions.

- **A restart locks the hub until a known user logs in, through any client.** The store is locked at boot (spec 9.1), and the sync worker pushes only while it is unlocked (`internal/sync/sync.go`). Any login unlocks it: Capture web, the hub UI, or Android, whose background sync sends the stored credentials with every request, so a facility with Android devices set to sync periodically recovers without anyone doing anything. The user must have logged in to this hub online before (that is how they get a key slot), and a password changed on central while the hub was locked no longer opens their old slot. The gap is real mainly at web-only facilities, overnight, or before work starts. Option I closes it; a UPS (DEPLOYMENT.md, options under consideration) makes it rarer.
- **A is largely in place.** The hub probes central every 15 seconds, starts a sync when the connection returns, and runs a full cycle every 15 minutes (±10 %). Left to do: push before pulling, so that a short window gets the outbox out first; small push batches; resuming partway through the outbox.
- **C combines well with F.** A mirror encrypted like the main store needs the key file and a user's password to open, so it shares the hub's unlock problem. A mirror written as F's copies, encrypted to the per-hub public key, is unreadable at the facility, harmless if the stick is lost, and recoverable centrally: one mechanism, kept in two places.
- **E partly exists, but without the cryptographic limit.** Any user with a key slot can unlock the hub today, including a district setup user without tracker access; the hub then pushes as the original authors, and that user sees offline only what their DHIS2 access allows. The data file is encrypted under one data key, cache included, so an outbox-only key slot means first splitting the outbox and the tokens into a separately keyed file. That split also enables option I.
- **F: points for the recovery tool.** Records carry their own UIDs, so re-importing is mostly idempotent. Two cases need care: an update replayed after the record was edited on central (it would overwrite the newer version), and dependency order (the outbox already records which entry depends on which). The bundle holds every entry central has not confirmed; the hub drops the copies once central confirms them. The encryption itself is standard public-key cryptography and small on the hub.
- **G, locally and centrally.** The status page already shows pending counts; the age of the oldest pending entry and a warning after a set number of days would be added. Central already receives heartbeats with outbox counts, so it could list hubs "last heard from N days ago, M entries pending at last contact" for district staff, which catches the hubs nobody at the facility notices.
- **Token lifetime.** The default is 90 days (`pat_expiry_days`), and each user's token is renewed while the hub is online and it has under a week left, so expiry only matters after a long offline period plus a slow recovery. The lifetime is a configuration value, within the country's policy.
- **Related, already done:** both store files sync on every commit, so a save the hub has confirmed survives a power cut (it previously could lose the last few).
