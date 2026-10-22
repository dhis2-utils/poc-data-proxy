# Answering facility reads from the hub while online (proposed)

Status: proposed, from the conversation of 2026-10-21/22; revised after a review on 2026-10-22 to keep it small (AGENTS.md). Not decided and not built. AGENTS.md asks for a measured problem before a new mechanism: section 7 says what to measure first, and the decision to build follows from those numbers.

## Intent

The hub already answers tracker reads itself while online, but only in one case: after an outage, until its outbox is pushed (`core.PassThrough`, `trackerapi.go`: one decision between central and `serveLocal`). Every other online request goes to central, and the hub learns from the answer (`learn` stores central's version of what users read).

The proposal: an **optional mode** in which, at that same decision, the hub also answers the facility's most common reads when it knows its answer is central's: **working lists, and a person, an enrollment or an event by ID**. Everything else goes to central as now.

Expected gains, to be measured (section 7):
1. **Faster clients**, on two counts: the facility's link (hundreds of milliseconds to seconds on a 4G stick), and central's own time to build a working list, which on a large national instance can be seconds even on good broadband. The hub answers in 10–30 ms.
2. **Less load on central.** Working lists and person pages are most of a facility's reads.
3. **Less bandwidth** on slow or metered links.

Not part of this: searches, aggregate forms, metadata and app files (section 2).

## 1. The mode

- `local_reads: true` in `hub.yaml`; off by default. A country setting in the Facility Hub app can follow once the mode is proven.
- Only reads (`GET`). **Saves go to central at once, as today**, so central's refusals still show in the app while the patient is in front of the staff member.
- Answers the hub gives itself while online carry `X-Hub-Mode: local` (offline ones keep `offline`); the request log already records which requests the hub served.

## 2. The rule: one function, complete or central

One function, `local(request, session)`, decides at the existing decision point. It answers locally only when **all** of these hold; anything else, and any doubt, goes to central exactly as without the mode:

| Condition | Why | Known from |
|---|---|---|
| The mode is on, and the user is the hub's staff (a hub session with a key slot) | others are passed through today (`registerOnline`) | the session |
| The user's access was refreshed less than 15 minutes ago | online, central decides access on every read; locally the hub's copy does (section 4) | the user's "access refreshed" time, kept today |
| The hub's last sync cycle succeeded less than 15 minutes ago | changes made elsewhere arrive with the pulls (section 3) | the cycle's time, kept today |
| The request is a working list of this facility (`orgUnits` = the facility, `ouMode` `SELECTED`), or one person, enrollment or event by ID | the reads that matter most, and the simplest to know complete | the request |
| The program has no retention window, and a person asked for by ID has no retention stub | with retention, central holds records the hub pruned | the retention settings and the hub's own records |

**Not in this version**, each for a reason:
- **Searches.** They are a guarded area (AGENTS.md: SQL and Go that must mean the same), and a local search is complete only for staff whose search scope is the facility and person types without a retention window. Most of the rules a complete search needs, for a gain not yet measured.
- **Aggregate forms.** Few reads per day.
- **Programs with a retention window**, and **date filters** compared with retention cutoffs: parsing Capture's filters would break when Capture changes how it asks. Such programs go to central.
- **Metadata and app files**: cached as today, not answered from the cache while online.

Each can be added later, with its own measurement.

**The window after an outage.** It answers every tracker read locally today, without these rules. Either it uses `local()` too, so both cases follow one rule, or it stays as it is because it is short and ends once the outbox is pushed. To decide (section 8); the spec should not leave the two differing unawares.

## 3. Fresh enough to answer

- **One hub-wide condition:** the last sync cycle succeeded less than `local_reads_max_age` ago (default 15 minutes). No per-program or per-group bookkeeping.
- **The user's first pull** must be done: a staff member who has just logged in for the first time has nothing on the hub yet (their first pull starts at that login). Known from the pulls.
- **Saves made online.** A save goes to central, and the hub's copy of the record lacks central's own fields (timestamps, `createdBy`, `updatedBy`) until the next pull. Instead of marking such records, the hub **reads the saved people back from central once, as the user who saved**, when central confirms the save (`storePersisted`), and stores central's version as it does for any online read. One extra request per save; saves are few next to reads.
- **Unsent saves** in the outbox do not block a local answer: the hub's copy is then newer than central's.

The sync interval stays as it is (15 minutes). Whether it should be shorter with the mode on is a separate question, answered only if the measurements show staleness matters (section 7).

## 4. Access

Online today, central decides on every read what each user may see. With a local answer, the hub's copy of the user's access decides; it is refreshed hourly (`access_refresh_interval`) and at each login.

**The rule:** when a user's access is older than 15 minutes, the request goes to central, and the hub refreshes that user's access in the background; their next requests are answered locally again. That is the whole mechanism:
- no periodic job and no notion of "active users": a user who keeps working costs one forwarded request every 15 minutes, and nobody costs anything outside working hours;
- Android tablets syncing in the background are no special case;
- an access change (a user moved away, a programme's sharing changed) shows within 15 minutes of the user's last forwarded request.

## 5. What stays the same

- Offline behaviour, the outbox, pushes.
- Saves, online and offline.
- What the hub pulls: still each user's own view, with their own token.
- Users who are not the hub's staff: always central.

## 6. Risks

- **Changes made outside the hub** (a referral, a lab result from an integration, a correction on central, a tablet syncing straight to central) show at the next pull, up to about 15 minutes late. Today they show at once online. Section 7 asks the users whether that is acceptable.
- **Access changes** show within 15 minutes instead of at once.
- **A wrong completeness check** would show a list missing records. The rule forwards on any doubt, and the oracle tests compare every local answer with central's.

## 7. Measure first

Before building, on the demo central and a test hub:
1. **Time:** the same working list and person page through the hub (offline answers stand in for local ones) and straight from central, over a typical facility uplink (a 4G stick) and over good broadband. Central's own time to answer, apart from the link, on the demo central and, if possible, on a large national instance.
2. **Share:** a working session through the hub (`log_requests`): the share of requests that this version's rules would answer. Capture keeps much in the browser already, so the share is to be counted, not assumed.
3. **Load on central:** requests per facility per day, with and without that share.
4. **Staleness:** ask staff and the national team which workflows need changes made elsewhere within minutes (referrals, lab results). If some do, the mode stays off where they run, or the pull interval is looked at (and measured) then.

Build if local answers are clearly faster and a large share of requests qualify. If the gain shows only on slow links, the mode stays optional and per hub.

## 8. Tests

- **Oracles in this mode:** the live suite (`internal/live`) with `local_reads` on; every read the hub answers itself compared with central's answer for the same user, online, against central's current state.
- **The rule:** a table test of `local()`, one case per reason to forward: another org unit or mode, a program with retention, a person with a stub, stale access, a stale cycle, a user without a first pull, a user who is not staff, the mode off.
- **Fallback:** e2e tests that a local answer never reaches central and a forwarded one does (the fake central's request counts).
- **Saves read back:** an e2e test that a save made online is followed by one read of the saved people, and that the local answer after it carries central's fields.
- **Guards** for the areas this touches: shared listings (the twin users); `TestQueryPlansUseIndexes` for the working lists.

## 9. Open questions

- The window after an outage: `local()` there too, or as today (section 2).
- Whether the mode becomes a country setting in the Facility Hub app, per hub or for all.
- Whether the user should see that an answer came from the hub (Capture shows "Online" either way).

## Size

One function at the existing decision, one read-back after online saves, the header, the setting, and the tests. No new stored state: the times it reads are kept today. Roughly a couple of hundred lines besides the tests.
