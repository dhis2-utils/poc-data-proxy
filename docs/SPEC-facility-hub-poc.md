# Facility hub: proof-of-concept spec

Status: draft for a POC. Author: Olav Poppe (HISP Centre). Context: DHIS2 offline support initiative, "lightweight facility hub" option.

> This is the original specification, kept as it was asked. Where the hub deviates from it, and why, is in [FINDINGS.md](FINDINGS.md) ("Things in the spec that turned out wrong or needed a decision"). The design has since moved on in places, notably the query index in the encrypted SQLite file instead of in memory (9.3), ID-list pulls shared per access group and 15-minute syncs (11.3), and TLS (12): see [PERFORMANCE.md](PERFORMANCE.md) and [DEPLOYMENT.md](DEPLOYMENT.md).

## 1. Purpose

Staff at a primary care facility enter tracker data in the DHIS2 Capture web app or the Android Capture app, often a mix of both. Several staff see the same patient during one visit (reception, triage, consultation, lab). When the internet is down, they cannot see each other's data.

The hub is a small device on the facility LAN. It caches the facility's tracker data and relays tracker writes to the central DHIS2 server. When central is unreachable, clients use the hub instead. Each user sees only the data that central DHIS2 allows them to see.

The POC must show that:

1. Two users at one facility, one on the web Capture app and one on Android, can register a patient and add events while offline, and each sees the other's data.
2. Users with different program or stage access see different data from the hub.
3. Queued data reaches central when the connection returns, attributed to the user who entered it.
4. Rejections from central are captured and reported, without blocking other data.

## 2. Decisions already made

These are fixed. Do not reopen them in the POC.

- The hub is a cache and relay. It never configures metadata and never owns metadata.
- The hub serves both the web Capture app and the Android Capture app. Both talk to the hub through the same API, so the hub has no client-specific logic beyond what section 5.1 lists.
- Each hub release supports the tracker API of two DHIS2 major releases (current and next). The hub reads the central version at setup and on every sync. All users at a facility use the same DHIS2 version and program set.
- The hub does not implement DHIS2 sharing logic. Central decides access. The hub stores, per user, the list of IDs that central says the user can access, and matches against that list.
- Anything the hub cannot decide is refused ("fail closed"), with a clear "needs connection" error.
- The hub pushes and pulls as each individual user, using a personal access token (PAT) per user. It never uses a shared service account for data writes.
- Data at rest is encrypted with a per-hub key. The key is wrapped once per user with a key derived from that user's password (key slots).
- No analytics, no server-side program rules. Aggregate data entry and event programs were added later: docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md, docs/superpowers/specs/2026-10-15-event-programs-design.md.

## 3. Non-goals for the POC

- Aggregate data (`/api/dataValueSets`). Superseded for the Aggregate Data Entry app: docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md.
- Metadata editing of any kind.
- Server-side program rule evaluation. Both Capture apps evaluate rules on the client.
- Merging conflicting edits. The hub keeps the latest local write; central decides on push.
- Multi-facility hubs. One hub serves one facility org unit.
- Production TLS, OS updates, a fleet dashboard. Hub self-upgrade is in scope (section 12a).
- In the POC, one supported DHIS2 version is enough, as long as the code keeps version-specific request and response handling in one place per version.
- Access to patients owned by other facilities, including break-the-glass. The hub refuses these offline.

## 4. Language and stack

Use Go.

Reasons: the workload is network and disk I/O, not CPU, so Rust's speed gives little here. Go builds one static binary for `linux/arm64` without a C toolchain. The standard library has an HTTP server, a reverse proxy and AES-GCM. Go is also easier to hand over to a maintainer than Rust.

Libraries (keep the list short):

- `net/http`, `net/http/httputil` (reverse proxy)
- `modernc.org/sqlite` (pure Go, no cgo)
- `golang.org/x/crypto/argon2` (password hashing and key derivation)
- `crypto/aes`, `crypto/cipher` (AES-256-GCM)
- `html/template` (hub admin UI)

Target hardware: Raspberry Pi 4 or 5 (4 GB RAM), 64-bit Raspberry Pi OS, SSD or eMMC storage. The binary must also run on a laptop (amd64) for development.

## 5. Architecture

```
 Browser (Capture web app)    Android Capture app
        │  HTTPS, hub origin        │  HTTPS, hub as server URL
        └────────────┬──────────────┘
                     ▼
 ┌──────────────── Hub ────────────────┐
 │ Router                              │
 │  ├─ /api/auth/*      → Auth         │
 │  ├─ /api/tracker/*   → Tracker      │
 │  ├─ /hub/*           → Hub API + UI │
 │  └─ everything else  → Proxy/cache  │
 │ Store (SQLite, encrypted blobs)     │
 │ In-memory index (after unlock)      │
 │ Sync worker (pull + outbox push)    │
 │ Connectivity monitor                │
 └─────────────────────────────────────┘
        │  HTTPS, per-user PAT
        ▼
 Central DHIS2
```

### 5.1 Client access in the POC

The long-term design is that the client keeps central as its origin and falls back to the hub when central is unreachable (a change in the shared app-runtime). That client change is out of scope.

For the POC, the browser uses the hub's own origin. The hub reverse-proxies every path to central while online, and caches GET responses (including app static files) per user. Offline, it serves cached responses and handles tracker endpoints itself. This lets us test with the unmodified Capture web app.

Android: the user enters the hub's address as the server URL at login. The Android app already works offline on the device; the hub only changes who it syncs with. Points that differ from web:

- Android uses its own login flow and calls more endpoints at login and metadata sync (system info, `/api/me`, metadata, the Android Settings app data store keys). The pass-through cache covers these; record them as fixtures like the web calls.
- Android requests reserved values for generated attributes (`/api/trackedEntityAttributes/{uid}/requestValues`). Offline, serve these from the hub's pool (section 10.3). This makes 10.3 required, not a stretch goal.
- Android pulls and pushes through the same `/api/tracker` endpoints, but with its own query parameters and paging. Log its calls and add any missing parameters to section 10.1.
- Android syncs on a schedule (30 minutes minimum) or by hand. Hand-over between a tablet and other users is only as fast as that sync. Fixing this needs an Android change (send on save when the server is reachable) and is out of scope for the hub.
- TLS: Android does not trust a self-signed certificate by default. Find out what the Android Capture app accepts (user-installed CA, network security config) and record it in `FINDINGS.md`. For the POC, use whatever works on a test device.

### 5.2 Connectivity monitor

- Probe `GET {central}/api/system/ping` every 15 seconds (configurable), with a 5-second timeout.
- State is `online` or `offline`. Switch to `offline` after 2 failed probes and to `online` after 1 success.
- Any failed upstream request (network error or timeout) sets the state to `offline` immediately.

## 6. Configuration and setup

The hub has no facility-specific configuration file. All hubs in a country run the same image. A facility hub is set up through the hub web UI by logging in.

### 6.1 Image defaults

One small YAML file, the same for every hub in a country, baked into the image:

```yaml
central_url: https://hmis.example.org   # optional; if empty, asked at setup
setup_user_group: <userGroupUID>        # users allowed to set up a hub
listen: ":8443"
data_dir: /var/lib/hub
sync_interval: 5m
ping_interval: 15s
pat_expiry_days: 90
release_channel: https://releases.example.org/hub/stable
```

### 6.2 Setup flow

The hub must be online during setup.

1. On first boot, the hub generates a hub ID and a signing key pair, and starts in setup mode. It is reachable at `https://hub.local` (mDNS) and at its LAN IP. Print both on the device label.
2. The setup page asks for the central server URL (prefilled from the image if set), username and password.
3. The hub logs in to central. The user must be a member of `setup_user_group`. Otherwise refuse setup.
4. The hub reads the user's org units from `/api/me`. If there is exactly one, use it as the facility. If there are several (for example a district officer), show a picker limited to facility-level org units within the user's scope.
5. The hub reads the central server version from `/api/system/info`. If the hub release does not support it, stop with a clear message.
6. The hub derives the programs to cache: tracker programs (`programType=WITH_REGISTRATION`) assigned to the facility org unit. No manual program selection.
7. The hub registers itself in the central data store: `facility-hub-registry/{hub_id}` with facility UID, set-up-by user, time, hub version and public key. If another active hub is registered for the same facility, show it and ask whether to replace it (mark the old one `retired`).
8. The hub creates its hub-level token (section 11.2) and stores it encrypted.
9. The hub starts the first full pull and shows progress.
10. The setup page ends with an instruction: every staff member must log in once through the hub while it is online. Offline login only works for users who have done this.

A "reset hub" action in the admin UI pushes the outbox, then wipes all data and returns to setup mode. Only members of `setup_user_group` can use it.

Open question: the hub-level token. A PAT created from the setup user's session dies when that user leaves or is disabled. Alternatives: a dedicated low-privilege "hub system" user per country, or an OAuth2 client. Record the options in `FINDINGS.md`; use the setup user's PAT in the POC.

### 6.3 Runtime state

Everything set at setup (hub ID, central URL, facility, programs, server version, hub token) is stored in the encrypted store, not in a file. Programs and server version are refreshed on every sync.

## 7. Authentication

### 7.1 Login

Implement `POST /api/auth/login` with the same request and response shape as the target DHIS2 version (the login app uses it). Check the shape against the target version and record it as a fixture.

Online login:

1. Forward the login to central. If it fails, return central's response.
2. On success, if the hub has no valid PAT for this user, create one. Call `POST /api/apiToken` on central with the user's session. Set expiry to `pat_expiry_days`. The key is returned only once; store it encrypted.
3. Store an Argon2id hash of the password for offline login.
4. Unlock the store if it is locked (section 9), and add or refresh this user's key slot.
5. Refresh the user's access list (section 8).
6. Issue a hub session cookie.

Offline login:

1. Look up the user by username. If unknown, return 401 with "needs connection: first login must be online".
2. Verify the password against the stored Argon2id hash.
3. Unlock the store with the user's key slot if it is locked.
4. Issue a hub session cookie.

Also implement `GET /api/me` (cached from the last online login) and logout.

The Android app does not use the login endpoint. It sends HTTP Basic auth on each request and starts with `GET /api/me`. The hub must accept Basic auth on any request and run the same online or offline login steps the first time it sees a user (or when the password no longer matches the stored hash). Check the Android login sequence against the fixtures in 13.1.

Verify on the target version: whether a normal user can create their own PAT, and which fields `/api/apiToken` needs. If users cannot, log it as a finding and fall back to storing the encrypted password for the POC.

### 7.2 Auth for upstream calls

All calls to central that carry user data use that user's PAT: header `Authorization: ApiToken <key>`. Never use a shared account for tracker reads or writes.

## 8. Access control

### 8.1 Access list per user

Fetch with the user's PAT on each online login and on each sync cycle:

- `GET /api/me?fields=id,username,organisationUnits[id],teiSearchOrganisationUnits[id],authorities`
- `GET /api/programs?filter=id:in:[<cached programs>]&fields=id,access[data[read,write]],trackedEntityType[id,access[data[read,write]]],programStages[id,access[data[read,write]]]&paging=false`

Store per user:

- `orgUnits` and `searchOrgUnits` (UIDs)
- `authorities`
- for each program, program stage and tracked entity type: `read` and `write` flags as returned by central

Category option access (attribute category combos) is out of scope. If an event has a non-default `attributeOptionCombo`, refuse it offline.

A user may use the hub only if `facility_org_unit` is in their `orgUnits`. Otherwise refuse all hub-local tracker calls for that user.

### 8.2 Visibility rules

Each stored object (tracked entity, enrollment, event, relationship) has an `origin` and a visibility set.

Objects pulled from central: visible to user U only if U's own pull returned that object. Record U in the object's visibility set. Central has already applied sharing, ownership and stage access.

Objects created or updated locally: visible to user U if all of these hold:

- U can read the object's program (enrollments, events)
- U can read the object's program stage (events)
- U can read the tracked entity type (tracked entities)
- the owner org unit is `facility_org_unit`

Nested responses must be filtered per object. A user who can read the tracked entity but not a stage gets the tracked entity without those events.

Writes: the user needs `write` on the same items, and the org unit must be `facility_org_unit`. Otherwise reject that object with an error report entry.

Refuse offline, with "needs connection":

- tracked entities owned by another org unit (this includes break-the-glass for PROTECTED programs)
- searches outside `facility_org_unit`
- deletes (POC)

PROTECTED and CLOSED programs need no special handling. Every object the hub serves is owned by `facility_org_unit`, and every hub user has that org unit in capture scope, so they are owners in DHIS2's terms.

### 8.3 Staleness

Access lists can be out of date while offline. Show the time of the last refresh per user in the hub UI. Do not attempt to detect revoked access offline.

## 9. Storage and encryption

### 9.1 Keys

- On first start, generate a random 256-bit data key (DK).
- For each user, derive a key-encryption key with Argon2id from the password and a per-user salt. Wrap DK with AES-GCM under it. Store the wrapped DK in the user's row (a key slot).
- At boot, the store is locked. The first successful login of any known user unwraps DK into memory. DK never touches disk unwrapped.
- When a user's password changes (detected at online login), replace their key slot.
- Escrow is out of scope for the POC. Note it as an open question.

### 9.2 Tables (SQLite)

Tracker payloads and PATs are encrypted with DK (AES-256-GCM, random nonce per value). Index columns hold UIDs and timestamps only, no patient data.

```
users(id, username, pw_hash, kek_salt, wrapped_dk, pat_enc, pat_expires,
      access_json_enc, access_refreshed_at, last_login_at)
objects(uid PK, kind, te_uid, enrollment_uid, program, stage, tet,
        owner_org_unit, updated_at, origin, sync_state, payload_enc)
visibility(object_uid, user_id)
outbox(seq PK, user_id, payload_enc, created_at, state, attempts,
       last_error_enc, depends_on_seq)
http_cache(user_id, method, url, status, headers_enc, body_enc, fetched_at)
issues(id PK, outbox_seq, object_uid, user_id, error_enc, created_at,
       reported_central_at)
```

`kind` is one of `trackedEntity`, `enrollment`, `event`, `relationship`. `origin` is `central` or `local`. `sync_state` is `synced`, `pending` or `rejected`.

### 9.3 In-memory index

After unlock, decrypt all objects and build an in-memory index for queries: tracked entity attributes by value, events by program/stage/status/dates, enrollments by program/status. Facility scale (under 50,000 objects) fits in RAM on a 4 GB Pi. Rebuild the index on unlock and update it on every write.

## 10. Tracker API on the hub

Online behaviour for all `/api/tracker/*` calls: pass through to central with the user's PAT, return central's response, then update the store from the response (GET) or from the submitted payload once central reports success (POST).

Offline behaviour: handled locally as below.

### 10.1 Reads

Implement for the target version:

- `GET /api/tracker/trackedEntities`
- `GET /api/tracker/trackedEntities/{uid}`
- `GET /api/tracker/enrollments`, `/enrollments/{uid}`
- `GET /api/tracker/events`, `/events/{uid}`
- `GET /api/tracker/relationships`

Query parameters to support: `orgUnits`/`orgUnit`, `orgUnitMode` (SELECTED only; others refused), `program`, `programStage`, `trackedEntity`, `trackedEntityType`, `enrollment`, `filter` (attribute filters with `eq`, `like`, `ilike`), `status`, `enrollmentStatus`, `updatedAfter`, `occurredAfter`, `occurredBefore`, `scheduledAfter`, `scheduledBefore`, `order`, `page`, `pageSize`, `totalPages`, `paging`, `fields`.

For `fields`, return the full stored object and then filter out nested objects the user cannot see. Do not implement field selection syntax in the POC beyond `*` and the nested collections (`enrollments`, `events`, `relationships`, `attributes`).

Unsupported parameters return 400 with "not supported offline: <param>". Log every unsupported parameter so we can see what the Capture app needs.

Response shape (pager, collection key, error format) must match the target version exactly. Build it from recorded fixtures (section 13).

### 10.2 Writes

Implement `POST /api/tracker` with `importStrategy` (CREATE, UPDATE, CREATE_AND_UPDATE), `atomicMode`, `async=false`. Handle both nested and flat payloads.

Offline:

1. Validate structure only: UIDs present and well formed, references resolve (in the payload or in the store), access per section 8.2.
2. Reject objects that fail. Accept the rest (behave as `atomicMode=OBJECT` even if the client sent ALL).
3. Upsert accepted objects with `origin=local`, `sync_state=pending`. Add them to the in-memory index.
4. Append one outbox entry per request, owned by the authoring user. If the entry references objects that are still pending in an earlier entry, set `depends_on_seq`.
5. Return an import report in the target version's format, with stats and per-object errors. Use the same error codes as DHIS2 where they apply (for example E1000 for access). Record these against fixtures.

`async=true` returns 400 offline in the POC.

### 10.3 Generated attribute values

`GET /api/trackedEntityAttributes/{uid}/generate` (web) and `GET /api/trackedEntityAttributes/{uid}/requestValues?numberToReserve=N` (Android): offline, serve from a pool reserved on central with `requestValues` during sync. Keep at least 50 values per generated attribute, plus what Android devices typically request. A value handed out is removed from the pool and never handed out twice. Required in the POC.

## 11. Sync worker

Runs every `sync_interval` while online, and immediately on the switch from offline to online.

### 11.1 Push (outbox)

1. Process outbox entries in `seq` order. Skip entries whose `depends_on_seq` is not yet `synced`.
2. POST to `{central}/api/tracker?async=false&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE` with the authoring user's PAT.
3. For each object in the import report: if OK, set `sync_state=synced`. If rejected, set `sync_state=rejected` and create an `issues` row with central's error.
4. If a parent object is rejected, mark dependent entries as rejected with "blocked by <uid>". Do not retry them automatically.
5. On network error, stop and retry in the next cycle. On HTTP 401 (expired or revoked PAT), keep the entry and flag the user as "needs online login".

Rejected objects stay visible locally in the POC, flagged in the hub UI. How to present them to users is an open question.

### 11.2 Report issues to central

For each new issue, write to the central data store with a hub-level token (the one exception to per-user tokens, as this is not patient data entry):

`POST {central}/api/dataStore/facility-hub-issues/{hub_id}-{issue_id}`

Payload: `hub_id`, facility org unit, user ID, object UID, kind, central's error report, created time. No patient attribute values in the issue payload: each error report keeps its code, type and UID, and its message only masked (`tracker.MaskMessage`): every value the hub sent for the object, its person and its enrollment is replaced by `***` wherever it appears, quoted parts stay only when they are identifiers or metadata type and field names, long numbers are masked, and a message that quotes nothing is left out. Central staff so see which data element or attribute caused a refusal, not what was entered.

### 11.3 Pull

For each user with a valid PAT and each cached program (section 6.2, step 6) they can read:

`GET {central}/api/tracker/trackedEntities?orgUnits=<facility>&orgUnitMode=SELECTED&program=<p>&updatedAfter=<last pull for user+program>&fields=*,enrollments[*,events[*]],relationships[*]&pageSize=200`

Page through all results. Upsert objects with `origin=central`, `sync_state=synced`, unless a pending local version exists (local pending wins until pushed). Add the user to each object's visibility set.

Remove a user from the visibility set of objects that central no longer returns to them only on a full (non-incremental) pull. Do a full pull per user at most once a day.

### 11.4 Heartbeat

Each cycle, write `facility-hub-status/{hub_id}` in the central data store: version, uptime, last push, last pull, pending count, issue count, disk free.

## 12. Hub API and admin UI

- `GET /hub/status` (no auth): `{ "mode": "online|offline", "hubId", "lastPush", "lastPull", "pending": n, "rejected": n }`. Clients will use this for a status indicator later.
- `/hub/ui` (hub session required, user must be in `setup_user_group`): a simple server-rendered page with the status, outbox, issues list, users with access refresh times, and a "sync now" button. No patient data on this page beyond UIDs.

TLS in the POC: a self-signed certificate generated at first boot. Production TLS is an open question.

## 12a. Upgrades

Hubs must upgrade centrally and automatically. No one visits a facility to upgrade a hub.

- A hub release supports the DHIS2 major versions it was tested against. A new major version can change the API in ways nobody knows a year ahead, so a hub may need a new release before the server moves to a new major version: test the hub against the new version first, and roll out the hub release before the server upgrade.
- On each sync cycle, the hub checks `release_channel` for a newer release. A release is one binary plus a signature. The hub verifies the signature against a public key in the image.
- Upgrade steps: push the outbox until empty (or until only rejected entries remain), download, verify, run SQLite migrations on a copy of the store, swap the binary, restart, run a health check (store opens, central reachable, `/hub/status` responds). If the health check fails, restore the previous binary and store copy.
- Staged rollout: the release channel can list a percentage or a list of hub IDs. The hub decides from its hub ID whether it is in the current stage.
- The hub reports its version and last upgrade result in the heartbeat (section 11.4).
- When central reports a DHIS2 version the hub does not support, the hub stops offline tracker handling, passes everything through while online, and shows "hub needs upgrade" in `/hub/status`.

OS security updates are separate (unattended upgrades from the distribution). Out of scope for the POC.

POC scope: implement signature check, swap, health check and rollback against a local release channel (a static folder over HTTP). Staged rollout can be a stub.

## 13. Fixtures and testing

### 13.1 Fixtures

Before writing handlers, record real request/response pairs from a DHIS2 instance on the target version: login, `/api/me`, each tracker GET with typical Capture app parameters, `POST /api/tracker` success and failure, `/api/apiToken`. Save them under `testdata/<version>/`. Use them to define response shapes and as golden files in tests.

Also run the Capture web app and the Android Capture app through the hub while online with request logging on (login, metadata sync, search, registration, data entry, data sync). Save the list of endpoints and parameters each client calls. These lists set the offline scope.

### 13.2 Test setup

- A DHIS2 instance on the target version with the Sierra Leone demo database, or a small test program.
- One facility org unit and one tracker program with at least three stages.
- Users:
  - `nurse_a`: program read/write, all stages
  - `nurse_b`: program read/write, stage 3 not shared
  - `clerk_c`: tracked entity type read/write, program read only
  - `outsider_d`: not assigned to the facility

### 13.3 Acceptance scenarios

1. Online pass-through: `nurse_a` registers a patient through the hub; it appears on central with `createdBy=nurse_a`.
2. Offline sharing: cut the uplink. `nurse_a` registers a patient and adds a stage 1 event. `nurse_b` logs in on another browser, finds the patient by attribute search, adds a stage 2 event.
2a. Mixed clients: cut the uplink. `nurse_a` registers a patient on the web app. `nurse_b` syncs an Android tablet against the hub, finds the patient, adds a stage 2 event and syncs. `nurse_a` sees the event on the web. Repeat in the other direction (register on Android, continue on web). Include a generated attribute value from the hub's pool.
3. Stage access: `nurse_a` adds a stage 3 event offline. `nurse_b` does not see it. `nurse_a` does.
4. Program access: `clerk_c` cannot create an event offline (write denied), gets an import report error.
5. Facility scope: `outsider_d` cannot use hub-local tracker calls.
6. Reconnect: restore the uplink. All pending data reaches central in order, each object attributed to its author.
7. Rejection: create offline a tracked entity with a unique attribute value that already exists on central. On reconnect, it is rejected; its dependent events are marked blocked; other pending data still syncs; an issue appears in the central data store and the hub UI.
8. Offline login: reboot the hub while offline. Data is unreadable until a known user logs in. Unknown users get "first login must be online".
9. Encryption: with the hub stopped, the SQLite file contains no patient attribute values in plain text (grep test).
10. Setup: a fresh hub is set up by a member of `setup_user_group` with only a login. It finds the facility and programs by itself and registers in the central data store. `nurse_a` cannot set up a hub.
11. Upgrade: publish a new signed release to the local release channel. The hub pushes its outbox, upgrades and passes the health check. Publish a release that fails the health check; the hub rolls back and reports the failure in its heartbeat. An unsigned release is ignored.

## 14. Open questions (record findings, do not solve)

- Can normal users create their own PAT on the target version? Any authority or setting needed?
- Which endpoints and parameters do the Capture web app and the Android Capture app actually call (from 13.1)? Anything the hub cannot serve offline?
- Which certificate setup does the Android Capture app accept for a hub on the LAN?
- Does Android behave well when its server is a hub that is sometimes cut off from central (login, metadata sync, version checks)?
- How should rejected data be shown to the user who entered it?
- Key escrow for the data key, for when every user's password changes while offline.
- Long-term TLS: public name per hub with ACME DNS challenge, or a private CA trusted by managed devices.
- Client-side fallback in app-runtime (central origin, hub as alternative API base URL), including CORS and auth to the hub.
- Is a hub-level token for issue reporting and heartbeat acceptable, and with which minimal authorities?

## 15. Deliverables

- Go module with `cmd/hub` and packages for auth, store, tracker, sync, proxy, ui.
- `README.md`: build for `linux/arm64` and amd64, configure, run, run tests.
- Fixtures and tests for scenarios 1–11 (automated where possible; manual steps documented otherwise).
- `FINDINGS.md`: answers to section 14, the Capture app endpoint list, and anything in this spec that turned out wrong.
