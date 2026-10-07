# Findings

Facility hub POC. Results recorded against DHIS2 **2.42.6** (Sierra Leone demo database) on 2026-10-07. The test setup (users, groups, sharing, UIDs) is in [`testdata/2.42/SETUP.md`](testdata/2.42/SETUP.md). The recorded request/response pairs are in [`testdata/2.42/`](testdata/2.42/).

## Acceptance scenarios (spec 13.3)

| # | Scenario | Result | How it was tested |
|---|---|---|---|
| 1 | Online pass-through, `createdBy=nurse_a` on central | Pass | e2e test; live DHIS2 (curl through the hub) |
| 2 | Offline sharing between nurse_a and nurse_b (search by attribute, add event) | Pass, with the real Capture web app (clean run, no request patching) | e2e test; live DHIS2 (curl); Capture web app via Playwright |
| 2a | Mixed clients (Android + web), generated value from the hub's pool | Pass. Registration, upload, reserved values and hand-over both ways while offline; hand-over speed limited by the app's sync, as spec 5.1 says | e2e test; Capture 3.4.2 on the emulator plus API calls as the web user |
| 3 | Stage access: nurse_b does not see stage 3, nurse_a does | Pass (local and pulled data) | e2e test; live DHIS2 |
| 4 | clerk_c cannot write events offline (import report error) | Pass: 409 with E1095, as central answers | e2e test; live DHIS2 |
| 5 | outsider_d cannot use hub-local tracker calls | Pass: 403 | e2e test; live DHIS2 |
| 6 | Reconnect: pending data reaches central in order, attributed to its author | Pass | e2e test; live DHIS2 (`createdBy` per author checked on central) |
| 7 | Rejection: unique value conflict; dependents blocked; other data syncs; issue in central data store and hub UI | Pass | e2e test; live DHIS2 (E1064 on the tracked entity, E5000 on its enrollment, "blocked by" on its events, 4 issues in `facility-hub-issues`) |
| 8 | Reboot offline: data unreadable until a known user logs in; unknown users get "first login must be online" | Pass | e2e test; live (hub restart, then online and offline login) |
| 9 | No patient values in plain text in the database file | Pass (database, WAL and request log grepped) | e2e test; live hub database grepped for demo-data values |
| 10 | Setup by a member of `setup_user_group` with only a login; nurse_a refused | Pass | e2e test; live DHIS2 (registry entry written, replace prompt for a second hub) |
| 11 | Upgrade: signed release installs; failing release rolls back and is reported; unsigned release ignored | Pass | `scripts/test-upgrade.sh` |

`go test ./...` runs scenarios 1–10 end to end against `internal/fakecentral`. The fake is shaped after the recorded fixtures. To cut the uplink it drops connections, so the hub handles real network errors. The same tests cover a hub reset, a revoked PAT (401 on push), and offline serving of cached app files.

## Answers to the open questions (spec 14)

### Can normal users create their own PAT on 2.42?

Yes. No authority or setting is needed; 2.42 has no PAT-related authority at all.

- **Request.** `POST /api/apiToken` with `{"type":"PERSONAL_ACCESS_TOKEN_V2","expire":<epoch ms>,"attributes":[]}`. `{}` also works and defaults to V2 with a 30-day expiry. It works with Basic auth, a session cookie or another token.
- **Response.** 201, with the key in `response.key` (`d2p_…`), shown only once. The token id is in `response.uid`; use it with `DELETE /api/apiToken/{uid}`.
- **Expiry is not validated.** 400 days and a date in the past were both accepted. The hub uses `pat_expiry_days` and renews a PAT at online login when it has less than 7 days left.
- **Using the token.** `Authorization: ApiToken <key>` works for `/api/me` and `/api/tracker`.
  - Expired or deleted key: 401. The hub then flags the user "needs online login" and keeps their outbox entries.
  - DHIS2 still creates a server session and sets a cookie on token requests. The hub ignores that cookie.
- **Fallback.** The fallback in spec 7.1 (store the encrypted password) is implemented but was not needed.

### Which endpoints and parameters do the Capture apps call?

The raw endpoint logs from runs through the hub are [`testdata/2.42/capture_web_endpoints.txt`](testdata/2.42/capture_web_endpoints.txt) and [`testdata/2.42/capture_android_endpoints.txt`](testdata/2.42/capture_android_endpoints.txt). The main points for offline scope:

**Capture web app (2.42 bundled).** Every call is **versioned**: `/api/42/…`.
- **Login:** `GET /api/loginConfig`, `/api/42/locales/ui`, `POST /api/42/auth/login`.
- **Startup.** These are all metadata or app configuration, except `me`, `me.json`, `userSettings` and `me/dashboard`:
  - `system/info`, `me?fields`, `me.json?fields=authorities|userCredentials[userRoles]|settings[keyUiLocale]`, `userSettings`, `me/dashboard`, `apps`, `apps/menu`, `systemSettings[/…]`
  - `programs…`, `trackedEntityTypes`, `trackedEntityAttributes`, `dataElements`, `optionSets`, `optionGroups`, `categoryOptions`, `constants`
  - `programRules`, `programRuleVariables`, `programIndicators`, `relationshipTypes`
  - `organisationUnit*`, `dataStore/capture[/ruleEngine]`, `icons/{key}/icon`, `staticContent/logo_banner`
- **Working list:** `tracker/trackedEntities?order,page,pageSize,orgUnits,orgUnitMode=SELECTED,program,fields`.
- **Search:** `tracker/trackedEntities?filter,fields,program,page,pageSize,`**`orgUnitMode=ACCESSIBLE`** (no `orgUnits`).
- **Registration:**
  - `trackedEntityAttributes/{uid}/requiredValues`
  - `trackedEntityAttributes/{uid}/generate?expiration=3`
  - uniqueness checks: `tracker/trackedEntities?filter=<attr>:EQ:<value>&orgUnitMode=ACCESSIBLE` with `program` or `trackedEntityType`
  - duplicate check: `tracker/trackedEntities?filter,orgUnitMode,program,page,pageSize`
  - `POST tracker?async=false`: nested, **without an enrollment UID**, atomicMode not set (so ALL)
- **Dashboard and data entry:**
  - `tracker/trackedEntities/{uid}?fields[&program]`, `tracker/enrollments/{uid}?fields`, `tracker/events/{uid}`
  - `tracker/relationships?trackedEntity|event,fields,paging`
  - `POST tracker?async=false` (flat events), `…&importStrategy=UPDATE`
- **Sync:** none on the client side. Every write is a direct POST.

**Android Capture 3.4.2 (SDK 1.14.2).** Basic auth on every request, no versioned paths.
- **Login:**
  - `GET /api/loginConfig`, `/api/me?fields=…`
  - `dataStore/ANDROID_SETTINGS_APP/info` and `ANDROID_SETTING_APP/general_settings`; 404 is a normal answer here
  - `/api/system/info?fields`
- **Metadata sync:**
  - `ping`, `userSettings`, `systemSettings?key`, `me/authorization`
  - data store keys (`ANDROID_SETTING_APP/*`, `APK_DISTRIBUTION/versions`, `USE_CASES/stockUseCases`)
  - `organisationUnits?…page`, then every metadata collection with `fields, filter=id:in:[…], paging=false`
- **Reserved values:** `generateAndReserve?numberToReserve=100` at first sync, then small top-ups. It never calls `requestValues`.
- **Upload:** `POST /api/tracker?atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE` (**async**). Then it polls `GET /api/tracker/jobs/{id}` and `…/report`, and fetches each uploaded TE again with `GET trackedEntities/{uid}?program&includeDeleted=true&orgUnitMode=ACCESSIBLE`.
- **Download:**
  - `trackedEntities?fields&includeDeleted=true&order=createdAt:desc&orgUnitMode=DESCENDANTS&orgUnits=<facility>&page&pageSize=50&paging=true&program&updatedAfter=<watermark>`
  - `events?…&orgUnit=<facility>&orgUnitMode=DESCENDANTS&…`
  - aggregate `dataValueSets`, `completeDataSetRegistrations`, `dataApprovals/multiple`
- **Search:** `trackedEntities?filter&orgUnitMode=ACCESSIBLE&pageSize=30&includeDeleted=false`, no `orgUnits`.

**Which of these the hub can serve offline:** all tracker reads and writes above; generated values; `/api/me` in any field variant; and the metadata/config GETs. A metadata GET is served if any user fetched it online through the hub. The hub cannot serve aggregate data (`dataValueSets`, out of scope), writes to `userDataStore`/`dataStatistics` (the Dashboard app's landing page; not needed by Capture), or anything never fetched online.

### Which certificate setup does the Android Capture app accept?

Tested with Capture **3.4.2** (Android SDK 1.14.2).

- **Cleartext HTTP is allowed.** The app's manifest sets `android:usesCleartextTraffic="true"` ([AndroidManifest.xml line 52](https://github.com/dhis2/dhis2-android-capture-app/blob/3.4.2/app/src/main/AndroidManifest.xml)). The SDK's [`network_security_configuration.xml`](https://github.com/dhis2/dhis2-android-sdk/blob/1.14.2/core/src/main/res/xml/network_security_configuration.xml) has `<base-config cleartextTrafficPermitted="true">`. The emulator test used `http://10.0.2.2:<port>`, and the app accepted it as the server URL.
- **User-installed CAs are not trusted.** The trust anchors are `@raw/isrgrootx1`, `@raw/isrgrootx2` and `system`, with no `user` entry. On Android 7.0 and below, `OkHttpClientFactory.kt` installs a trust manager that trusts only the two ISRG roots.
- **Consequences:**
  - A self-signed or private-CA hub certificate works only if the CA is in the **system** store (rooted or MDM-provisioned devices) or the app is rebuilt with `<certificates src="user"/>`.
  - A **Let's Encrypt certificate** for a public name per hub (ACME DNS-01) works on stock devices. This favours option 1 under "Long-term TLS".
  - For the POC, plain HTTP on the facility LAN is what works with the stock app.

### Does Android behave well when its server is a hub that is sometimes cut off from central?

Mostly, once the hub met three conditions found in testing:

1. **Never answer with a redirect.** SDK 1.14.2 (`ServerURLVersionRedirectionPlugin.kt`) takes the `Location` of any redirect on an internal request as the device's new server URL. The first version of the hub let Go's `ServeMux` send a 307 for `/api/trackedEntityAttributes` (no trailing slash). The relative `Location` then became the server URL, and every later request went to the host "api". The hub now routes API paths without any mux redirect and rewrites proxied `Location` headers to absolute hub URLs.
2. **Accept the SDK's download parameters offline:** `includeDeleted=true`, `orgUnitMode=DESCENDANTS` with `orgUnits=<facility>`, `orgUnitMode=ACCESSIBLE` on single-object fetches and searches, and `updatedAfter` from central's clock. Objects written on the hub always pass `updatedAfter`, so clock skew between hub and central cannot hide them. A rejected download (400) did **not** show as a sync failure in the app. The SDK just did not advance its watermark.
3. **Emulate async imports offline** (the SDK posts with the default `async=true` and polls the job).

Even then, hand-over between a tablet and other users is only as fast as the app's sync, as spec 5.1 says. nurse_a's event reached the tablet on the app's next sync. With the uplink cut, the app's own state, login and data entry were unaffected, because it works offline on the device anyway.

### How should rejected data be shown to the user who entered it?

Not solved; options observed while building:

- Rejected objects stay visible locally, flagged `rejected` in the hub UI, which only an admin sees.
- The hub knows the author of every outbox entry. It could show a per-user banner in a hub-served page, or expose `GET /hub/issues?user=me` for a future app-runtime status component next to the `/hub/status` indicator.
- Central's error messages can contain the offending value. E1064 quotes the duplicate attribute value. The hub redacts backticked non-UID values before reporting issues to central or showing them on the admin page; the full message stays encrypted on the hub.
- Fixing a rejection usually needs central data the hub does not have (for example, who holds the duplicate national ID). A realistic flow is "show the rejection to the author, let them correct or discard it online".

### Key escrow for the data key

Not solved. If every key-slot holder's password changes while the hub is offline or locked, the data key (DK) is lost together with any unsynced data. A related gap appeared:

- After a reboot, the store stays locked until a user with a key slot logs in. A user who has never logged in on this hub, or whose password changed on central since their last login, cannot be registered until someone else unlocks the store. Their online login still works as a plain pass-through.

Options:

1. **Hub key pair escrow.** Wrap DK to a country-level escrow public key and store the wrapped DK in the `facility-hub-registry` entry. The hub already creates an ed25519 key pair at setup; escrow would need an X25519/age-style recipient key.
2. **Recovery code.** Print a one-time recovery code at setup and use it as an extra key slot.
3. **Hub token slot.** Wrap DK under the hub-level token. This only helps while central is reachable, since the token itself must be verified.

### Long-term TLS

POC: a self-signed certificate made at first boot, valid for `hub.local`, the host name, `localhost` and the LAN IPs, and marked as a CA so a test device can install it. Options for production:

1. **Public name per hub.** For example `<hubid>.hubs.example.org`, with an ACME DNS-01 certificate. The hub needs DNS API credentials or a central issuing service, and local DNS must resolve the name to the LAN IP; split-horizon DNS or a public record pointing to the private IP both work.
2. **Private CA.** A country CA trusted by managed devices through MDM. This needs Android network-security-config support for user CAs (see the Android finding).

### Client-side fallback in app-runtime

Not built; the POC uses the hub's origin. Notes from building the proxy:

- **Absolute central URLs.** Central puts its own base URL in pager `nextPage`/`prevPage` links and in tracker job `location`. The hub rewrites the central base URL to its own origin in proxied JSON. An app-runtime fallback would need the same rewrite, or clients that ignore absolute links.
- **Login flow.** The login app calls `GET /api/loginConfig`, then `POST /api/auth/login` (`{loginStatus, redirectUrl}`), then navigates to `redirectUrl`. The session cookie name is set per instance (here `JSESSIONID_agent-facility-hub`), so clients must not assume `JSESSIONID`.
- **Logout.** The 2.42 header bar logs out with `GET /dhis-web-commons-security/logout.action`. `/api/auth/logout` does not exist (404). Its `Set-Cookie` clears plain `JSESSIONID`, not the instance's real cookie name.
- **Hub sessions.** The hub issues its own `HUBSESSION` cookie and never forwards central's cookies. A cross-origin fallback would instead need CORS on the hub and token or cookie auth to it.

### Is a hub-level token for issue reporting and heartbeat acceptable, and with which authorities?

The POC uses a PAT of the setup user, as the spec allows. It is used for:

- writing `facility-hub-issues`, `facility-hub-status` and `facility-hub-registry`;
- reading program metadata (`/api/programs`) and the default category option combo;
- reserving values with `generateAndReserve`.

This token dies when the setup user leaves or is disabled. Options:

1. **Dedicated "hub system" user per country.** Give it no data capture org units, metadata read on the tracker programs, and write sharing on the three data store namespaces. Reserving values only needs the attribute to be readable. This user cannot read tracker data, which is the property we want.
2. **OAuth2 client credentials** (2.41+ has an OAuth2 authorization server). This gives the same scope with rotating secrets.

Either way, **protect the namespaces**. By default any user can read and write any data store namespace on 2.42; nurse_a and outsider_d could both write `facility-hub-registry`. Set namespace protection (sharing) so that only the hub system user and admins can write.

## Things in the spec that turned out wrong or needed a decision

1. **`requestValues` does not work on 2.42.6.** `GET /api/trackedEntityAttributes/{uid}/requestValues?numberToReserve=N` answers 200 `{}` for every user. Android reserves values with `GET …/generateAndReserve?numberToReserve=N[&expiration=days]`, which returns a bare array valid for 60 days; `generate` (web) returns one object valid for 3 days. The hub serves all three paths offline from its pool and fills the pool with `generateAndReserve`.
2. **Spec 10.2 step 2 ("behave as atomicMode=OBJECT even if the client sent ALL") produced orphan data.** Capture web sends no `atomicMode`, so ALL. When the hub accepted a TE but rejected its enrollment, Capture saw `status: OK`, stayed on the form, and the half-registered person reached central. The hub now honours the client's atomicMode, like central. Android sends OBJECT explicitly.
3. **Sync imports with `atomicMode=OBJECT` answer 200 and `"status":"OK"` even when objects are rejected.** Errors appear only in `validationReport.errorReports` and `stats.ignored`. Children of a rejected parent can be ignored with **no error report of their own**: in the unique-conflict case the nested event had none. The hub therefore treats an object as persisted only if it appears in `bundleReport…objectReports` without errors, and marks the rest "blocked by <parent>". With `atomicMode=ALL` (the default) any error gives 409 / ERROR and no `bundleReport`.
4. **`async=true` is central's default.** The spec has the hub answer 400 offline. The hub instead imports synchronously and keeps the report in memory, answering `GET /api/tracker/jobs/{id}` and `…/report` in 2.42's shapes. A client that does not send `async=false` therefore still works offline.
5. **Error codes.**
   - Central checks stage write access before program write access. A program-read-only user (clerk_c) gets **E1095**, not E1091 or E1000; the hub uses the same order.
   - A missing enrollment reference gives E1079 + E1313; there is no dedicated code.
   - An unreadable stage in a `programStage` query gives 403 E1006.
   - A single object not found gives 404 E1005. Offline the hub answers 503 "needs connection" instead, for both "not here" and "not visible to you", so nothing leaks and nothing is falsely reported as missing.
6. **`/api/tracker/events` takes `orgUnit` (singular).** `orgUnits` gives 400 E1003. Tracked entities and enrollments take `orgUnits`. The hub accepts both.
7. **Pager shapes.**
   - Default: `{page, pageSize[, prevPage][, nextPage]}` with no total. `nextPage` appears only when the page is full.
   - `totalPages=true`: adds `total` and `pageCount`.
   - `paging=false`: no `pager` key at all.
8. **Encrypted runtime state (6.3).**
   - The central URL, server version, hub ID and mode are kept in a small plaintext `meta` table. The hub needs them before the store is unlocked: to forward the first login to central, and to know whether it is in setup mode.
   - The hub signing key pair is created at setup rather than first boot, so that it can be stored encrypted. At first boot there is no data key yet.
9. **Program attributes per program.** Spec 8.2 filters nested objects by program/stage/TET access. The hub also drops tracked entity attributes that belong to programs the user cannot read. Otherwise a pulled tracked entity carries every program's attributes, and so would a locally merged one.
10. **Unique attributes.** Offline, the hub also rejects a unique attribute value that already exists in its own store (E1064). Central stays authoritative on push (scenario 7 uses a value created on central while the hub was offline).
11. **Static app files.** Spec 5.1 caches GETs per user. App static files (`/dhis-web-*/…`, `/api/apps/…` with file extensions) hold no user data. Offline, the hub serves them from any user's cache entry, so a user who never opened Capture through the hub online can still load it. API responses stay strictly per user.
12. **Logging.** Request logs (spec 13.1) record parameter **names** only. Query values can carry patient search terms (`filter=<attr>:like:<name>`), and an early version logged them in plain text.
13. **Two-factor authentication.** Offline login checks only the password, so 2FA cannot be enforced offline. Online, 2FA users log in through central as usual. With a missing or wrong code, central answers 200 with `loginStatus: INCORRECT_TWO_FACTOR_CODE_TOTP`. Basic auth for a 2FA user gets 401, so Android 2FA users cannot use the hub's Basic path.
14. **`depends_on_seq` is a single value.** The hub records the latest pending entry that holds any object the new entry references or updates. Before pushing an entry, it also checks each object's parents in the store and blocks objects whose parent was rejected, so an object depending on two earlier entries is still handled.
15. **Upgrades must not lock the store.** A re-exec loses the in-memory data key, which would leave the hub locked (and not syncing) after every upgrade until a staff member logs in. The upgrader hands the key to the new process over an inherited pipe (`HUB_DK_FD`). Rollback does the same.
16. **Retired hubs.** Setup marks the previous hub `retired` in the registry, but the retired hub does not notice and keeps running. A hub should check its registry entry on each sync and stop syncing if retired.
17. **Generated values come in pairs on 2.42.6.** Central's `generateAndReserve` for the `RANDOM(##########)` pattern returns values in related pairs: `1916135085, 0916135085, 9066229890, 0066229890, …` (checked directly against central). Both are reserved on central, so this is not a duplicate. It does mean half the "random" IDs are predictable from the other half, which is worth reporting to DHIS2 core.
18. **Dates must be normalised.** Clients post `"occurredAt":"2026-10-07"`, and central stores and returns `"2026-10-07T00:00:00.000"`. The hub first returned dates as posted. The Android SDK failed with `ParseException: Unparseable date: "2026-10-07"`, silently skipped the object, and still reported the sync as successful. The hub now normalises the date fields of offline writes to central's form.
19. **Online writes and visibility.** The hub records objects that users write while online. Spec 8.2 gives central-origin objects visibility only through each user's own pull, so a patient registered on a tablet seconds before the uplink went down stayed invisible to the web user until their next pull. Objects learned from online writes, including Android's async job reports, are now shared by the same rules as local objects. The next pull then replaces this with central's view.
20. **Event programs.** Android also downloads event programs (`WITHOUT_REGISTRATION`) assigned to the facility. The hub caches only tracker programs (spec 6.2 step 6) and answers 503 for these offline. The app tolerated it.
21. **`/api/me` offline.** Spec 7.1 says `GET /api/me` is cached from the last online login. Capture asks for many field variants (`me.json?fields=authorities`, `userCredentials[userRoles]`, `settings[keyUiLocale]`). The hub stores the user's full `/api/me` at online login and applies the top-level field selection itself, matching central's answers: unknown fields are dropped, so `userCredentials[userRoles]` gives `{}` on 2.42.
22. **SQLite library.** The spec names `modernc.org/sqlite`. This build environment could reach GitHub but not `proxy.golang.org` or GitLab, where modernc's dependencies live, so the hub uses `github.com/ncruces/go-sqlite3`: also pure Go and no cgo, with SQLite compiled to Go. Only `internal/store` touches the driver (`database/sql` with driver name `sqlite3`). Switching back to modernc means changing the import and the driver name.

## Capture web app through the hub

Tested with Playwright and Chromium against the real 2.42.6 Capture app, scenarios 2 and 3. The first run failed in several places; each failure is now fixed in the hub. The clean second run used no request patching:

- **Online, nurse_a.** Login page, Capture, working list, search, registration and a stage 1 event all worked. Central shows `createdBy=nurse_a`.
- **Offline, nurse_a.**
  - Capture reloaded.
  - The working list was served live from the hub and included a patient registered minutes earlier.
  - Attribute search worked.
  - A registration went through (the hub assigned the enrollment UID), and stage 1 and stage 3 events saved.
- **Offline, nurse_b.**
  - Logged in offline and searched successfully.
  - The **stage 3 event was hidden** in the UI and API.
  - Filled in the scheduled stage 2 event.
  - nurse_a then saw nurse_b's values.
- **Reconnect.** All outbox entries synced with 0 rejected. On central, the person, enrollment and stage 1/3 events have `createdBy=nurse_a`. The stage 2 event has `updatedBy=nurse_b`, and its data values `createdBy=nurse_b`.

What the first run exposed, and what the hub does now:

| Problem in run 1 | Fix |
|---|---|
| Capture calls `/api/42/tracker/…`, `/api/42/auth/login`, `/api/42/…/generate`. The hub only handled unversioned paths, so tracker reads came from the GET cache (stale), writes and login failed offline, and `generate` replayed an already-used value from the cache | Versioned paths are routed like unversioned ones |
| The login app loaded blank: the hub forwarded the browser's `Origin`, and central answers non-allowlisted origins with empty 200 bodies | `Origin` is not forwarded (the hub is a same-origin client of central) |
| The app shell's `<meta name="dhis2-base-url">` pointed at central, so the app bypassed the hub | The central base URL is rewritten to the hub origin in HTML, JS and JSON |
| Search and the generated-attribute uniqueness check use `orgUnitMode=ACCESSIBLE`. Refused offline as spec 10.1 says, which blocked registration entirely | Answered from the facility's data (`offline_search_facility: true`, the default). **Deviation from spec 10.1** |
| The registration payload has no enrollment UID; the hub rejected it (E1048) | Missing UIDs are generated, as central does |
| The hub accepted the TE but rejected the enrollment and still answered `status: OK`, so the orphan TEs reached central | `atomicMode=ALL` (central's default) is honoured: any error rejects the whole payload with 409/ERROR, as central does. **Deviation from spec 10.2 step 2**, see below |
| Dashboard crash: hub-written events had no `dataValues`/`notes` | Locally written objects get central's default fields |
| nurse_b, who never opened a dashboard online, missed per-user cached metadata, `dataStore/capture`, and `me.json?fields=…` variants | Metadata and app config GETs fall back to another user's cached copy; `/api/me` in any field variant is answered from the user's own `/api/me`, stored at login |
| Landing after offline login: `/` and `/dhis-web-capture/index.html` were not cached | 302 and 404 responses are cached and replayed; offline `/` redirects to Capture |

Two tracked entities without enrollments ("HUBWEBOFF Gamma" `VyNjN0NIvz4`, "HUBWEBOFF Delta" `QF0giYXqXWd`) remain on the test instance from run 1, before the atomicMode fix.

## Android Capture app through the hub

Tested with Capture 3.4.2 on the emulator, server URL `http://10.0.2.2:<port>` (plain HTTP):

- **Online.**
  - Login as nurse_b unlocked the hub's store.
  - Full metadata and data sync completed (after the redirect fix).
  - Search and the dashboard worked.
  - A registration got its TB identifier from the device's pool. The device had reserved 100 values through the hub at first sync.
  - Central shows `createdBy=nurse_b`.
- **Offline (uplink cut).**
  - A registration and a stage 2 event uploaded to the hub: async POST, job polling and job report all worked, and the hub showed `pending=3`.
  - The app topped up reserved values from the hub's pool (`generateAndReserve?numberToReserve=2`).
  - nurse_a found the patient through the hub and added an event.
- **Reconnect.** Every object reached central with the right `createdBy` (nurse_b for the device's data, nurse_a for her event).
- **Hand-over across runs 2–4.**
  - **Run 2.** nurse_a's offline event reached the tablet only after reconnect, because the hub refused the SDK's download parameters (`includeDeleted=true`, `orgUnitMode=DESCENDANTS`).
  - **Run 3.** Offline search, upload, single-object fetch and download all returned 200, and the download contained nurse_a's event. The app still dropped it: `ParseException` on the bare date `2026-10-07`. Separately, a patient registered on the tablet online just before the cut was invisible to nurse_a offline.
  - **Both are fixed** (items 18 and 19 above).
  - **Run 4.** nurse_a's offline event with a bare date reached the tablet during an offline sync, with no parse error. The app showed it.
  - **Visibility of the tablet's online registration.** At first this still failed, because the handler that records online reads also caught the job-report request. That is fixed with a regression test (`online async write from the tablet is visible to others offline`). Re-checked against the real DHIS2 with the tablet's request pattern:
    - nurse_b posted async with Basic auth and fetched the job report;
    - the uplink was then cut;
    - nurse_a read the patient offline (200) and added an event;
    - after reconnect, central shows the event with `createdBy=nurse_a`.
- **Offline validation is structural only** (spec 10.2). In run 4 an offline event carried the value `false` for a TRUE_ONLY data element. The hub accepted it; central rejected it on push (E1302), and it appeared as an issue. Value-type validation offline would need data element metadata on the hub. Spec 10.2 deliberately leaves that out, and the Capture apps validate value types themselves; the bad value came from a hand-written API call.

The emulator test also created the key `ANDROID_SETTING_APP/general_settings` (`allowScreenCapture: true`) on the test instance, so screenshots of the app are possible.
