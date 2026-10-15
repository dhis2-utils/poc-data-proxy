# Aggregate data entry through the hub

Status: implemented 2026-10-15 (plan: docs/superpowers/plans/2026-10-15-aggregate-data-entry.md; results in docs/FINDINGS.md, "Aggregate Data Entry through the hub"). Event programs (without registration) get a spec of their own.

## Goal

Staff at a facility keep entering aggregate data (monthly and weekly reports) in the Aggregate Data Entry app while the uplink is down, on any device at the facility, including after the browser was closed. Every value reaches central under the user who entered it. Nothing is lost silently: a value central refuses later is kept and shown.

## What the spike found (2026-10-15, DHIS2 2.42, Sierra Leone, app release 102.1)

Central, probed directly:

- Validation rules are not checked when a value is saved (`POST dataValues` saved a violating value, 201) and **not when a form is completed**, even with `validCompleteOnly`: `dataEntry/dataSetCompletion` answered 200 with a violation. Only the app checks them, online, before completing; offline it skips the check and queues the completion.
- Compulsory fields are checked on completion: 409 "All compulsory data element operands need to be filled".
- A value in a period locked by expiry days or approval: 409 `E2017` "Data set is locked". A data set the user may not write: 409 `E1100`.
- Completing an expired or approved period was accepted (200).
- Bug: for an expiry-locked period that no one has used yet (no row in central's period table), opening the form and saving a value both answer 409 "could not extract ResultSet" instead of locked.

The app through today's hub (Playwright, one shared browser profile):

| Situation | Result |
|---|---|
| Close the browser offline, reopen, log in | Works: the hub logs the user in and replays the visited form |
| Edit offline | The hub answers 503; the app queues the edit in the browser and marks the cell pending. Its header still says "Online" (the hub answers `/api/ping`) |
| A form never opened in this browser | Opens empty and editable; central's values are not shown |
| A second user, offline, same browser | Sees the first user's queued edits |
| Back online, the second user opens the app | The browser sends the first user's queued values in the second user's session: central stores them as the second user's |

The app treats 502/503/504 and network errors as "offline" (queue, retry indefinitely) and every other 4xx as final (roll the cell back, show the error). Its write queue and data cache are per browser, not per user.

Two gaps in the hub, for any app on 2.42+ (fixed first, separately):

1. `/apps/<app>` (the global shell's page for an app) has no file extension, so it is not treated as an app file: a user who never opened the app online through the hub cannot open it offline.
2. The hub caches 200s, 404s and redirects, not 304s. A browser holding another user's copy asks conditionally, central answers 304, and the hub never stores a copy for this user: the app then fails offline ("Failed to fetch user locale"). The hub should drop conditional headers on GETs it would cache.

## Scope

In:

- The Aggregate Data Entry app on 2.42 and 2.43: values, comments, follow-up flags, completion and un-completion, lock state.
- Offline answers only. Online (and while the outbox is pushed after reconnecting) every request passes through to central, as for tracker (`PassThrough`); late entry for old periods works whenever there is a connection.

Out:

- Reports filled from tracker data; analytics; validation rules on the hub.
- File values and min-max edits offline (refused, "needs connection"); audit and history (online only, as in the app).

## Design

### What the hub holds

**Window per data set.** The periods central would still accept writes for, within retention:

- Expiry days and data input periods close periods; approved periods are left out (central refuses both).
- Retention: a table in central's dataStore next to the hub registry, data set → number of periods of its own type, with a default (12). It mirrors the planned retention by programme for tracker. Data sets without a lock (common) are bounded by retention alone.

**Access.** Each user's `dataEntry/metadata`, fetched with the hourly access refresh, lists the data sets the user can enter, with their categories and options. That is central's decision; the hub stores it and matches against it. A value is served to a user only if its data set and attribute option combination are in that user's list. Users with identical lists share one group key, as tracker listings do.

**Pull,** on the sync cycle, per data set, as a user who can read it:

- `dataValueSets` for the facility and the window, incremental by `lastUpdated`, with deletions.
- `completeDataSetRegistrations` (completion state), approval state (data sets with a workflow), lock exceptions, min-max values for the facility.

A facility holds a few thousand values: small next to tracker.

**Store.** A new table of values (data set, period, org unit, attribute option combination, data element, category option combination → value, comment, follow-up, stored by, created, last updated), sealed like tracker payloads, plus completion, approval and lock-exception state per form. Pruning drops synced values that left the window.

### Answering offline

- `GET dataEntry/dataValues` for a form in the window: built from the store in central's shape, with the values (pending local edits on top), `completeStatus`, `minMaxValues` and `lockStatus` (OPEN, LOCKED, APPROVED) from expiry, input periods, approval, lock exceptions and the user's `F_EDIT_EXPIRED`.
- A form outside the window or not pulled yet is answered as locked, so it cannot open blank and editable. **To revisit** once seen in use: "locked" reads as "expired" in the app, and a clearer answer may be needed.
- `dataEntry/metadata`, custom forms and the app's other GETs come from the per-user GET cache, filled by the access refresh rather than only by visits.

### Writes offline

- `POST dataValues` (value, comment or follow-up) and `DELETE dataValues`: the hub checks what central would (the user's access to the data set and attribute options, the value type, the lock state). A write central would refuse is refused with central's error code as a 409, so the app rolls the cell back and shows why. Otherwise it goes into the outbox under the user who made it and the hub answers 201: the app never queues in the browser.
- Consecutive edits of one cell by the same user collapse into one outbox entry.
- `POST dataEntry/dataSetCompletion`:
  - data sets with `validCompleteOnly`: refused offline, "Completing needs a connection" (central does not check the rules, so a queued completion would bypass them);
  - compulsory fields: checked against the store, refused offline with central's message when missing;
  - otherwise queued; un-completion likewise.
  - **To check in the app:** how it shows a refused completion (message, state of the "Mark complete" button), and that the form stays usable.
- File values, min-max edits and anything else the hub cannot answer: "needs connection".

### Pushing

- Per user, as that user (the existing per-user token and outbox). Cells do not block each other; a form's values go before its completion.
- Last write wins, as on central: two users' offline edits of one cell are pushed in the order they were made.
- A refused entry moves to the issues list with central's message; the value is kept. The hub dashboard shows it ("Child Health, September 2026, Penta1 <1y: 31, refused: data set is locked (E2017)"); it is resolved on central (lock exception, or re-entry by a user with `F_EDIT_EXPIRED`) or dismissed.
- The dashboard warns about pending values whose period locks within 3 days.

### Versions

The app calls `/api/42/…` or `/api/43/…`; the hub routes both, and response shapes go in `internal/compat`. The spike found no 2.42/2.43 differences in the app's code. Central's 409 "could not extract ResultSet" for unused expired periods goes in FINDINGS; the hub answers those forms as locked.

## Testing

- **Read oracle:** for every test user and form in the window, the hub's offline answer against central's: MATCH, LEAK or MISSING per value, plus lock and completion state.
- **Write oracle:** offline edits, completions and refusals; after the push, central's values and `storedBy` match what the hub accepted, and every refusal matches central's.
- **Scenarios:** a shared browser (the spike's case, now attributed correctly); expiry during an outage (refused on push, kept in issues); completion refused offline for a `validCompleteOnly` data set; compulsory fields; retention pruning.
- **Real client:** the spike's Playwright driver as `TestAggregateDataEntry` (`HUB_CLIENTS=1`).
- **Profiles:** Sierra Leone (Child Health; a data set with an approval workflow; one with an attribute category combination) and synthetic (a small data set with expiry, `validCompleteOnly` and compulsory fields), on 2.42 and 2.43.
- **Performance:** fakecentral generates aggregate data at HC IV size; pull time and form answers are measured (`internal/perf`).

## Open questions

- The answer for forms outside the window (see above): still "locked"; to revisit once seen in use.
- How the app presents "Completing needs a connection": answered. Through the hub the app first asks for validation, which the hub cannot run offline; where validation is mandatory the app refuses itself ("Couldn't validate the form, please try again later") and sends nothing, so the hub's refusal is a guard for other clients.
- Default retention, and whether the dataStore table is shared with tracker's retention table: default 12 periods, in `facility-hub-config/aggregate`; tracker's retention is not built yet.
