# Critical review — 2026-10-27

An independent critical review of the facility hub: security, data leaks and data loss,
bugs, redundancy and KISS. Read-only; no code was changed. File references are
`path:line` from the repository root.

Scope: the Go hub (`cmd/`, `internal/`), the Facility Hub app (`apps/facility-hub`),
the Pi image (`image/`, `deploy/`), the installers (`windows/`, `macos/`), CI
(`.github/workflows/`), the test clients (`clients/`, `scripts/`, `perf/`).

Verification: this environment has no Go toolchain and no installed `node_modules`, so
`go build`/`go vet`/`go test` and the app's jest suite were **not run**. Findings are
from reading the code; the code's own e2e suite (`internal/e2e`, incl.
`security_test.go`) covers much of what is praised below and should be run before any
change.

## Summary

| Severity | Count | Headline |
|---|---|---|
| High | 3 | Central-move verification leaks the hub token; world-writable `facility-hub-*` namespaces are trusted inputs; any USB stick auto-receives a full copy of the hub's secrets and pending patient data |
| Medium | 7 | Facility-wide data key; hub CA trusted for all private IPs; 8-digit setup Wi-Fi password; macOS installer version check is wrong; upgrade reports false success; Reexec failure leaves a zombie; `Wipe()` is not crash-atomic |
| Low | 20 | See the findings list |

The design is, on the whole, unusually careful: encrypted store with per-row AAD,
TPM-bound key slots, throttled logins, parameterised SQL, signed releases, masked
logs/heartbeats, a default-drop firewall, and no SSH or console user on the image.
The findings below are the places where that care has edges.

## High

### H1. The central-move check sends the hub's token to the candidate before verifying it — and the check is forgeable

`internal/core/move.go`:

- `refreshCentralMove` (move.go:44) reads the announcement
  `facility-hub-admin/central-move` from central and stores it.
- `TryCentralMove` (move.go:81) runs while central is unreachable, after
  `central_move_after` (default 1 h, `config/config.go` `CentralMoveAfter`).
- For each candidate it calls `sameCentral` (move.go:117), which does
  `GET <candidate>/api/dataStore/facility-hub-registry/<hubID>` **with the hub's PAT
  in the `Authorization` header**, and accepts the answer if it is 200 with a body
  whose `hubId` equals this hub's ID.

Two problems:

1. **The token is sent before verification.** Whatever the answer, the hub's PAT has
   already reached the candidate. If the candidate is an attacker's server, the
   housekeeping token is gone.
2. **The verification is satisfiable by anyone.** The hub ID is public: `/hub/status`
   answers it unauthenticated (ui/ui.go:110), the heartbeat carries it, the registry
   entry is readable, and `hub.txt` on the USB stick prints it. An attacker who can
   write `facility-hub-admin/central-move` just serves `{"hubId": "hub-…"}`.

On the shipped image `central_url` is empty and `central_hosts` is commented out
(`image/files/etc/hub/hub.yaml`), so `movable()` (move.go:66) accepts **any** https
URL. During any outage of an hour or more, every hub that can read the announcement
probes the attacker's server with its PAT and then switches to it — after which user
PATs (pushes authenticate as their authors, `sync/push.go`) and all outbox patient
data flow to the attacker.

The redirect path (`Monitor.MovedTo`, `central/central.go` `noteMove`) is safer — it
requires the *current*, TLS-verified central to answer with a permanent redirect —
but funnels into the same weak `sameCentral`.

**Recommend:** verify the candidate *anonymously* against the full registry entry
(the registry is world-readable on central) — check `publicKey`, `boxKey` and
`facility` against the hub's own runtime — and only then switch and send the token.
Alternatively sign the announcement. Until then, treat "protect the
`facility-hub-admin` namespace" as a hard prerequisite (see H2).

### H2. The hub trusts the `facility-hub-*` dataStore namespaces; on 2.42 they are world-writable until a country protects them

The code says so itself (`internal/core/core.go` `Retired()`: "on 2.42 the registry
namespace is writable by any user until protected"), and then trusts several of these
namespaces as inputs:

| Namespace key | Read by | Effect if an any-user writes it |
|---|---|---|
| `facility-hub-admin/accounts` | `core/tokens.go:45 shareWithManagers` | Every PAT the hub creates (user logins, hub token renewal) is shared `rw` with the named group — the attacker's group can use and delete the tokens |
| `facility-hub-admin/central-move` | `core/move.go refreshCentralMove` | H1: hub PAT and then all traffic to the attacker's server |
| `facility-hub-admin/release` | `upgrade/upgrade.go fetchChoice` | Force every hub to a chosen (signed) version — fleet-wide change trigger |
| `facility-hub-admin/retries` | `sync/push.go applyRetries` | Trigger resends of refused saves |
| `facility-hub-registry/<hubId>` | `sync/sync.go checkRegistry` | Mark a hub retired/revoked: it stops serving patient data and drops its tokens (availability; acknowledged in code) |
| `facility-hub-config/*` | `core/central_config.go refreshConfig` | Retention windows, token lifetime |

The Facility Hub app cannot enforce this itself — its "managers only" buttons are
client-side; the real control is central's sharing on these namespaces. The hub should
treat an unprotected namespace as a setup-time failure (or at least a loud warning),
and deployment docs should make protection a checked prerequisite. Today nothing on
the hub side detects it.

### H3. Any USB stick plugged into the hub is auto-mounted and auto-receives a full copy of the secrets and pending patient data

- `image/files/etc/udev/rules.d/90-hub-usb.rules` mounts **any** FAT/exFAT USB block
  device at `/media/hub-usb` (`systemd-mount --owner=hub`).
- `image/files/etc/hub/hub.yaml` sets `usb_dir: /media/hub-usb`.
- `internal/stick/stick.go Tick` mirrors every minute to that path:
  `keys.db` — the key file with each user's key slot **with the password alone**
  (`store/mirror.go CopyKeysTo` replaces the device-bound slots with `stick_slot`);
  `state.rec` — every user's sealed PAT/password, the hub token and the hub account's
  password; `tls.rec` — the CA **private key**; `outbox/<seq>.rec` — each unsent save,
  i.e. pending patient data; `hub.txt` — the setup Wi-Fi password.

This is the designed recovery copy (docs/RECOVERY.md, C2), and the restore path is
good (a staff password is checked against the copy's slots, `stick/mirror.go Stage`).
But because the udev rule mounts *any* stick, anyone with a minute of physical access
gets all of it. The device-key (TPM) binding that protects the on-disk key file is
deliberately removed on the stick copy, so the stick alone supports offline Argon2id
guessing (64 MiB, t=2 — slow per guess, but a GPU rig parallelisises it) and then
decrypts everything: all users' central credentials, the hub account password, the CA
key (→ H5) and the unsent patient data.

**Recommend:** bind the mount to the box's stick (filesystem UUID/label in the udev
rule), or require the stick to be armed explicitly (a file on the boot partition, or
an admin-page action per stick, as Windows' `usb_dir: auto` already does with its
`Locate`/`Choose` choice). Document the exposure prominently.

## Medium

### M1. The data key is a facility-wide confidentiality boundary

Every user's PAT and (with `password_fallback`, off by default) central password, the
hub account's password (`Runtime.HubPassword`, `core/core.go:76`) and the hub token
are sealed under one 256-bit data key (`store/users.go`, `store/store.go`). Any
facility user who logs in unwraps their slot and holds the DK; from there
`UpstreamAuth`/`Store.PAT`/`Store.Password` decrypt **every** user's credentials.
There is no API that exposes them to other users, but there is no separation either:
one weak staff password (plus the key file, or the USB stick — H3) opens all users'
central accounts. This is inherent to pushing offline saves as their authors, and the
Argon2id parameters are reasonable; the blast radius should be stated in the docs and
considered when `password_fallback` is ever enabled.

### M2. The hub's CA is trusted for **all private IP addresses**, and installed into the system trust store

`internal/hubtls/hubtls.go` `newCA` sets `PermittedIPRanges` to 127/8, 10/8,
172.16/12, 192.168/16, 169.254/16, 100.64/10, fc00::/7, fe80::/10 (the DNS constraints
are properly narrow). The Windows installer puts the CA in the machine **Root** store
(`windows/installer.nsi` `certutil -f -addstore Root`), the macOS script in the
**System** keychain (`macos/install.sh` `security add-trusted-cert -k
/Library/Keychains/System.keychain`). A leaked CA key — and it is copied to the USB
stick (`tls.rec`, sealed with the DK) — can mint a certificate for *any* private
address: the facility router, an internal central server, any LAN host. Devices that
trust the hub CA would accept it. The intent was "only the hub", but "private ranges"
is every other device on the facility LAN. **Recommend:** narrow the IP constraints to
the hub's actual addresses (the leaf certificate already re-issues when addresses
change; the CA constraints are fixed at creation, which is why they were widened), or
document the risk explicitly.

### M3. The setup Wi-Fi password is 8 digits

`internal/netsetup/manager.go` `setupPassword`: 8 numeric characters, "the least
Wi-Fi allows, typed on a phone's number pad". 10^8 is brute-forceable offline from a
captured WPA2 handshake. The setup code protects the setup flow itself, but on the
setup Wi-Fi (10.42.0.0/24) the network-change endpoints (`/hub/network/join`,
`/hub/network/facility`, `ui/network.go`) need only the Wi-Fi password — an attacker
on the setup network can repoint the hub's uplink to their own AP (TLS to central
still verifies, so this is DoS/traffic-visibility, not data theft) and reach every
unauthenticated page. **Recommend:** a longer random password (the hub already prints
one on the stick's `hub.txt`).

### M4. The macOS installer's version check is wrong (and fails open)

`macos/install.sh` compares the installed and new versions with
`printf '%s\n%s\n' "$OLD" "$NEW" | sort -V | tail -1`. Two problems:

- `sort -V` is GNU version sort, not semver: `0.2.0` sorts **before** `0.2.0-ci.57`
  (verified), so a CI pre-release is installed *over* a release — the exact downgrade
  the check exists to prevent ("Never put an older version over a newer one").
- macOS ships BSD `sort`, which has no `-V`; the pipeline then fails, the substitution
  is empty, the `[ "" != "$NEW" ]` test is true, and the script silently keeps the old
  binary with a wrong message ("Keeping the newer hub …").

The Windows installer does it right (`installer.nsi` runs `hub-new.exe version
$OLD`, which uses the hub's own `CompareVersions`). **Recommend:** the macOS script
call `"$WORK/hub-new" version "$OLD"` the same way.

### M5. A failed store swap is reported as a successful upgrade

`internal/upgrade/upgrade.go` `check`: the marker (`upgrade.json`, state `swapped`)
is written and `upgrade_skip` set **before** `swapStore`. If `swapStore` fails, the
hub exits and restarts on the old binary; `AfterBoot` then health-checks the old
binary, passes, and records `lastUpgrade: {result: "ok", to: <new version>}` — and
`upgrade_skip` prevents any retry. The heartbeat tells central the hub upgraded when
it did not. **Recommend:** write the marker only after a successful swap, or have
`confirm` compare the running version with `mk.To` before recording `ok`.

### M6. A failed `Reexec` on Unix leaves a zombie process

`upgrade/reexec_unix.go` `Reexec` returns only when `syscall.Exec` fails. In
`check()` that happens after `Shutdown()` (store closed, server stopped) and with
`ok=true` set, so the deferred `ThawLocalWrites` is skipped: the process keeps
running with a closed store, frozen local writes and no listener; systemd does not
restart it (the process never exits). Rare (exec fails only if the new binary cannot
be executed), but the hub should `os.Exit(1)` on Reexec error so the service manager
restarts it — on Windows `Reexec` already exits (`reexec_windows.go`).

### M7. `Store.Wipe()` is not crash-atomic

`store/store.go` `Wipe`: removes the data file (and its WAL/SHM) **before** clearing
the key file's rows. A power cut in between leaves key slots present with no data
file; `SetDK` then refuses ("data file is missing", store.go:392) and, because
sessions are memory-only, nobody can log in to reset — the hub is unrestorable without
the USB stick. **Recommend:** clear the key file first (or write a wipe marker and
let `SetDK` tolerate a missing data file when it is present).

## Low

Bugs and robustness:

- **L1 `/hub/status` is unauthenticated** (`ui/ui.go:110`): hub ID, exact hub version,
  sync/lock state, stuck-outbox count to anyone on the LAN. Version disclosure aids
  targeting. It is the health-check and start-page probe endpoint; consider trimming
  or gating it.
- **L2 `proxy.shared()` compiles a regex per request** (`proxy/proxy.go:218`,
  `regexp.MustCompile(`^[0-9]{2}/`)` inside the function). Every proxied request pays
  for it; `meRe` at package level shows the intended pattern. Hoist it.
- **L3 `swapStore` deletes the live WAL/SHM before renaming** (`upgrade/upgrade.go`):
  after a clean `Close()` SQLite has checkpointed, so this is normally a no-op, but a
  kill in the wrong instant could drop committed transactions. Fragile rather than
  wrong; a comment saying why the removal is safe would help.
- **L4 The downloaded upgrade binary is verified at the start of the cycle but used
  minutes later** (`upgrade/upgrade.go` `check` → `replaceBinary`): drain, backup and
  migration run in between; the file is in a hub-writable directory. Re-verify the
  checksum immediately before the swap. (Local-attacker only.)
- **L5 `checkSetupToken` uppercases the typed code but not the stored one**
  (`ui/setuptoken.go`): an old-format code stored with lowercase letters can never
  match, despite the comment that it "still works until it is used".
- **L6 `testkit/android.go` `Screenshot` builds a shell command by concatenation**
  (`sh -c "adb … > '"+path+"'"`) — command injection if `path` contains a quote.
  Test-only code, but the argv form costs nothing.
- **L7 `TypedMove` verifies no server at all** (`core/account.go`): unlike the
  announced path it never checks `sameCentral`; the hub moves to the typed address
  vouched only by the 50-bit move code and then sends the hub PAT and all user traffic
  there. By design (the code is the vouch), but the asymmetry with `TryCentralMove`
  is worth a comment, and a leaked code plus social engineering moves a hub.
- **L8 `hubaccount.token()` / `approve.ts token()` use `% len(alnum)`** — modulo bias
  (256 % 62 = 8). Negligible at 28 characters; noted for completeness.
- **L9 `internal/aggregate` masking on the admin page**: `tracker.Redact` keeps
  anything outside backticks, so a central error message carrying a patient value
  *outside* quotes would render on `/hub/ui`. Central shows the same message to the
  same user online, so exposure is equivalent; noted, no action.

Security hygiene / hardening:

- **L10 Wi-Fi passwords as `nmcli` argv** (`netsetup/manager.go` `Join`,
  `MakeFacilityWifi`): visible in the process list to same-user/root while the
  command runs. Minor on the single-purpose image.
- **L11 polkit grants the `hub` user all `org.freedesktop.NetworkManager.*` actions**
  (`image/files/etc/polkit-1/rules.d/50-hub-network.rules`). Broad but defensible on
  the appliance; enumerating the used actions would be tighter.
- **L12 `hub.service` has no systemd hardening** (`deploy/hub.service`): no
  `NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`, etc. The hub needs only its data
  dir, the dnsmasq drop-in dir and the USB mount; hardening would shrink the blast
  radius of a hub compromise. (`AmbientCapabilities=CAP_NET_BIND_SERVICE` is already
  minimal.)
- **L13 Windows installer excludes the whole data folder from Defender**
  (`installer.nsi` `Add-MpPreference -ExclusionPath '$DataDir'`, included in silent
  installs) — and the hub binary itself runs from that folder. The folder's ACLs
  (Administrators/SYSTEM/service account, `TakeOverDataDir`) contain the risk; still a
  blind spot worth documenting.
- **L14 `hub-diag` writes hub logs to the boot partition** (`image/files/usr/local/sbin/hub-diag`):
  150 lines of `journalctl -u hub.service` plus all warnings, on FAT, readable by
  anyone with the SD card. Staff usernames and network state; no patient data (the
  masking is careful). Fine for support; note the audience.
- **L15 `unattended-upgrades` reboots the hub at 03:00** (`image/files/etc/apt/apt.conf.d/20auto-upgrades`,
  `Automatic-Reboot "true"`): an availability decision for a device holding unsent
  patient data; fine if intended.
- **L16 `image/customize.sh` installs apt packages unpinned** (the base image is
  sha256-pinned): image integrity depends on the Pi OS mirrors at build time.
- **L17 CI does not run on push or pull requests**: `test.yml`, `windows.yml` and
  `macos.yml` are `workflow_dispatch` only; `release.yml` runs on `v*` tags. PRs are
  not gated (the comments say this is a spending decision).
- **L18 `macos/install.sh` does not verify `release.json.sig`**: it checks the
  binary's SHA-256 against `release.json` fetched over TLS, relying on TLS to GitHub
  alone; the hub's own upgrade path verifies the ed25519 signature. Defence-in-depth
  gap.
- **L19 `.gitignore` does not cover the release signing key**: the README's
  `./hub keygen -out release` writes `release.key` (the release workflow's secret) in
  the repo root; `.gitignore` has `/hub`, `/hub-*`, `*.db`… but no `*.key`. One
  `git add -A` from committing the signing secret.
- **L20 `AccountCode` is derivable from the public registry entry** (unkeyed SHA-256
  over public values — `hubaccount/codes.go`, the comment admits the key is public).
  Its value is as a human-verification ritual, not a secret; the real gate is the
  box-key encryption. Fine as designed; noted so nobody mistakes it for one.

Memory / availability:

- **L21 `core.MaxBody` = 64 MiB buffered in RAM per request** (`core/util.go:39`,
  used by the proxy and tracker import): on a 1 GB Pi with 60 concurrent users,
  concurrent large sync payloads pressure memory. The comment acknowledges it; consider
  a smaller cap for offline writes or streaming the import.
- **L22 The login throttle is in-memory** (`auth/throttle.go`): a restart resets it.
  Bounded and acceptable; noted.
- **L23 The aggregate value-type check (`aggregate.ValidValue`) and lock logic are
  conservative and correct as read; the offline aggregate path refuses file values and
  min-max edits rather than approximate them.** No finding — recorded as reviewed.

## Data leaks — where the sensitive data is, and what protects it

| Data | At rest | In transit | Notes |
|---|---|---|---|
| Patient tracker payloads | `hub.db.data`, AES-256-XTS page encryption + per-row GCM | TLS to central; hub's own TLS to clients | Strong; the whole file is encrypted, not just columns (store/store.go) |
| Per-user PATs / passwords (fallback) | Sealed per row under the DK | Sent to central only | M1: one DK opens all |
| Hub account password, hub token, box/move keys | `state` table, sealed | To central only | M1 |
| Key slots | Key file, plaintext by design, wrapped under Argon2id(password) (+ device key where present) | — | H3: the stick copy is password-only |
| TLS keys | Sealed with the device key (TPM) where present | — | H3: CA key also on the stick |
| HTTP cache (per user) | Sealed per row with user-bound AAD | — | Shared only for static/loginConfig paths (`proxy.shared`) |
| Logs, request log, heartbeat, issue reports | Data dir; heartbeat/issues to central | — | Masked of patient values (`tracker.OutgoingMessage/MaskMessage`, `Redact`); request log keeps parameter names only; L14 (boot partition) |
| USB stick copy | See H3 | — | The weakest link |

## Data loss — the paths that could lose unsent saves

- **H3** (stick handling) is the main leak; the mirror deletes a save from the stick
  once pushed or refused (`stick/mirror.go`), so a restore never resends — good.
- **M7** (`Wipe` ordering) can brick a hub mid-wipe.
- **M5** (false upgrade success) can leave hubs on an old version believing they
  upgraded — combined with a later release's migrations, an un-upgraded hub then
  refuses to start on a newer store (handled: `migrate` refuses newer schemas).
- **M6** (Reexec failure) freezes local writes until restart.
- The outbox itself is well done: sealed payloads with seq-bound AAD, dependency
  ordering, rejected entries kept with issues, refused-then-changed-on-central
  resolution (`core/objects.go changedSinceRefused`), stuck entries reported, reset
  refuses to wipe with unreported refused changes unless `RESET AND DISCARD` is typed
  (`app/app.go Reset`). A facility staff session can wipe everything with two typed
  words (`ui/ui.go handleReset`) — by design ("any staff member may reset"), and the
  refusal is loud; worth keeping the confirmation exactly as loud as it is.
- The upgrade path drains the outbox before swapping and tolerates entries whose
  author must log in again (`upgrade.go` step 2) — good; a stuck author's saves survive
  in the store copy.

## Redundancy and KISS

- **L2** (per-request `regexp.MustCompile` in `proxy.shared`) is the one clear KISS
  violation found: a package-level pattern already exists (`meRe`).
- The deliberate mirrors — `CompareVersions` (Go) vs `compareVersions` (TS),
  `AccountCode`/`MoveCode` (Go) vs `codes.ts`, FNV staging (`upgrade.InStage` vs
  `stage.ts`), the approval flow (`hubaccount/approve.go` vs `hubaccount/approve.ts`)
  — are documented as contracts, pinned by shared test vectors (`testdata/stages.json`,
  `hubaccount/testdata`, `codes_test.ts`). Not redundancy; do not merge.
- `internal/compat` keeps one file per DHIS2 version, per AGENTS.md. Fine.
- The config parser (`config/config.go`) is a hand-rolled flat-YAML reader, justified
  in its comment (no nesting in the image file). It validates `hub_hosts: *` away.
  Fine.
- `auth.LoginWeb`/`loginWeb` and `LoginBasic`/`loginBasic` are thin throttle wrappers,
  not duplication.
- Nothing found that is dead code; the tree is consistent with AGENTS.md's "keep the
  structure" rule.

## What is done well (so it is not "fixed")

- Store: two-file design, XTS page encryption, per-row AES-256-GCM with AAD,
  `secure_delete`, WAL + `synchronous(FULL)`, TPM-sealed device key, Argon2id key slots
  bound to the device key, timing-equalised unknown-user logins, login throttling with
  per-user/per-source keys, constant-time code comparisons.
- Supply chain: ed25519-signed release manifest, public key baked in at build, draft
  release published only when complete, tag must match `VERSION`, Windows installer
  dropped rather than shipped unsigned.
- Web: parameterised SQL throughout with allowlisted `ORDER BY`; `Host` validation
  (anti-DNS-rebinding); CSRF via `Origin`/`Sec-Fetch-Site`; CSP/XFO/nosniff on hub
  pages; `html/template` auto-escaping with no unsafe template funcs; no
  `dangerouslySetInnerHTML` anywhere in the app.
- Network: default-drop nftables, LAN-only 80/443, mDNS scoped to `dhis2.local`;
  captive-portal detection that refuses to call a proxy error page "online".
- Secrets hygiene: proxy URLs with credentials rejected (`core/reach.go SetProxy`);
  error text that could quote submitted values masked before logs/heartbeat
  (`tracker.OutgoingMessage`/`MaskMessage`); request log keeps parameter names only;
  Wi-Fi passwords not written to diag; the setup code removed from the stick once used.
- Image: no SSH, no console user, dedicated `hub` user with only
  `CAP_NET_BIND_SERVICE`, watchdog, base image sha256-pinned, image checked without
  booting (`image/check.sh`).
- Tests: a real e2e suite with a security section (`internal/e2e/security_test.go`),
  golden fixtures per DHIS2 version, live oracles, and an upgrade scenario script.

## Suggested order of work

1. H2/H1 — namespace protection as a checked prerequisite + anonymous candidate
   verification (or signed announcements).
2. H3 — bind the USB mount to the box's stick (or arm per stick).
3. M5/M6/M7 — upgrade/wipe bookkeeping and crash-atomicity fixes (small, local).
4. M4 — macOS installer version check (use `hub version <other>`).
5. M2 — narrow the CA's IP constraints or document; L19 — ignore `*.key`.
6. L2 — hoist the regex; L17 — decide whether PRs should run tests.
