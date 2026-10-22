# Answering facility reads from the hub while online (proposed)

Status: proposed, from the conversation of 2026-10-21/22. Not decided and not built. AGENTS.md asks for a measured problem before a new mechanism: section 8 says what to measure first, and the decision to build follows from those numbers.

## Intent

Today the hub answers clients itself only offline, and online while saves from an outage are still waiting (`core.PassThrough`). Every other online request goes to central, and the hub learns from the answer.

The proposal: an **optional mode** in which the hub also answers, while online, the reads it can answer *completely* from what it holds: the facility's working lists, a person's page, aggregate forms, and searches by staff whose search scope is the facility. Everything else goes to central as now.

Expected gains, to be measured (section 8):
1. **Faster clients.** A working list over the LAN instead of across the facility's uplink: from hundreds of milliseconds to the hub's 10–30 ms on a slow link.
2. **Less load on central.** Working lists and person pages are most of a facility's reads.
3. **Less bandwidth** on slow or metered links.

App files are not part of this: browsers already cache them per device, and Capture keeps its metadata in its own browser storage.

## 1. The mode

- `local_reads: true` in `hub.yaml`; off by default. A country setting in the Facility Hub app can follow once the mode is proven.
- Only reads (`GET`). **Saves go to central at once, as today**, so central's refusals still show in the app while the patient is in front of the staff member. Saving into the outbox first and pushing later would hide refusals until later; that is not part of this proposal.
- Answers the hub gives itself carry a header (`X-Hub-Mode: local`, next to today's `offline`), for the request log, the heartbeat's counts and debugging.

## 2. The rule: complete, or central

The hub answers a read itself only when it knows its answer is the one central would give. When in doubt, the request goes to central, exactly as without the mode. Every condition below is a reason to forward, never to answer partly.

### What can be answered locally

| Read | Answered by the hub when |
|---|---|
| Working list (tracker or event program) | scoped to the hub's facility (`orgUnits` = the facility, mode `SELECTED`), and its date filters stay inside the program's retention window, or it lists active enrollments only (never pruned) |
| A person's page (tracked entity with enrollments and events) | the hub holds the person in full: no enrollment of theirs is a retention stub |
| An event or enrollment by ID | held in full |
| Search, by name or other attributes | the user's **search scope** is the facility (or inside it), and the person type has **no retention window** (with one, people whose records were all pruned are gone from the hub but not from central) |
| Search by a unique attribute | as above, or: a value unique across the system with a local match (a unique value belongs to one person); no local match goes to central |
| Aggregate form (data values, completeness) | the facility, a period the hub holds |

### What stays on central

- searches by staff whose search scope is wider than the facility (district or national, given so they find patients registered elsewhere);
- anything outside the facility, or reaching past a retention window;
- analytics, dashboards' charts, program indicators, notifications, and everything else central computes;
- metadata and app files (cached as today; not answered from the cache while online).

### Retention

The check uses the same rules pruning uses (`core/prune.go`):
- **event programs:** events older than the program's cutoff are deleted, so a list or search that reaches before the cutoff goes to central;
- **tracker programs:** closed enrollments whose last change is older than the window become stubs, their events deleted. A person with a stub is not held in full; lists of closed enrollments with dates before the cutoff go to central;
- **person types with a window:** people with nothing left in full are deleted, so searches for that type go to central.

Different windows per program and person type only change the cutoff each check reads. The cutoff comes from `Retention().Cutoff`, as pruning does.

## 3. Fresh enough to answer

Three conditions, per user and program, checked on every local answer:

1. **First pull done.** The user's access group has finished its first full pull of the program. Pulls are already tracked per program and access group.
2. **Pulled recently.** The group's last successful pull of the program is within `local_reads_max_age` (default 15 minutes).
3. **Access refreshed recently.** The user's access was refreshed within the same window (section 4).

Plus one per record:

4. **Saved online, not yet pulled back.** A save sent through the hub online is stored as the client sent it; central's own fields (its timestamps, `createdBy`, `updatedBy`) arrive with the next pull. Until then a read that includes such a record goes to central. The hub marks these records when it stores them (`trackerapi.learn`) and clears the mark when a pull brings central's version.

With the mode on, the sync interval can be shorter while online (5 minutes, say): incremental pulls list IDs and update times only, so they are cheap when little changed. That trade is to be measured too (section 8).

Unsent saves waiting in the outbox do not block a local answer: the hub's copy is then newer than central's.

## 4. Access for active users

Online today, central decides on every read what each user may see. With local answers, the hub's copy of the user's access decides, and it is refreshed hourly (`access_refresh_interval`) and at each login.

**Proposal:** while the mode is on, refresh every 15 minutes the access of **active users** only: users with a request through the hub in the last 15 minutes. Sessions already record when they were last used (`Session.lastSeen`, used for `session_idle`), so no new bookkeeping is needed.

- **Cost:** a refresh is a few small reads per user (the user's details, and their access to the facility's programs and stages; `core.FetchAccess`). 15 active users cost about 240 reads an hour per hub. Every user every 5 minutes would be about 70 000 a day per hub at 60 users, roughly 400 a second for 500 hubs: too much.
- **Outside working hours** nobody is active, so the mode adds nothing; the hourly refresh of everyone stays as today.
- **A user coming back after a break** has stale access for that first request: it goes to central, the hub refreshes their access in the background, and later requests are answered locally.
- **Android tablets left on** sync in the background every 5 minutes and would count as active all night. Count only interactive use (browser sessions, and Android requests other than its periodic sync), or accept the few extra refreshes. To decide (section 9).

## 5. What stays the same

- Offline behaviour, the outbox, pushes, and the pass-through after an outage.
- Saves, online and offline.
- What the hub pulls: still each user's own view, with their own token; central still decides what each user sees, and the hub keeps it per user.
- Users who are not the hub's staff (`registerOnline`, "pass-through only"): always central.

## 6. Risks

- **Changes made outside the hub** (a referral, a lab result from an integration, a correction on central, an Android tablet syncing straight to central) show at the next pull: up to `local_reads_max_age` late. Today they show at once online.
- **Access changes** show within 15 minutes for active users instead of at once.
- **A wrong completeness check** would show a list missing records. The rule forwards on any doubt, and the tests (section 7) compare every local answer with central's.

## 7. Tests

- **Oracles in this mode.** The live suite (`internal/live`) run with `local_reads` on: every read the hub answers itself compared with central's answer for the same user. Equivalent answers are already what the offline oracles check; here the comparison runs online, against central's current state.
- **Completeness:** unit tests for each forwarding reason (scope beyond the facility, a date before the cutoff, a stub, a person type with a window, a wider search scope, a record saved and not pulled back, a stale pull, stale access).
- **Fallback:** e2e tests that a forwarded read reaches central and a local one does not (the fake central's request counts).
- **Guards** from AGENTS.md for the guarded areas this touches: searches (`TestQueryPlansUseIndexes`), shared listings (the twin users).

## 8. Measure first

Before building, on the demo central and a test hub:
1. The same working list, person page and facility search, through the hub (offline answers stand in for local ones) and straight from central, over a typical facility uplink (a 4G stick), and over good broadband.
2. A working day's requests through the hub (`log_requests`, the request log): the share that would qualify for a local answer.
3. Central's requests per facility per day, with and without that share.
4. With a 5-minute pull while online: the extra load of the pulls against what local answers save.

Build if local answers are clearly faster on a slow link and a large share of requests qualify. If the gain shows only on slow links, the mode stays optional and per hub.

## 9. Open questions

- The Android background sync and "active" (section 4).
- Whether the pull interval shortens while online with the mode on, and to what.
- Whether the mode becomes a country setting in the Facility Hub app, per hub or for all.
- Whether the user sees that an answer came from the hub (Capture shows "Online" either way).
