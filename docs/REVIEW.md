# Critical review: facility hub PoC

Review of the approach (SPEC-facility-hub-poc.md, DEPLOYMENT.md) and the code (~9,700 lines of Go under `cmd/` and `internal/`), with a focus on **security and confidentiality**. Date: 2026-10-07.

**Method.** Static review: all non-test Go sources, templates, deploy files and scripts were read end to end; findings were cross-checked against FINDINGS.md (which documents the live-instance testing). No code was changed. There is no Go toolchain in the review environment, so `go build` / `go test ./...` / `scripts/test-upgrade.sh` were **not run** — the findings below are from reading, not execution. Run the test suite after any fix.

**Overall verdict.** The approach is sound and the code is unusually disciplined for a PoC: fail-closed defaults, per-user PATs, AEAD with AAD everywhere, a locked store, strict parameter allowlists, and a signed upgrade path. The confidentiality posture is strong in the crypto layer and weak in three specific places: **what is stored in plaintext alongside the encrypted payloads** (H1), **what trust is placed in the LAN and in request headers** (M1, M3, M8), and **what happens to error text on its way to central and to admins** (M6). None of the findings are exotic; all are fixable without redesign.

---

## Findings: security and confidentiality

Severity: **High** = patient-data confidentiality or credential capture; **Medium** = confidentiality/integrity/availability with a realistic path; **Low** = hardening.

### H1. Patient search terms and full request URLs are stored in plaintext at rest (High)

The per-user HTTP cache is keyed by the full request URI, including the query string, and that key is stored **unencrypted** in the `http_cache.url` column:

- `internal/proxy/proxy.go:264` — `p.H.Store.PutCache(cacheUser(sess), r.Method, r.URL.RequestURI(), ...)`
- `internal/store/misc.go` — `PutCache` writes `url` as a plain column; only the headers and body are sealed.

Capture web and Android searches put patient identifiers in the query: `GET /api/42/tracker/trackedEntities?filter=<attr>:like:<patient name>&orgUnitMode=ACCESSIBLE...`, and the registration-time uniqueness check sends `filter=<attr>:EQ:<value>`. Every such search made through the hub while online is therefore persisted in cleartext in `hub.db` (and its WAL) for as long as the cache lives — there is no eviction. Spec 9.2 states "Index columns hold UIDs and timestamps only, no patient data"; the URL column breaks that promise. Scenario 9's grep test passes only because it greps for fixture values from the demo database, not for search terms typed by live users.

The plaintext side channel goes further than search terms. A stolen SSD (the DEPLOYMENT theft scenario) yields, without the data key:

| Table | Plaintext content |
|---|---|
| `http_cache.url` | full request URIs, incl. patient search terms and searched values |
| `objects` index columns | the facility's patient UID graph (TE/enrollment/event UIDs, program, stage, owner org unit), `updated_at` timeline, sync state |
| `users` | staff usernames, user UIDs, PAT UIDs and expiry, login/access-refresh times |
| `meta` | hub ID, central URL, server version, last push/pull, upgrade history |
| `outbox`, `pull_state`, `reserved_values` | activity timeline, attribute UIDs |

That is a meaningful confidentiality loss — who the patients are (searched names), the facility's case load over time, and which staff use the hub — and it contradicts the "encrypted at rest" story in the README and DEPLOYMENT ("Patient data on the SSD is encrypted by the hub itself").

**Recommendation.** Key the cache by a hash of (user, method, URL) — or seal the URL column too — and treat the remaining plaintext index as a documented, accepted metadata side channel (or reduce it: the UID graph and timeline are arguably acceptable; search terms are not). Add a regression test that greps the DB for a searched patient name.

### M1. The hub's own origin is taken from the request's Host header (Medium-high)

`proxy.Origin(r)` (`internal/proxy/proxy.go:102`) builds `scheme://r.Host` and is used to:

- rewrite `Location` headers of proxied responses (proxy.go:81),
- rewrite the central base URL inside proxied HTML/JS/JSON, including the app shell's `<meta name="dhis2-base-url">` (proxy.go:88),
- build pager `nextPage`/`prevPage` and tracker job `location` URLs returned offline (`trackerapi.go` `selfURL`/`selfBase`, lines ~265 and ~300),
- decide CSRF safety on admin POSTs (`ui.sameOrigin`, `internal/ui/ui.go:130`, compares `Origin` to `"://"+r.Host`).

An attacker who can control the Host header of a victim's request can make the hub emit *their* origin in all of these. The concrete paths: DNS rebinding (DEPLOYMENT.md section 4 explicitly discusses rebinding protection on facility routers), or any crafted request from the LAN. Because the Android SDK adopts a redirect's `Location` as its new server URL (FINDINGS), a poisoned `Location` can repoint a tablet; a poisoned `dhis2-base-url` makes the web app bypass the hub and talk to the attacker directly. The same trust weakens the admin CSRF check.

**Recommendation.** Derive the hub origin from a configured canonical base URL (set at setup — the address printed on the label) instead of `r.Host`, and reject requests whose Host does not match it.

### M2. Tracker writes accepted during the self-upgrade window are silently lost (Medium-high)

`upgrade.check()` sets `u.H.Upgrading.Store(true)` (upgrade.go:143) with the comment "Block tracker writes from here on, so the store copy is complete". But `Upgrading` is only consulted in one place: `internal/trackerapi/trackerapi.go:75` gates the *online pass-through*. With `Upgrading` set, the request falls through to `serveLocal` → `importLocal` → `PersistImport`, which happily writes the objects and an outbox entry to the **live** store — the same store that `swapStore` then moves aside and replaces with the pre-upgrade copy (upgrade.go:150-190). The client receives a 200 import report; the data never reaches central. The loss window is the store copy plus the migration run of the new binary (up to the 2-minute migration timeout).

Related: `Reset` (`internal/app/app.go:126`) and the upgrade `Drain` both call `Worker.Push` directly, while `Worker.mu` is only held inside `Cycle` (`internal/sync/sync.go:64` vs `:144`) — so pushes can run concurrently with a sync cycle and with each other, double-pushing outbox entries (mostly idempotent under `CREATE_AND_UPDATE`, but it races `FinishOutbox` state transitions).

**Recommendation.** While `Upgrading` is set, refuse tracker writes with 503 ("upgrade in progress") instead of serving them locally. Serialize `Push` with the same mutex `Cycle` uses.

### M3. No rate limiting or lockout on login; offline username enumeration (Medium)

There is no throttling anywhere (grepped: no rate/lockout/attempt-limit code):

- `POST /api/auth/login` (`internal/auth/auth.go:297`) forwards attempts to central; DHIS2 has no lockout either.
- HTTP Basic auth is attempted on **any** request (`Auth.Identify` → `LoginBasic`, auth.go:82). Offline, each miss costs the hub a full Argon2id derivation (~0.3 s at 64 MB, `internal/hubcrypto/hubcrypto.go:27`) — a LAN attacker can brute-force offline passwords and burn hub CPU at the same time.
- Offline, an unknown username answers "needs connection: first login must be online" while a wrong password answers "Bad credentials" (auth.go:41 vs `LoginFailure()`) — valid usernames are enumerable.

**Recommendation.** Per-source and per-account throttling with backoff on both login paths; one generic failure message; a small constant delay on offline failures so the Argon2 cost cannot be parallelized away.

### M4. No session expiry; revocation lag for disabled users and changed passwords (Medium)

- Sessions live until hub restart: `Session.Created` (`internal/core/sessions.go:29`) is recorded but never checked, and the `HUBSESSION` cookie has no `MaxAge`/`Expires`.
- The Basic-auth credential cache (`sessions.go:86-92`) is keyed by a hash of (user, password) and is checked **before** any credential validation. A user who is disabled on central, or whose password was changed there, keeps full hub access — offline reads *and* outbox writes — with the old credential until the hub restarts or the user logs in again with the new password (which replaces the cache entry). PAT revocation is only noticed at push time (401 → `needs_login`, sync.go), never at request time.
- Logout (`HandleLogout`) deletes cookie sessions but not the Basic cache entry.

Spec 8.3 says not to detect revoked *access* offline, but account disablement is a different, cheap-to-check condition — the hub is online at least every sync interval.

**Recommendation.** Add session TTL and idle timeout; while online, revalidate cached Basic credentials each sync cycle (or on a timer) and drop sessions whose user is `needs_login`; make `needs_login` a request-time gate for hub-local tracker data.

### M5. The hub-level token is created once and never renewed (Medium)

The hub token (the setup user's PAT, `internal/ui/setup.go:261`) is created at setup with `pat_expiry_days` (default 90) and **nothing renews it** — `EnsurePAT` renews user PATs, but the hub token is only read via `HubAuth()` (`internal/core/core.go:292`). After 90 days, every cycle's `RefreshServerVersion`, `FetchCatalog`, `ReportIssues`, `RefillPool` and `heartbeat` fail with 401 (all of `internal/sync/sync.go`'s `cycle()`). Data push/pull keeps working (per-user PATs), so this fails silently fleet-wide until someone reads the monitoring app — and FINDINGS already recommends replacing this token with a dedicated hub-system user or OAuth2 client.

**Recommendation.** Renew the hub token on the sync cycle when it approaches expiry (same pattern as `EnsurePAT`), and alert on heartbeat 401s.

### M6. Unredacted upstream error bodies reach the heartbeat, central's data store, and the admin page (Medium)

- `central.HTTPError.Error()` embeds up to 300 characters of central's response body (`internal/central/central.go:128`). It flows into `SyncStats.LastErr` → heartbeat field `lastError` → `PUT /api/dataStore/facility-hub-status/{hubId}` (`internal/sync/sync.go:527`). On 2.42 any user can read any data store namespace by default (FINDINGS), so central error text — which can quote patient values — becomes readable by all users.
- `pushEntry` stores raw central bodies (up to 500 chars) as issue messages (`internal/sync/sync.go:267`, `fmt.Sprintf("central returned %d: %.500s", ...)`). `tracker.Redact` only masks *backticked* non-UID values (`internal/tracker/model.go:229`); a value quoted any other way, or a whole JSON error body, survives into `facility-hub-issues` on central.
- The admin outbox table renders `LastError` raw (`internal/ui/templates/admin.html`), and upstream bodies also reach `slog` via sync warnings.

**Recommendation.** Never put upstream bodies in the heartbeat. Everywhere error text leaves the hub (issue reports, heartbeat, logs), reduce it to error codes + UIDs rather than redacting free text with a regex.

### M7. Reserved-value pool: no per-user cap, no attribute-access check; unbounded offline job map (Medium-low)

- `ServeGenerated` (`internal/trackerapi/trackerapi.go:359`) hands out any `numberToReserve` to any facility user without checking that the attribute is generated or readable by that user. One user can drain the shared pool for everyone; impact is bounded because the pool refills each sync (default 5 min), but it is a cheap nuisance DoS on registration.
- The offline async-import job map (`trackerapi.go:324`) has no expiry sweep (unlike `onlineJobs`, which sweeps after an hour). Import reports — which can contain patient values in error messages — accumulate in memory until restart.

**Recommendation.** Cap `numberToReserve` (e.g. pool size), check attribute readability, and sweep `jobs` periodically.

### M8. Setup trusts any central URL entered on the setup page (Medium)

`setupLogin` (`internal/ui/setup.go:143`) accepts any `central_url`; the setup-user-group check runs against *that* server. During the setup window, whoever reaches the hub first can point it at a hostile DHIS2 instance (the configured group UID can exist on any instance; the hub probes the URL but cannot tell friend from foe). Staff then log in through the hub and their DHIS2 credentials are forwarded to the attacker's server (`auth.LoginWeb` forwards the login verbatim). The image defaults prefill the URL but do not pin it. The setup flow also holds the setup user's plaintext password in memory for up to 15 minutes (`pendingSetup`).

**Recommendation.** Pin `central_url` from the image defaults (or verify it against an allowlisted domain), and document that setup must happen on a trusted LAN. A one-time setup token printed on the device label would close the race entirely.

### M9. Local-object visibility uses capture scope, not search scope (Medium-low, spec-level)

`usable()` (`trackerapi.go:206`) requires the facility in the user's `orgUnits` (capture scope), and `CanRead` for local-origin objects (`internal/tracker/index.go:201`) checks program/stage/TET read + facility ownership but never `SearchOrgUnits` (which the hub stores, `access.List`). A user with capture-but-not-search scope at the facility would see locally created objects offline that central would hide from them. Central-origin objects are safe (visibility only via the user's own pull). Spec 8.2 defines the same rule, so this is a spec gap the PoC inherits — in practice facility staff have both scopes, which is why the e2e tests pass.

**Recommendation.** Add the facility to the viewer's search-scope check for local objects, or document the assumption explicitly.

### L1. Reset races the sync worker (Low)

`Reset` (`internal/app/app.go:126`) pushes and wipes without excluding a concurrent `Cycle`. An in-flight pull can commit rows after `Wipe()`, leaving the re-set-up hub with rows encrypted under the *old* data key; the next `Unlock` then fails on undecryptable rows (index load error) until the file is cleaned manually.

### L2. Relationship writes: no write-access check offline (Low)

`validateRelationship` (`internal/tracker/import.go:384`) only resolves (reads) both ends. Central rejects on push, so this becomes an issue rather than a leak — an integrity/UX gap.

### L3. `createdBy` is forgeable on offline update (Low)

`newObj`/`Merge` (`import.go:179`) let a payload replace top-level `createdBy`; `updatedBy`/`updatedAt` are always the author. Central is authoritative on push, so impact is cosmetic.

### L4. DoS/robustness surface on a 4 GB Pi (Low)

- 256 MB request bodies are fully buffered in RAM (`proxy.go:247`, `trackerapi.go:73`).
- The HTTP server sets only `ReadHeaderTimeout` (`cmd/hub/main.go:99`) — no write/idle timeouts; slow readers hold memory.
- `page`/`pageSize` are unbounded; `page=9223372036854775807&pageSize=2` overflows the page offset and panics (recovered by the middleware → 500) (`internal/tracker/query.go:407`).
- No eviction on `http_cache` — unbounded growth over months.
- The release-channel fetch has no timeout and runs synchronously inside the sync cycle (`internal/upgrade/upgrade.go:220`); a hung channel stalls push/pull until the context ends.
- Argon2id at 64 MB / 2 iterations is a reasonable Pi cost, but it is the main CPU cost per offline login attempt (ties into M3).

### L5. Smaller items

- `filterAttributes` fails **open** when `Catalog == nil` (`query.go:308`) — `h.Catalog()` never returns nil in practice, but the default should be deny.
- The self-signed certificate is `IsCA: true` with cert-sign usage (`cmd/hub/tls.go:39`): anyone who reads `tls.key` can mint certificates trusted by devices that installed the hub cert. SANs snapshot the interface IPs at first boot, so a DHCP change breaks TLS. `tls: false` ships plain HTTP — the documented Android reality, meaning credentials cross the LAN in cleartext.
- Admin authorization uses the *stored* access list (`ui.admin`): a member removed from `setup_user_group` keeps admin access until their next access refresh (up to a sync interval). Spec 8.3 acknowledges staleness generally; for the admin group it deserves an explicit note.
- `sameOrigin` allows an empty `Origin` header; the real CSRF protection is `SameSite=Lax`.
- `NewUID` has modulo bias (`internal/tracker/model.go`) — negligible for UIDs.
- The `eq` filter is case-insensitive (`query.go` `valueFilter.match`) — a fidelity difference from central, not a security issue.
- The proxy forwards the client's `Referer` to central; central already sees the same queries while online, so this is marginal.

---

## Approach review

### What the approach gets right

- **Central stays the source of truth.** The hub is a cache and relay; it never owns metadata and never invents sharing decisions. Visibility for central-origin objects is *derived from what central returned to that user* (`UpsertCentral` adds per-user visibility), which is the strongest possible offline rule short of reimplementing sharing.
- **Fail closed, consistently.** Unknown query parameters → 400; unsupported `orgUnitMode` → 503; deletes refused offline; non-default attribute option combos refused; single-object miss and no-access get the same answer (no existence leak); users outside the facility get 403; the store is locked until a known user logs in. The code matches the spec's "refuse what you cannot decide" principle almost everywhere.
- **Per-user PATs for all data access.** No shared account touches tracker data; outbox entries carry the author's credential, so attribution survives reconnect (verified in FINDINGS scenario 6).
- **The crypto layer is solid.** AES-256-GCM with random nonces and per-row AAD (so ciphertexts cannot be swapped between rows); the data key never touches disk unwrapped; key slots via Argon2id with per-user salts; constant-time password comparison; password hashes are Argon2id-encoded.
- **Upgrade path.** Ed25519-signed manifest + SHA-256 per binary, unsigned releases ignored, migration on a copy, health check, rollback, and the DK hand-over over a pipe so an upgrade does not lock the store. Even a release channel served over plain HTTP is safe, because both the manifest and the binary are authenticated by content, not by transport.
- **Logging discipline.** The request log records parameter *names* only; `SafeQuery` redacts `filter`/`query` values; issue reports and the admin page redact backticked values (see M6 for the gaps).
- **The two spec deviations are justified by evidence.** Answering `orgUnitMode=ACCESSIBLE` from facility data and honoring `atomicMode=ALL` both came with concrete failure stories in FINDINGS (registration blocked; orphaned tracked entities). This is how a PoC should deviate.
- **Proxy hygiene.** Client `Cookie`/`Authorization`/`Origin`/`Host`/`X-Forwarded-For` are stripped toward central; central's `Set-Cookie` never reaches the browser; the hub's own cookie is `HttpOnly`, `Secure` (when TLS), `SameSite=Lax`.

### Where the approach is questionable

1. **"Encrypted at rest" is overstated.** The payloads are, but the surrounding metadata is not, and one column (`http_cache.url`) contains patient search terms (H1). The DEPLOYMENT theft scenario ("the store is unreadable without a staff password") is true for payloads and false for the URL column and the UID graph.
2. **"The LAN is trusted" carries a lot of weight.** Host-header trust (M1), an open setup page that accepts any central URL (M8), no login throttling (M3), and plain HTTP for Android (FINDINGS) all assume an attacker-free LAN. DEPLOYMENT's own network cases (Wi-Fi client isolation, guest networks, DNS rebinding) show facility LANs are shared and imperfectly controlled. Each of these should be closed individually rather than covered by a general assumption.
3. **Offline revocation is weaker than it needs to be.** The spec rules out re-evaluating fine-grained access offline (8.3), but account disablement and password changes are detectable while online at negligible cost (M4). As written, a disabled user's tablet keeps reading and writing until the hub restarts.
4. **The hub-level token is a known shortcut with a sharpened edge.** FINDINGS already flags the setup user's PAT dying with that user; the code adds a second failure mode — it is never renewed, so it dies after `pat_expiry_days` even if the user stays (M5). The dedicated hub-system user or OAuth2 client from FINDINGS should be treated as a requirement, not an option, and the `facility-hub-*` namespaces must be protected (FINDINGS notes any user can write them by default — combined with M6, an unprotected namespace plus leaked error text is a real exposure).
5. **Key escrow is unsolved** (FINDINGS): if every key-slot holder's password changes while the hub is offline or locked, the data key and all unsynced data are lost. Acceptable for a PoC; it should be a stated non-goal with a documented recovery path (re-setup loses the outbox unless it was pushed).
6. **The hub necessarily holds all patient data in plaintext in memory** (index after unlock). A compromised *running* hub yields everything; DEPLOYMENT acknowledges the powered-on theft case. Worth stating as an accepted risk with the mitigations that exist (locked store at boot, no swap use, DK lifetime = process).
7. **Android hand-over latency** (30-minute sync) is a documented UX limitation, not a security issue.

### Confidentiality-specific observations on the data flow

- **Client → hub:** credentials (web login, Basic) cross the LAN; protected only by the self-signed TLS or by nothing when `tls: false` (the Android case). This is the weakest transport link and is a deployment decision more than a code one — the ACME/DNS-01 design in DEPLOYMENT section 4 is the right target.
- **Hub → central:** per-user PATs, TLS verification on (default transport), no redirect following. Good. The hub does not add `X-Forwarded-For`, so central cannot see client IPs — privacy-positive.
- **Hub → release channel:** content-authenticated (signature + checksum). Good; only the missing timeout is a problem (L4).
- **At rest:** payloads sealed; index/URL metadata plaintext (H1).
- **Hub → central data store (issues, heartbeat, registry):** redaction is regex-based and incomplete (M6); the namespaces are writable by any user by default until protected.
- **Logs:** request log is parameter-names-only (good); `slog` gets `SafeQuery` (good) but also raw upstream error bodies via wrapped errors (M6).

---

## Suggested fix order

1. H1 — seal or hash the cache key; add the DB-grep regression test for a searched name.
2. M1 — canonical hub origin from config; stop trusting `r.Host`.
3. M2 — refuse tracker writes while `Upgrading` is set; serialize `Push`.
4. M6 — strip upstream bodies from heartbeat/issues/logs; ship error codes, not free text.
5. M4 — session TTL; revalidate Basic credentials while online.
6. M5 — renew the hub token on the sync cycle.
7. M3 — login throttling + generic offline failure message.
8. M8 — pin the central URL at setup.
9. M7, M9, L1–L5 — as time allows.

## Verification status

Not run in this environment (no Go toolchain): `go build ./...`, `go test ./...` (scenarios 1–10), `scripts/test-upgrade.sh` (scenario 11). The findings are from static review; the e2e suite in `internal/e2e` covers the acceptance scenarios but does not cover H1, M2, M4, M5 or M6 — regression tests for those would be new.
