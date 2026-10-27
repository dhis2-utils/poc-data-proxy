# Critical review: facility-hub (v0.1.5)

Read-only review of the Go hub (`internal/`, `cmd/`), the Facility Hub app (`apps/facility-hub`), the Python clients (`clients/`), and the deployment surface (`image/`, `windows/`, `macos/`, `deploy/`, `.github/workflows/`, `scripts/`).

Scope: security, data leaks, data loss, bugs, redundant code, and KISS. Every finding below was verified against the code as read; line numbers refer to this snapshot. No code was changed.

---

## Executive summary

The core hub is unusually well-engineered for a PoC: the crypto layer (Argon2id key slots, device-bound slots, AES-256-GCM with per-row AAD, XTS-encrypted data file) is correct; per-user visibility and the AEAD-bound offline cache prevent cross-user data leakage; SQL in the query index is fully parameterized; the TLS CA is name-constrained; there is no XSS, no localStorage secrets, and no patient data in logs on the paths checked. The problems concentrate in five places:

1. **The Windows installer** executes a user-writable binary elevated and installs it as a permanent service (F1).
2. **Login throttling misses 2FA failures** — DHIS2 answers wrong TOTP codes with HTTP 200, so guessing is unthrottled (F2).
3. **The sync push path converts transient upstream trouble into permanent refusals** of unsynced writes (F3).
4. **The upgrade swap is not power-cut atomic** — the one operation that must be, on devices chosen for power-cut-prone sites (F4).
5. **Registry (dataStore) updates in the Facility Hub app are unguarded read-modify-writes** — races on the token-revocation and central-move paths can leave revoked tokens live or hand the move channel to another user (F22, F24, F25).

Findings are grouped by category and ordered by severity within each group.

---

## Top 10 findings

| # | Severity | Category | Where | What |
|---|----------|----------|-------|------|
| F1 | High | Security | `windows/installer.nsi:170-199` | Elevated installer runs and permanently installs a binary from the user's LocalAppData |
| F2 | High | Security | `internal/auth/auth.go:83-89,96-113` | Wrong 2FA codes return HTTP 200 and are never throttled |
| F3 | High | Data loss | `internal/sync/push.go:154,167-181` | Transient 429/redirect/truncated response permanently rejects unsynced writes |
| F4 | High | Data loss | `internal/upgrade/upgrade.go:239-250,528-560,597-624` | Upgrade store swap is not crash-atomic; power cut can delete the only data copy or brick the hub |
| F5 | Medium | Security | `internal/ui/ui.go:187-207` | Login CSRF on `POST /hub/login` (the only state-changing POST without a same-origin check) |
| F6 | Medium | Security | `.github/workflows/release.yml` et al. | Release-signing workflows use mutable `@v4/@v5` action tags and workflow-wide `contents: write` |
| F7 | Medium | Data loss | `internal/stick/mirror.go:77,112-113` | Recovery stick deletes refused saves, the entries central never accepted |
| F8 | Medium | Data loss | `internal/store/misc.go:118-181,287-326` | Three write paths bypass the store's write lock; duplicate issues possible |
| F9 | Medium | Security | `image/files/etc/polkit-1/rules.d/50-hub-network.rules:4-6` + `nftables.conf:26,31` | Blanket NetworkManager grant plus unscoped DHCP/DNS firewall rules |
| F10 | Medium | Security | `internal/store/store.go:493-496`, `core.go:57`, `tokens.go:79` | Revoked/retired hub flag fails open on a swallowed meta read error |

---

## Security findings

### High

**F1 — Windows installer executes user-writable code elevated, then installs it as a service.**
`windows/installer.nsi:170-199`. `TakeOverDataDir` (line 110) hardens ProgramData — its own comment says "an elevated installer must not run or trust anything an ordinary user could have put in ProgramData first" — and then the per-user → whole-PC migration moves the *elevating user's* `%LOCALAPPDATA%\DHIS2 Facility Hub` into the admin-only data folder (`robocopy ... /E /MOVE`, line 176, auto-confirmed by silent `/S` installs via `/SD IDYES`) and runs the moved binary: `nsExec::ExecToStack '"$HubExe" version'` (line 183, `$HubExe = $DataDir\bin\hub.exe`). If it self-reports a newer version it is kept (line 199) and later installed as the permanent service (`service install ... -exe "$HubExe"`). LocalAppData is writable by any process running as the logged-in user, so malware in that account can plant `ca.key` + `bin\hub.exe` and, on the next admin install, gain elevated execution and a persistent service binary. Note that `windows/check.sh:46-51` passes while the hole is open: it only verifies that `/setowner` appears before the first `'"$HubExe" version'`, and the robocopy sits between them. Fix direction: probe the version on `$PLUGINSDIR\hub-new.exe` (already extracted there), skip `bin\` in the move, and/or verify the moved exe's Authenticode signature before executing it.

**F2 — 2FA code guessing is unthrottled.**
`internal/auth/auth.go:83-89` with `auth.go:96-113`. `count()` records a throttle failure only when `f.Status == http.StatusUnauthorized`:

```go
func (a *Auth) count(src, user string, f *Failure) {
	switch {
	case f == nil:
		a.thr.ok(src, user)
	case f.Status == http.StatusUnauthorized:
		a.thr.fail(src, user)
	}
}
```

But DHIS2 answers a wrong (or missing) 2FA code with **HTTP 200** and `loginStatus: "INCORRECT_TWO_FACTOR_CODE_TOTP"` — the repo's own fixture `testdata/2.42/auth_login_2fa_wrong_code.json` records exactly that (`"status": 200`). So a correct password plus a wrong TOTP produces a `Failure{Status: 200}` that increments no throttle key. The throttle exists because, per its own comment (throttle.go:9-10), "DHIS2 has no lockout of its own": with the password known, an attacker guesses 6-digit TOTP codes at line speed, forever. Count any login failure whose body was not `SUCCESS`, not just 401.

### Medium

**F5 — Login CSRF on `POST /hub/login`.**
`internal/ui/ui.go:187-207`. Every other state-changing POST on the hub checks `sameOrigin(r)` (`admin()` at ui.go:165; setup at setup.go:94). `handleLogin` goes straight from `r.ParseForm()` to `u.Auth.LoginWeb(...)` with no origin check, and the session cookie is `SameSite=Lax` (sessions.go:183), which does not stop a cross-site form POST from *setting* the cookie. A page on the facility LAN (captive portal, another device's page) can silently log a staff member in as the attacker's account; every record they enter is then queued and pushed to central under the attacker's username, corrupting authorship and audit of health data. `GET /hub/logout` (ui.go:227) is likewise a cross-site-gettable logout (nuisance only). Add the same `sameOrigin` guard the sibling handlers have; make logout a POST or check `Sec-Fetch-Site`.

**F6 — Release pipeline trusts mutable action tags while holding signing secrets.**
`.github/workflows/*.yml` (e.g. release.yml:20,24,45,86-88) pin `actions/checkout@v4`, `actions/setup-go@v5`, `actions/upload-artifact@v4` — mutable tags, not commit SHAs — and release.yml:17 grants `permissions: contents: write` at workflow scope, to all jobs. release.yml carries `secrets.SIGN_CERT`, `secrets.SIGN_KEY`, `secrets.RELEASE_SIGNING_KEY` and publishes the artifacts every deployed hub auto-upgrades from. A moved `v4`/`v5` tag runs attacker code with those secrets and release write access. SHA-pin the actions; move `permissions` to the publish job only. Related (Low): release.yml:101 passes `secrets: inherit` to image.yml, which needs none; release.yml:79-81 writes the signing key to a `mktemp` dir that is never wiped (harmless on ephemeral runners, a plaintext key on disk on self-hosted ones).

**F9 — Network takeover chain: blanket polkit grant + unscoped DHCP/DNS firewall rules.**
`image/files/etc/polkit-1/rules.d/50-hub-network.rules:4-6` grants the `hub` user *every* `org.freedesktop.NetworkManager.*` action with no authentication — including `network-control` and system-wide connection sharing. `image/files/etc/nftables.conf:26,31` accept DHCP (udp/67) and DNS (tcp+udp/53) from any RFC1918/CGNAT source on any interface, though both comments say "on the hub's own Wi-Fi" — there is no `iif` or subnet restriction (also flagged independently in the firewall review). The hub process is this device's main network-facing surface; a compromise of it can reconfigure networking and become the DHCP/DNS authority on the facility's *wired* LAN, silently poisoning clients of a health facility. Whitelist the specific NM actions the setup code needs, and bind the DHCP/DNS rules to the AP interface or shared subnet. Related (Low): `delete table inet hub` (nftables.conf:9) before recreation leaves a brief no-rules window on every reload.

**F10 — The revoked/retired kill switch fails open on a swallowed error.**
`internal/store/store.go:493-496` (`Meta` ignores the `QueryRow` error, so any I/O or corruption error reads as an empty value) feeds `Retired()` (core.go:57: `h.Store.Meta(MetaRetired) != ""`) and `Revoked()` (tokens.go:79). A hub the national team revoked keeps serving patient data from its store if that one read fails, and `CreatePAT` (refresh.go:282) keeps minting tokens. The flags are designed so "a forged entry must cost at most offline service" (core.go:22-24); a transient read error achieving the same fail-open undermines that. Fail closed on anything but a clean empty row.

**F11 — PAT revocation is best-effort, and UIDs are forgotten regardless.**
Two sides of the same problem:
- Hub side (`internal/core/tokens.go:82-99`): `DropTokens` deletes each PAT on central with no error check (`h.Central.JSON(ctx, "DELETE", ...)` at tokens.go:87,93) then unconditionally forgets it (`SetPAT(u.ID, "", "", "")`). Per this file's own header, PATs on 2.42.6 "outlive their user … nobody but the owner can delete one": a network blip during revocation leaves a live token on central that the hub can never name again — `TokenUIDs()` (tokens.go:71-77) no longer lists it, so "Revoke all tokens of this hub" misses it until country expiry (up to 365 days). Same pattern in `EnsurePAT`: the *old* token is deleted (refresh.go:225) before the *new* one is durably stored (refresh.go:228).
- App side (`apps/facility-hub/src/hubs/registry.ts:111-112`): `revokeTokens` is a read-modify-write with no conditional update; if the hub renews its tokens between the read and the PUT (renewal is hourly), the PUT stomps the entry with the *older* token list. The renewed tokens keep working after "revoke all tokens" and are no longer listed, so **no future revoke can ever delete them**. The same unconditional-PUT pattern is at `approve.ts:186-191`.

Make UID removal conditional on a confirmed delete (retry next cycle), and version/conditionally update registry entries.

**F12 — Token ownership takeover race on `facility-hub-admin/central-move`.**
`apps/facility-hub/src/hubs/adminEntry.ts:44-47`. Taking over an entry created by someone else does `DELETE` then `POST`; between the two, any other user can create the entry and become its owner — and per the file's own comment (lines 7-9), the owner keeps write access whatever the sharing says. This is the entry **every hub reads and acts on** (central moves). The window is one round-trip and needs a racing attacker with app access, but the payoff is a write channel to all hubs. There is also a documented public read-write window between entry creation and sharing being set.

**F22 — Proxy forwards spoofable `Forwarded` / `X-Forwarded-*` headers to central.**
`internal/proxy/proxy.go:33-40,67-76`. The denylist strips `Authorization`, `Cookie`, `X-Forwarded-For` and hop-by-hop basics — showing the intent — but not `Forwarded`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-Port`, and it does not remove headers *named in* `Connection` (RFC 7230 §6.1). A facility device can spoof its apparent protocol/host/for-chain to central, which matters when central sits behind a TLS proxy that trusts these for URL generation, CSRF-origin checks, or audit. Smuggling toward central is effectively prevented (the hub re-buffers the body and recomputes framing), and the attacker is an authenticated client — hence Medium, not High. A denylist at the trust boundary should cover the forwarded family completely or become an allowlist.

**F23 — Defender exclusion on by default for the service's whole folder.**
`windows/installer.nsi:282-288`. A default-checked section runs `Add-MpPreference -ExclusionPath '$DataDir'` for AllUsers installs — the folder holding the service executable, the data store, and everything the service ever writes. Anything landing there via F1, a service-account compromise, or a bug is invisible to AV. Make it opt-in or scope it to the database file. (Correctly reverted on uninstall, line 329.)

**F24 — Arbitrary mount of removable media.**
`image/files/etc/udev/rules.d/90-hub-usb.rules:5` auto-mounts any vfat/exfat stick as `hub` the moment it is plugged in — the intended setup/recovery path, but it also feeds the hub process and the kernel's FAT/exFAT parsers (historically a rich bug source) a crafted filesystem image from anyone with physical access. Deliberate tradeoff; at minimum treat stick contents as untrusted data and document it.

### Low

**F13 — `rand.Read` errors ignored for session tokens and the Basic-cache salt.**
`internal/core/sessions.go:47,68`. On a failing `crypto/rand` (fd exhaustion on a small device), every session token is 48 hex zeros and every `basicKey` shares a zero salt — cookie auth trivially guessable. Propagate the error or fail loudly.

**F14 — Request-log redaction is an allow-list of two parameter names.**
`internal/core/reqlog.go:149-159` with `app.go:325`: `SafeQuery` redacts only `filter` and `query`; every other parameter value lands verbatim in the plaintext `hub.log` (Info level, 10 MB, months on disk) when `log_requests` is on. Any other parameter that ever carries patient-identifying values on some DHIS2 endpoint or future app version leaks to disk. Redact by default and allow known-safe keys.

**F15 — Old key-slot ciphertext lingers in the key file's WAL.**
`internal/store/store.go:229` sets `secure_delete(1)`, but after `UpsertCredentials` replaces a slot (password change), the old wrapped slot stays readable in the key file's WAL frames until a checkpoint; the key file is tiny, so auto-checkpoint (1000 pages) may not run for a long time. On a stolen disk an ex-staff member's *old* password still unwraps the unchanged data key from WAL remnants. A `wal_checkpoint(TRUNCATE)` after credential replacement closes it.

**F16 — Error strings from internal failures are returned to clients.**
`internal/auth/auth.go:101-103` and `internal/proxy/proxy.go:263-265` both answer 502 with `a.H.WebError(w, 502, err.Error(), "")` — raw Go error text (URLs, hostnames, driver messages) to the browser. Information disclosure only; log it server-side and return a generic message.

**F17 — Client-side-only visibility filter in the Facility Hub app.**
`apps/facility-hub/src/hubs/useHubs.ts:80-118` + `flags.ts:99-110`: the org-unit filter is applied in the app after `listNamespace` has delivered every registry entry and heartbeat (hub IDs, facilities, private LAN IPs, CA fingerprints, error strings, offline-miss paths) to every app user's browser. The comment concedes "This is filtering in the app, not access control". Metadata-only today; a real leak if heartbeat contents ever grow patient-adjacent fields.

**F18 — Minor crypto/credential hygiene items.**
- `apps/facility-hub/src/hubaccount/approve.ts:21-22`: `Array.from(randomBytes(n), b => alnum[b % alnum.length])` has modulo bias (256 % 62 = 8). Negligible for the 28-char password/10-char UID in practice; rejection sampling is a one-line fix. (The Go side, `tracker/model.go:391`, gets this right.)
- `clients/eregisters_setup.py:24,131,161`: hardcoded `PASSWORD = "Hub-Test-2026!"`, admin password written into `MANIFEST.json`, password printed to stdout (CI logs). Test tooling only, but credentials persisting to manifests/logs is a habit worth breaking.
- Windows/macOS CI builds embed no release key, so such installs silently never self-upgrade (`windows/build.sh:19`; `internal/upgrade/upgrade.go:131-134` treats an empty key as "development build"); the macOS path also verifies only a sha256 over TLS, never `release.json.sig`.

**F19 — Accept-by-design risks that deserve documentation, not code.**
- The USB stick copy of the key file replaces device-bound slots with password-only slots (`internal/store/mirror.go:100-137`): one lost stick converts the hub from "TPM-bound, guess-resistant" to "Argon2id-only, guess at leisure" for all staff passwords (`hubcrypto.go:88-90`, `users.go:110-113`). State it in RECOVERY.md and in stick-handling procedure.
- Offline login is password-only by construction for 2FA-protected accounts (the key slot is the only verifier), so the hub inherently weakens a 2FA account to its password while offline. Appears accepted; worth a line in the spec.
- The spec 7.1 fallback stores the user's central password (GCM-sealed under the data key, `store/users.go:209-219`) for accounts that cannot create PATs. A hub compromised while unlocked yields central passwords for those users.

---

## Data loss findings

### High

**F3 — Transient upstream failures permanently reject unsynced writes.**
`internal/sync/push.go:154,167-181`. After the switch on 401 and 5xx, *anything else* that is not a valid tracker import report rejects the whole outbox entry:

```go
rb, _ := io.ReadAll(resp.Body)   // body-read error swallowed
...
persisted, errsByUID, perr := h.API().TrackerResult(resp.StatusCode, rb)
if perr != nil {
    // A non-report answer (e.g. 400 parse error) rejects the whole entry.
    for _, it := range send { w.reject(e, it, nil, ...) }
    return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, perr.Error())
}
```

A transient HTTP 429 (central rate limiting), a 3xx redirect (the client uses `CheckRedirect: ErrUseLastResponse`, so a proxy/context-path redirect arrives as a 30x), or a connection reset mid-body *after central already persisted the import* all land here. Unsynced offline writes are marked `SyncRejected` and leave the pending queue (revivable only by a manual retry instruction in the Facility Hub app); in the truncated-body case the hub records data as refused that central actually accepted — silent divergence on exactly the flaky-uplink device class this product targets. Distinguish "central answered about the import" from "transport failed", and retry transient statuses.

**F4 — The upgrade swap is not crash-atomic, and it swaps store before binary.**
`internal/upgrade/upgrade.go:239-250,528-560,597-624`. `swapStore` performs sequential `os.Rename` calls — `os.Rename(live, saveAs)`, then `os.Rename(incoming, live)`, then the data file twice — with rollback logic but no journal: between the first and third rename there is no live store at all. Two power-cut windows:
1. A cut in that window leaves no live store; on boot SQLite creates a fresh empty one, `AfterBoot → confirm` health-checks it — `SELECT count(*) FROM objects` (upgrade.go:564) *succeeds on 0 rows* — and `removeStore(mk.PrevStore)` (upgrade.go:532) deletes the only remaining copy of all data.
2. A cut (or `Reexec` failure) after `swapStore` but before `replaceBinary` leaves the old binary with the migrated store; `migrate` refuses it ("store schema %d is newer than this binary", store.go:309), `app.New` fails before `AfterBoot` can run, and the service manager restart-loops a bricked hub — the marker is unreadable because the store cannot open.

Related: the marker itself is written with a bare `os.WriteFile` (upgrade.go:590-594) instead of `atomicfile`, in a package that imports `atomicfile` for exactly this class of file; a torn `upgrade.json` is silently discarded by `AfterBoot` (`json.Unmarshal` fails → `os.Remove(path)`), orphaning the rollback copy. And the Windows `replaceBinary` (reexec_windows.go:21-31) renames the running exe away *before* copying the new one — a window spanning the whole multi-megabyte copy with no executable at `dst`; the Unix path is correctly atomic. On rural, power-cut-prone devices, the fix belongs in this sequence, not in documentation.

### Medium

**F7 — The recovery stick deletes refused saves.**
`internal/stick/mirror.go:77,112-113`. `PendingRaw()` selects only `state = 'pending'`, and the mirror's save step removes stick files that are no longer pending — "pushed or refused meanwhile". For pushed saves that is safe; for **refused** saves it is not: central explicitly does not have that data, the issue reported to central's dataStore deliberately contains no values, and the only remaining copy is the disk store. A disk failure after a refusal permanently loses the edit — defeating the stick, which is the system's answer to disk loss, for exactly the entries most at risk. Keep refused saves on the stick until resolved.

**F8 — Three store write paths bypass the store's own write lock.**
`internal/store/misc.go:118-151,158-181,287-326`. `AddIssue`, `AddReservedValues` and `TakeReservedValues` open transactions with `s.DB().Begin()` directly, bypassing `writeMu` — the lock `Tx` and `write` take because, per its own comment (store.go:562-568), "on a starved CPU … writers gave up after the busy timeout with 'database is locked'". These run on the push cycle and on offline Android writes, the exact contended paths. `AddIssue` additionally does count-then-insert with no unique index on `(outbox_seq, object_uid)`, so concurrent refusals of the same object can insert duplicate issues. Route them through `Tx`/`write`; add the unique index.

**F20 — Rollback proceeds after a failed store restore.**
`internal/upgrade/upgrade.go:542-548`: a failed `swapStore` in the rollback path is logged and the code unconditionally continues to `replaceBinary(mk.PrevBinary, ...)` + `Reexec` — re-exec'ing the old binary against the new, schema-migrated store that `migrate` refuses → boot failure → restart loop, with `PrevStore` intact on disk but unused. A rollback that fails halfway should stop and keep the hub down for manual recovery.

**F21 — A single failed request flips the hub offline; one probe flips it back.**
`internal/central/central.go:76,331-337,432-443`: `ReportFailure` sets `m.failures = 2; m.set(false)` — any one failed upstream request marks the hub offline instantly — while recovery needs one successful probe. With `ResponseHeaderTimeout = 60s`, a tracker import that merely exceeds 60s on a busy central flips the hub offline mid-cycle, aborts the cycle, and the next 15s ping flips it back (firing `TriggerSync`). Flapping aborts pushes and pulls mid-way, spams outage history and heartbeat `lastError`, and shows staff "needs connection". The probes deliberately require 2 failures; the request path requiring 0 is the asymmetry.

**F26 — Half-applied registry undo leaves a facility with no active hub, unrecoverably.**
`apps/facility-hub/src/hubs/registry.ts:156-157`: `undoReplacement` writes two entries non-atomically — `PUT entryPath(newId)` then `PUT entryPath(oldId)`. If the first succeeds and the second fails, the replacing hub is retired while the old hub stays retired: zero active hubs. The guard at lines 147-154 (`if (retiredEntry(replacing)) throw ...`) then blocks a retry, making the half-undone state permanent without a manual dataStore edit. The test at `registry.test.ts:181` covers only first-PUT failure.

### Low

**F27 — Users dropped from pulls, silently.**
`internal/sync/pull.go:47-49` (also `aggregate.go:55-57`): `auth, err := h.UpstreamAuth(u.ID); if err != nil { continue }` — no log and, unlike the push path (push.go:152-153), no `needs_login` flag. A read-only staff member (never in the outbox) has their cached data silently stop updating, with no signal on the admin page or heartbeat.

**F28 — Transient 4xx on a full pull wipes offline visibility.**
`internal/sync/pull.go:261-265`: any 4xx counts as "central decided", and `pullGroup` then runs `RemoveVisibilityExcept` for those programs. A transient 403/404 from a misconfigured proxy or WAF — not a real access revocation — removes users' offline visibility of the program until access is restored. Distinguish "central answered about access" from "an intermediary refused".

**F29 — Bad release asset permanently skips that version.**
`internal/upgrade/upgrade.go:342-345,146`: a checksum mismatch sets `upgrade_skip` for the version string; if the supplier publishes a bad asset and fixes it under the *same* version, every hub that saw the bad download never retries — including security releases.

**F30 — hub-diag writes to the boot partition every 30 minutes.**
`image/files/usr/local/sbin/hub-diag:19-21` + `hub-diag.timer`: diagnostics (and the loadtest agent log) are written to `/boot/firmware/hub-diag.txt` on a journal-less FAT filesystem the Pi must boot from. vfat directory/FAT updates are not crash-atomic, and these hubs live where power cuts are routine; a cut at the wrong moment corrupts the boot partition and bricks an offline facility's hub — the highest-cost failure this device can have.

**F31 — Misc swallowed errors on durability-relevant paths.**
- `internal/stick/mirror.go:176`: `copyKeys` ends with a bare `os.Rename` and no directory fsync — `keys.db` is the one file everything else on the stick is useless without; it deserves `atomicfile` treatment.
- `internal/stick/mirror.go:246`: `filepath.Glob` error discarded; restore silently proceeds without the saves.
- `internal/sync/sync.go:301-305`: `RefillPool` ignores `json.Unmarshal` errors (malformed reserved values never expire) and aborts remaining attributes on one failure instead of joining errors.
- `internal/upgrade/upgrade.go:539`: `dk, _ := u.H.Store.DK()` in the rollback path hands a nil key to `Reexec` — the rolled-back hub silently starts locked.

---

## Data leak findings (specific)

**F32 — Anyone can evict another user's pending online import payload.**
`internal/trackerapi/trackerapi.go:168-172`: the entry is deleted from `onlineJobs` *before* the owner check:

```go
j, ok := a.onlineJobs[id]
delete(a.onlineJobs, id)
a.jobsMu.Unlock()
if !ok || j.userID != sess.UserID { return }
```

Any authenticated user who learns a central job ID (returned in the importer's own response) deletes another user's pending payload; the author's later `GET .../jobs/{id}/report` finds nothing, `storePersisted` never runs, and data entered online just before an outage is never learned into the offline store — exactly the case `onlineJobs` exists for. The offline twin (`serveJob`, line 411) checks ownership before any state change; make the online path match.

**F33 — Client-side metadata exposure and shared-prefix offline cache.**
- F17 (app-side filtering) above.
- `internal/proxy/proxy.go:150,163-172`: offline, `loginConfig`, `staticContent`, `locales` and app static files cached by one user are served to another (`GetCacheAnyUser`). Documented and limited to non-user data; noted for completeness. The `/api/me` offline fallback re-serves the *owner's* full cached `/api/me` to the same user only (`sess.UserID`-keyed), which is correct.

---

## Bugs

**F34 — Concurrent unlock races on `SetDK`/`Hub.Unlock`.**
`internal/store/store.go:405-436` with `core.go:371-413`: `SetDK` checks `if s.Unlocked() { return nil }` and only later takes `s.mu` to install `s.dk, s.data`; `Hub.Unlock` has no mutex of its own. Two staff logging in right after a reboot both pass the check, both run `openData`, and the second `s.data` assignment orphans the first `*sql.DB` (leaked pool of up to 4 connections). Worse, `Hub.Unlock`'s deferred `if err != nil { h.Lock() }` means a *failing* concurrent unlock relocks the store and clears all sessions the *successful* one established (`Lock()` → `Sessions.Clear()`): an intermittent "login failed, try again" on multi-user boots. Serialize `Unlock`/`SetDK`.

**F35 — Unvalidated category option combo on offline cell edits.**
`internal/aggregateapi/aggregateapi.go:208-216`: `edit()` validates data set membership, facility, AOC, `MayUse`, lock, value type — then takes the client's `co` as-is. A made-up COC is queued, rendered as a phantom cell in the offline form, answered `201 Created`, and only rejected by central on push (landing in "issues"). Data-quality/UX defect, not security.

**F36 — Online tracker passthrough double-logs and double-authenticates.**
`internal/trackerapi/trackerapi.go:71,92-96` + `proxy/proxy.go:264`: an unauthenticated online tracker request is recorded by trackerapi's own `defer ReqLog.Record` and again by `proxy.ServeHTTP`'s, and `Identify`/`LoginBasic` runs twice (double throttle accounting, double central round-trip for Basic clients). The `served` label is also stale in the passthrough-fallback path.

**F37 — Offline HEAD requests always miss.**
`internal/proxy/proxy.go:308,325` (with proxy.go:239): `serveOffline` admits HEAD but looks the response up by `r.Method` — only GET responses are ever stored. A HEAD for a resource cached under GET gets `503 needs connection`; the `/api/me` fallback is keyed `"GET"` too.

**F38 — Swallowed store errors become a panic or silent skip in trackerapi.**
`internal/trackerapi/trackerapi.go:359,246-249`: `u, _ := a.H.Store.UserByID(sess.UserID)` then `u.Username` — on a store error the handler panics into the recoverer as a bare 500. In `storePersisted`, an unparseable online import answer is dropped with `return` and no log: the write never reaches the offline store and nobody learns why. Also: the reserved-value handler logs status 200 unconditionally (trackerapi.go:443) and calls `EndLocalWrite()` manually rather than by `defer` (trackerapi.go:470) — a panic inside leaks the `localWrites` RLock and permanently blocks hub upgrades (`FreezeLocalWrites` would hang).

**F39 — Frontend form bugs.**
- `apps/facility-hub/src/pages/ApprovalsPage.tsx:45`: the `groupsAndRoles` query's error is never rendered — a failed `/userGroups`/`/userRoles` leaves three working-looking empty selects with no explanation. Every other page surfaces query errors; this one stands out.
- `apps/facility-hub/src/hubs/settings.ts:96`: `String(a?.retentionPeriods || defaults.periods)` — a stored `0` ("the hubs read 0 as the default") displays and then saves as `12`, silently changing semantics from "hub default" to "12 periods". `tokensForm` (line 113) gets this right with `??`.
- `apps/facility-hub/src/pages/MovesPage.tsx:164`: move codes computed from stored keys without validating decoded length — a malformed `facility-hub-keys` entry yields a plausible-looking but wrong code (HMAC over an empty key) instead of an error; `approve.ts:90` validates (`pub.length !== 32`), this path does not.
- `apps/facility-hub/src/pages/HubPage.tsx:170`: `https://${a}` hardcodes the scheme for the hub's private IPv4 addresses; a hub serving plain http on its LAN shows staff a URL that fails.
- `apps/facility-hub/src/hubs/refusals.ts:105-110`: two managers clicking Resend concurrently lose one retry silently (last writer wins on `facility-hub-admin/retries`).

**F40 — Race-condition password rotation in approval.**
`apps/facility-hub/src/hubaccount/approve.ts:108` vs `:168-184,264`: `account()` runs (creating the hub user or rotating an existing account's password) *before* the registry re-read that validates nothing changed. If the re-read throws, the new password was never delivered to anyone: the hub's existing account has credentials nobody holds until a re-approval. Availability bug on the primary approval path.

**F41 — Pathological paging input.**
`internal/tracker/search.go:290-298`: `pageSize` near `math.MaxInt64` overflows `len(objs)+size-1` to negative, truncates to 0, and page 1 returns an empty page though objects matched. `paging()` accepts any `n >= 1`. No memory-safety impact.

**F42 — CI and build-process behaviors that will surprise.**
- `.github/workflows/test.yml:1` claims tests run "on every push" but the trigger is `workflow_dispatch` only — nothing runs automatically on any push; only release tags run `go test`. If intended, fix the comment; if not, fix the trigger.
- `image/build.sh:50-51` + `fixmodes.py`: `debugfs rdump` does not carry xattrs, so every extended attribute in the base image is silently dropped — `security.capability` on e.g. `/usr/bin/ping` is lost, those binaries break, and `image/check.sh` has no capability check to notice.
- `windows/installer.nsi:22`: `RequestExecutionLevel user` with a comment saying the opposite — `MultiUser.nsh` re-issues `RequestExecutionLevel highest` and the last call wins; the line is dead code with a misleading comment.
- `deploy/hub.service:13-16`: `Restart=always` covers process exit and the hardware watchdog covers a hung kernel, but there is no `WatchdogSec=` for the process itself — a hub that deadlocks while staying alive serves nothing and never syncs, indefinitely, on a device with no console. `Type=notify` + sd_notify closes this.

---

## Redundant code and KISS

Overall verdict: **this codebase is disciplined about KISS** — a rarity. The AGENTS.md policy ("name the measured problem a mechanism solves") is visibly enforced: the crypto layer is one file; the proxy is a single denylist; caches each carry a written invalidation rule; removed mechanisms are documented with the numbers that justified removal. The remaining redundancy is small and localized:

**R1 — Six copies of the facility-name fallback, already drifting.**
`h.orgUnit?.displayName ?? h.registry.facilityName ?? h.registry.facility` is repeated at `HubsPage.tsx:24-27`, `ApprovalsPage.tsx:217-218`, `HubPage.tsx:73-76`, `ApproveDialog.tsx:59-62`, `MovesPage.tsx:162`, `VersionPage.tsx:73` — and the copies differ: `VersionPage` falls back to `h.id`, `ApprovalsPage` omits the `?? facility` tier. Belongs on `HubRow`, where a test can pin it once.

**R2 — Dead exports and unreachable branches in the app.**
`src/components/sidebar/sidenav/Sidenav.tsx`: `SidenavParent` and `SidenavFooter` are exported and never used. `Layout.tsx:5-11` defines `RouteHandle.collapseSidebar` and `PageWrapper.tsx:22` reads `fullWidth` from route handles, but no route in `App.tsx` sets a `handle` — both branches are unreachable as written.

**R3 — No-ops that read like guards but are not.**
`internal/tracker/query.go:493`: `strings.TrimSuffix(kind, "")` is a no-op. `proxy.go:137`: `cacheable(r *http.Request, ...)` never reads `r`. `proxy.go:186`: `shared()` tests `strings.HasPrefix(rest, p+"?")` though `rest` is a path and cannot contain `?`. Harmless individually, but they invite false confidence.

**R4 — Double work on hot paths.**
`internal/auth/auth.go` `LoginBasic`: `a.H.Sessions.Basic(username, password)` is called in `LoginBasic` (line 118) and again immediately inside `loginBasic` (line 126) — two lock acquisitions and two hashes per Basic request. With F36, one unauthenticated online tracker request currently records, hashes, and authenticates twice.

**R5 — Expired sessions are never swept.**
`internal/core/sessions.go`: `byTok` entries are removed only when their token is next presented (`Get`); sessions created for pass-through users (`Sessions.New(0, ...)`, auth.go:186-196) or abandoned logins accumulate until process restart. Not a leak per se (memory-bounded by login rate), but a periodic sweep is one line and removes the class.

**R6 — Repo hygiene.**
`README.md` links to `docs/*.md` (SPEC, FINDINGS, TESTING, PERFORMANCE, DEPLOYMENT, RECOVERY, reviews) which are absent from this snapshot — if the docs directory was dropped intentionally, the README's document table and the code comments citing `docs/PERFORMANCE.md` etc. now dangle. `.DS_Store` is present in the tree although `.gitignore` lists it.

**What is *not* redundant, for the record:** the TS `src/hubaccount` mirroring Go `internal/hubaccount` is a documented, tested interop contract (the app must match the hub's approval/move steps), not duplication; `internal/compat`'s per-version files are the stated design; `fakeApi.ts` does not ship (imported only by tests; unreachable from the `d2.config.js` entry). I looked for duplicated utility functions across Go packages and found none of substance.

---

## Verified clean (highlights)

- **Crypto:** Argon2id (t=2, m=64 MiB, p=2, per-slot salt) meets OWASP minimums and is sized for the Pi; AES-256-GCM with fresh nonces and per-row AAD everywhere; HKDF labels are unambiguous; XTS file key derived with a label; concurrency-bounded derivations; anti-enumeration login (unknown users pay a real Argon2 derive against a dummy salt and get the identical 401).
- **Data loss on power cut (normal operation):** WAL + `synchronous=FULL` on both files; migrations one transaction each with `user_version` inside; temp files spill to disk under the encrypted VFS; boot secrets via `atomicfile` (tmp + fsync + rename + dir sync); `VACUUM INTO` backups. The outbox write path (`PersistImport`: objects + outbox entry in one transaction, seq-bound AAD; pull cannot overwrite pending local versions) is solid. The upgrade path is the exception (F4).
- **SQL:** every condition in the query index is parameterized; LIKE values escaped (`index.go:277-281`); ORDER BY columns from fixed sets. No injection path found.
- **Per-user visibility:** enforced uniformly (`visibleWith`/`CanRead`); offline cache AEAD-sealed with `cacheAAD(userID, method, url)` so entries cannot be swapped across users even by DB tampering; cross-user reuse confined to documented shared prefixes and static files.
- **TLS/CA:** name-constrained (`PermittedDNSNamesCritical`, private IP ranges only), ServerAuth-only, sealed keys, atomic writes, correct leaf lifetime (397d).
- **Frontend:** no localStorage/sessionStorage anywhere (grep-verified); auth via DHIS2 session cookie; no `dangerouslySetInnerHTML`/`innerHTML`; CSV formula injection handled (`MovesPage.tsx:110-111`); token IDs (never secrets) in URLs; no `console.*` calls.
- **Setup flow:** server address pinned by nonce (a client-posted `central_url` is never used unless the earlier step checked it); setup code constant-time compared and throttled; failed setups roll back the registry entry and PAT; mDNS parser bounds-checked; nmcli driven via argv with no shell, and `nmcliError` strips arguments so Wi-Fi passwords never reach logs.
- **Image supply chain:** base image pinned by sha256, cloud-init/default user removed, no SSH, no login-capable users (checked by `image/check.sh`), unprivileged service user with `CAP_NET_BIND_SERVICE` only, `macos/install.sh` verified downloads and a name-constrained CA.
- **pprof:** off by default and refused on non-loopback (`cmd/hub/pprof.go`).
- **Test discipline:** ~1,466 lines of frontend tests for ~2,600 of source; extensive Go test suites including live oracles against real DHIS2; performance guards named in AGENTS.md are present in the code.

---

## Method note

Five parallel read-only review passes (auth/crypto/store; proxy/APIs/UI/TLS/firewall; sync/upgrade/stick; React app + Python clients; image/windows/CI), consolidated and spot-verified: the highest-severity claims (F1, F2, F3, F4, F5, F26 and the swap/login code paths) were re-read in the source before inclusion. No code was modified and no tests were executed (no Go toolchain in the review environment); `go vet`/`go test` results are therefore not part of this report.
