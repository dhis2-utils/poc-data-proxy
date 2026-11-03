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
2. **Fleet-wide moves through the signed release channel** (removed 2026-10-19 with the release channel; moves now use the planned move and move codes, DEPLOYMENT.md section 6). The signed release manifest could carry `centralUrl`. A hub that reads it checks that the URL is allowed and that the new server knows the hub (its registry entry is there and the hub token works), then switches. Only the holder of the release key can move hubs, and no visit or new image is needed.
3. **A one-time setup token for images with neither.** If the image sets neither `central_url` nor `central_hosts`, setup requires the token the hub writes at first boot to `data_dir/setup-token`, printed on the label at the depot. Only someone holding the device can choose the server.

### Offline search covers this facility only, and Capture should say so

Online, a search with `orgUnitMode=ACCESSIBLE` covers the user's whole search scope, often the district. Offline, the hub answers it from this facility's data: refusing it broke registration (item 2 under "Things in the spec"). A patient registered at another clinic therefore comes back as "no results", and the nurse may register a duplicate. Central's uniqueness check on push catches duplicates only through unique attributes such as a national ID, not names.

The hub cannot tell the user on its own. The tracker API has no field for partial results, and the apps believe they are online, because the hub answers `/api/ping` itself. Answering "offline" instead would switch Capture into its own offline mode, which is not wanted either.

**Requirement for Capture web, the app runtime and Android:**

1. A state "connected through a facility hub, central unreachable", read from the hub (`GET /hub/status` reports `"mode": "offline"`; every tracker answer the hub gives itself carries `X-Hub-Mode: offline`, also right after reconnecting while its outbox is pushed). Show it next to the online indicator.
2. On search results in that state, a note: "Searched this facility only. Patients registered elsewhere won't appear." Offline searches that the hub narrowed carry `X-Hub-Scope: facility`. Optionally, before a new registration: "Check online before registering, if possible."

The hub side of this exists; the Capture side is to build.

### Shared metadata cache (REVIEW-2 F4)

Offline, a user whose own copy of an API answer is missing gets another user's copy only for answers that do not depend on who asks: the login page's settings and logo (`loginConfig`, `staticContent`) and the server's language lists (`locales`), besides the apps' static files (item 11 under "Things in the spec"). Everything else is per user, metadata included.

Until 2026-10-22, metadata (programs, stages, option sets, data elements, program rules, org units by ID, system info, the app list and more) was shared too, on the assumption that the same URL gives every user the same answer. It does not: central filters metadata lists by each user's metadata sharing, which combines public, user-group and individual-user sharing, and answers display names in the user's language. The hub does not evaluate sharing, so it cannot know whether another user's copy is right for this one; a user served another's program list could see programs shared only with the other (their metadata, not their data, which the hub always filtered per user) and miss programs of their own. Grouping users by their access list (`access.List.GroupKey`) was considered and not used: that list holds data access to the facility's programs, stages and types, not metadata access or other objects, and not sharing given to individual users on those. The org unit tree, tracked entity filters and working lists were already per user.

The cost, measured on 2026-10-29 (`TestOfflineScreens`, the real Capture and Data Entry apps on 2.43.2, synthetic profile, each user in a fresh browser): the nurse opened 16 screens online (Capture's front page, five programmes' working lists and an event programme's, a dashboard, five registration forms, an event form, two data sets' forms in the Data Entry app); offline she opened all 16 again. Her twin (the same access), who had only logged in online, opened none of them: every app showed its login form, because the global shell's first request (`/api/system/info`) was not in his cache (15 "needs connection"). The restricted nurse, who had opened Capture's front page online once, opened Capture but met 33 "needs connection" over 11 endpoints: working list definitions (`programStageWorkingLists`, `trackedEntityInstanceFilters`, `eventFilters`), the Data Entry app's data sets (`dataSets`, `dataSets/{uid}`), `filledOrganisationUnitLevels`, `configuration/offlineOrganisationUnitLevel`, `organisationUnits/{uid}` and generated attributes' `requiredValues`; three of her working lists, the event list and the second data set's form did not open, and that form left the Data Entry app retrying in a loop until the browser was killed.

What was built (2026-10-29, `internal/proxy/replay.go`): at a staff member's online login in a browser, the hub loads in the background, with their own token, the requests clients of any user made through it in the last 14 days and they have no copy of from the last day, one at a time 200 ms apart, at most 300, once a day per user; the answers go into their own cache. Central answers as them, so metadata sharing and language stay theirs, and nothing is shared between users. Not repeated: data (tracker, aggregate values, analytics, audits, messages, files), generated IDs (`generate` reserves values), central's validation, `/api/me` (kept at login), requests naming another hub user (a user's UID, or a `user` parameter), the hub's own answers, and copies the hub loaded this way (a column marks them), so a request seen once is not repeated for ever. Android logins do not start it: Android Capture keeps its own metadata. With it, on the same run, the hub loaded 58 requests (52 kB) for each of them, in 15 s after their login: settings, the apps menu, programmes, stages' working list definitions, program rules, option sets, org unit levels and groups, the data sets' forms and Capture's data store entries. Offline, the twin and the restricted nurse opened every screen the nurse opened, and the hub answered "needs connection" to none of the three (0 misses, against 15 and 33 before). `TestReplayAtLogin` checks it against the fake central, with the exclusions.

Limits: only what someone opened online through the hub in the last 14 days is loaded, so on a new hub the first staff member opens each screen once (DEPLOYMENT.md, section 3, step 4); a staff member who goes offline within a minute of logging in may not have it all yet; the browser's own caches (Capture's IndexedDB, the Data Entry app's service worker) are separate and fill as each person uses the apps. `TestPerUserMetadataNotShared` still checks that nobody is served another user's copy.

### Long-term TLS

What the POC does: at first boot the hub creates a CA limited by name constraints to its own names and private address ranges, issues a server certificate for `hub.local`, the host name, `localhost` and the LAN IPs, and discards the CA key; devices install `ca.crt` (served at `/hub/ca.crt`). An earlier version made the server certificate itself a CA, so anyone reading its key could have minted certificates for any site. Since 2026-10-19 the hub keeps the authority's key, names itself `dhis2.local`, and issues its own certificates (`internal/hubtls`). The decision for production (`https://dhis2.local` and the hub's authority installed on each device) and the options weighed are in DEPLOYMENT.md, "Reaching the hub securely"; what devices did is under "Devices trusting the hub's certificate" below.

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
11. **Static app files.** Spec 5.1 caches GETs per user. App static files (`/dhis-web-*/…`, `/api/apps/…` with file extensions) hold no user data. Offline, the hub serves them from any user's cache entry, so a user who never opened Capture through the hub online can still load it. API responses stay strictly per user, metadata included, apart from the few that do not depend on who asks ("Shared metadata cache" above).
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

23. **Spec 9.2's plaintext index was a confidentiality flaw.** "Index columns hold UIDs and timestamps only, no patient data" is not true in effect. Program, stage and owner org unit per tracked entity, with dates, say who is enrolled in which program (HIV, TB, ANC) at which facility and when they visited. UIDs are the same as central's and appear on printed records, URLs and exports, so they identify patients. Visibility and outbox rows say which staff see or entered whom, and SQLite keeps deleted rows in free pages. The store is now a plaintext key file (hub ID, mode, central URL, usernames and key slots) and a data file encrypted page by page with AES-256-XTS (WAL and temp files too), under a key derived from the data key; rows keep their AES-GCM sealing. The query index lives in the same encrypted file (docs/PERFORMANCE.md), so its columns are encrypted at rest too. Stores from before the split were moved on their first unlock until the migrations were squashed before the first release (2026-10-21). What still leaks from a stolen disk: usernames, the number and size of pages, and the fact that it is a hub.
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
- **Category option sharing changes reach the hub at the weekly full pull** (`full_pull_interval`). The hub's per-user access list holds org units, user groups and access to programs, stages, tracked entity types and relationship types, re-read hourly and at login; any change there gives that user a full pull at the next sync, so removing a user from a group that granted access takes effect within about an hour (up to two while central caches the user's token login: "Tokens outlive their user", below). Category option sharing is not in the list, and tracker programs rarely use it: a known limitation.
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

When central came back, the hub passed tracker requests through at once, while its outbox was still waiting to be pushed (a random wait of up to a minute, then the push itself). In that window central lacked what was written offline: a person registered offline an hour before was not found, an event for them failed on central, and new writes could reach central before older offline ones. The hub now keeps answering tracker requests itself, and queueing writes behind the offline ones, until every entry it can push has been pushed; entries waiting for their author's online login do not count, or one of them would keep the hub local for good. A push that leaves new entries behind (written while it ran) is followed by another at once. With that, the wait before the first push is invisible to staff and was raised to five minutes, to spread hubs over central after a national outage (`TestReconnectBeforePush`). While staff keep saving faster than the hub pushes, the outbox does not empty and the hub keeps answering locally with the uplink up, until the saving slows down: seen in the adversarial tests of 2026-10-27 (docs/ADVERSARIAL-TESTING.md, D11). **Accepted:** nothing is at risk, and staff notice nothing.

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
- **Still open:** a refused change that can never pass as it is (repair).
- **Resent in bulk** once central is fixed: a hub manager presses "Resend" on a group in the Facility Hub app (docs/DEPLOYMENT.md, "Refused in bulk").


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

## Building the image without mounts

Measured 2026-10-19, building the hub's image from Raspberry Pi OS Lite (trixie, 2026-10-06) in a container without loop devices, FUSE or mount rights (as in many CI containers):

- **The build works without mounting anything:**
  1. cut the partitions out with `dd`;
  2. extract the root file system with `debugfs rdump`;
  3. customise it in a `chroot` (apt works without `/proc`, warning only that `/dev/pts` is missing);
  4. rebuild it with `mkfs.ext4 -d`;
  5. edit the FAT boot partition with mtools;
  6. write a new disk with `sfdisk` and the original disk identifier, so the `PARTUUID`s in `cmdline.txt` and `fstab` still match.
- **`debugfs rdump` drops setuid bits and owners.**
  - It extracts every file owned by the user running it, with modes masked: `sudo` came out 0755 instead of 4755.
  - `image/fixmodes.py` reads every directory's entries back from the ext4 (`debugfs ls -p`, one batch) and restores mode, owner and group: 32 entries in the stock image.
  - Extended attributes, such as file capabilities, are not restored. None were found on the files checked: trixie's `ping` uses ICMP sockets.
- **The stock image sets itself up at first boot**, which a hub must not do: cloud-init with a seed on the boot partition, `userconf-pi` asking for a user, a user `pi`, and SSH. The build removes all of them. Pi OS's own first-boot growth of the root file system (`resize` in `cmdline.txt`) is kept.
- **`proot` crashed in this container** (signal 11, with and without seccomp); a plain `chroot` as root works.

## Tokens outlive their user

Tested on DHIS2 2.42.6 (2026-10-20), with a personal access token of a plain user:

- **Disabling the user, changing their password, or setting their account to expire does not stop the token.** It kept answering `/api/me` and data reads.
- **Nobody but the owner finds the token.** A superuser gets 404 on `GET` and `DELETE /api/apiToken/{id}`, and does not see it in the list.
- **Deleting the user fails** (HTTP 500) while they have tokens; it works once the tokens are deleted.
- **Sharing makes it deletable.** The owner can share it with `POST /api/sharing?type=apiToken&id=…` (sharing in the create request is ignored). A plain member of a group it is shared with (`rw------`) can then delete it, and the token stops at once (401). The list still shows only one's own tokens, so the deleter must know the ID.

So a stolen hub's tokens are its users' access to central, for as long as they live, whatever is done to the users. The hub now:
- shares every token it makes with the hub managers;
- lists the IDs in its registry entry;
- takes its life from the country's setting.

The Facility Hub app's "Revoke all tokens of this hub" deletes them (`internal/core/tokens.go`, `TestTokensSharedWithManagersAndListed`, `TestRevokedHubDropsItsTokens`). Through the hub, a disabled user is still refused within minutes: the hub re-checks Basic credentials with central. 2.43 is to check. (On 2.42.7 a token keeps working for up to an hour: below.)

**Measured again on 2.42.7 (2026-10-27, docs/ADVERSARIAL-TESTING.md B1, B2, B8), with a different result:** central caches a token's login, with the user's access as it was, for one hour after its first use (`ApiTokenAuthManager`, `createApiKeyCache` with `expireAfterWrite(1, HOURS)`; the same in 2.43.2 and in the development branch), and only deleting the token clears it early. Within that hour a disabled user's token, or that of a user removed from the facility, still works with the old access; after it, the token is refused (401). Deleting a user who had created data went through, and their token was then refused. Which of the two 2.42 patches changed this, or whether the 2.42.6 test ran within the hour, was not checked.

What it means for the hub (decided 2026-10-27: accepted, documented):
- Through a token, a change of access on central reaches the hub between at once and an hour later; the hub's hourly access refresh reads with the user's token, so in the worst case the hub shows the old access for up to **two hours**.
- An online login reads access with the user's password: fresh, at once.
- Where it matters (someone leaving on bad terms), deleting the person's tokens on central ends it at once.
- Saves of a user whose token central no longer accepts wait for their online login. If none comes within 7 days (a user disabled or deleted), the hub refuses them itself under the code `NO_LOGIN`, and a hub manager can resend them as another user of the hub (RECOVERY.md, "What already helps").

## Images and files

Checked on 29 October on DHIS2 2.43.2 (the eRegisters instance), with a hub in front: `TestFileValues`, `TestFileSizes`, `TestCaptureWebFiles` and `TestFileUploadOffline` in `internal/live/files_test.go` (`HUB_FILES=1`; they add an IMAGE and a FILE_RESOURCE data element to the primary program's first stage and an IMAGE attribute to the program, and take them out at the end). Central's side was also read in its code (2.41.10.1, 2.42.7.1, 2.43.2.1), the Android side in the SDK (`develop`, 14 October), Capture web's in capture-app (`master`).

**Decided 2026-10-29: a limitation, documented.** Images and files need a connection to be added and to be viewed. The hub keeps no files, and it gets no mechanism for them until a programme it serves needs one (AGENTS.md).

**How central does it:**

- **The file goes first.** A client uploads it (`POST /api/fileResources`, multipart field `file`, domain `DATA_VALUE` by default). Central answers 202 with the file's UID, `storageStatus` PENDING; it is STORED a second or two later. The tracker save then carries that UID as the value.
- **Central checks the UID at the save.**
  - A UID it does not have is refused: E1302 for a data element, E1007 for an attribute.
  - A file already used by another object is refused with E1009.
  - The save marks the file as assigned. The storage status is not checked.
- **A client may choose the UID** (`?uid=`; measured). A second upload with the same UID gets 409 E1119.
- **Limits.**
  - `max.file_upload_size` in dhis.conf, 10 000 000 bytes by default. Measured: 10 000 001 bytes refused with 409.
  - A list of file types: an `.bin` file is refused with E6101.
- **Reads.**
  - The bytes come from `/api/tracker/events/{ev}/dataValues/{de}/file` or `/image?dimension=small|medium|large|original`, and from `/api/tracker/trackedEntities/{te}/attributes/{at}/image`. Central answers 409 while the file is PENDING.
  - `/api/fileResources/{uid}/data` refuses every data value file to everyone (403 E1006), the superuser included.
  - `/api/fileResources/{uid}` gives the name and size (Capture web shows a file's name from it).
- **Files never saved.** Central's daily clean-up (02:00) deleted files not assigned to anything 2 days after their upload. The latest patches (2.41.10.1, 2.42.7.1, 2.43.2.1) no longer do this for data value files (DHIS2-21427). On an earlier patch, a file uploaded online and saved offline more than 2 days later would be refused at push with E1302.

**What the hub does today** (measured, Android client, 3.5 MB PNG and 2 MB PDF):

| Request | Online | Offline |
|---|---|---|
| `POST /api/fileResources` | passed to central: 202 | 503 "needs connection: POST /api/fileResources is not available offline" |
| `GET /api/tracker/…/file`, `…/image` | passed to central, not cached: 200, the same bytes | 503 "needs connection", also for files read online |
| `GET /api/fileResources/{uid}` (name) | passed to central, cached per user | from the cache if the user read it online, else 503 |
| `GET /api/fileResources/{uid}/data` | central's 403 | 503 (a 403 is not cached) |
| A save naming a file uploaded online | central | accepted. At push central takes it and assigns the file |
| A save naming a file central does not have | central refuses it (E1302) | accepted (the hub does not check). At push central refuses it with E1302, kept as a refused change |
| Aggregate `/api/dataValues/file` | passed to central | 409 "needs connection" |

- **Between reconnection and the push**, the hub answers tracker requests itself (`core.PassThrough`), so file reads still get 503 while the hub is online. This usually lasts one cycle.
- **Sizes.** 9 999 000 bytes went through the hub in 1.5 s. Central refused 10 000 001 bytes (409) and a non-allowed type (409 E6101). The hub refuses bodies over 64 MB with 413 (`core.MaxBody`).
- **Memory.** The hub buffers the whole upload and the whole answer of a download (`proxy.Forward`). Only caching has a size limit (64 MB). A download is bounded only by what central stored, which is 10 MB by default. A central whose `max.file_upload_size` is raised raises what one download costs the hub.

**What the clients do with that:**

- **Capture web** uploads a file the moment it is picked: a direct request, outside its offline queue, while the field says "Uploading image".
  - Any failure (offline, 503, 413) shows the same toast, "Async field update failed". The hub's message goes only to the browser console.
  - The field stays empty. The form still saves, without the image, unless the field is mandatory.
  - It shows images from `/api/tracker/…/image?dimension=small` and links files to `…/file`. The hub caches neither, and Capture has no service worker for them, so offline every image is missing, including those seen online.
  - Measured (`TestCaptureWebFiles`, Capture 107.3.1 on 2.43.2, a 3.5 MB PNG). Online through the hub, the event's image showed, and an image picked for a new event uploaded (202) and saved (200). Offline, the same image did not show: its `…/image?dimension=small` got 503. An image picked offline got 503, and Capture showed "Async field update failed" beside an empty "Select image" button. "Save without completing" then saved the event without the image (200). Capture's header still said "Online", since the hub answers.
- **Android Capture** (from the SDK code; to run on the tablet, below) keeps the file on the tablet.
  - The file is compressed unless the program's setting asks for the original.
  - At sync it uploads the file before the tracker data, then swaps its own UID for central's.
  - If the upload fails (the hub offline: 503), only that person or event is left out of the sync and tried again at the next one. The rest syncs, and nothing is lost or nulled.
  - So through an offline hub, records with a new image stay on the tablet until the hub is online again. Until then other devices do not see them.
  - Downloads take images in small or medium size (at most 6 MB) and files at most 6 MB. A 503 is skipped silently, so a tablet that synced from an offline hub has no images.
  - Aggregate file values: the upload has no per-item catch, so a failure probably stops the whole aggregate upload. This is from the code, not run.

**How often programmes use them:** rarely, as far as could be checked.
- eRegisters (Uganda, the hub's first target): none of 5 218 data elements or 40 attributes is IMAGE or FILE_RESOURCE.
- The Sierra Leone demo (play, dev): 11 of 1 062 data elements are FILE_RESOURCE, 3 of them in tracker ("Birth certificate", "Documentation", "TB treatment card"). None is IMAGE, and no attribute is.
- Laos was not checked.

**Options considered:**

1. **A limitation, documented** (chosen 2026-10-29, without the optional change). No hub code.
   - The hub's answers are already clear: 503 "needs connection" for uploads and file reads. Capture web does not queue the upload. Android retries the record at the next sync without blocking the rest.
   - What staff must know: an image or file added offline in Capture web is not saved, and the toast says only "Async field update failed". In the Android app it waits on the tablet until the hub is online.
   - Small optional change, about an hour: pass file reads (`GET /api/tracker/…/file|image`) to central while online with the outbox waiting. They would then work as soon as the uplink is back, as other online reads do.
2. **Offline support.** About 3–5 days with tests, for upload and push alone.
   - The hub would accept `POST /api/fileResources` offline, stream the file to an encrypted file on disk (not into memory), and answer 202 with a UID it chooses.
   - It would queue an outbox entry that comes before the save that refers to the file, then push `POST /api/fileResources?uid=<that UID>` as the author, taking E1119 as "already there".
   - It would serve its own copy offline from `/api/tracker/…/file|image`, in sizes (the hub would have to resize images itself: central makes small, medium and large), and delete the copy once central has the file.
   - Showing images seen online, offline, would be a second mechanism, about 1–2 days: a per-user cache of tracker file reads, tied to who may see the event.
   - Risks: disk on an SD card, at up to 10 MB a file; a file pushed whose save central then refuses (left unassigned on central); the outbox order, file before save, across users.

Not measured: memory with several large downloads at once on a Pi (the 10 MB default bounds each). Android was not run (no tablet attached); the steps are in docs/ADVERSARIAL-TESTING.md, "Images and files on Android".

## Found by adversarial testing (docs/ADVERSARIAL-TESTING.md)

Found on 2026-10-27 by the adversarial scenarios (`internal/adversarial`), run against DHIS2 2.42.7 with the testkit's network switch between the hub and central. The result of each scenario is in ADVERSARIAL-TESTING.md, "Progress".

**Hub bugs, fixed:**

1. **A push answer cut off on its way back was read as a refusal** (D1). Central had stored the saves; the hub recorded them as refused with "central returned 200". A redirect, a 408 or 429 and an answer that is no import report were refused the same way, and a full pull took a 408 or 429 as central withdrawing access and hid the program. These answers now keep the saves for the next cycle, as a 5xx does (`TestPushRetriesAnswersFromTheWay`, `TestAggregatePushRetriesAnswersFromTheWay`, `TestPullRateLimitKeepsAccess`; also review GLM5-F3). An answer that is no import report and never changes (a proxy's 413 page, a firewall's 403 page) would then hold the entry and every save after it, so after a day of such answers the hub refuses the entry with that answer (`TestPushRefusesAfterADayOfOtherAnswers`).
2. **No progress while the link flaps** (D11). Each cycle renewed the hub's token and re-read the catalog and every user's access before it pushed. On a link that dropped every few seconds that never finished: 0 pushes in 90 s. The push now comes right after the registry check, and central decides on each save with whatever the hub knows of access (`TestPushBeforeAccessRefresh`, `TestD11PushProgressWhileFlapping`).
3. **Saves failed with 500 when staff logged in together after a restart** (E1). Each login found the store locked and unlocked it, rebuilding the index while the first saves came in: "UNIQUE constraint failed: objects.slot" (also review GLM5-F34). Unlocks were first made one at a time (`TestConcurrentLoginsAfterRestart`), but a login during an unlock still found the store unlocked before its index was loaded: 5 saves in 10 restarts failed. Logins now wait for an unlock in progress (`Hub.Unlocked`, `TestUnlockedWaitsForAnUnlockInProgress`); E1 then had 0 failures in 10 restarts.
4. **Saves of a disabled or deleted user waited for ever** (B2, B8). Once central refuses the user's token (after its hour of cache, "Tokens outlive their user"), the hub kept their saves for an online login that never comes. After 7 days the hub now refuses them itself under the code `NO_LOGIN`, and a hub manager can resend them as another user of the hub (RECOVERY.md, "What already helps"; `TestAuthorGoneRefusedAfter7Days`, `TestResendAsUnknownUserAppliesNothing`, `TestB2UserDisabled`, `TestB8UserDeleted`).
5. **Saves lost when a good release rolled back** (N5). The new release's health check also required central. With central slow or unreachable as it started, a good release rolled back, and the rollback put back the store copied before the migration. Saves the new release had confirmed in between were lost: 53 registrations in the run of 27 October. The health check now tests the hub itself (its store and its status page). Until the check has passed, the new release refuses saves of its own with the hub's "upgrading" answer (503 with `Retry-After`; 409 for aggregate edits), which clients retry, and it pushes nothing. A rollback then has nothing to lose. On 28 October, with central half-open as the new release started, the release was kept 412 ms after the swap (`TestUpgradeConfirmedWithCentralDown`, `TestUnconfirmedUpgradeTakesNoSaves`, `TestN5HealthCheckFailsAfterMigration`).
6. **A release whose upgrade was cut short was never tried again** (E4; also review ML4). A power cut between the swap and the new release's first start marked the version as failed for good. Every hub runs the one release a country chooses, so a hub hit by one badly timed power cut stayed behind until the supplier published another. Such a version is now tried once more, and skipped after a second cut. A failed health check or migration still skips it at once (`TestUpgradeCutShortKeepsStore`, `TestE4KillDuringUpgrade`).
7. **A hub that needs upgrade refused work online and crashed once a release was chosen** (N13, H3). With saves queued, the hub kept answering tracker requests itself until its outbox was pushed, which it cannot do for a central version it does not support: every read and save got 503 "hub needs upgrade". When the country then chose a release, the upgrade's push of the outbox read central's answer through the version's handling, which is nil, and the hub crashed at every start. Online, such a hub now passes tracker and aggregate requests through to central; offline it answers nothing, as before. The upgrade leaves the queue in the store for the new release to push. On 28 October against 2.43.2 (central's version given as 2.44.0): 249 queued objects kept, an online read passed through, and after the upgrade the outbox drained in 32 s, with 0 of 83 registrations lost (`TestNeedsUpgradePassesThrough`, `TestN13UnsupportedMajor`, `TestH3CentralVersionNotSupported`).
8. **Offline validation showed the answer from before the edit** (I6c, 28 October, 2.43.2). The hub cached `GET /api/validation/dataSet/<ds>?pe&ou` like any other read and replayed it offline. A nurse ran validation online ("No validation alerts"), then offline entered a value that breaks a rule on central (number ≤ integer, 661 against 561, which central's own validation flags). The app showed "No validation alerts for this data." Validation answers are now neither cached nor replayed: offline they get "needs connection" (503). The app tries four times, then shows "Validation could not be run for this data"; completing offline behaves as recorded under "Aggregate Data Entry through the hub" (`TestValidationNeedsConnectionOffline`, `TestDataEntryAppDrafts`).
9. **eRegisters dropped a person the hub served offline** (I7c, 28 October, 2.43.2). A client registered offline on one device was pushed to the hub and served to the second device's offline pull, but that device did not store it, and its search found nothing. The hub's copy of a person written at the hub named the author in `createdBy` and `updatedBy` by uid and username only. Central also gives `firstName` and `surname`, which the app asks for (`createdBy[uid,username,firstName,surname]`). eRegisters 1.1.5 checks pulled records against a schema that requires both (`UserSchema` in `src/schemas.ts`), so the hub's record failed it ("createdBy.firstName: expected string, received undefined") and was dropped. The hub's own copy now names the author with first name and surname, taken from the user's `/api/me` kept at their online login (`tracker.Importer`). After the fix the second device finds the client offline (`TestOfflineAuthorAsCentralNamesIt`, `TestERegisters`).

   The hub passes clients' writes to central unchanged, and it should not change anything between a client and central. This fix is an exception, kept as small as possible: it fills in two fields that central itself adds, in the hub's offline copy only, because eRegisters requires them. What the hub pushes is still exactly what the client sent; the test checks that the push carries no author or times. If more cases like this come up, ask the app to relax its check before the hub fills in anything else.

10. **A person transferred out through the hub and back on central stayed hidden offline** (C4). Central (2.42.7) gives the person a new `updatedAt` at each transfer, but another user's pull of the person type fetched them first, so the program's pull found them current and the hub kept the sibling as their owner, even after a full pull, until the person changed on central. The ownership check now also compares the people the hub holds as owned elsewhere with central's list of the facility's people, fetches again those central lists, and shows them by the local rules (`TestTransferBackOnCentral`, `TestC4TransferredBackOnCentral`). It runs at most once an hour (`ownership_check_interval`), so the person can stay hidden offline for up to an hour after the transfer back.
11. **Logging out while online gave "needs connection"** (hardware session, 28 October, Pi 5 on the demo central). When the browser leaves a page at logout it cancels the app's requests still running; a cancelled read of central's answer was taken for "central unreachable", and the hub went offline for up to 15 seconds and answered the login app from its cache, which did not hold it. A request the client cancelled no longer takes the hub offline; a connection that really drops still does (`TestLogoutOnlineReachesLoginApp`).
12. **A referred-in event opened or completed online was kept as the facility's** (C10, 28 October, 2.42.7). A one-time referral puts an event at the facility while the person stays owned by the referring facility. When the nurse read or saved such an event online through the hub, the hub stored it as the facility's: without its enrollment, an event counted as the facility's when its own org unit was the facility. Offline the hub then listed it among the facility's events, where central lists events by the person's owner and does not (open program). In a protected program, after the nurse broke the glass and completed the referral, the hub served the event offline (200) and listed it, while central answers her 404 for it even with temporary ownership: a leak. An event of a tracker program is now the facility's only when the hub holds its enrollment as the facility's; the event's own org unit counts only in event programs. Offline the referral is now "needs connection", as the person is; an event saved online into an enrollment the hub has not pulled yet appears offline after the next pull (`TestEventOwnedElsewhereReadOnline`, `TestBreakTheGlassEventAlone`, `TestC10OneTimeReferral`, `TestC10ProtectedAfterBreakingTheGlass`).
13. **The facility moved to another district: the old district kept seeing it offline** (A12, 28 October, 2.42.7, a leak). The hub kept the facility's org unit path from set-up, and every "is this facility staff" check uses it. After the facility moved North→South on central, offline the old district's officer saw 5 387 people where central answers 403 (E1006), and the new district's officer was refused. The catalog refresh (hourly, with the access refresh) now reads the facility's path with the hub's token and keeps central's; the access refresh in the same cycle applies it (`TestFacilityMovesDistrict`, `TestA12FacilityMovesDistrict`). Until that refresh, up to an hour after the move, the old path still decides.
14. **A forged "revoked" disabled the hub, and recovering discarded unsent saves** (L3, 29 October, 2.42.7, security: high where the registry entry is unprotected, any member of the hub owners group otherwise). The cycle's registry check and every new token acted on "revoked" in the hub's registry entry without checking it: the hub deleted its own and its users' tokens and stopped, and only a reset that discards unsent saves made it usable again. The hub now takes "revoked" as real only once central refuses its own token (401). The Facility Hub app deletes the tokens before it marks the entry, so a real revocation still takes effect, at the hub's next token (a staff login, or its own token's renewal); a "revoked" read with the hub's working token is logged and ignored. A revocation whose manager could not delete the hub's own token no longer stops the hub (`TestForgedRevokedIgnored`, `TestRevokedHubDropsItsTokens`, `TestRevokedFromAppMakesNoNewTokens`, `TestL3ForgedRegistryRevoked`).
15. **The lockout answered right and wrong guesses differently over Basic auth** (L2, 29 October, 2.42.7, security: medium). During a lockout a wrong Basic guess got 429 at once, and the right password 200 while the hub cached the credential (up to 12 h, the normal state while a tablet is in use): a device on the facility network could test a staff member's password, which is also their central password, about 2 800 times a second. `auth.LoginBasic` checked the throttle only when the cache missed. A cached credential now passes a lockout only from the address it was verified from: from any other address the right password gets 429 as a wrong one does, and the staff member's own tablet goes on working, even when its address is locked out too (`TestBasicLockoutAnswersRightAndWrongAlike`, `TestL2OfflinePasswordGuessing`).
16. **One import central answered 500 to held up everything behind it** (H8, 28 October, 2.42.7). Central answering 500 to every push that carried one registration (as for a payload that trips a bug or a lock) ended each push cycle at that entry and retried it for ever: 0 of 126 other offline registrations reached central in 2 minutes, and with the outbox not empty the hub kept answering online saves itself. The one-day rule for answers that are no import report did not apply. After a plain 500 the push now goes on to the next entry; entries that depend on the failing one wait as before; the failing one is tried each cycle and refused after a day of 500s, with central's answer. A 502, 503 or 504 (central or the way to it down) still ends the cycle (`TestPushGoesOnPastAFailingImport`).
17. **A reshaped import report was read as central refusing everything** (N14, push, 28 October, 2.42.7). With `bundleReport` renamed in central's answer to a push (as a new major might), the hub took any answer with a `status` for an import report and every object it did not find in `bundleReport` as not stored: 102 registrations central had stored were marked refused, and 48 later edits of them refused unsent as blocked. Central answers without `bundleReport` only when it refuses (status ERROR); any other answer without one is now no import report: the saves are tried again, and refused after a day of such answers, as a proxy's page is (`TestPushReshapedReportIsNoReport`).
18. **A deleted hub token was never replaced, though the hub's own account still worked** (G9, 28 October, 2.42.7). The hub replaced its token only near its expiry (`RenewHubToken`), and the account's password was used only then. With the token deleted on central (someone tidying tokens), every housekeeping call got 401: the heartbeat, issue reports and programme list stopped, and the hub page said nothing beyond a terse last sync error. A 401 to the hub's token at the start of a cycle now makes a new one at once, with the account's password as before; when that fails too (no account of its own, or the account disabled), the hub page says the housekeeping fails and points to the take-over. Staff saves were never affected (`TestHubTokenDeletedIsReplacedWithTheAccount`, `TestHubTokenRefusedShownOnTheAdminPage`).
19. **A replaced hub still took aggregate values offline** (G7, 28 October). A hub the registry marks as replaced by another for its facility refuses tracker requests offline ("this hub was replaced"), but the aggregate endpoints did not check it: a value entered offline on the old hub was confirmed (201). Offline the aggregate endpoints now refuse too, with 409 "needs connection" as for their other refusals (a 503 the Data Entry app would queue in the browser); online they pass through as before (`TestAggregateRetiredHubRefuses`).
20. **A release that cannot start was never rolled back** (G2, 28 October). A release that exits at every start before its health check (a panic, a store or port it cannot open) was started again and again by the service manager: 15 starts in 2 minutes, none answered. `upgrade.Recover`, which puts the old store back, runs inside the new binary and left a swapped-in version alone, and the health check that rolls back never ran. Each start of a swapped-in release now counts itself in the upgrade marker, first thing in `serve`; the fourth start without a confirmed upgrade puts the old store and binary back and exits, and the old release reports the failure and skips the version, as after a failed health check (`upgrade.CountStart`, `TestReleaseThatCannotStartRollsBack`, `TestG2NewReleaseFails`). A start that reaches serving while locked resets the count, so power cuts before the first staff login do not roll back a good release, and the rule for an upgrade cut short (tried once more) is unchanged. On Windows the service runs the same `serve`, and its recovery actions restart the hub after every exit. A binary that cannot be executed at all is not counted, but it never gets this far: the upgrade runs the new binary on the store copy (`migrate`) before the swap, and a failure there skips the version.
21. **Android's incremental syncs re-downloaded every person written during an outage** (a week offline at HC IV size, 29 October; docs/PERFORMANCE.md). The hub counted an object written on it as updated after any `updatedAfter`, since a device's watermark may come from central's clock. So each sync during an outage got everyone written since the link went down: from day 2 of the week, 3 000 people (the device's limit of 500 per program), and syncs of 8–12 s instead of 3.4 s. An object written on the hub now counts as updated by the hub's own write time (in central's time zone, also its `updatedAt` in the offline copy), at most a day before `updatedAfter`: the floor central's times already get (`updatedFloor`), for a hub clock that drifted. A sync gets the last day's work. When `updatedAfter` is more than a day ahead of the hub's clock in any time zone, the hub's clock is behind central's (a Pi without a clock battery started with no network), and everything written on the hub counts as updated, as before (`hubClockBehind`; the acceptance test's Android download with a watermark two days ahead). Still open: records written while the clock was behind, once the clock is right again, carry the old times and are left out of incremental syncs until they come back from central with its times; the clock test with the Pi 4 started without network (F1) is to show whether that matters (`hubUpdated`, `TestWeekAndroidSyncGetsTheLastDay`, `TestUpdatedMatchAsReadingEveryChild`, `TestAcceptance`).
21. **Large payloads exhausted the hub's memory** (I8c, 29 October). A 60 MB tracker payload, within the 64 MB body limit, took the hub from 174 to 605 MB; six at once to 5.7 GB, which would kill a Pi 4. An import costs 7 to 15 times its size in memory while the hub decodes it and answers it, online and offline. `/api/tracker` now takes at most 16 MB (`core.MaxTrackerBody`; other paths keep 64 MB, for files); larger bodies get 413 before they are read further. Android Capture (2.41 and later, the SDK's tracker importer) posts all it holds unsynced in one request and cannot split it after a refusal, so the limit leaves a tablet room for a long time offline: a person with an enrollment and three events is about 7 kB, so 16 MB holds some 2 000 people (a first limit of 4 MB, about 500, could have stuck a tablet after a campaign). Bodies above 4 MB are imported one at a time (`core.LargeTrackerBody`), so several large ones at once cost what one does. No cap on objects per import: the bytes already bound them. Measured in process against the fake central (heap in use above the start; the process's RSS includes the test's own copies of the body): one 60 MB payload +942 MB offline before, now refused in 12 ms (+16 MB); six of 60 MB at once ran the test process past 6 GB before, now refused in 48 ms (+58 MB); the largest accepted, about 4 MB, +58 MB for one and +377 MB for six at once offline (`TestTrackerBodyLimit`, `TestI8cLargePayloads`).
22. **A captive portal's answers were kept as central's** (D7, 29 October). A portal or hotel login in front of the internet answers every request until the hub's ping notices (3 to 3.5 s). Meanwhile its 200 page replaced a cached read, which was then served offline, and an aggregate value it swallowed was answered 200, shown offline as saved and never sent. An answer to an `/api/` request that cannot be central's now counts as central unreachable: the hub goes offline, serves the request itself (queues a save, answers a read from its copy) and keeps nothing of the answer. That is an HTML page where the client asked for JSON (the hub asks for JSON when the client names nothing; app-runtime and the Android app do), outside the apps' files under `/api/apps/`, or a redirect to a host that is not central's. Central's files, images and pages under `/api/`, asked for as such, are central's as before (`proxy.notCentral`, `TestPortalAnswersAreNotCentrals`, `TestNotCentralIsNarrow`, `TestD7PortalAnswersKept`, `TestD7CaptivePortal`).
23. **Concurrent online logins of one user left tokens on central** (S1, found in the M1 soak, 29 October). After a restart a user's clients logged in again within the same second, and each login ran `core.EnsurePAT` with no lock per user, so each created a PAT; the hub kept the last and the others stayed valid on central until they expired, unlisted in the registry entry, so the Facility Hub app could not revoke them. `EnsurePAT` now runs under a lock per user and re-reads the stored user under it, so logins of one user within the same second make one token (`TestM1ConcurrentLoginsMakeOneToken`).
24. **A person saved during a full pull was hidden from its author offline until the next pull** (S2, found in the M1 soak, 29 October). A save passed on to central while a full pull listed its program was not in that listing, and `RemoveVisibilityExcept` (`sync.pullGroup`) removed it from its author; reproduced 29 Oct on synthetic 2.42, 6 of 107 people 503 offline after the full pull. `RemoveVisibilityExcept` now compares with the listing's start time and keeps what was updated after it, and the hub stamps the objects it records from online writes with its own time until the next pull gives central's (`TestM1SaveDuringFullPullStaysVisible`, `TestFullPullKeepsSaveMadeDuringItVisible`).
25. **Program expiry was not read** (A10, 28 October): expired events were taken offline and refused at push with E1047. The catalog now holds each program's `expiryPeriodType`, `expiryDays` and `completeEventsExpiryDays`, and the offline import refuses an event dated in an expired period with E1047, letting users with `F_EDIT_EXPIRED` through (`TestA10HubLockFollowsProgramExpiry`, `TestEventProgramExpiryAndReopen`).
26. **A lost authority was not applied offline** (B5, 28 October): reopening a completed event was taken offline and refused at push. The offline import now refuses it with E1083 when the user's stored authorities lack `F_UNCOMPLETE_EVENT`, in the same check as A10's expiry (`TestB5HubRefusesAfterSync`, `TestEventProgramExpiryAndReopen`).
27. **Android's old password worked online through the hub for up to 5 minutes after central changed it** (B3, 28 October): a cached Basic credential is re-checked with central only every 5 minutes (`BasicRecheck`), and a failed web login did not touch it. When central refuses a user's web login (401), the hub now unverifies that user's cached Basic credentials, so the device's next request is checked with central at once; they stay cached, so during a lockout the staff member's own device still passes from its address (L2) (`TestB3OldPasswordOnlineThroughCache`, `TestB3OldPasswordDroppedWhenCentralRefusesWebLogin`).
28. **A reset rewrote the hub's registry entry without the fields the Facility Hub app shows** (G6, N23, 28 October), `tokens` among them. The reset now edits the entry (`EditRegistry`) instead of replacing it, so those fields stay (`TestG6ResetWithRefusedChanges`, `TestResetKeepsRegistryFields`).
29. **A mislabelled or refused release manifest was recorded and fetched every cycle** (G3, 28 October). The upgrader now remembers a refused manifest once per chosen version (`upgrade_refused`) and fetches it no more until the country's release choice changes (its version, pilot hubs or percentage). The manifest is not part of the choice: a republished or re-signed release for the same chosen version is not fetched again (DEPLOYMENT.md, section 2, release key rotation), as it did for unsigned manifests (`TestG3G4ReleasesRefused`).
30. **The hub flapped while name lookup failed** (D6, 28 October; seen on a Pi 5 on 30 October when Firewalla blocked its internet: offline, back online 2 s later, offline for good 44 s after that). The connectivity probe reused a kept-alive connection, so it succeeded while new connections failed. The probe now opens a new connection each time — one TLS handshake per probe, at `ping_interval` while offline (15 s by default) and four times that while online (60 s), since the load on central comes from successful probes only — a failing probe never completes a TLS handshake (B1, decided 2026-11-02; `TestD6DNSFails`, `TestProbeCadenceFollowsTheState`, `TestProbeOpensANewConnectionEachTime`).
31. **A new device was not registered after a locked start** (I9c, 28 October): a pass-through session made while the store was locked was cached for up to 12 hours, so the hub never registered the user once the store unlocked, and offline the device was refused. A pass-through session made while the store is locked is no longer cached; the device's next request after the unlock registers the user (`TestI9cNewDeviceWhileStoreLocked`). While the store is locked, a Basic request from a user the hub has no key slot for is no longer cached either, so each costs one central login until the store unlocks — usually minutes; a known user's login unlocks the store and caches at once.
32. **Forms opened and closed at the exact time on the hub, by the day on central** (F5, 28 October): data input periods were compared to the moment, while central takes values all of the closing day and from the start of the opening day, so on those days the hub refused offline what central takes. `aggregate.InputOpen` now compares the dates (`TestF5InputPeriodPassesWhileOffline`, `TestLockStatus`).
33. **Reserved values ran out offline, and a tablet got at most half the pool per request** (T2/T4, 28 October): the pool was a flat `reserved_pool_size` (default 150 — less than a day at a busy HC IV's ~560 registrations a day), and a request could take at most half of it (75, against Android's own 100 from central). Decided 2026-10-30 (Olav): the pool size is a per-attribute setting managed in the Facility Hub app (`facility-hub-config/reserved`, `{"default": N, "attributes": {"<attrId>": N}}`), with `reserved_pool_size` as the fallback — its default is now 500. The hub refills a pool from central only when it falls below a quarter of its target, up to the target, in requests of at most central's 1000 per call (central's cost is per value, not per request, so fewer bigger requests are fine). A tablet gets up to Android's own 100 values per request when the pool allows it. The heartbeat lists the facility's generated attributes so the app can offer them (`sync.Status`). An explicit 0 for an attribute turns its offline pool off: no refill, and offline requests for it get needs-connection even when leftovers sit in the pool (decided 2026-11-02). The watermark comparison is exact for every target — `have*4 >= target` skips the refill; the division it replaced once truncated the watermark of a target below 4 to 0, so a pool of 1–3 never refilled, even empty. `TestReservedPoolSetting` covers the setting, the watermark (including a target of 3 and an explicit 0) and the 100 cap.

34. **Event lists broke ties on a shared date by UID, central by creation time** (the oracle's "also seen"; decided 2026-10-30 to change the hub — staff see the working list in registration order). Measured on 2.42.7.1: central orders by `<column> <dir>`, then `created_at DESC` (when the column is not createdAt), then `uid DESC`. The hub's pushed-down orders now do the same (`internal/tracker/search.go`), the Go sort path too (`sortObjs`), read-on paging generalizes the sort key to any number of columns with per-column direction (`after`/`afterConds`), and the indexes carry the tie columns (`internal/store/store.go`; no migration — no hub is deployed yet, so a store from before the first release keeps the old indexes; see DEPLOYMENT.md, "Upgrades", "Stores from before the first release"). `TestEventListTieOrderMatchesCentral` covers the orders and paging across a tie boundary, scheduledAt ties included; `TestQueryPlansUseIndexes` and `TestPagesReadOnFromLastPosition` cover the plans and read-on; the live oracle passes. Widened 2026-11-02 (C5/C6): the oracle now compares UID sequences on every ordered query and seeds its own ties, and measured on 2.42 that central ties any order column — scheduledAt and attributes too, not just occurredAt — by `created_at DESC`, so `sortObjs` now appends it for every order whose first key is not createdAt; two divergences are accepted as known gaps (locale collation on the search attribute's order, NULL scheduled dates in a descending paged list).
**Accepted** (decided 2026-10-28):

- **Central restored from a backup** (H4): what central accepted after the backup is lost from central, and the hub does not send it again. Decided 2026-10-28, not a hub bug: once central has accepted data, its backup and recovery are central's; the hub answers for what central has not seen. Saves the hub had not sent when central was restored still reach it (`TestH4CentralRestoredFromBackup`). After its next full pull the hub hides offline the people central no longer has, as for any deletion; an edit made on central and lost in the restore stays on the hub until the record changes again (RECOVERY.md, "Central restored from a backup").
- **Generated values with `ORG_UNIT_CODE` carry the chosen org unit's code** (T1, decided 2026-10-30, a limitation by decision): the hub's reserved pool is filled with the facility's own code (`FacilityCode`), so offline it hands out values made with that code only. A hub set up on an org unit with children — set-up allows choosing any level — would hand those children the facility's code offline. Set the hub up on the facility itself, not a parent unit (DEPLOYMENT.md, section 3, step 3).
- **A person owned elsewhere, opened offline with a protected program.** A person registered at the facility and transferred to another facility on central before the hub's first pull is held by the hub without their enrollment in that program. Opened by UID with the program, the hub answers 200 with no enrollment and no owner in it, and only the person-type attributes; central answers 404. The hub cannot tell "enrolled elsewhere" from "not enrolled", and refusing would stop staff from enrolling a known person offline. Nothing is shown that central does not give the same user without the program (checked on 2.42.7 as all 11 users the oracle flagged); a user who may not see the person at all gets "needs connection". An offline enrollment of that person is refused at push and kept. `TestTransferredBeforeSetup`; the oracle counts it as accepted, not a leak (`shownWithoutProgram`).

**Open** (from physical session A, real devices, 3 November; Pi 5 on the demo central 2.43.2 as Needy CHC, Galaxy Tab A7 Lite with Chrome and Android Capture 3.5.0; screenshots in `windows/out/session-a/`):

- **Refused changes nobody can resolve.** Two offline edits (one from Capture web, one from Android) were refused at push because the event had been deleted on central meanwhile (E1082, "already deleted and cannot be modified"). The hub kept each in its outbox as rejected, listed it on its page and reported it to central (`facility-hub-issues`), where the Facility Hub app shows it. But: the author never learns of it (Capture web shows the event gone, with nothing about the edit; Android Capture had marked the edit synced when the hub confirmed it, and at its next sync removed the event without a word); the hub page is for hub admins; and the Facility Hub app can only Resend a group, which for "already deleted" fails again. So nobody can close it, and it stays in the list for ever. Needed: a plan for who resolves each kind of refusal and how. Options: a Discard action in the Facility Hub app next to Resend (a hub manager closes the entry, the hub drops it and keeps it in its log); for refusals that no correction can fix (E1082 and the like: the target is gone on central), the hub closing them itself after reporting them; and telling the author (the open question "How should rejected data be shown to the user who entered it?", above).
- **An offline edit silently overwrites a newer value on central.** `needy2` set a weight offline at 13:59:20; it was changed on central at 14:00:41; at the push (14:06:43) central took the hub's older value (last write wins, by arrival). Nobody is told. The hub cannot see it without an extra read: central's change came after the hub's last pull, and central's import answer does not give the previous update time. Detecting it would take one request per push batch (the batch's records' `updatedAt`) before pushing. To decide whether it is worth that.
- **The stock Android Capture app cannot use a hub on the image.** It trusts only the system's certificates and Let's Encrypt (FINDINGS, "Which certificate setup does the Android Capture app accept?"): "There is a problem with the server certificates" at `https://dhis2.local`. A test build that also trusts user-installed certificates (`network_security_configuration.xml`, `<certificates src="user"/>`, re-signed with a debug key) connected and synced through the hub. Deployment needs one of the options in DEPLOYMENT.md ("Reaching the hub securely"): a public certificate per hub, the app pinning the hub's key, or managed devices.
- **A tablet a day ahead of central, online and through an offline hub (F4, and the time-zone follow-up above).** With the tablet's date on 4 November and central (UTC) on 3 November, Android Capture took the tablet's today as the enrollment date (Child Programme allows no future dates; the app judges by the device). Online through the hub, central refused the enrollment (E1020) and the app showed it at once as a sync error, "Enrollment date … cannot be a future date", and kept the person unsynced. With the hub offline, the hub confirmed the same record and the app marked it synced; at the push central created the person but refused the enrollment (E1020) and its two events (E5000), and the hub kept and reported them. After its next sync the tablet still shows the person enrolled, with both events, and nothing about the refusal, while central has the person without the enrollment. So the offline path turns an error the app would show into a silent difference between the tablet and central (the refused-changes item above).
- **Capture web shows the hub's "needs connection" as an unknown error.** Offline, a screen not opened online answered 503, which Capture shows as "Error: An unknown error occurred - (503)" (org unit tree and an event program's working list).
- **The Facility Hub app's hub list should be able to show each hub's ID** (asked 3 November): the ID is what the hub's own page, `hub.txt` and the issue keys show, so matching a hub to its row needs it. A column that can be shown or hidden.
- B1 on hardware: a registration pushed 3.5 minutes after its author lost the facility was accepted, through central's one-hour cache of the author's token, as in the sandbox (B1 in docs/ADVERSARIAL-TESTING.md).
- For the M2 finding's proposed fix (pushed objects visible to their author): the author may no longer see what they pushed on central (B1 after the token cache's hour; a one-time referral in a protected program, C10). The fix must give back only what the hub's own visibility check allows, as for online saves, not mark every pushed object visible.

**Open** (from the Android scenarios on a tablet, 28 October; small, not decided):

- ~~**Reserved values with `ORG_UNIT_CODE`**~~ decided 2026-10-30 as a limitation (T1), recorded under "Accepted" above: the pool is made with the chosen org unit's code; set the hub up on the facility itself.
- **No generated values on a user's first login offline**: a user's programs join the hub's catalog at their login, but the pool fills only at the next cycle; if the uplink drops before then, generated attributes are refused (503).
- **A misleading message on a first login offline** (T3): a user the hub has not served through the Android app online sees "The URL provided is not a DHIS2 server." (the cached `/api/system/info` is per user). Decided 2026-10-30: leave it, fix later. The check is done: `/api/system/info` on 2.42 has only server-wide fields plus per-request `userAgent`/`serverDate` — nothing differs per user (checked on a live 2.42.7.1 and in `testdata/2.42/system_info.json`) — so when we do fix it, the hub can serve its own cached copy to any user offline, like it already does for `loginConfig` and `locales`, and Android then shows its usual login failure.


**Open, from wave 3** (28–29 October):

- ~~**Cached metadata replayed offline after central changed it** (A15)~~ **measured 2026-10-30, no fix needed**: with the real apps (Capture web in Chromium, Capture 3.5.0 on a tablet over adb) against a hub whose uplink was cut after central renamed the program, neither app reloads metadata offline — Android made **0** metadata requests on its offline sync and showed its local copy; Capture web on reload made only `GET /api/me` (answered from the hub's per-user cache) and rendered the working list and a registration form from the browser's own cache. No client was shown a form staler than what it had cached online (`TestA15MetadataReloadOffline`).

Also seen, fixed 2026-10-30 (item 34 above): the oracle's first page of an event program's list broke ties in a different order from central (the hub by UID, central by creation time) when many events share a date.

**Open** (from the soak in the sandbox, M1, 29 October, 2.42.7; docs/PERFORMANCE.md, "Ten hours in the sandbox"):

- ~~Not explained: the soak's last full pull, with no saves running, also removed 51 objects for its three users.~~ **Explained 2026-11-03: saves were running.** The soak's 10 hours were timed on the monotonic clock, and the sandbox's VM was suspended for 19 min 51 s during the outage at 06:48 (a gap in the hub's log; the last sample says 10.0 h at 08:01:35, 10 h 20 min after the first). So the writers stopped at 08:01:33 wall time, not 07:41, and the last full pull (07:57–08:00) ran with them saving: 56 saves in it. That is S2 ("Found by adversarial testing" 24), fixed on 29 October, which the soak's tree did not have. Reproduced on the tree before the fix (`5ed88e4^`) with the soak's three writers saving through a full pull (`TestM1FullPullDuringSavesRemovesOnlyWhatCentralHides`): it removed 12, 28 and 22 objects for the admin, the nurse and the restricted user, as the soak's 14, 12 and 25; on this tree, 0 with 102 saves during the full pull; with nothing saved during it (`TestM1FullPullRemovesOnlyWhatCentralHides`), 0 on both trees. Nothing left to fix.

**Open** (from the Pi 5 soak, M1, 2 November; follow-up decided 2026-11-03):

- **Central in another time zone than its facilities refuses today's dates near midnight.** In the soak 398 registrations were refused between midnight and 1 a.m. on the driver's clock (UTC+1) by the demo central (UTC): "Enrollment date … cannot be a future date" (E1020), and their events with them (E5000). Servers are often hosted in another time zone than the facilities, and not always configured for theirs, so real clients will meet this too, online and through the hub. To find out: what the hub does offline with such a date (confirm and have it refused at push, or refuse it itself), whether it judges "future" by central's zone (`MetaServerTimeZone`) or its own, and what Capture web and Android show. A sandbox scenario with the fake central's zone offset (`fakecentral`, `ClockSkew` and its zone) and a client in another zone; then decide. Read from the code (3 November): the hub has no future-date check of its own (`Importer.validateEnrollment` and `validateEvent`; E1020 and E1021 are central's alone), so online the client gets central's 409 at once, and offline the hub confirms the save, central refuses it at push and the hub keeps it as a refused change for its author. Central judges "future" in its own zone, clients date by theirs, so this happens without the hub too. The fake central does not check dates, so a sandbox scenario needs a live instance. Still to see: what Capture web and Android show for such a refusal, online and as a refused change (physical session 4). Decided 2026-11-03: no hub change, and no future-date check in the hub (the save would fail all the same, and a hub clock behind central's would refuse good dates); DEPLOYMENT.md, "Central's time zone", says to set central's server time zone to the facilities' (in a country with several, the easternmost).

**Decided** (from the live suite on synthetic 2.43.2, 3 November):

- **Persons created in the same millisecond list in another order than on 2.43.** The live suite passed on 2.42.7 and failed on 2.43.2 on order alone: 79 answers of `/api/tracker/trackedEntities` with a program, ordered by `createdAt:asc` (58) or by a search attribute ascending (21), list the same people in another order. On 2.43 the seed import created several people in the same millisecond, and central breaks those ties in the order's direction (`createdAt:asc`: SynTe000041, …075, …101 … …230; `:desc` the reverse). The hub breaks every tie by UID descending (`sortObjs`, `simplePlan`), measured on 2.42, where these people were created a few milliseconds apart and no tie arose. Only records created in the same millisecond are affected (imports, bulk creation); nothing is shown that should not be. The same run also found 2.43.2 refusing `order=scheduledAt` on an event program's events (400, "Cannot order by 'scheduledDate'"), which 2.42 and the hub answer; also a known gap, as the hub only answers more. Accepted as a known gap on 3 November, the simpler choice: the oracle (`createdTies`, `internal/live/oracle_test.go`) accepts a createdAt-ordered list only when both lists hold the same people, each sorted by the creation times central gives them. Raw data: `testresults/synthetic-43/oracle.json` in the sandbox clone.

**DHIS2's side, to explore before the hub changes** (decided 2026-10-28):

- **A value into an approved form** (J1, 2.43.2). As the nurse, `POST /api/dataValues` answers 201 while the form is approved (`APPROVED_HERE`); reproduced with curl, without the hub. A value entered offline before the approval therefore lands in approved data when the hub pushes it, with no refusal. The Data Entry app shows the form locked, and after its next sync the hub refuses new offline values (E2017). Whether central should refuse it is to be checked with DHIS2; the hub does not check approval before pushing.
- **An event into a deleted person's enrollment** (C2, 2.42.7). Central accepts a new event into the enrollment of a person it has deleted (soft-deleted), as superuser and as the nurse; reproduced with curl. Through the hub, such an event saved offline is confirmed and seen by nobody. The event also changes the person's `updatedAt`, and once in three runs that closed the issue of the person's refused edit at the next pull, as if someone had fixed it on central. A deletion on central after a refusal is meant to close it; this one came from the event.
- **A million-digit number is confirmed offline, then refused at push and kept** (I8c, accepted as a limitation 2026-10-30): the hub's offline value check does not match central's NUMBER limits. Nothing is lost — the refusal is kept and reported — but the client was told the value was saved. To raise with DHIS2, next to J1 and C2.
- **A queued completion skips validation** (J4, accepted as a limitation 2026-10-30): central's own completion endpoint does not validate, so the hub's offline one does not either; a completion queued offline for a data set that now requires valid completion is refused at push and kept. To raise with DHIS2, next to J1 and C2.

