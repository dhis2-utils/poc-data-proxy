# Security and code quality review — 2026-10-20

Third review of the facility hub PoC: a combined **security and code quality** pass over the whole codebase (~28,600 lines of non-test Go under `cmd/` and `internal/`, the NSIS installer and its build scripts, the Pi image files, the Facility Hub app on central, the deploy files and the CI workflows), as of 2026-10-20. The two earlier reviews (REVIEW, 2026-10-07, H1–M9/L1–L5; REVIEW-2, 2026-10-08, F1–F9) are recorded in [reviews/](reviews/README.md); this one verifies their fixes in the code as it stands and reports what is new since — mostly the Windows work (installer, service, DPAPI key handover, `hub init`, mDNS), the hub's own account (`hubaccount`, take-over, typed moves), the USB-stick restore, netsetup, and the aggregate data entry feature, none of which existed at the last review.

**Method.** Static review: all non-test Go sources were read end to end, plus `windows/installer.nsi`, `windows/build.sh`, `windows/sign.sh`, `windows/check.sh`, the `image/` files, `deploy/`, `.github/workflows/`, the app's TypeScript, and the e2e security regressions (`internal/e2e/security_test.go` and the newer scenario files). Findings were cross-checked against FINDINGS.md, DEPLOYMENT.md, RECOVERY.md and TESTING.md. No code was changed. There is **no Go toolchain in this environment**, so `go vet ./...`, `go test ./...` and `scripts/test-upgrade.sh` were **not run** — everything below is from reading. Run the suite after any fix.

**Overall verdict.** The two rounds of fixes are real and hold up under re-reading; the fail-closed discipline, the crypto layer, the signed upgrade path and the outbound-data redaction are all still in place, and the new code (aggregate, hub account, moves, stick, Windows) follows the same rules — per-user attribution, sealed-at-rest, central-stays-authoritative, refuse what you cannot decide. The new findings cluster in three places: **the hub's CA private key is now kept unencrypted in the data folder** (a deliberate reversal of review 1's "key discarded" fix, with real impersonation consequences), **the restore-from-stick restart never actually restarts the process** (the documented disaster-recovery flow stops serving and waits for a restart that nobody sends), and **the Windows release ships test-signed** (the installer users run is Authenticode-signed by a throwaway certificate). Everything else is hardening. Nothing found is exotic; all of it is fixable in place.

Severity: **High** = patient-data confidentiality or credential capture; **Medium** = confidentiality/integrity/availability with a realistic path; **Low** = hardening.

---

## Part 1 — Verification of the second review's fixes (F1–F9)

Each fix was re-read in the current code, not taken from the response table. All nine hold.

| Finding | Status in code |
|---|---|
| F1 (setup front door) | **Fixed.** `central_hosts` allowlist with wildcard-suffix matching (`internal/core/hosts.go` `CentralAllowed`); with neither key, a one-time setup token from `data_dir/setup-token` — 16 bytes over a 32-character alphabet (~80 bits), constant-time compare, throttled under a fixed username, written 0600 (`internal/ui/setuptoken.go`); setup POSTs require `sameOrigin`; the login step uses only an address the server step checked (with its code), never one posted with the login; a failed attempt restores the previous URL; the form is prefilled only from the image; `pendingSetup` holds the password 5 minutes, not 15. Windows adds `setup_local_only` (setup only from the machine itself). Exercised by `TestSetupCentralURL`. |
| F2 (unsigned manifest) | **Fixed.** `fetchManifest` (`internal/upgrade/upgrade.go:270`) reads at most 64 KB, verifies the ed25519 signature **before** parsing, and on failure records one fixed message keyed by the manifest's hash (`upgrade_unsigned`), never the attacker's strings. |
| F3 (stuck outbox blocks updates) | **Fixed.** The upgrade proceeds when only entries of users who must log in again remain (`pending > stuck` is the failure test, `upgrade.go:158`); they surface as `stuckOutbox` in `/hub/status` and the heartbeat. Scenario 11 covers it. |
| F4 (shared metadata cache) | **Fixed.** `organisationUnits` (the list) is explicitly excluded from `sharedPrefixes`, with a comment naming the per-user reason; `trackedEntityInstanceFilters` and `programStageWorkingLists` are not in the list. `GetCacheAnyUser` remains only for static app files and facility-wide metadata. Exercised by `TestPerUserMetadataNotShared`. |
| F5 (any central user registers) | **Fixed.** `registerOnline` (`internal/auth/auth.go:160`) passes users without the facility in their capture scope through with `UserID == 0` — no key slot, no stored PAT; `usable()` refuses hub-local tracker calls with 403. Exercised by `TestOutsiderNotRegistered`. |
| F6 (UIDs in logs) | **Fixed.** The slog request line uses `core.Template(r.URL.Path)` (`internal/app/app.go` `logRequests`); the request-log file records templates and parameter names only. |
| F7 (`hub_hosts: "*"`) | **Fixed.** Refused at config load with an error (`internal/config/config.go` `hub_hosts` case). |
| F8 (password escrow) | **Fixed.** `password_fallback` is off by default; without a PAT, offline changes are refused rather than queued (`EnsurePAT`, `internal/core/refresh.go`). Exercised by `TestNoPasswordFallback`. |
| F9 (smaller items) | **Fixed.** Security headers on `/hub` pages (nosniff, X-Frame-Options, Referrer-Policy, CSP — `internal/app/app.go:117`); `sameOrigin` requires `Origin` or `Sec-Fetch-Site: same-origin`/`none` (`internal/ui/ui.go:166`); the probe follows no redirects and accepts only pong, 401, or a redirect to central's own host (`internal/central/central.go` `isCentral`); a retired hub stops serving patient data and keeps pushing (`checkRegistry` + `usable()`), exercised by `TestRetiredHubStopsServing`. Not changed, as agreed: 2FA accounts keep a key slot (offline login is password-only, FINDINGS 13); XTS gives no integrity (stated in DEPLOYMENT). |
| Part 2 pushback (M1 cache invariant) | **Fixed.** `TestHostHeader` asserts the stored cache row never contains the requesting Host and that an offline replay to another client carries central's own base URL. |

The first review's fixes also still hold where I re-checked them: the two-file store with AES-256-XTS over the whole data file and per-row AES-GCM with AAD (`internal/store/store.go`), the login throttle with a uniform offline failure and bounded Argon2 concurrency (`internal/auth/throttle.go`, `internal/hubcrypto`), session idle/max expiry with 5-minute Basic revalidation (`internal/core/sessions.go`), hub-token renewal (`RenewHubToken`), codes-only outbound errors (`HTTPError.Error`, `tracker.OutgoingMessage`/`MaskMessage`, which fails closed — a message quoting nothing is dropped), the reserved-value pool caps and job TTLs (`internal/trackerapi`), the local-write freeze during upgrades (`BeginLocalWrite`), search-scope for local objects (`CanRead`), `createdAt`/`createdBy` preserved on offline update, the 64 MB body cap, read/idle timeouts, the 30-day cache pruning, and the fail-closed attribute filter.

---

## Part 2 — New findings

### S1. The hub's CA private key is kept unencrypted in the data folder (Medium)

Review 1's L5 fix was "name-constrained CA, **key discarded**, server certificate issued from it". Since commit `b2131c3` ("its authority **kept**, renewed and backed up"), `internal/hubtls/hubtls.go` keeps `ca.key` (0600) next to `tls.key` so the hub can renew its server certificate without a restart (package comment, lines 1–9; `newCA` writes it at line 339; `loadCA` reads it at line 256). The key is a plain file in `data_dir` — **not** inside the XTS-encrypted data file.

The mitigations are real and deliberate: the CA is name-constrained (critical `PermittedDNSDomains` limited to `dhis2.local`, the host name, `hub_hosts` entries that pass `ValidHostName`, and private IP ranges), `ExtKeyUsage` serverAuth only, `MaxPathLenZero`, 10-year life. A stolen `ca.key` can therefore mint certificates only for the hub's own names and private addresses — which is exactly the set devices use to reach the hub. Combined with the theft scenario DEPLOYMENT already names ("Physical access to the card or disk"), a stolen SSD now yields two things, not one: offline password guessing against the key slots (known), **and the ability to impersonate the hub to every device that installed its authority** — serve a login page at `https://dhis2.local` signed by the hub's own CA and capture staff central passwords at the next login. That is credential capture without touching the encrypted store, and it contradicts the at-rest story in DEPLOYMENT ("everything except a small key file … is in a data file encrypted with AES-256-XTS") — `ca.key`, `tls.key`, `setup-token`, `setup-wifi` and `network.json` are also plaintext in that folder and are not named there.

**Recommendation.** State this in DEPLOYMENT's threat model explicitly: the data folder's plaintext inventory is the key file, the CA and server keys, the setup token and Wi-Fi password, and the logs. Then either accept it in writing (name constraints bound the blast radius to hub impersonation) or seal `ca.key` under the data key while the store is unlocked — renewal while locked is the only capability lost, and a locked hub has no clients to serve a fresh certificate to anyway.

### S2. The restore-from-stick restart never restarts the process (Medium)

The setup page's restore step stages the copy and calls `u.Restart()` (`internal/ui/setup.go:141`). In production `u.Restart` is the `shutdown` passed to `app.New` (`internal/app/app.go:102`), which only calls `srv.Shutdown`. `serveCtx` then blocks on `<-ctx.Done()` (`cmd/hub/main.go:174`) — the signal context, which fires only on SIGINT/SIGTERM. The process **neither exits nor re-execs**, so systemd's `Restart=always` never fires and the Windows service manager never restarts the service (the service keeps reporting "Running" with no listener; the per-user hub is dead until re-login). The staged restore is applied by `stick.Finish` "at the next start" (`internal/app/app.go:70`) — a start that never comes on its own. Meanwhile the hub serves nothing (the page says "restarting") while the sync workers keep running against the old store.

RECOVERY.md C2 and the e2e test both assume the hub restarts itself; the test simulates it by stopping the hub process itself (`internal/e2e/stick_test.go:82` `e.stop()`), so the production restart path is untested end to end. Upgrades are unaffected (they re-exec via `syscall.Exec` / `ExitRestart`).

**Recommendation.** After staging, exit the process so the service manager restarts it (`os.Exit(0)` after shutdown, as the Windows per-user re-exec already does), or re-exec like the upgrade path. Add an e2e step that stages a restore on a running hub and asserts the process actually comes back.

### S3. The released Windows installer is signed with a throwaway TEST certificate (Medium)

`windows/sign.sh` signs with `SIGN_CERT`/`SIGN_KEY` when set, otherwise with a self-signed test CA in `~/.cache/hub-signing` whose own comment says "Windows does not trust: no supplier certificate exists yet". The release workflow (`.github/workflows/release.yml`, "Windows installer" step) sets neither variable, so the published `dhis2-hub-<version>-setup.exe` — the one artifact a PC user runs — carries a signature no Windows machine trusts. Users will see the untrusted-publisher warning on every install and learn to click through it, which trains exactly the behaviour a future malicious installer needs. DEPLOYMENT's rollout section already names a supplier code-signing certificate as the requirement.

**Recommendation.** Make the release workflow fail (or publish the Linux assets and hold the installer) until `SIGN_CERT`/`SIGN_KEY` are set. `windows/check.sh` can assert the signing CA is not the test one.

### S4. Wi-Fi passwords appear in nmcli's process arguments (Low)

`internal/netsetup/manager.go` passes the PSK as argv (`"wifi-sec.psk", pw` at lines 192, 263, 305), so the facility Wi-Fi's password — and the setup Wi-Fi's — is visible in the process table to any local user who can run `ps` while nmcli works (up to 60 s for `connection up`). The error path was already hardened (`nmcliError` names the command, never its arguments — the authors knew), but the argv itself remains. On the Pi image the exposure is small (no login users; `hub` is `nologin`), but the hub also runs on multi-user Windows PCs and any future maintenance account would see it.

**Recommendation.** Pass the PSK over stdin (`nmcli --ask`) or a 0600 file, as NetworkManager supports, rather than argv.

### S5. The health check skips TLS verification against the hub's own status URL (Low)

`internal/upgrade/tls.go` builds a transport with `InsecureSkipVerify: true`, used only by the post-upgrade health check, which GETs `https://127.0.0.1:<port>/hub/status` (`cmd/hub/main.go` builds the URL). It is loopback-only and the URL comes from the hub's own listener, so the practical risk is a local process spoofing the endpoint to confirm an upgrade whose server did not actually come up — a broken upgrade would be recorded `ok` in the heartbeat.

**Recommendation.** Pin the hub's own leaf certificate (from `hubtls.Certs`) in the health-check client instead of skipping verification.

### S6. `HandedOverDK` trusts an inherited `HUB_DK_FD` at every start (Low)

`internal/upgrade/dk.go:17` reads `HUB_DK_FD` from the environment on every start (and `migrate`). A service environment or wrapper that leaks the variable makes the hub read a "data key" from an arbitrary inherited fd — or block on stdin if the value is `"stdin"`. A wrong key fails closed (the data file will not open), so this is robustness, not a vulnerability, but the variable is meant for the re-exec path only.

**Recommendation.** Clear `HUB_DK_FD` from the environment except in the re-exec child (it is already unset after reading), and treat `stdin` as valid only in the `migrate` subcommand.

### S7. The admin page renders the outbox's `LastError` raw; aggregate refusals now store central's raw message there (Low)

`internal/ui/templates/admin.html:41` renders `{{.LastError}}` unredacted (html/template-escaped; hub-side only). Since the aggregate work, `pushAggregate` stores central's raw refusal message in the outbox error (`internal/sync/aggregate.go:358`), and central can quote the entered value. This stays inside the hub — encrypted at rest, admin-only page — which is the residual review 1 accepted for tracker; the aggregate path widens it. Issue reports and the heartbeat remain reduced to codes and UIDs.

**Recommendation.** Run the stored outbox error through `tracker.Redact` (or the same masking the issues table gets) before rendering, as the issues rows already do.

### S8. The NSIS uninstaller's hosts rewrite drops the line endings (Low, quality — verify on Windows)

`un.RemoveHostsLine` (`windows/installer.nsi:362`) reads the system `hosts` file line by line and writes each kept line with `FileWrite $5 $8`. NSIS `FileWrite` appends no newline, so every kept line is concatenated into one, corrupting name resolution on the PC after uninstall. (`AddHostsLine` writes explicit `$\r$\n`; the rewrite does not.) The Windows CI uninstall step checks that the hub's line is gone, not that the file is still well-formed, so it would pass.

**Recommendation.** Write `$8$\r$\n` (skipping the hub's marked line). Add a CI check that the hosts file still has more than one line after uninstall. I could not run Windows here — worth verifying by hand.

### S9. Polkit grants the hub user every NetworkManager action (Low)

`image/files/etc/polkit-1/rules.d/50-hub-network.rules` returns YES for **all** `org.freedesktop.NetworkManager.*` for user `hub`. The hub needs connection management, but the grant also covers settings, permissions and device controls. A compromised hub process (which already holds the data key) could reconfigure the machine's network arbitrarily.

**Recommendation.** Restrict to the actions the hub uses (connection add/up/down/delete, device Wi-Fi list/rescan), or accept and note it.

### S10. The installer opens the hub's ports on every firewall profile (Low)

`windows/installer.nsi:219-220` adds the hub's TCP ports and UDP 5353 with `profile=any`. A hub PC on a hostile network (a laptop carried to a conference) exposes the hub there; the hub's TLS, authentication and Host check mitigate, but `profile=private,domain` would match how the PC is actually used.

**Recommendation.** Drop `profile=any` (or make it an install-time choice), and say so in WINDOWS-INSTALL.md.

### S11. `sameOrigin` accepts `Sec-Fetch-Site: none` on POSTs (Low)

`internal/ui/ui.go:177` passes a POST with no `Origin` and `Sec-Fetch-Site: none`. Browsers send `none` for direct navigations, not for form posts from a page; combined with `SameSite=Lax` (which does not send the cookie on cross-site POSTs) the residual risk is small. Review-2 F9 closed the empty-header case; `none` remains.

**Recommendation.** Accept `none` only for GETs, or drop it — `same-origin` is what a hub page's own posts send.

### S12. The login throttle's per-address and per-account counters survive a successful login (Low)

`throttle.ok` (`internal/auth/throttle.go:75`) clears only the `user@src` counter; the per-source (20) and per-account (50) counters keep counting, despite the comment "clears the per-user counters". A shared device where several staff mistype locks the whole device out (30 s doubling to 15 min) even after someone logs in successfully — a self-DoS on shared hardware. It is a deliberate anti-guessing tradeoff, but the comment overstates what happens and the behaviour is not documented anywhere.

**Recommendation.** Clear the per-source counter on any success from that source (the account counters should survive), and fix the comment.

### S13. Release downloads have an idle timeout but no total timeout inside the sync cycle (Low)

`upgrade.get` (`internal/upgrade/upgrade.go:298`) cancels a download that delivers nothing for 2 minutes, but a slow drip keeps it alive, and `Check` runs inside the sync cycle with the worker's context, which has no deadline. A stalling release channel can hold the cycle (and the next one) open indefinitely. The fix from review 1 (L4) covered the hung-download case, not the trickle.

**Recommendation.** Add a total cap (the binaries are ~16 MB; 10 minutes is generous) on top of the idle timer.

### S14. Smaller items (Low / notes)

- **`Hub.Unlock` leaves the store unlocked when the runtime state fails to parse or the index fails to load** (`internal/core/core.go:382,393` — the `afterUnlock` path does call `SetDK(nil)`, these two do not). A corrupt runtime leaves an unlocked store with no runtime: cycles skip, tracker calls refuse ("hub is not set up"), but cached GETs keep being served. Fail-safe enough; make it consistent.
- **The hub's own account password is now stored at rest** (`Runtime.HubPassword`, sealed under the data key, kept so a token that lapsed while offline can be recreated — `RenewHubToken`). New since review 2, deliberate, and lower-value than staff passwords (a dedicated no-authority account, facility-only), but it is one more secret in the encrypted state and belongs in the documented inventory.
- **The USB stick is now a near-equivalent of the SSD for credentials**: `keys.db` (plaintext key file — the offline password-guessing oracle) rides on it unsealed, while `state.rec`, `tls.rec` and the outbox records are sealed under the data key. RECOVERY.md documents this; the stick should be treated as a theft-tier-1 object.
- **The setup Wi-Fi password is written to the stick (`hub.txt`) and kept in the data folder** (`setup-wifi`, 0600). By design ("what anyone holding the box needs"); the setup page points to the stick instead of displaying it, and the password guards only the temporary provisioning network. Accepted; listed here for the inventory.
- **The admin-set proxy is process-global and persisted in the key file** (`MetaProxy`). Validated shape (http/https, host, port, no path or userinfo — `core.SetProxy`), admin-only. It sees CONNECT hostnames for central and the release channel; TLS stays end-to-end. Fine; noted because it is the one admin action with process-wide effect.
- **The migration child's output reaches the heartbeat** (`upgrade.go`: `Reason: "migration failed: " + out`, and the returned error to `LastErr`). The child is the signature-verified new binary, so only the supplier controls that text — acceptable, but it is the same class as M6 and worth a comment saying why it is trusted.
- **`throttle`'s memory bound and the Basic cache's fast hash** (`sha256(salt‖user‖password)` in memory) are both fine as they stand: the map eviction bounds memory, and a memory dump already yields the data key while the store is unlocked (`Lock` clears the sessions), so the hash adds nothing an attacker does not have.

---

## Part 3 — Code quality

**What is strong, and should be kept.**

- **Fail-closed is the default everywhere it matters.** Unknown query parameters → 400; unsupported `orgUnitMode` → 503; unknown config keys → error; `hub_hosts: "*"` → refused; deletes, file values and min-max edits refused offline; single-object miss and no-access get the same answer; attribute filters fail closed when the catalog is missing. The allowlists are explicit and the exceptions (`orgUnitMode=ACCESSIBLE` from facility data, `atomicMode=ALL` honoured, the shared static-file cache) each carry a comment naming the evidence and the review that decided them.
- **SQL is parameterized throughout.** The only string-built SQL is fixed identifiers and `IN (?,…)` placeholder lists (`internal/tracker/search.go`, `internal/store/objects.go`); I found no injection path. The search plans keep the SQL part and the Go part next to each other with the guards (`TestQueryPlansUseIndexes`, `TestFirstPageReadsOnWithoutGaps`, the live oracles) named in AGENTS.md.
- **The crypto and the file hygiene are careful**: random-nonce GCM with per-row AAD, HKDF-derived XTS file key, Argon2id slots with bounded concurrency, constant-time code/token compares, atomic synced writes for certificates, `secure_delete` and checkpointing for the key file, DPAPI sealing with a 2-minute life for the Windows key handover, and the DK never on disk unwrapped (pipe fd on Unix).
- **The comments cite their reasons** — reviews by ID, FINDINGS items, performance numbers, the failure that motivated a rule. This is what makes the invariants auditable, and it is why this review could verify the earlier fixes quickly.
- **Tests match the risks.** `internal/e2e/security_test.go` covers both earlier rounds' findings by name; the newer features have their own scenario files (`account_test.go`, `move_test.go`, `network_test.go`, `reach_test.go`, `stick_test.go`, `stick_auto_test.go`, `certificate_test.go`, `upgrade_choice_test.go`, `windows_setup_test.go`). The Windows CI installs, checks the service and ACLs, and uninstalls on a real runner.

**What to fix or watch.**

1. **S2 is the one lifecycle bug**: a documented recovery flow that stops serving and waits for a restart that never comes. It is also the clearest gap between docs and code found in this review.
2. **S8 (hosts rewrite)** is a plain quality bug in shipped installer code, invisible to CI as written.
3. **Comment/behaviour mismatches**: `throttle.ok` (S12); the DEPLOYMENT at-rest sentence vs the real plaintext inventory (S1). Both are one-line fixes and both mislead a reader who does not open the code.
4. **Consistency nits**: `Unlock`'s two unlocked-on-error paths (S14); `sameOrigin`'s `none` (S11); the release download's missing total timeout (S13).
5. **File sizes** (`internal/sync/sync.go` 1220 lines, `internal/fakecentral` 978, `internal/store/objects.go` 867, `internal/tracker/search.go` 765) are large but each opens with its invariants and its guards, and the structure is the one AGENTS.md asks to keep. No change recommended.
6. **Test gaps worth closing with the fixes above**: the restore's actual restart (S2); the CA-key-at-rest decision pinned by a test that greps the data folder for plaintext key material (S1); the hosts file's integrity after uninstall (S8); the health check pinned to the hub's own certificate (S5).

---

## Part 4 — Approach review (what carries forward)

**Still sound, unchanged.**

- Central stays the source of truth; visibility for central-origin data is derived from what each user's own pull returned, and local objects are checked against program/stage/type read plus search scope plus option-combination readability. The pull, the ownership check (transfers out) and the retired-hub check all tighten this further since the last review.
- Per-user PATs for all data access, attribution preserved across reconnect (aggregate edits queue as their author too), the sealed outbox, and the outbound discipline (codes and UIDs to central; parameter names in the request log; `MaskMessage` failing closed).
- The signed upgrade path: content-authenticated manifest and binaries, migration on a copy, frozen local writes, health check, rollback, key handover without locking the store. The one soft spot remains unsigned-content side effects — now closed (F2) — and the stuck-outbox precondition — now closed (F3).

**The residual risks, updated.**

1. **The facility-wide data key** still means the weakest staff password defines the theft boundary, and every key slot is an offline oracle for that user's *central* password. Unchanged; TPM remains a requirement for a national rollout (DEPLOYMENT says so).
2. **A running hub is the highest-value target** — data key plus every payload in RAM. Unchanged, and now sharper: the hub's own account password and move key live in the same encrypted state, and the CA key lives *outside* it (S1).
3. **Disk theft now also yields hub impersonation** (S1) — the CA key is plaintext, name-constrained to the hub's own names, so the thief can capture staff central passwords from devices that trust the hub's authority. New since the last review; the fix is either sealing the key or an explicit, written acceptance.
4. **DHIS2-semantics drift** is unchanged and is now the largest *correctness* surface: the aggregate forms (locks, windows, compulsory fields, value types), the enrol-once and non-repeatable checks, the AOC write/read rules, and the per-version compat files all re-implement slices of central. The guards (oracles, fixture tests, the endpoint log) are the right ones; the risk to manage is maintenance.
5. **Deployment prerequisites still open**: the `facility-hub-*` namespaces must be protected before the first hub (a forged registry entry retires hubs — a remote availability attack); the Windows installer needs a real signing certificate (S3); plain-HTTP Android remains the weakest transport link until the ACME/DNS-01 design exists; and the restore flow needs its restart fixed (S2) to be a recovery story at all.

**Confidentiality of the data flow, end to end (as the code stands).**

| Link | Protection | Gap |
|---|---|---|
| Client → hub | TLS by default (name-constrained CA, serverAuth-only); start page on plain HTTP serves nothing else; Lax/HttpOnly session cookie | Plain HTTP for stock Android (deployment); `ca.key` at rest widens the theft case (S1) |
| Hub → central | Per-user PATs, no redirect following, no `X-Forwarded-For`, verified TLS | Password fallback sealed at rest (off by default, F8); hub-account password sealed at rest (new, S14) |
| At rest | XTS data file + GCM rows, AAD-bound; locked at boot; slots pruned; WAL/temp encrypted | Key file (usernames, slots) and `ca.key`/`tls.key`/setup token/Wi-Fi password plaintext (S1, S14) |
| Hub → central data store | Codes and UIDs only; `MaskMessage` fails closed | Namespaces default-open on 2.42 (deployment prerequisite); admin page shows stored errors raw (S7) |
| Hub → release channel | Signed manifest + SHA-256, rollback, skip-after-failure | Test-signed Windows installer (S3); no total download timeout (S13) |
| USB stick | Sealed state, certs and saves; plaintext key file; password-gated restore | Stick ≈ SSD for credential guessing (S14) |
| Logs | Parameter names only; `Template` masks UIDs in slog too | requests.log retention undocumented (config-gated, off by default) |

---

## Suggested fix order

1. **S2** — the restore restart: exit after staging so the service manager restarts the hub; e2e it for real.
2. **S1** — CA key at rest: name the plaintext inventory in DEPLOYMENT's threat model; then seal `ca.key` under the data key or accept in writing.
3. **S3** — refuse to publish a test-signed installer; make the release workflow fail without `SIGN_CERT`/`SIGN_KEY`.
4. **S4** — Wi-Fi PSKs off the process arguments.
5. **S8** — the hosts rewrite's line endings (verify on Windows; extend the CI uninstall check).
6. **S5, S6, S7, S11, S12, S13** — health-check cert pinning; `HUB_DK_FD` hygiene; `Redact` on the admin outbox error; `Sec-Fetch-Site: none` on POSTs; throttle `ok` clearing the per-source counter; a total download timeout.
7. **S9, S10** — polkit action restriction; firewall profile narrowing.
8. **S14** — `Unlock` consistency, and the documented inventory of secrets at rest.

## Verification status

Not run here (no Go toolchain, no Windows): `go vet ./...`, `go test ./...` (unit, e2e, security regressions, live suite against the fake), `go test -race` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store`, `scripts/test-upgrade.sh`, `windows/check.sh` on a built installer, and the Windows CI install/uninstall. All findings above are from reading the code. Two claims I could not execute and would verify first: the NSIS `FileWrite` newline behaviour (S8 — NSIS documentation says no line ending is appended, but confirm on a real uninstall), and the restore-restart hang (S2 — the code path is clear, but confirm the process state on a real hub after staging a restore).

---

## Response (2026-10-20)

Each finding was checked against the code. Fixed on the branch `security-review-fixes`; the rest are accepted, with the reason.

| # | Verdict | What was done |
|---|---|---|
| S1 | **The facts are right; the conclusion is overstated.** `ca.key` is unencrypted, but `tls.key` must be too: the hub serves HTTPS before anyone logs in. `tls.key` alone already allows impersonating `dhis2.local` to the devices that trust the hub. `ca.key` adds only issuing new certificates for the same names and private addresses. Encrypting it would not close the risk. | DEPLOYMENT.md section 2 now lists every unencrypted file in the data folder and what a stolen disk or stick gives. TPM or secure boot closes it. |
| S2 | **Real.** The restore's restart only stopped the web server; the process then waited for a signal and was never restarted. | The hub now restarts itself after staging a restore, as an upgrade does: it stops, closes the store, and replaces the process (`cmd/hub/restart.go`). `TestRestartStopsClosesAndReplaces`. |
| S3 | **Real.** | A tagged release leaves the installer out unless the `SIGN_CERT`/`SIGN_KEY` secrets hold a real certificate. The installer can be built and signed on Linux or a Mac and attached by hand (`windows/README.md`); `macos.yml` builds it on a Mac. |
| S4 | Accepted. | `nmcli` runs only on the Pi image, which has no login users. Windows and macOS don't use it. |
| S5 | Accepted. | The check talks to the hub's own port on the same machine. The hub holds that port, so another program cannot answer in its place. |
| S6 | Accepted. | Only the hub sets the variable, for its own restart. A wrong key fails closed. |
| S7 | **Real, low.** | Aggregate refusals are stored with quoted values hidden, as tracker ones (`refusalNote`). `TestAggregateRefusalHidesValues`. |
| S8 | **Probably not real.** NSIS `FileRead` keeps the line ending, so writing the line back keeps it. | Windows CI now checks, after uninstalling, that `hosts` still has its lines and `localhost`. If this is wrong, CI shows it. |
| S9 | Accepted for now. | Narrowing the polkit rule can't be tested without a Pi. Revisit with the hardware tests. |
| S10 | Accepted, deliberate. | Facility networks are often marked "Public" on Windows; private-only rules would cut those hubs off. Noted in WINDOWS-INSTALL.md. |
| S11 | Accepted. | A cross-site page cannot make a browser send `Sec-Fetch-Site: none` on a form post, so it's no attack path. |
| S12 | **The comment was wrong; the behaviour stays.** | Clearing the per-device counter on any success would let one working account reset it and keep guessing others. The comment now says what happens and why. |
| S13 | **Fixed** (2026-10-20, branch `hardening-and-settings`). | A hard cap would stop a slow link from ever finishing. The binary now downloads in the background, beside the sync cycle, and a cycle upgrades once it is there and matches the signed release. `TestDownloadIdleTimeout`, `TestChoiceUpgrades`. |
| S14 | **Fixed** (`Unlock`, 2026-10-20). | Every failed step of `Unlock` locks the store again. `TestUnlockFailureLeavesStoreLocked`. The other items are inventory, now in DEPLOYMENT.md. |

