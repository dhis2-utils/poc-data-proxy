# Refused changes: who closes them, and how

Proposal of 4 November 2026, reviewed and decided by Olav the same day ("Decided", at the end). Base: `main` at dad9c4d. Sources: physical session A (3 November, FINDINGS.md on branch `session-a`, commit 2f7fbc4, "Open (from physical session A…)"); FINDINGS.md, "Refused changes" and "Tokens outlive their user"; ADVERSARIAL-TESTING.md (A, B, C, J and K scenarios); the code named below. Nothing here is built.

## The question

Olav asked: "how can this kind of 'already deleted' issues actually be solved by anyone? How and who?" In session A two offline edits were refused with E1082 (the event was deleted on central meanwhile). The hub kept them and reported them, but nobody could close them. The Facility Hub app offers only Resend, which fails again for E1082. The same session showed two related gaps: a refused enrollment (F4) that leaves the tablet and central different, and an offline edit that overwrote a newer value on central with no refusal at all.

## What the hub does today

- Push (`internal/sync/push.go`, `pushEntry`). One `POST /api/tracker` per outbox entry, as the entry's author, with `atomicMode=OBJECT`. A refused object is marked `rejected` and gets an issue (`reject`, `store.AddIssue`). Objects under a refused parent are not sent; they are refused as "blocked by <parent>".
- Report (`ReportIssues`). Each issue goes to `facility-hub-issues/<hub ID>-<n>` with the hub's token. It carries the error codes, kind, object UID and author UID, with entered values masked.
- Close (`clearResolvedIssues`, `store.ResolvedIssues`). An issue closes in two ways. A later version of the object is sent and accepted, or central's version changes more than a second after the refusal (`core.changedSinceRefused`). The hub then deletes the key on central.
- Resend (`applyRetries`, `store.Requeue`). A hub manager presses Resend on a group (kind, programme, error code) on the Facility Hub app's Refusals page. Each hub sends that group again, with what it blocked, once. For `NO_LOGIN` the manager can name another user to send as.
- Reset. The hub's own page has RESET AND DISCARD, which throws away every refused change together with the whole hub (G6).

The issues are shown in three places:

| Place | Who can open it | What it shows |
|---|---|---|
| Facility Hub app, Refusals page | Hub managers (`ManagersOnly`) | Counts by kind, programme and code, across hubs; Resend |
| Facility Hub app, a hub's page | Anyone who sees the hub in the app | The 200 newest issues: when, kind, object UID, codes, masked message |
| The hub's own page (`/hub/ui`) | Staff assigned to the facility (`UI.admin`) | The 100 newest issues, with the author's username |

Nothing closes an E1082. The object stays `rejected`, and central's tombstone is older than the refusal, so `changedSinceRefused` never fires. Meanwhile the hub keeps serving the deleted event offline (K3).

## What the clients can show

Both Capture apps show a sync error only when the server refuses their own request.

- Capture web saves each change straight away (`POST /api/tracker?async=false`). When the hub is online it passes central's 409 back, and Capture shows it. When the hub is offline it shows only the hub's own refusals ("needs connection", E1039 and the like; I1c). A refusal at push comes later, so Capture never sees it.
- Android Capture keeps a conflict only from the import report of its own upload (SDK docs, `docs/content/developer/workflow.md`, "Tracker conflicts"). When the hub confirms an upload, Android marks the records `SYNCED`. Nothing it downloads later carries the refusal.

So no change to the hub can bring a later refusal into either app. The author can learn of it only from a person, from a page outside the app, or from a later change to the record itself. One case already works this way: a record deleted on central disappears from both apps once they read it online (session A).

## Refusals by who can fix them

The codes are the ones the hub has met, in the tests or in the field. Their texts are from DHIS2 2.43.2 (`ValidationCode.java`).

| Who fixes it | Codes | Met in |
|---|---|---|
| The author, by editing again | E1302, E1007, E1125 (a value central does not accept); E1303 (compulsory value missing at completion); E1064 (unique value taken); E1020, E1021 (future enrollment or incident date); E1015, E1016, E1039 (the record already exists on central) | I8c, A1, A2, A7, C5, F4, soak M1, A4, A8 |
| A supervisor or admin on central | E1000, E1102 (org unit or ownership); E1091, E1095, E1099, E1104 (sharing); E1010, E1013, E1069, E1029, E1041 (programme or stage not available); E1057 (option ended); E1047 (expired period); E1083 (authority); E1300, E1301, E1305, E1306 (programme rules or stage changed); `NO_LOGIN`; a plain 500 for a day; aggregate E2017, E2022, E2025, E2031, E1100 | B1, C3, A9, K2, K4, B6, B11, K1, A5, A10, B5, A3, B2, B8, H8, J6, J7 |
| Nobody: the target is gone | E1082, E1113, E1114, E4017 (event, enrollment, person or relationship already deleted) | session A, K3, C2 |

An object refused as "blocked by", or with E5000, belongs to its parent's group and closes with it.

### Fixed by the author

These need the author, because only the author knows the right value. The supervisor sees them first and passes them on.

- Who sees it. The district or facility supervisor sees the change on the hub's page in the Facility Hub app. Today that page shows the object UID but not the author, although the issue carries the author's UID. Add the author's username, and a link that opens the record in Capture on central. Staff can also see their own refusals on the hub's own page, which already names the author.
- What the author sees when they open the record. Offline, the hub serves its own copy, with the refused change in it. Online, once nothing waits to be sent, the hub passes reads to central (`core.PassThrough`), so the author sees central's version without the change. Android shows its local copy until a download replaces it. The issue masks the values, so neither the supervisor nor the app can tell the author what they had entered. The hub's own page therefore shows signed-in staff their own refused changes with the values (item 4 below): the values are on that hub already, and only its own staff can open the page.
- What closes it. The author opens the record and saves a corrected version, through the hub or on central. The hub then closes the issue itself. A duplicate (E1015, E1016, E1039, or E1064 when it is the same person) closes differently. The author enters what is missing on the existing record, and a hub manager discards the refused one.
- E1020 and E1021 should no longer reach the outbox: the hub refuses them offline, as central does (see "The F4 case").

The author cannot be told inside Capture (above). Whether to reach the author another way is a decision for Olav (below).

### Fixed on central

Here the change was right when it was made, and central changed underneath it: access, sharing, an option, a rule, a period.

- Who sees it. Hub managers, on the Refusals page, grouped across hubs. This is built.
- What closes it. Often nothing on central should be undone: the change on central was deliberate (a new programme rule, a closed period, a user moved), and the waiting data does not fit it. A manager then needs Discard (item 3). Resend, which is built, serves the cases where central was wrong and is put right.
- Preventing it. There is no clever way around a deliberate change that waiting data does not fit. What helps is timing the change: before a breaking change, the central team sees which hubs have not been online since a given date, and so probably hold an outbox, and waits or contacts them. The Facility Hub app has the parts: each hub's last report time, its outbox count at that report (`pendingOutbox`) and a fixed "Silent" flag after 48 hours (`flags.ts`). Missing is a filter on the hub list by "not online since <date>" (item 5).
- Later, not for the first release: filter that list by the programmes and data sets assigned to each hub's facility, so an admin changing the HIV programme sees how many of its hubs have not been online recently.

### Fixed by nobody

Central has deleted the target, so the change has nothing left to apply to. This is the session A case. Online, DHIS2 behaves the same: an edit sent after a deletion is refused, and the deletion stands.

- Who sees it. Nobody needs to act. The hub log records each one, and the refused outbox entry is kept for 30 days, like a sent entry (`store.SentOutboxKept`).
- Can it be reopened? No. Checked on 2.42.7.1 and 2.43.2.1 (synthetic, as the facility nurse, 4 November): after a delete, sending the same event again is refused with E1082 under CREATE_AND_UPDATE, CREATE and UPDATE, and with `"deleted": false`; a person sent again gets E1114. The tracker API has no undelete or restore endpoint (404). The event stays listed with `includeDeleted=true` as `deleted: true`. So a deletion made by mistake can only be undone by registering the record again under a new UID; the refused outbox entry keeps the offline change for 30 days. Re-creating records from it is not proposed. Not tried as a superuser.
- What closes it. The hub, at the refusal. Central has said that the object is deleted. The hub turns its copy into a tombstone, which closes the issue and removes the key on central. Whatever the change blocked closes with it. Offline the record then leaves the lists, and Android's download marks it deleted, as for any deletion on central.

## The F4 case

The tablet's date was a day ahead of central's. The hub confirmed an offline registration. At push, central created the person but refused the enrollment (E1020) and its two events (E5000). The tablet still shows the person enrolled with both events. Central has the person only. Online through the hub, central refused the same record at once and Android showed the error. Offline, the hub turned that error into a silent difference.

The soak (M1, 2 November) met the same code 398 times, from a central in UTC and clients in UTC+1, between midnight and 1 a.m.

Waiting is wrong for these. A mistyped year (2036 for 2026) would sit in the outbox for ten years. And a programme set up to refuse future dates refuses them because a future registration is doubtful: the hub should not confirm what the programme forbids and send it later.

What the clients do without the hub (measured 4 November on the demo central, 2.43.2; screenshots in `windows/out/android-baseline/`):
- **A mistyped year.** Both clients refuse it at entry, judged by the device's own date: Android 3.5.0 "Date out of range (01/01/2008 - 04/11/2026)", Capture web "A date in the future is not allowed", and Capture web does not save. A typo never reaches the hub.
- **The device's clock a day ahead.** Both accept the date at entry, as it is the device's today. Capture web saves online at once, central refuses, and Capture web shows "Error saving enrollment"; nothing is created. Android offline keeps the record; at sync central creates the person without the enrollment and refuses the enrollment and its events (E1020, E5000). Android shows "Sync error" with central's message, marks the person "Sync Error" with "Retry sync", and resends at every sync: a full sync did not remove it. It would pass once central's date reaches the enrollment date, with the wrong date stored.

Decided (4 November, reversing the 3 November "no hub future-date check", and confirmed after the measurements above): **the hub refuses future dates offline, as central does.** Through the hub the user then gets what they get online: the refusal at entry, by central's date. The check matters only for a device whose clock or time zone is ahead of central's; the clients catch typos.
- Central's E1020 and E1021 follow two programme settings, `selectEnrollmentDatesInFuture` and `selectIncidentDatesInFuture`. The hub reads them into its catalog with the programme, and refuses an offline enrollment whose enrollment or incident date is after central's today, with central's codes and messages.
- "Today" is central's date: the hub's clock in central's time zone (`MetaServerTimeZone`). This is the same kind of check as A10's programme expiry (E1047), and it goes next to it.
- Clients then show the error at entry, as they do online (Android showed E1020 at once in session A). The hub no longer turns a visible error into a silent difference.
- Guard: a scenario on the fake (which then needs the two settings) and a live one with a date one day ahead; then F4 again on the tablet.

A wrong clock on the hub moves this check too. A hub whose clock is behind lets a future date through, and central refuses it at push as today; a hub whose clock is ahead refuses a correct date. The RTC that DEPLOYMENT.md requires keeps this rare.

**A warning when a device's clock is off.** Android sends `createdAtClient` and `updatedAtClient` with each record. When they differ from the hub's clock in central's time zone by more than an hour, the hub's page shows a warning naming the user, and the heartbeat counts it. This catches a wrong tablet clock and a time zone mismatch alike. Capture web sends no client time, so this covers Android only. Guard: an `internal/e2e` test with a device an hour and a day off.

**Setting the hub's clock from a client** (a Pi 4 without an RTC that starts offline) is possible, from Android's client times or a browser's. But a wrong tablet clock, as in F4, would then set the hub wrong. Not proposed: session B's F1 measures what a hub with an old clock actually gets wrong, then decide.

A gap read from the code, not yet tested. When the author fixes a refused parent by editing it again, the parent is accepted, but the events it blocked stay refused until someone presses Resend. Nothing requeues them (`pushEntry` sends only the new entry). Write the test first (`TestFixedParentReleasesBlocked`). Build nothing until it shows the gap.

## Silent overwrites

In session A, `needy2` set a weight offline at 13:59:20. Someone changed it on central at 14:00:41. At the push (14:06:43) central took the hub's older value. Nobody was told. C1 found the same in the sandbox for attributes and data values. Central's change log keeps both values, so nothing is lost from the record.

### What detecting it would cost

The push sends one `POST` per outbox entry, one at a time (`Worker.Push`). A hub would detect an overwrite by reading central's `updatedAt` for each object it is about to update and comparing it with the version the edit started from. New records need no read.

- Requests. Done per entry, it is one extra `GET` for each entry that updates an existing record. In `TestPushOverSlowUplink`'s mix (one entry in three an update) that is a third more requests. With updates only, it is twice as many. On 4G each request costs about the link's 100 ms, so the busy HC IV's 3 000 saves a day, 7 minutes to push after a day offline, would take up to about 14 minutes. Done per push run, grouped by author and by kind (`trackedEntities`, `enrollments`, `events`, with lists of UIDs), it is 1 to 3 `GET`s per author per run, plus one for every few hundred UIDs. Session A's push would have been one `POST` and one `GET`. The week's drain of 21 000 entries would have needed some tens of `GET`s.
- Other costs. The hub would have to record, for each offline edit, the `updatedAt` it started from, which is new data in the store. A change on central between the read and the `POST` would still go unseen. A person's `updatedAt` also moves when one of their events changes or is deleted (FINDINGS.md, "Deletions on central"), so a check on people would also flag changes that overwrote nothing. Finally, every detected overwrite needs a person to compare two versions. The hub cannot show central's value in the issue, because values are masked.

### DHIS2 without the hub

DHIS2's own clients behave the same way.

- Android Capture offline uploads later with `CREATE_AND_UPDATE`, and the later write wins. A download never overwrites records with pending changes (SDK docs, `docs/content/dev/synchronization.md`, "Overwriting existing data").
- Central does not check versions. The 2.43.2 importer stores `lastUpdatedAtClient` (`TrackerObjectsMapper`), but no validator compares update times.
- In Capture web, two users with the same record open overwrite each other in the same way. Aggregate values do the same (J3).

The hub adds nothing for Android users. For Capture web users it makes the window longer: from the minutes a form stays open to the length of an outage.

### Recommendation

Do not detect overwrites in the first release. DEPLOYMENT.md and RECOVERY.md should say this: an offline edit wins over a newer change on central, as on Android without the hub, and central's change log keeps the earlier value. In the pilot, count how often it happens from central's change logs: the same value changed by two users within an outage, one of the changes through a hub. Decide after the pilot.

## Proposed for the first release

Each item answers a session A observation. Each comes with the test that guards it.

1. Close moot refusals on the hub. Observation: two E1082 that nobody could close. On E1082, E1113, E1114 or E4017 the hub makes its copy a tombstone and closes the issue. What the change blocked closes too. Guard: `TestMootRefusalClosesItself` in `internal/e2e` (issue gone from `facility-hub-issues`, the record out of offline lists, Android's download with `deleted: true`). K3 changes from "refused and kept" to "closed".
2. Refuse future dates offline, and warn about device clocks. Observation: F4, and 398 in the soak. See "The F4 case" for the check, the warning and their guards.
3. Discard in the Facility Hub app. Observation: no manager can close a refusal that no fix will pass and that no rule closes. There is a Discard next to Resend for each group, and one for each change on the hub's page. It travels as resends do (`facility-hub-admin/retries`, with an action field, applied once, listed under "Resends asked for" with who and when). On the hub:
   - If central has the record, the hub reads central's version by UID, as the author, and keeps that.
   - If central does not have it (a refused registration), the hub keeps a tombstone, so Android's next download drops it.
   - The refused entry is kept for 30 days, then pruned.
   - A refused registration (E1020 left over from a wrong hub clock, a duplicate) can then be entered again by its author.

   Hub managers only, as for Resend. Guards: `TestDiscardGroup` and `TestDiscardRefusedRegistration` in `internal/e2e`, and a jest test for the app.
4. Show staff their own refused changes on the hub's page (`/hub/ui`), with the values they entered, the record and what closes it. Observation: the author never learned of either refusal, and the issue masks what they had entered. Guard: a `internal/ui` test that a user sees their own refusals with values and nobody else's.
5. Filter the Facility Hub app's hub list by "not online since <date>", showing each hub's outbox count at its last report. Observation: Olav's review: before a breaking change on central, the team needs to see which hubs probably hold an outbox. Guard: a jest test for the filter.
6. Name the author on the hub's page in the app. Observation: the author never learned of either refusal. The supervisor needs to know whom to tell. Add the username to the issue payload (`ReportIssues`, next to the user UID), and a link to the record in Capture. The app also shows a short "what closes it" per code, from the table above. Guards: an assertion in `TestRefusedCountsClearWhenFixed` that the username is in the issue, and a jest test for the code map.

Before item 1, see what Android does without the hub (below, "How to work").

Items 1, 4, 5 and 6 need no store change. Item 2 adds two programme settings to the catalog; check whether its stored form needs a migration. Item 3 marks outbox entries as discarded. That is a new value in the existing `state` column, so no migration and no `HUB_PERF_MIGRATION` timing is needed.

## Decided (Olav, 4 November)

- **How the author learns of a refusal:** the hub's own page shows staff their own refused changes with values (item 4), and the Facility Hub app names the author for the supervisor (item 6). No DHIS2 messages for now; the heartbeat's counts by code will show in the pilot whether more is needed. Asking the Capture teams to show the hub's refusals stays the long-term answer.
- **Future dates:** refused offline by the hub, as central does (item 2). The plan's earlier automatic resend of E1020 and E1021 is dropped: a mistyped year would have waited for years.
- **Silent overwrites:** not detected in the first release. Even if the hub saw one, a client connected to it could do nothing with it, and DHIS2's own clients behave the same way.
- **Discard:** wanted. A deliberate change on central that waiting data does not fit has no other way out.
- **Undelete:** not possible in DHIS2 (checked above), so a moot refusal closes as a tombstone.

## How to work

When an offline sync question has no clear answer, look first at how DHIS2 Android Capture handles it on its own, without the hub, and measure it on the tablet where that is cheap. Give what you find to Olav to review before building; Android is a reference, not the answer (AGENTS.md, 4 November). For this plan:
- Before item 1: on the tablet against central directly, edit an event offline, delete it on central, sync. Done 4 November: Android shows "Sync error" with central's message ("Event: `…` is already deleted and cannot be modified."), marks the event "Sync Error" with "Retry sync", and drops the event at the next full sync; the offline edit is lost. Item 1 ends the same way on the device, without the message.
- Before item 2: done 4 November, Android and Capture web ("The F4 case").
- Before item 4: done 4 November. Android's sync error lists one line per record, "<date> | <org unit>", with central's message as it is, and marks records "Sync Error" with "Retry sync". The hub's page uses the same words. Settings' "Open sync error log" showed only metadata errors in the first 25 screens.

## Not proposed

- Automatic closing for codes other than the four "already deleted" ones. Each of the others has a fix, by the author or on central.
- Showing refusals in Capture, or rewriting what the hub serves to Capture so that it shows them.
- Merging an offline edit with a newer change on central.
- A heartbeat counter of moot refusals that closed themselves. The hub log is enough until someone needs the number.
