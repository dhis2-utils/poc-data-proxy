# Findings

What building the facility hub POC taught us about DHIS2, the Capture apps and the [spec](SPEC-facility-hub-poc.md): answers to its open questions, where it was wrong, and what the test framework found. The first rounds were recorded against DHIS2 **2.42.6** (Sierra Leone demo database) on 2026-10-07; later items come from the test framework (docs/TESTING.md) on 2.42. The first round's test setup (users, groups, sharing, UIDs) is in [`testdata/2.42/SETUP.md`](testdata/2.42/SETUP.md). The recorded request/response pairs are in [`testdata/2.42/`](testdata/2.42/).

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
| 8 | Reboot offline: data unreadable until a known user logs in; unknown users get the same answer as a wrong password | Pass | e2e test; live (hub restart, then online and offline login) |
| 9 | No patient data in plain text on disk | Pass (every file in the data directory grepped, for values and for UIDs, programs and dates) | e2e test; live hub database grepped for demo-data values |
| 10 | Setup by a staff member assigned to the facility with only a login (originally: by a member of `setup_user_group`, nurse_a refused; the group was dropped 2026-10-17) | Pass | e2e test; live DHIS2 (registry entry written, replace prompt for a second hub) |
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
  - A **Let's Encrypt certificate** for a public name per hub (ACME DNS-01) works on stock devices.
  - The POC's tests used plain HTTP on the facility LAN, which the stock app accepts. Deployments must not (DEPLOYMENT.md, "Reaching the hub securely", which also has the options, including the app pinning the hub's key).

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
- Central's error messages can contain the offending value. E1064 quotes the duplicate attribute value. Issue reports to central therefore carry only error codes, tracker types and UIDs, never central's message text; the admin page shows the message with backticked values masked, and the full message stays encrypted on the hub.
- Fixing a rejection usually needs central data the hub does not have (for example, who holds the duplicate national ID). A realistic flow is "show the rejection to the author, let them correct or discard it online".

### Key escrow for the data key

Not solved. If every key-slot holder's password changes while the hub is offline or locked, the data key is lost together with any unsynced data; and after a reboot the store stays locked until a user with a key slot logs in. The options (a per-hub recovery key held on central, an unlock-and-sync role, a key available at boot) are in [RECOVERY.md](RECOVERY.md); a one-time recovery code printed at setup, used as an extra key slot, is another.

### Changing the central server URL

Countries move DHIS2 to new domains and hosting, so a hub's central URL must be changeable without a new image or a site visit. It must still not be choosable by whoever stands next to a hub in setup mode (REVIEW-2 F1): the hub forwards every staff login to that server.

Before this round, a set-up hub kept its central URL in its own store and ignored the config file, so moving a fleet meant resetting and setting up every hub again. The design now in the code:

1. **An allowlist instead of one URL.** `central_hosts` in the image lists permitted hosts (`hmis.example.org`, `*.hmis.example.org`). Setup accepts any https URL on those hosts. `central_url`, if set, is the default and is always allowed.
2. **Fleet-wide moves through the signed release channel.** The signed release manifest may carry `centralUrl`. A hub that reads it checks that the URL is allowed and that the new server knows the hub (its registry entry is there and the hub token works), then switches. Only the holder of the release key can move hubs, and no visit or new image is needed.
3. **A one-time setup token for images with neither.** If the image sets neither `central_url` nor `central_hosts`, setup requires the token the hub writes at first boot to `data_dir/setup-token`, printed on the label at the depot. Only someone holding the device can choose the server.

### Offline search covers this facility only, and Capture should say so

Online, a search with `orgUnitMode=ACCESSIBLE` covers the user's whole search scope, often the district. Offline, the hub answers it from this facility's data: refusing it broke registration (item 2 under "Things in the spec"). A patient registered at another clinic therefore comes back as "no results", and the nurse may register a duplicate. Central's uniqueness check on push catches duplicates only through unique attributes such as a national ID, not names.

The hub cannot tell the user on its own. The tracker API has no field for partial results, and the apps believe they are online, because the hub answers `/api/ping` itself. Answering "offline" instead would switch Capture into its own offline mode, which is not wanted either.

**Requirement for Capture web, the app runtime and Android:**

1. A state "connected through a facility hub, central unreachable", read from the hub (`GET /hub/status` reports `"mode": "offline"`; every tracker answer the hub gives itself carries `X-Hub-Mode: offline`, also right after reconnecting while its outbox is pushed). Show it next to the online indicator.
2. On search results in that state, a note: "Searched this facility only. Patients registered elsewhere won't appear." Offline searches that the hub narrowed carry `X-Hub-Scope: facility`. Optionally, before a new registration: "Check online before registering, if possible."

The hub side of this exists; the Capture side is to build.

### Shared metadata cache (REVIEW-2 F4)

Offline, a user whose own cache misses a metadata URL is served another user's copy (spec 5.1 deviation, item 11 under "Things in the spec"). Some metadata URLs are the same for every user while central's answer is not. The web app's `organisationUnits?withinUserHierarchy=true` and `?userDataViewFallback=true` return each user's own org unit tree; tracked entity filters and working lists depend on sharing. These are no longer shared between users: a user who never loaded them online gets "needs connection" for them.

### Long-term TLS

What the POC does: at first boot the hub creates a CA limited by name constraints to its own names and private address ranges, issues a server certificate for `hub.local`, the host name, `localhost` and the LAN IPs, and discards the CA key; devices install `ca.crt` (served at `/hub/ca.crt`). An earlier version made the server certificate itself a CA, so anyone reading its key could have minted certificates for any site. The decision for production (`https://dhis2.local` and the hub's authority installed on each device) and the options weighed are in DEPLOYMENT.md, "Reaching the hub securely"; what devices did is under "Devices trusting the hub's certificate" below.

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

This token dies when the setup user is disabled: the hub then stops its heartbeat and issue reports, stops refreshing its programme list and retention settings, and, once its reserved pool runs out, refuses offline registrations that need a generated ID. It also renews itself with itself, so it cannot recover. Patient data is not affected: every pull and push uses the user's own token. If the setup user only moves to another facility, the hub keeps working with the credentials of someone who no longer works there.

Interim measure (DEPLOYMENT.md, section 6; built 2026-10-17, for members of the hub owners group): any staff member assigned to the facility can take the hub over from its page (a token of theirs replaces the old one), and the heartbeat says whose token the hub uses and whether it works, so central sees a broken one before the pool runs out.

Options for the lasting answer:

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
   - The central URL, server version, hub ID and mode are kept in a small plaintext key file with the key slots. The hub needs them before the store is unlocked: to forward the first login to central, and to know whether it is in setup mode. Everything else, including sync times, is in the encrypted data file (item 23).
   - The hub signing key pair is created at setup rather than first boot, so that it can be stored encrypted. At first boot there is no data key yet.
9. **Program attributes per program.** Spec 8.2 filters nested objects by program/stage/TET access. The hub also drops tracked entity attributes that belong to programs the user cannot read. Otherwise a pulled tracked entity carries every program's attributes, and so would a locally merged one.
10. **Unique attributes.** Offline, the hub also rejects a unique attribute value that already exists in its own store (E1064). Central stays authoritative on push (scenario 7 uses a value created on central while the hub was offline).
11. **Static app files.** Spec 5.1 caches GETs per user. App static files (`/dhis-web-*/…`, `/api/apps/…` with file extensions) hold no user data. Offline, the hub serves them from any user's cache entry, so a user who never opened Capture through the hub online can still load it. API responses stay strictly per user.
12. **Logging.** Request logs (spec 13.1) record parameter **names** only. Query values can carry patient search terms (`filter=<attr>:like:<name>`), and an early version logged them in plain text.
13. **Two-factor authentication.** Offline login checks only the password, so 2FA cannot be enforced offline. Online, 2FA users log in through central as usual. With a missing or wrong code, central answers 200 with `loginStatus: INCORRECT_TWO_FACTOR_CODE_TOTP`. Basic auth for a 2FA user gets 401, so Android 2FA users cannot use the hub's Basic path. Web 2FA users still get a key slot at online login, so offline they log in with the password alone: a country that relies on 2FA should know that the hub weakens it offline.
14. **`depends_on_seq` is a single value.** The hub records the latest pending entry that holds any object the new entry references or updates. Before pushing an entry, it also checks each object's parents in the store and blocks objects whose parent was rejected, so an object depending on two earlier entries is still handled.
15. **Upgrades must not lock the store.** A re-exec loses the in-memory data key, which would leave the hub locked (and not syncing) after every upgrade until a staff member logs in. The upgrader hands the key to the new process over an inherited pipe (`HUB_DK_FD`). Rollback does the same.
16. **Retired hubs.** Setup marks the previous hub `retired` in the registry. The old hub now reads its entry on each sync; while it is retired, the hub stops serving patient data offline and only pushes its outbox. It never wipes itself, because on 2.42 anyone can write the registry namespace until it is protected: a forged entry costs offline service, no data, and ends when the entry is active again.
17. **Generated values come in pairs on 2.42.6.** Central's `generateAndReserve` for the `RANDOM(##########)` pattern returns values in related pairs: `1916135085, 0916135085, 9066229890, 0066229890, …` (checked directly against central). Both are reserved on central, so this is not a duplicate. It does mean half the "random" IDs are predictable from the other half, which is worth reporting to DHIS2 core.
18. **Dates must be normalised.** Clients post `"occurredAt":"2026-10-07"`, and central stores and returns `"2026-10-07T00:00:00.000"`. The hub first returned dates as posted. The Android SDK failed with `ParseException: Unparseable date: "2026-10-07"`, silently skipped the object, and still reported the sync as successful. The hub now normalises the date fields of offline writes to central's form.
19. **Online writes and visibility.** The hub records objects that users write while online. Spec 8.2 gives central-origin objects visibility only through each user's own pull, so a patient registered on a tablet seconds before the uplink went down stayed invisible to the web user until their next pull. Objects learned from online writes, including Android's async job reports, are now shared by the same rules as local objects. The next pull then replaces this with central's view.
20. **Event programs.** Android also downloads event programs (`WITHOUT_REGISTRATION`) assigned to the facility. The hub held only tracker programs and answered 503 for these offline. It now holds them ("Event programs through the hub", below).
21. **`/api/me` offline.** Spec 7.1 says `GET /api/me` is cached from the last online login. Capture asks for many field variants (`me.json?fields=authorities`, `userCredentials[userRoles]`, `settings[keyUiLocale]`). The hub stores the user's full `/api/me` at online login and applies the top-level field selection itself, matching central's answers: unknown fields are dropped, so `userCredentials[userRoles]` gives `{}` on 2.42.
22. **SQLite library.** The spec names `modernc.org/sqlite`. This build environment could reach GitHub but not `proxy.golang.org` or GitLab, where modernc's dependencies live, so the hub uses `github.com/ncruces/go-sqlite3`: also pure Go and no cgo, with SQLite compiled to Go. Only `internal/store` touches the driver (`database/sql` with driver name `sqlite3`). Switching back to modernc means changing the import and the driver name.

23. **Spec 9.2's plaintext index was a confidentiality flaw.** "Index columns hold UIDs and timestamps only, no patient data" is not true in effect. Program, stage and owner org unit per tracked entity, with dates, say who is enrolled in which program (HIV, TB, ANC) at which facility and when they visited. UIDs are the same as central's and appear on printed records, URLs and exports, so they identify patients. Visibility and outbox rows say which staff see or entered whom, and SQLite keeps deleted rows in free pages. The store is now a plaintext key file (hub ID, mode, central URL, usernames and key slots) and a data file encrypted page by page with AES-256-XTS (WAL and temp files too), under a key derived from the data key; rows keep their AES-GCM sealing. The query index lives in the same encrypted file (docs/PERFORMANCE.md), so its columns are encrypted at rest too. A store from before is migrated on its first unlock. What still leaks from a stolen disk: usernames, the number and size of pages, and the fact that it is a hub.
24. **Spec 7.1/9.2 store a password hash next to the key slot.** Both are offline password verifiers for the user's central password. The hub keeps only the key slot, which verifies the password by opening, and removes slots unused for `key_slot_max_age`.
25. **Spec 8.2's rule for local objects ignores search scope.** Program, stage and TET read plus facility ownership let a user with capture but not search scope at the facility see records central would hide. The hub also requires the facility in the user's effective search scope.
26. **Spec 8.3 and revocation.** Fine-grained access cannot be re-checked offline, but a disabled account or a changed password can be detected cheaply while online. The hub re-checks cached Basic credentials with central every 5 minutes, ends all sessions of a user central refuses, and refuses that user offline until an online login succeeds. Sessions also expire (`session_idle`, `session_max`).
27. **Spec 11.2's hub token expires.** It is a PAT with `pat_expiry_days`; nothing in the spec renews it. The hub renews it on sync a week before expiry.
28. **Spec 12a's store copy needs writes stopped.** Writes accepted while the upgrade copies the store were lost with the old store. The hub refuses local writes (503, `Retry-After`) for the duration; online writes still pass through to central.

29. **Spec 6.2 step 6 lists programs with the setup user's access.** The catalog came from the hub token, so the setup user's metadata access. A district officer often cannot see every program facility staff work in, and those programs were never cached or pulled. The catalog is now the union of what the hub token and every registered user can see, extended at each online login and rebuilt on sync.
30. **Spec 12a: "push the outbox until empty" can block upgrades for good.** An entry whose author central no longer accepts may never push. Since the store copy carries the outbox over, the upgrade proceeds when only such entries remain; they are reported as `stuckOutbox`.
31. **Spec 7.1's password fallback.** Storing a user's central password whenever PAT creation fails was never needed on 2.42 and is now off by default (`password_fallback`). Without a PAT, the user can work online and read offline, but offline changes are refused: they could never be sent.
32. **Who registers on the hub.** Spec 7.1 registers any user who logs in online. The hub now registers only users with the facility in their capture org units (the setup user group that could also register was dropped 2026-10-17); others are passed through to central.

## Found by the test framework (docs/TESTING.md)

The differential oracle compares the offline hub with central for every role and access-matrix user, on real 2.42.6 instances.

**Hub bugs, fixed:**

1. One user's refused pull (403 on a program whose tracked entity type they cannot read) aborted the pull for every user after them. A single odd sharing setup stopped the facility's sync.
2. Nested field selections (`attributes[attribute,value]`) were ignored; the hub returned whole objects.
3. A program-scoped answer carried enrollments of every program the user can read, and attributes of the user's other programs.
4. People registered at the facility but enrolled in nothing the user can read were never pulled.
5. DHIS2 2.42 shows the attributes of **every** tracked entity type on the server, plus the queried program's (`TrackedEntityAggregate.getAllowedAttributeUids`). The hub had assumed the entity's own type only; Sierra Leone's metadata showed the difference.
6. At login, a user's access was fetched only with a stored token; a user without one got no access list.
7. A user whose capture org unit is **above** the facility (a district, say) was treated as an outsider: the hub required the facility itself in the user's capture org units. On central the capture scope includes everything below, so the hub refused offline writes central accepts (the write oracle's OVER-REJECT on Laos, `hla_mx07`). The hub now matches the facility's path against the user's capture org units, like central, both when it registers a user at online login and in its offline access checks (`TestCaptureAboveFacility`).
8. A second event in a **non-repeatable stage** was accepted offline and rejected on push with E1039 (LATE on Laos, 67 writes). The hub now fetches each stage's `repeatable` flag and refuses the second event offline, counting the events it holds and earlier ones in the same payload. It can still miss an event created on central that it has not pulled yet; central then rejects it on push, as before.

9. **A user who lost read access to a program kept seeing its data offline.** The pull skipped programs the user can no longer read, so nothing removed what they had been shown before, and offline reads trusted that. Programs a user may not read are now settled empty on a full pull, a changed access list triggers a full pull at the next sync, and offline reads of central records also check the current access list (`TestAccessChangeOfOneUser`). Access changes that the access list does not show, such as category option sharing, take effect at the weekly full pull (known gaps, below).

10. **A person transferred out of the facility stayed visible offline.** Incremental pulls list what the facility owns, so a transfer out never showed; only the full pull (then daily) removed it. On Laos (protected programs) central answered the nurse 404 while the hub showed the enrollment offline. The hub now checks ownership every `ownership_check_interval` (1 hour): it lists the facility-owned tracked entities per program (identifiers only) and reads each held one that is missing to learn its owner. A transfer made through the hub applies at once. Enrollments and events owned elsewhere are hidden from everyone, and opening the person in that program offline answers "needs connection" (`TestTransferOnCentral`, `TestTransferThroughHub`, `TestOwnershipLive`).
11. **Breaking the glass online left the person visible offline.** The hub learned the online read as facility data, because the person was registered and enrolled at the facility, although another facility owned them in the protected program. Temporary ownership then outlived the connection and its own expiry. The hub now keeps what it learns from online reads only when the program owner (`programOwners`) is the facility, and refuses offline to open a person in a program that another org unit owns (`TestBreakTheGlass`, `TestOwnershipLive`).

12. **An event from a user who may write the stage but only read the program was refused offline.** Central accepts it (tracker program events need stage write access, not program write access); the hub asked for both. Found by the write oracle on the synthetic profile.
13. **Events in a program with an attribute category combination were accepted offline and rejected on push** (E1055: such programs need a non-default attribute option combo, which the hub does not support offline). The hub now fetches each program's category combination and answers "needs connection" at once.
14. **Records created offline were stamped in UTC; central writes times in its own time zone.** DHIS2 gives and expects `createdAt` and `updatedAt` without an offset, in the server's zone (`serverTimeZoneId`). On a server on East Africa Time, a person registered offline looked three hours old until synced, and reserved values' expiry dates were compared three hours off. The hub now reads the zone at each sync and stamps its own times in it. The test instances all run in UTC, so only the fake central with a zone set shows it (`TestOfflineRecordsUseCentralTimeZone`).

**DHIS2 behaviour worth knowing:**

- `/api/tracker/events` shows events to a user who has program and stage access but **no access to the tracked entity type**; a tracked entity search refuses that user (E1006). Event access does not depend on tracked entity type sharing.
- An unfiltered `orgUnitMode=ACCESSIBLE` search answers 409 "At least 1 attributes should be mentioned in the search criteria".
- An ownership transfer bumps the tracked entity's `updatedAt`, so a transfer **in** shows in an incremental pull; a transfer out does not, since the facility no longer owns the person.
- Central caches ownership per user: a user who read a person shortly before the person was transferred away can still open them for a while afterwards (Laos, protected program: 200 with the enrollment right after the transfer, while a user who had not read them got 404).
- Small 500s on `null` lists: `"enrollments": null` in a tracker import, and `"userAccesses": null` in `POST /api/sharing`. Both work with `[]`.

**Known gaps, reported by the oracle without failing:**

- Events for users without tracked entity type access (above): central shows them, the hub pulls by tracked entity.
- Unfiltered `ACCESSIBLE` searches: central rejects them, the hub answers from facility data the user can read anyway.
- **Category option sharing changes reach the hub at the weekly full pull** (`full_pull_interval`). The hub's per-user access list holds org units, user groups and access to programs, stages, tracked entity types and relationship types, re-read hourly and at login; any change there gives that user a full pull at the next sync, so removing a user from a group that granted access takes effect within about an hour. Category option sharing is not in the list, and tracker programs rarely use it: a known limitation.
- Users without the facility in their capture scope (a district officer with capture elsewhere and only search scope here, for instance) are passed through only, by design (REVIEW-2 F5): offline they see nothing.

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

## Reconnecting

When central came back, the hub passed tracker requests through at once, while its outbox was still waiting to be pushed (a random wait of up to a minute, then the push itself). In that window central lacked what was written offline: a person registered offline an hour before was not found, an event for them failed on central, and new writes could reach central before older offline ones. The hub now keeps answering tracker requests itself, and queueing writes behind the offline ones, until every entry it can push has been pushed; entries waiting for their author's online login do not count, or one of them would keep the hub local for good. A push that leaves new entries behind (written while it ran) is followed by another at once. With that, the wait before the first push is invisible to staff and was raised to five minutes, to spread hubs over central after a national outage (`TestReconnectBeforePush`).

## DHIS2 2.43

All three profiles and eRegisters pass on 2.43.2 (docs/TESTING.md). Replaying the recorded 2.42 reads against 2.43 showed one new field in the shapes the hub serves, `attributeOptionCombo` on enrollments; the live suite found the rest. 2.43's differences are in `internal/compat/v43.go`.

1. **Ownership transfers name the new owner with `orgUnit`, not `ou`.** 2.43 answers 400 "Required parameter 'orgUnit' is not present" to `ou=`. The hub reads the new owner of a transfer made through it from that parameter, so on 2.43 it recorded an empty owner (`TestOwnershipLive`). It now reads the version's name.
2. **Enrollments can have a category combination of their own** (`enrollmentCategoryCombo` on the program, separate from `categoryCombo`, which events use). When it is not the default, central checked on 2.43.2:
   - an enrollment needs a non-default attribute option combo (E1055 without one);
   - writing it needs write access to its category options (E1099);
   - reading it needs access to them: a user without it does not see the enrollment;
   - but central still lists the person in the program's searches, without that enrollment, and shows the enrollment's events whose own category options the user may see.

   The hub reads the field (2.43 only), pulls such programs per user, as it does for programs with an attribute category combination on events, and refuses enrollments in them offline with "needs connection" (it cannot check category option access offline; central would reject them on push). Known gap: since the hub pulls people with their enrollments, it holds neither the person nor those events when central hides the enrollment, so offline it shows less than central, never more. Changes in category option sharing reach the hub at the weekly full pull (Found by the test framework, known gaps).
3. **Events for a user without tracked entity type access:** the 2.42 known gap (central shows them, the hub does not) does not appear on 2.43.
4. **`dataValueSets` lists fields every value shares once, at the top** (`period`, `attributeOptionCombo`, `orgUnit`), not on each value. With one attribute option combination in the answer, the hub's aggregate pull read every value under an empty one and fetched none of the forms (found on Sierra Leone 2.43, whose seed has current data; synthetic hid it, its values spanning several combinations). The pull now falls back to the top-level fields (`TestAggregatePullCompactValueSets`). `completeDataSetRegistrations` keeps the full shape.

## eRegisters through the hub

eRegisters (github.com/HISP-Uganda/eregisters, 1.1.7) is a DHIS2 web app that keeps its own copy of the facility's data in the browser and syncs with the server: through a hub, each browser syncs with the hub, and the hub is what lets devices at the facility share data while offline, as for Android Capture. Tested on 2.43.2 with the eRegisters configuration (`TestERegisters`, docs/TESTING.md): a client registered offline on one device reached the other through the hub, and central after reconnection, created by its author.

- **It pushes with `skipPatternValidation=true&skipSideEffects=true`.** The hub refused the first offline ("not supported offline"), so every offline push failed. Both flags only switch off checks or side effects on central, which the hub does not run offline; the hub now accepts them and sends them, with `skipRuleEngine`, along when it pushes the queue. `skipSideEffects` and `skipRuleEngine` had been accepted offline but dropped at push.
- **Its connectivity check is `GET /api/me?fields=id`,** which the hub answers offline: the app shows "Online" and syncs with the hub. That is what makes it work, but the user cannot tell that central is unreachable; the app can read `/hub/status` (see "Offline search covers this facility only").
- **Its pull** is `tracker/trackedEntities?program&orgUnits&orgUnitMode=SELECTED&fields=…&page&pageSize&updatedAfter`, with `updatedAfter` from `system/info`'s `serverDate`, which offline comes from the hub's cache: an older date, so the app fetches a little more than needed. It pulls every person of the facility per program, without the limit Android's settings app sets: one device's first download through the hub takes about 4 minutes at HC IV size in process, since pages read on from where the last one ended (docs/PERFORMANCE.md, "A device's full download"); a per-program limit in the app, as Android has, would still cut it.
- **Not offline:** aggregate reports through `dataValueSets` (the app keeps them as drafts; its `dataEntry/dataValues` and `dataEntry/dataSetCompletion` calls are now answered offline as the Aggregate Data Entry app's are, below, but untested with eRegisters), its reports service (`routes/…/run`), dataStore writes, and deletes (`importStrategy=DELETE`, refused offline as for every client).
- **2.43 opens apps inside its global shell** (an iframe at `/apps/<app>`); a reopened PWA is served from its own service worker without the shell. Both work through the hub.
- **Browser storage belongs to the address.** Moving a browser from central's address to the hub's starts the app with an empty local store: sync everything to central first (DEPLOYMENT.md, section 5).

## Aggregate Data Entry through the hub

The Aggregate Data Entry app (release 102.1; the same code on 2.42 and 2.43) through the hub, on 2.42 with the synthetic and Sierra Leone profiles (`TestAggregate*`, `TestAggregateDataEntryApp`; design in docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).

**What central does** (probed on 2.42):

- Validation rules are not checked when a value is saved, nor when a form is completed, even with "complete only if validation passes" (`validCompleteOnly`): only the app checks them, online, before completing. Compulsory fields are checked on completion (409 "All compulsory data element operands need to be filled").
- Refusals: a locked period (expiry days or approval) `E2017` "Data set is locked"; a value of the wrong type `E2030`; a period past the open future periods `E2022`; no write access to the attribute option combination's category options `E2031`; no access to the data element `E1100`. Completing an expired or approved period was accepted.
- `dataEntry/metadata` lists the data sets the user may write, with the categories of their attribute category combinations but not those combinations' option combinations (the hub reads them from `categoryOptionCombos`), and the category options the user may write.
- Bug: for an expiry-locked period nobody has used yet (no row in central's period table), opening the form and saving a value answer 409 "could not extract ResultSet" instead of locked. The hub answers such forms as locked.

**The app without the hub's support** (the spike, today's hub caching pages): it reopens offline through the hub, but a form not opened online in that browser opens empty and editable, and its own write queue belongs to the browser, not the user: on a shared computer, the next user to open the app online sent the previous user's queued values in their own session, and central stored them as theirs.

**What the hub does now:**

- It holds each data set's forms for a window: the current period, earlier ones up to the retention (default 12 periods; per data set in the dataStore at `facility-hub-config/aggregate`) and the open future periods, without periods expiry days or data input periods have closed. It pulls them per access group: one `dataValueSets` (by `lastUpdated`) and one `completeDataSetRegistrations` per cycle, the forms that changed, and every held form once a day.
- Offline, and until the outbox is pushed after an outage, it answers the app's forms from that store with pending edits on top, works out the lock (central's answer, then expiry and input periods now), and accepts edits (201) into the outbox as their author, refusing what central would refuse with central's codes (409: the app rolls the cell back and shows why). The app never queues in the browser, so a shared browser no longer mixes users. Repeated edits of a cell fold into one entry; the order of edits to a cell holds.
- A form outside the window, or never pulled, is answered as locked, so it cannot open empty and editable (to revisit once seen in use: the app shows it as expired).
- Completing offline: the app first asks for validation, which the hub cannot run offline. Where validation is mandatory the app then refuses itself ("Couldn't validate the form, please try again later") and sends nothing; otherwise it says "Couldn't validate the form. This does not effect form completion!" and completes, and the hub queues it behind the form's values. The hub also refuses a mandatory-validation completion itself, and one with an empty compulsory field, for other clients.
- Online writes passed to central are put into the stored form, so a value entered just before an outage is shown offline (found by `TestAggregateDataEntryApp`).
- Refused edits stay as issues with the value, named on the hub's page ("Fake monthly, 202610, Fake number default: 5"); the page also warns about edits waiting for a period that locks within three days.
- File values and min-max edits are refused offline (409 with "needs connection", never 503, which would make the app queue them in the browser).

**Gaps found on the way, fixed for every 2.42+ app:** `/apps/<app>`, the global shell's page for an app, was not served offline to users who had never opened it; and a browser holding another user's cached copy asked conditionally, central answered 304, and the hub, which cannot cache a 304, had nothing for this user offline. When it holds no copy for the user, the hub now asks central without the conditions.

Also found on the profiles: with no open future periods central refuses the current month (E2022 "is after latest open future period"): the latest open period is the current one shifted by `openFuturePeriods` − 1, and the hub's window follows that (Laos). Central accepts a category option combination outside the element's combination, so the hub does not check it either.

**Known gaps:** each user opens the app once online through the hub (its settings are cached per user, as for Capture); lock exceptions for expired periods work online only (expired periods are not held); period types other than Daily, Weekly, Monthly, BiMonthly, Quarterly, SixMonthly, SixMonthlyApril, Yearly and the April, July and October financial years, and non-ISO calendars, are not held (their forms are locked offline); the header still says "Online" offline, because the hub answers the app's ping; a change made on central reaches the hub at its next sync. Offline, the hub's clock decides the current period and expiry: without a working RTC, a board that restarts after a power cut at its last saved time would answer a new month's form as locked (DEPLOYMENT.md, "Clock"). Pulls follow central's clock (the newest `lastUpdated` central listed), not the hub's. Edits whose author must log in online again wait in the outbox; later offline edits of the same cell, and the form's completion, wait behind them. But online, with only such entries waiting, the hub passes requests through: an edit made online then reaches central first, and the waiting older edit overwrites it once its author logs in (last write wins, as on central). The hub's page lists entries waiting for a login.

## Event programs through the hub

Event programs (without registration) work offline through the hub since 2026-10-15 (design in docs/superpowers/specs/2026-10-15-event-programs-design.md), checked on all three profiles on 2.42 and 2.43 by the read and write oracles.

**What central does:**

- Events of event programs carry no `enrollment` and no `trackedEntity`; their org unit is `orgUnit`. Which a user sees depends on program and stage sharing and, where the program has an attribute category combination, on category option sharing (Sierra Leone's Ngelehun has 7 event programs, one with "Implementing Partner").
- A write with an attribute option combination needs data write access to each of its category options (central refuses it otherwise); `/api/categoryCombos?fields=categories[categoryOptions[id,access[data[write]]]]` answers that per user.
- Without an `order`, central lists events newest first.
- On 2.42 the single-event endpoint left out a data value that the same user's event lists show (Laos); 2.43 shows it in both. The hub answers as the lists.

**What the hub does:**

- The catalog holds the facility's event programs; the access refresh reads which category options of attribute category combinations each user may write.
- The pull lists each event program's events per access group (per user where an attribute combination decides), whole, with deleted ones: an event deleted on central leaves lists offline at the next pull and stays as a tombstone for Android's download (see "Deletions on central"). Event programs are not part of the ownership check (they have no program owner).
- Retention by program: central's dataStore `facility-hub-config/retention` (`{"defaultMonths": 0, "programs": {"<program>": 12}}`; 0 or no entry keeps everything). The pull asks only for events within it (`occurredAfter`), and each cycle prunes synced events that fell out of it; events with edits waiting are kept. Tracker programs will read the same table.
- Offline, working lists, single events and Android's download answer for event programs; new and edited events are queued as their author's; deletes stay refused.
- Offline writes with a non-default attribute option combination (events of any program, and 2.43 enrollments) are accepted when the user may write every option in it, refused with E1099 otherwise; before, all were refused offline.
- Event lists are read in index order (creation for Android's download, event date newest first for working lists): at 150 000 events in a program, 3–8 ms a page instead of 0.5–1.6 s (docs/PERFORMANCE.md).

**Known gaps:**

- On 2.43, an event under an attribute option combination in an enrollment that central hides from the user (its own enrollment category combination) is refused offline, while central accepts it (the write oracle's OVER-REJECT on the synthetic profile): the hub does not hold such enrollments (DHIS2 2.43, item 2).
- Users not assigned to the facility (district officers) get "needs connection" offline for event programs too, as for tracker programs.
- Android's real download of event programs through the hub was not run (no emulator attached); the oracle covers its query parameters.


## Deletions on central (tombstones)

**What central does** (2.42, synthetic):

- Deleting an event, enrollment or person is a soft delete. It moves `updatedAt` of the object and of what holds it (an event's enrollment and person), so `updatedAfter` finds it.
- A program listing of tracked entities with `includeDeleted=true` returns deleted people, enrollments and events nested, each with `deleted: true`. A single read (`trackedEntities/<uid>`) leaves the deleted events out even with `includeDeleted=true`; a deleted event read alone is 404.
- Bug: a listing by `trackedEntityType` with `includeDeleted=true` fails with 409 E7145, "Query failed because of a syntax error" (the SQL central builds has `…164ORDER`).
- Changing a deleted object is refused: E1114 (person), E1113 (enrollment), E1082 (event).

**What the hub does:**

- The pulls of tracker and event programs ask for deletions (`includeDeleted=true`) and keep each deleted object as a tombstone: the row stays, marked `deleted`. The pull of people enrolled in nothing (by type) does not, because of the bug above; a person deleted there leaves the hub at the weekly full pull.
- Offline, lists, a person's enrollments and events, and single reads leave tombstones out (a single read answers 404, as central). With `includeDeleted=true` (Android's download) they are returned with `deleted: true`, so a device drops what it downloaded before: before, an event deleted on central stayed on a tablet that synced only through the hub.
- An offline edit of a tombstone is refused with central's code.
- Event-program tombstones are pruned with retention like other events. Tracker tombstones stay; tracker retention (below) prunes by enrollment status and age, not by deletion.

## Retention for tracker programs

**What central does** (2.42 and 2.43; synthetic, Sierra Leone and Laos databases; checked live by `TestEnrollmentRulesOffline` and `TestRetentionWindowLive`, the same on every run):

- A program listing accepts `enrollmentStatus=ACTIVE` together with `updatedAfter`, `includeDeleted=true` and the identifiers-only fields. The active listing answered in 0.08–0.25 s at the test facility (Laos: 248 people listed with a 1-month window).
- A second active enrollment in a program is refused with E1015: "TrackedEntity: `X` already has an active Enrollment in Program `P`." (no comma after the tracked entity).
- A second enrollment in a program that allows one only (`onlyEnrollOnce`; Sierra Leone's malaria program is one, the tests switch the flag on elsewhere) is refused with E1016: "TrackedEntity: `X` already has an Enrollment in Program: `P`, and this Program only allows enrolling once.", also when the second one is completed.
- When both apply (a second active enrollment where one only is allowed), central reports both, and the order of the two varies between requests.

**What the hub does:**

- Retention by program (`facility-hub-config/retention`, as for event programs) applies to tracker programs; `trackedEntityTypes` adds windows by tracked entity type, with no default, so people are kept unless a country names their type.
- A full pull of a program with a window lists two things: people with an active enrollment, and people changed since the cut-off. Incremental pulls are unchanged.
- A completed or cancelled enrollment whose last change is before the cut-off is kept as a stub: its program, person, status and dates, marked `hubStub`, with no data and no events. Stubs never appear in lists, searches, a person's enrollments or Android's download. A single read answers "needs connection", since central has the enrollment. A stub is never merged with a full enrollment; one that is reopened on central comes back in full at the next pull.
- What a listing shows under a stub with the same `updatedAt` counts as held, so a full pull does not fetch those people again.
- Pruning runs after each sync cycle: aged enrollments become stubs and their events go; then tracked entities of types with a window that have nothing left in full go, with their stubs and relationships. Nothing with a pending or rejected change is pruned, and nothing a pending change refers to.
- Offline, E1015 and E1016 are refused with central's codes and messages, E1016 also against a stub. A cancelled new enrollment is checked by neither rule.
- **Known limitation: people transferred in long ago.** A person registered at another facility and owned here, whose enrollments here are all closed and older than the window, is in neither of a windowed full pull's listings: the type pull lists people by where they were registered. The hub then does not hold them, and offline search does not find them (checked on the fake). A transfer moves the person's `updatedAt` (2.42), so anyone transferred within the window is listed; the gap is people transferred earlier with no change since. Accepted for now: a person with no activity anywhere within the window is unlikely to be the one returning, and online search finds them. Fixing it would take a listing of everyone the facility owns in each program on full pulls.
- **Known limitation (by choice):** a tracked entity removed by a type window leaves no trace on the hub. A device that still holds it and edits it offline is therefore answered as for a new tracked entity (accepted), not "needs connection" as the spec intends for pruned objects. Central applies the push as an ordinary update, so nothing is lost or duplicated; the hub holds the person again from then on. Answering "needs connection" would mean keeping a record of every pruned tracked entity, which is what pruning is meant to remove. Pruned enrollments and events are not affected: their stubs, or their enrollment's stub, are still known.

## Listings, compression and the dataStore (measured)

2.42 and 2.43, Sierra Leone (`TestListingBytesLive`, `TestEventEditMovesUpdatedAtLive`, a heartbeat script; numbers in docs/PERFORMANCE.md):

- **Compression comes from the proxy.** DHIS2's Tomcat on its own (the test instances) answers uncompressed even when the client sends `Accept-Encoding: gzip`. In production it sits behind nginx or another reverse proxy that compresses; the hub's client asks for gzip, so ID listings cross the uplink 3–4 times smaller. Check that the proxy compresses `application/json` (nginx's `gzip_types` default is HTML only).
- **Event updates** move the event's and the tracked entity's `updatedAt`, not the enrollment's.
- **A large dataStore namespace (2.42, `facility-hub-issues` filled to 1 million entries of about 0.7 KB, 709 MB):**
  - single-key read, write, update and delete: 0.1 s at 10 000, 100 000 and 1 million entries;
  - listing all keys: 0.18 s and 18 MB at a million;
  - the first page of 50 values: 0.1 s;
  - a deep page: 1.7 s, growing with the page number;
  - a filter on a value: 0.2–0.3 s when matches are common, 0.3–0.6 s when they are rare (it reads the whole namespace; only `(namespace, key)` is indexed);
  - a filter on a field inside an array (an error report's code) matches nothing: the dataStore filter cannot reach into arrays.

  Hubs only touch single keys, so they are not slowed; browsing in an app is. Resolved issues are therefore deleted (DEPLOYMENT.md, "Refused changes").
- **dataStore writes are not audited.** 500 hubs writing heartbeats 11 times each (5 500 writes) left every audit table unchanged. `facility-hub-status` stays at one row per hub, about 1 KB each with Postgres overhead. The writes ran at about 40 per second from 8 clients; 500 hubs syncing every 15 minutes need 0.6 per second. All 500 heartbeats read in one request in about 0.1 s.

## Refused changes

What the hub does with a change central refuses (tested in `internal/e2e`: `TestRefusedCountsClearWhenFixed`, `TestCentralNewerReplacesRefused`, `TestCentralOlderKeepsRefused`):

- The object stays on the hub as refused, with the hub's data: the client was told it was saved, so offline reads keep showing it. The change stays in the outbox entry, and central gets the issue with its values masked.
- **Resolved by sending it again:** a fixed version entered through the hub (offline or online) and accepted by central marks the object synced and closes the issue.
- **Resolved by central:** if central's version changes after the refusal, central's version replaces the hub's and the issue is closed. Typical case: someone fixes the record online. This covers tracked entities, enrollments, events and relationships.
  - The hub records when central refused, by central's clock (its answer's `Date` header, in whole seconds). Central's version wins when its `updatedAt` is more than a second after that.
  - Objects blocked by a refused parent were never sent; their time is the hub's own clock.
- **Edge cases, accepted:**
  - An unrelated change on central after the refusal also replaces the hub's copy. An example is another user editing a different field of the same event. The refused values then disappear from offline reads; they remain in the refused outbox entry, and in the issue on central with values masked.
  - A change on central within a second of the refusal does not count, so the hub keeps its copy until central's next change.
  - Wrong clocks on the hub do not matter: the comparison uses only central's times.
- **Still open:**
  - a refused change that can never pass as it is (repair);
  - the retry instruction for refusals in bulk after central is fixed (docs/DEPLOYMENT.md, "Refused in bulk").

## Devices trusting the hub's certificate

Tested 2026-10-19 with a test authority made like the hub's (ECDSA P-256, name constraint to one `.local` name, server certificate for that name valid 30 days), served from a Mac on a phone hotspot under its mDNS name:

- **Samsung Galaxy Tab A7 Lite (SM-T220), Android 14, Chrome.**
  - The `.local` name resolved, both in the system resolver and in Chrome.
  - Opening the certificate file in Chrome only says it must be installed in Settings; there is no install from the browser.
  - Installed through Settings › Security and privacy › More security settings › Install from device storage › CA certificate, past a "Your privacy is at risk" warning and "Install anyway". Android requires a screen lock first.
  - Chrome then trusted the HTTPS page with no warning; the name constraint was honoured. No "network may be monitored" notice appeared (it may after a reboot).
- **iPhone, Safari.**
  - The `.local` name resolved, over HTTP and HTTPS.
  - Installing the profile alone was not enough: Safari kept warning until full trust was switched on in Settings › General › About › Certificate Trust Settings. Then the page opened with no warning; the name constraint was honoured.
  - A warning clicked through still loads the page, which is exactly what deployments must not teach.
- **Still to check:** older Android versions resolving `.local`; whether browsers keep HSTS for `.local` names; Windows and macOS with a name-constrained authority.
