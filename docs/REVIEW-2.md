# Critical review of the facility hub PoC — second review

An independent re-review of the approach (SPEC-facility-hub-poc.md, DEPLOYMENT.md) and the code (~11,400 lines of Go under `cmd/` and `internal/`), focused on **security and confidentiality**. Date: 2026-10-08. This is the second review: the first (REVIEW.md, 2026-10-07) found H1–M9, L1–L5; its findings were fixed and its response is recorded in REVIEW.md. This review therefore does two things: **verifies those fixes in the code as it stands**, and reports **what remains and what is new**.

**Method.** Static review: all non-test Go sources were read end to end (hubcrypto, store, auth, core, proxy, tracker, trackerapi, sync, upgrade, ui, central, access, config, app, cmd), plus the templates, deploy files and scripts, cross-checked against FINDINGS.md. No code was changed. There is no Go toolchain in the review environment, so `go build` / `go test ./...` / `scripts/test-upgrade.sh` were **not run**; per-package reading is the only verification here. Where the previous review's regression tests exist (`internal/e2e/security_test.go`, `internal/store/store_test.go`), I read them to confirm the fix is exercised, not just present.

**Overall verdict.** The first round of fixes is real and mostly well-targeted (see "Fix verification"). The confidentiality core of the PoC is now strong: the entire data file is AES-256-XTS encrypted with per-row AES-GCM on top, visibility for central-origin data is derived from what each user's own pull returned, outbound error text is reduced to codes, and login is throttled with a uniform offline failure. What remains clusters in four places: **the setup flow still has no protection against a first-reacher or a cross-site post** (F1), **the release channel can still write persistent state with unsigned content** (F2), **a stuck outbox entry permanently blocks the hub's own security updates** (F3), and **the cross-user "shared metadata" cache leaks user-dependent central responses and can be seeded by non-facility accounts** (F4). None of these require a redesign; all are fixable in place. At the approach level, the honest statement is: the crypto protects against theft at rest, but *nothing* protects the facility's data against one weak staff password or a drift between the hub's re-implemented DHIS2 semantics and central's — those are the two residual risks to carry forward explicitly.

---

## Part 1 — New and remaining findings

Severity: **High** = patient-data confidentiality or credential capture; **Medium** = confidentiality/integrity/availability with a realistic path; **Low** = hardening.

### F1. The setup flow is still an unauthenticated, CSRF-unprotected front door (Medium)

Three separate weaknesses compound in `internal/ui/setup.go` and `internal/app/app.go`:

1. **First-reacher race (acknowledged in the first review's response, still open).** In setup mode, anyone on the LAN can load `/hub/setup` and submit step `login` with any `https://` central URL — unless the image pins `central_url`. The setup-group check (`SetupUserGroup` UID) runs against *that* server, and the attacker chooses the server, so it passes on any DHIS2 the attacker controls (they create a user group with the configured UID there). `completeSetup` then pins the hub to the attacker's server, and every later staff login through the hub is forwarded to it verbatim (`auth.LoginWeb` sends the raw login body; `internal/auth/auth.go:100`). The `central_url` pin fixes this only for images that set it; a country image with an empty `central_url` — which the spec explicitly allows ("if empty, asked at setup") — keeps the hole.
2. **No CSRF protection on setup POSTs.** `sameOrigin` (`internal/ui/ui.go:130`) is wired only into `u.admin(...)` handlers. `/hub/setup` accepts cross-site form posts. The multi-step nonce stops a blind attacker from *completing* setup cross-site (they cannot read the nonce out of a cross-origin response), but a single cross-site `step=login` post with the attacker's URL and credentials still calls `h.Central.SetBaseURL(attacker)` (`setup.go:143`), and the next legitimate `GET /hub/setup` renders **that URL prefilled** (`setup.go:98`, `firstNonEmpty(h.Cfg.CentralURL, h.Central.BaseURL())`). The installer who does not read the prefill carefully completes setup against the attacker's server with their own credentials. Any staff member's browser on the LAN is the vector; no LAN access needed.
3. **The setup user's plaintext password sits in memory up to 15 minutes** in `pendingSetup` (acknowledged before; unchanged).

**Recommendation.** The one-time setup token printed on the device label (already suggested in the first review's response) closes 1 and 2 at once: no token, no setup steps at all. Independently: apply the same `sameOrigin` check to every setup POST, render the prefilled URL only from `h.Cfg.CentralURL` (not from the mutable client state), and shorten `pendingSetup.Expires`.

### F2. Unsigned release-manifest content is trusted for persistent side effects (Medium)

`fetchManifest` (`internal/upgrade/upgrade.go:229`) parses `release.json` **before** verifying the signature, and on a missing or invalid signature it:

- writes the unsigned manifest's `Version` string into the **plaintext key file** (`SetMeta("upgrade_unsigned", m.Version)`, unlimited length — the channel response is read with `io.LimitReader(resp.Body, 512<<20)`, so a hostile channel can push hundreds of MB into RAM on a 4 GB Pi and then into a meta row), and
- records it as `lastUpgrade: {result: ignored, to: <unsigned version>}` (`u.record` → `MetaUpgrade`), which the heartbeat writes to central's data store — attacker-controlled strings flowing hub → central fleet monitoring.

The signature check correctly gates the *upgrade*, so this is not code execution — but the channel is documented as servable over plain HTTP ("even a release channel served over plain HTTP is safe, because both the manifest and the binary are authenticated by content"), and this is the one place where unauthenticated content is not discarded but *acted upon*. The same read size also applies to the (signed-path) binary download, which is fine, but the manifest should never be larger than a few KB.

**Recommendation.** On signature failure: log a fixed message ("release channel served an unsigned manifest"), do not parse-and-store anything from it, and drop the `upgrade_unsigned` bookkeeping (or key it on the manifest hash, not the attacker's chosen string). Cap the manifest read at ~64 KB.

### F3. One stuck outbox entry blocks every future security update (Medium)

`upgrade.check()` (`internal/upgrade/upgrade.go:142`) refuses to proceed while `Drain()` reports pending entries: `if err != nil || pending > 0 { return ... }`. The outbox holds entries as pending whenever their author cannot be authenticated — the canonical case is a staff member who left and whose PAT central now refuses (401 → `needs_login`, `internal/sync/sync.go:243`; the user can never log in online again). Such an entry is **permanently pending**, so the hub can never self-upgrade again — not for features, and not for security fixes — and there is no escalation: the failure surfaces only as a `slog.Warn("upgrade check")` line. Spec 12a says "push the outbox until empty (or until only rejected entries remain)", and the implementation is faithful, but the design has no exit for entries that will never push. Since self-upgrade is the hub's *only* patch channel (no one visits facilities to upgrade hubs), this converts an ordinary staffing event into a silent, fleet-wide patching freeze.

**Recommendation.** After the outbox has had pending entries for N days (or M failed cycles), proceed with the upgrade anyway and surface "outbox stuck" in `/hub/status` and the heartbeat as its own alarm. Consider an admin action to cancel or hand over an entry whose author is gone.

### F4. The shared metadata cache serves user-dependent central responses across users, and anyone with a DHIS2 account can seed it (Medium)

Offline, when a logged-in user's own cache misses, the proxy falls back to *any* user's cached copy for the `sharedPrefixes` paths (`internal/proxy/proxy.go`, `shared()` / `GetCacheAnyUser`, which picks `ORDER BY fetched_at DESC` — the most recent fetcher wins). FINDINGS item 11 states the intent: "API responses stay strictly per user" except static app files. Two problems:

1. **The shared list contains endpoints whose central responses are per-user.** `organisationUnits` is the clearest case: Android's metadata sync fetches the user's own capture scope through this endpoint, and the response differs per user. `trackedEntityInstanceFilters` and `programStageWorkingLists` are also sharing-configurable. A facility user who never fetched a path online (new device, new account) receives, offline, whatever the most recent fetcher saw — potentially another user's org-unit subtree or working lists.
2. **The seeders are not limited to facility staff.** Nothing at login restricts hub registration to the facility: `registerOnline` → `ensureKeySlot` → `writeSlot` creates a key slot, session, PAT and access list for **any** central user who logs in through the hub (`internal/auth/auth.go`); only hub-local *tracker* calls are gated by `usable()`'s facility check (`internal/trackerapi/trackerapi.go:206`). So a district officer covering many org units — or `outsider_d`, exactly the account scenario 5 is about — can log in online through the hub, fetch `/api/organisationUnits?…` (identical URL shape to Android's), and become the most-recent fetcher whose copy facility staff get offline. It also means the hub stores PATs and password-derived key slots for accounts that have no business relationship with the facility (see F5).

The impact is bounded (org-unit structures and metadata, not patient payloads), but it is a cross-user disclosure the design claims not to make, and it is the one place where the "fail closed / strictly per user" principle is bent without the documentation that, say, `orgUnitMode=ACCESSIBLE` got in FINDINGS.

**Recommendation.** Remove the user-dependent prefixes (`organisationUnits`, `trackedEntityInstanceFilters`, `programStageWorkingLists`) from `sharedPrefixes` — a user who has not fetched them online gets "needs connection", which is the documented default behaviour for everything else. Alternatively keep them but only fall back to copies fetched *by users with the facility in scope*.

### F5. Any central user can register on the hub, not just facility staff (Medium-low)

Consequence of the login path described in F4: a non-facility user gets a key slot (offline password verifier over the facility-wide data key), a stored PAT, and pass-through use of the hub. Three costs:

- **Confidentiality surface at rest:** every slot is an offline guessing oracle for a *central* password, and the hub now holds slots for accounts with no facility relationship. `PruneKeySlots` mitigates by age only.
- **Cache seeding** (F4).
- **Proxy use:** the hub becomes an unauthenticated-ish relay between arbitrary accounts and central from the facility LAN — central sees the hub's IP, and its per-IP rate limits or audit see one source for many users.

Outsider access to *tracker data* is correctly refused (`usable()`), and central still enforces sharing on every pass-through call, so this is surface, not a patient-data leak.

**Recommendation.** At online login, refuse registration (no key slot, no PAT storage; pass through with a `UserID == 0` session as the locked-store path already does) for users without the facility in their org-unit scope. Keep the 403 answer for hub-local tracker calls.

### F6. Patient UIDs are written in plaintext outside the encrypted store (Low)

With `log_requests: true`, two sinks record `r.URL.Path` unencrypted:

- `data_dir/requests.log` (`internal/core/sessions.go`, `RequestLog.Record` writes the templated path — good — but `logRequests` in `internal/app/app.go` logs the **raw** path via slog, and single-object tracker reads carry tracked-entity UIDs in the path);
- journald/stderr, unbounded retention by default.

FINDINGS item 23 argues — correctly, I think — that tracker UIDs identify patients (they appear on printed records and exports). The store was fixed for exactly this class of leak; the request log was not. It is config-gated (default off) and the request-log file properly records only `Template(path)` (parameter names, UID positions masked); the `slog` path is the outlier.

**Recommendation.** Use `core.Template(r.URL.Path)` in the slog line too; document retention for `requests.log` (and consider putting it in the encrypted store or rotating it).

### F7. `hub_hosts: "*"` silently disables the Host-header check (Low)

`HostAllowed` returns true for any host when the config lists `"*"` (`internal/core/hosts.go:33`). The comment says "for development only", but nothing stops a country image from shipping it — and a single `hub_hosts: "*"` line reopens the whole M1 class (origin rewriting in proxied responses, pager/job URLs, the admin CSRF check's `r.Host` comparison) for every hub in the country.

**Recommendation.** Reject `"*"` at config load, or downgrade it to a boot-time refusal with a loud log line.

### F8. The password-escrow fallback keeps users' central passwords at rest (Low)

`SetPassword` / `Password` (`internal/store/users.go:196-214`) store a user's plaintext central password, sealed under the data key, as the fallback for when PAT creation is impossible (spec 7.1). Sealed is fine, but this is the highest-value secret the hub could hold (central passwords are reused elsewhere by staff, and the sealed blob is only as protected as the facility-wide DK — see the approach section). FINDINGS records that on 2.42 the fallback was never needed.

**Recommendation.** Keep it out of the default build path: gate it behind an explicit config flag, or drop it now that 2.42 always allows PAT creation, and fail login instead when no PAT can be created.

### F9. Smaller items (Low)

- **2FA accounts degrade to 1FA offline** (FINDINGS 13, inherent): offline login checks the password only. Consider excluding 2FA-enabled accounts from key-slot creation, or documenting the risk centrally.
- **A retired hub never notices** (FINDINGS 16, to build): after "replace hub", the old device keeps serving its full facility copy offline to every key-slot holder, indefinitely, with no check of its registry entry. Until the registry check exists, "retired" is an accounting state, not an enforcement one; the reset/retire flow should at least be part of the replacement checklist.
- **No CSP / `X-Frame-Options` / `X-Content-Type-Options` on hub HTML pages.** `sameOrigin` plus `SameSite=Lax` and html/template's escaping make the practical risk small, but the headers are cheap.
- **`sameOrigin` accepts an empty `Origin` *and* an empty `Sec-Fetch-Site`** (`internal/ui/ui.go:130`). Browsers send `Sec-Fetch-Site` on every request today; tightening to require a same-origin value would close legacy-client edge cases. Empty `Origin` on a *form* POST is the bigger relaxation — it means pre-2017 browsers and some toolkits bypass the check; the cookie's `SameSite=Lax` is then the only guard.
- **AES-256-XTS gives confidentiality, not integrity,** for the data file's index columns; the GCM layer covers only the sealed blobs. An attacker with disk *write* access can corrupt rows undetected (DoS at minimum). Acceptable — an attacker with write access to the powered-off hub's disk is already game over for availability, and read access alone yields nothing — but it should be stated in DEPLOYMENT's threat model rather than left implicit.
- **`Monitor.Probe` treats any status < 500 as "online"** (`internal/central/central.go:270`): a captive portal answers 200, the hub believes it is online, upstream calls fail and flip it back. Self-correcting, but a stricter probe (compare the body to "pong", or require the DHIS2 version endpoint) would avoid the oscillation.
- **The setup nonce and tracker job IDs are `NewUID()`** — 11 chars, unbiased rejection sampling (verified). Fine for the purpose, not for anything longer-lived.

---

## Part 2 — Verification of the first review's fixes

I read each fix in the code rather than trusting the response table. Summary: all claimed fixes are present and most are exercised by `internal/e2e/security_test.go`. Details worth recording:

| Finding | Status in code |
|---|---|
| H1 (plaintext index) | **Fixed as described.** Store split into a plaintext key file (identity, mode, central URL, usernames, key slots) and an AES-256-XTS-encrypted data file (`internal/store/store.go`, `vfs/xts`), with per-row AES-GCM under the data key retained for values (AAD binds row context; `objAAD`, `cacheAAD`, `outbox:%d` etc.). `secure_delete(1)` and the `moveLegacy` + `vacuumKeys` path handle the old single-file stores. Residual plaintext is as documented: usernames, hub identity, page counts. |
| M1 (Host trust) | **Fixed.** `HostAllowed` (own names, interface addresses re-read each minute, `hub_hosts`) returns 421 otherwise (`internal/app/app.go`); responses are cached unrewritten and localized per request (`proxy.Localize`). F7 above is the remaining foot-gun. |
| M2 (writes during upgrade) | **Fixed.** `BeginLocalWrite`/`FreezeLocalWrites` gate offline imports and pool hand-outs with 503 + `Retry-After`; verified by `TestUpgradeFreezesLocalWrites`. Online writes still pass through (central holds them), which is correct. |
| M3 (throttling) | **Fixed.** Per user+address (5), address (20), user (50) in a 15-minute window with doubling lockout; constant-time dummy Argon2 for unknown users; one failure message offline; two concurrent derivations max. Map eviction bounds memory. |
| M4 (sessions/revocation) | **Fixed.** Idle/max expiry with cookie `MaxAge`; cached Basic credentials re-checked against central every 5 minutes while online (`BasicRecheck`); a refused credential drops all sessions and sets `needs_login`, which `Identify`/`mustLogInAgain` enforce at request time; logout drops the Basic entry. |
| M5 (hub token) | **Fixed.** `RenewHubToken` runs each cycle, renews within 7 days of expiry via the token itself, revokes the old UID; `TestHubTokenRenewal` covers it. The dedicated hub-system user remains the right production answer. |
| M6 (error text) | **Fixed.** `HTTPError.Error()` carries status + errorCode only; `OutgoingMessage`/`OutgoingReports` reduce issue reports to codes/UIDs; the heartbeat's `lastError` is now built from those messages. The admin page still shows the full stored message with backticked values masked (`tracker.Redact`) — hub-side only, acceptable. |
| M7 (pool/jobs) | **Fixed.** Generated-attribute readability check, half-pool cap per request, 1-hour TTL on offline job reports (`jobTTL`), online job payloads swept after an hour. |
| M8 (central URL) | **Partially fixed — see F1.** Pinned when the image sets `central_url`; free-entry https URL otherwise; no setup token. |
| M9 (search scope) | **Fixed.** `CanRead` for local objects requires the facility in the viewer's effective search scope (`Viewer.SearchScope`, `access.SearchesIn`). |
| L1–L5 | Fixed where claimed (reset excludes cycles; `createdAt`/`createdBy` preserved; page overflow guarded before multiply; 64 MB body cap with 413; read/idle timeouts; release-channel timeout; 30-day cache pruning; name-constrained CA with discarded key; `Sec-Fetch-Site` check; `Referer` not forwarded; unbiased `NewUID`). Documented-as-accepted where claimed (relationship write access; admin staleness; `eq` case sensitivity). |

The one place I would push back on the response's own assessment: for M1 it says the "real path was cache poisoning" and the fix is unrewritten caching — correct, but with `hub_hosts: "*"` or a future refactor that reintroduces request-time rewriting before caching, the class returns. A regression test asserting that no stored cache row contains the requesting Host would pin the invariant better than the current Host tests alone.

---

## Part 3 — Approach review

### What the approach gets right (and should keep)

- **Per-user visibility derived from central's own answers** remains the strongest idea in the design. The hub does not re-implement DHIS2 sharing for pulled data; it records what each user's own pull returned (`UpsertCentral` → visibility sets; `CanRead` for central-origin objects is membership in that set plus facility ownership). Every drift risk below concerns *local* objects and *metadata*, not this core rule.
- **Fail closed, with two documented exceptions.** Unknown parameters, unsupported modes, deletes, non-default category option combos, single-object misses and no-access all refuse identically. The two bends — `orgUnitMode=ACCESSIBLE` served from facility data, and the shared metadata cache — differ in documentation quality: the first is in FINDINGS with evidence; the second (F4) is not.
- **The crypto layer is genuinely good for a PoC:** one random DK, never on disk unwrapped; per-user Argon2id key slots (64 MB / 2 iterations is a defensible Pi cost); AES-GCM with AAD binding every sealed row to its context (row-swap attacks fail); HKDF-derived XTS file key; no password hash next to the slot (the slot *is* the verifier); constrained CA with discarded key.
- **Outbound discipline:** issues and heartbeats carry codes and UIDs only; the request log records parameter names only; `Redact` guards the admin page; `SafeQuery` guards slog.
- **Upgrade path:** content-authenticated releases (ed25519 manifest + SHA-256 per binary), migration on a copy, marker-file state machine, health check with rollback, DK hand-over via inherited pipe so an upgrade does not lock the store. The design is careful; its weak spot is F2 (unsigned content) and F3 (the outbox precondition).

### The two fundamental risks the PoC should carry forward explicitly

1. **The facility-wide data key means the weakest staff password defines the theft boundary.** Any key slot unwraps the DK that decrypts *all* users' data; whoever holds the SSD can guess passwords offline against every slot in the plaintext key file, and a cracked password is that user's *central* password. Argon2id slows this; it does not stop it. DEPLOYMENT already recommends a TPM-mixed device secret; for a national rollout that should be treated as a requirement, not an option, alongside central password policy enforcement. Relatedly: a single successful login puts the *entire* facility dataset in plaintext in RAM (index after unlock). That is inherent to the design and fine — but it means the hub's own runtime is now a higher-value target than the SSD, which the deployment threat model should say out loud.
2. **The hub re-implements a slice of DHIS2 semantics, and every drift is a potential over-exposure.** Attribute-level readability, stage-vs-program check order, `orgUnitMode` meanings, `eq` case sensitivity, date normalization — the PoC handles each as a fixture-verified behaviour (`internal/compat` isolates version shapes, which is the right structure), but each is also a place where the hub's answer can differ from central's in a way that shows a user something central would not. The spec's two-version support promise doubles this surface per DHIS2 release. The mitigations that exist — visibility-from-pull for reads, central-authoritative writes, per-version compat files, the endpoint log for unsupported parameters — are the right ones; the risk to manage is *maintenance*, and the honest conclusion of this PoC is that the tracker read/write surface (including `organisationUnits`-shaped metadata) is tractable, while a full "DHIS2 but offline" would not be. The scope discipline in spec section 3 is what makes the approach viable; it must survive into production.

### Residual trust assumptions, ranked by how much weight they carry

1. **The LAN is attacker-free for transport.** Plain HTTP is the documented Android reality (the app does not trust user CAs), so Basic credentials cross the facility LAN in cleartext until the ACME/DNS-01 design in DEPLOYMENT section 4 exists. This is the single largest confidentiality exposure in the *deployed* system, and it is a deployment gap, not a code one. `tls: true` default + managed devices mitigates for web.
2. **Setup happens before an attacker finds the hub.** F1. The setup token removes the assumption.
3. **Central's data-store namespaces get protected.** `facility-hub-*` are writable by any user on default 2.42 (FINDINGS). Until namespace sharing is set, the registry can be forged, hubs retired by third parties, and heartbeats/issue reports read by everyone. This is a hard production prerequisite, already flagged in DEPLOYMENT — keep it at the top of the rollout checklist, because a forged `facility-hub-registry` entry ("retire this hub", or "another hub is active for this facility") is a remote availability attack on any hub.
4. **Facility staff are mutually trusted.** The hub's visibility rules are the only thing between nurse A and nurse B's patients, and they rest on stored per-user access refreshed every sync (stale up to `sync_interval` offline — spec 8.3, accepted). Admin membership staleness is the sharper edge (a removed setup-group member keeps the admin page until their next access refresh); consider refreshing access for admin-group members more eagerly while online.

### Confidentiality of the data flow, end to end (as the code stands)

| Link | Protection | Gap |
|---|---|---|
| Client → hub | TLS (default, self-signed constrained CA; Lax, HttpOnly session cookie) | Plain HTTP for stock Android (deployment); setup flow (F1) |
| Hub → central | Per-user PATs, no redirect following, no `X-Forwarded-For`, standard verification | PAT fallback stores the password (F8); hub-level token for metadata/issues (known, documented) |
| At rest | XTS file + GCM rows, AAD-bound; locked at boot; slots pruned; WAL/temp files encrypted | Usernames + hub identity in the key file (accepted); UIDs in logs (F6); XTS has no integrity (F9, state it) |
| Hub → central data store | Codes and UIDs only, redaction upstream of the wire | Namespaces default-open on 2.42 (deployment prerequisite) |
| Hub → release channel | Signed manifest + checksum, rollback, skip-after-failure | Unsigned content persists state (F2); stuck outbox blocks updates (F3) |
| Logs | Parameter names only; `SafeQuery`; `Redact` on admin | Raw path in slog with `log_requests` (F6) |

---

## Suggested fix order

1. F1 — one-time setup token on the label; `sameOrigin` on setup POSTs; prefill only from image config.
2. F4 + F5 — drop user-dependent prefixes from the shared cache; refuse hub registration for non-facility users.
3. F3 — escape valve for a stuck outbox (proceed after N days) plus status/heartbeat alarm.
4. F2 — discard unsigned manifests entirely; cap manifest size.
5. F7, F6, F8 — config-time refusal of `hub_hosts: "*"`; templated paths in slog; gate or drop the password fallback.
6. F9 — as hardening, plus the M1 cache-invariant regression test suggested in Part 2.

## Verification status

Not run here (no Go toolchain): `go build ./...`, `go test ./...` (scenarios 1–10 plus the security regressions), `scripts/test-upgrade.sh` (scenario 11). All findings above are from reading the code; the e2e suite covers the first review's regressions but none of F1–F5 — regression tests for those would be new. Specifically untested claims I relied on: that a user group with an arbitrary UID can be created on any DHIS2 (F1, plausible but untried against 2.42), and that Android's `organisationUnits` sync URL collides exactly with a district officer's (F4, taken from the recorded endpoint logs in `testdata/2.42/capture_android_endpoints.txt`).

---

## Response and status (2026-10-08)

Each finding was checked against the code and addressed on branch `review-2-fixes`, with regression tests in `internal/e2e/security_test.go`, `internal/central/central_test.go` and `scripts/test-upgrade.sh`. `go test -race ./...` and scenario 11 pass.

| Finding | Assessment | Status |
|---|---|---|
| F1 | Confirmed, for images without `central_url`. The suggested pin (central URL fixed in the image) would also make domain migrations need new images; and a set-up hub ignored later config changes anyway. | Fixed: `central_hosts` allowlist; with neither key, a one-time setup token from `data_dir/setup-token` (on the label); setup posts must be same-origin; form prefilled only from the image; a failed attempt restores the hub's URL; pending setup lasts 5 minutes. Hubs move to a new URL through the signed manifest (`centralUrl`, `hub release -central-url`), once the new server knows them. |
| F2 | Confirmed; no code execution, but unsigned content was stored and reported. | Fixed: nothing is parsed before the signature checks out; an unsigned manifest is reported once, by hash, with a fixed message; manifest read capped at 64 KB. |
| F3 | Confirmed, and the most important finding operationally. Draining is not needed for safety: writes are frozen and the store copy carries the outbox over. | Fixed: the upgrade proceeds when only entries of users who must log in again remain; they are reported as `stuckOutbox` on the status page and heartbeat. Scenario 11 covers it. |
| F4 | Confirmed through the web app's org unit tree (`withinUserHierarchy`, `userDataViewFallback`); Android's org unit URL filters on the user's own org unit, so it differs per user. | Fixed: the org unit list, tracked entity filters and working lists are no longer shared between users. |
| F5 | Confirmed. A district officer outside the facility's capture scope still needs registering for the admin page. | Fixed: only users with the facility in their capture org units, or in the setup group, are registered; others pass through and get 403 for hub data. |
| F6 | Confirmed. | Fixed: UIDs masked in the slog request line. |
| F7 | Confirmed. | Fixed: `hub_hosts: *` refused at config load. |
| F8 | Confirmed. | Fixed: `password_fallback`, off by default. Without a PAT, offline changes are refused rather than queued for ever. Found on the way: access at login was fetched with the stored token only, so a user without one got no access list; it now uses the credential just given. |
| F9 | Mostly agreed. The probe change as suggested ("require pong") would mark every real 2.42 hub offline: 2.42 answers an anonymous ping with 302 to its login page, which the old probe followed. | Fixed: security headers on hub pages; posts need Origin or `Sec-Fetch-Site`; the probe follows no redirects and accepts pong, 401 or a redirect on central's own host; a retired hub stops serving patient data offline and keeps pushing, never wiping (the registry is forgeable until namespaces are protected); XTS without integrity and the decrypted-in-RAM point stated in DEPLOYMENT. Not changed: 2FA users keep a key slot (documented in FINDINGS 13); setup nonce and job IDs stay `NewUID()`. |
| Part 2 | Agreed. | The Host test now also asserts that the stored response never contains the requesting Host. |
| Part 3 | Agreed. | TPM is a requirement for a national rollout in DEPLOYMENT; namespace protection must precede the first hub. |

**Found while working on this review.** A user's question exposed a gap neither review raised: the hub listed the facility's programs with the setup user's metadata access, so programs a district officer cannot see were never cached for anyone. The catalog is now the union of what the hub token and every registered user can see (FINDINGS 29). The offline search scope, which Capture cannot show today, is recorded as a requirement for the Capture teams in FINDINGS.
