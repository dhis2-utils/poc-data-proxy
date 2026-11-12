# Facility hub: independent review

Date: 10 November 2026. Subject: the tree in this folder, `VERSION` 0.1.5 (not a git repository, so no commit is named). Reviewer: Claude (Anthropic), working in a sandbox with a Go 1.26.8 toolchain and disposable DHIS2 2.42 and 2.43 instances.

## 1. Summary

The facility hub does what its specification asks, and does most of it carefully. The code builds cleanly, all of its own tests pass, the live suite passes against real DHIS2 2.42 and 2.43, and every acceptance scenario I tried by hand behaved as documented. Encryption at rest, per-user visibility, attribution of offline work to its author and the fail-closed rule all held up under direct probing.

It is not ready for a field pilot yet. The review found eight high-severity defects, five of which I reproduced on a running hub with one or a few HTTP requests:

| # | Finding | Who can trigger it | Status |
|---|---|---|---|
| R-01 | A relationship that points at itself makes the hub recurse until it dies, and again after every restart | Any registered user, including one with read-only access | Reproduced |
| R-02 | The hub trusts `deleted`, `hubStub` and `updatedAt` sent by a client: records can be deleted offline, and one bad value breaks every list that includes the row | Any user with write access to the type | Reproduced |
| R-03 | Event lists filter, and person lists sort, on attributes the caller may not read | Any registered user | Confirmed in code |
| R-04 | The login throttle counts a failure only after checking the password, so parallel guesses all get through | Anyone on the LAN | Reproduced |
| R-05 | One wrong Basic-auth password for a user ends that user's sessions and blocks their offline login | Anyone on the LAN, no login | Reproduced |
| R-06 | The request log keeps every distinct path in memory for ever | Anyone on the LAN, no login | Reproduced |
| R-07 | Instructions read from central's dataStore are trusted on a sharing check an ordinary DHIS2 user can satisfy, and some on no check | Any DHIS2 user on central, under conditions | Confirmed in code |
| R-08 | The release workflow signs "dry runs" with the real release key, and the key is reachable from any branch | Anyone with write access to the repository | Confirmed in the workflow |

None of these gives an unauthenticated person patient data. R-03 lets logged-in staff learn attribute values of programs they have no access to. The others are about availability (R-01, R-05, R-06), integrity of the hub's copy (R-02), password guessing (R-04), the fleet's control channel (R-07) and the supply chain (R-08).

Below these are 26 medium findings and a list of low ones (sections 6.2 and 6.3). The ones I would fix before any pilot, besides the eight above:

- **R-09:** a set-up interrupted at the wrong moment, followed by a power cycle, leaves a hub that can never be set up (reproduced).
- **R-10 and R-11:** an offline update can move an event to another patient, and an offline dry run (`importMode=VALIDATE`) is stored and sent as a real write (both reproduced).
- **R-12 to R-14:** two upgrade paths with no recovery, and the scenario 11 script fails as shipped.

What stands out on the positive side:

- **Discipline in the code.** `go vet` is clean on three platforms, staticcheck reports 17 minor items in about 85,000 lines of Go, and comments say why a rule exists and which test guards it.
- **Testing far beyond a proof of concept.** There are about as many lines of tests as of code. A differential oracle compares the hub's answers with central's for 37 users, and I ran it against DHIS2 2.42: 4,884 comparisons, no unexplained leak.
- **Sound security foundations.** Row-bound authenticated encryption, a single password verifier, an unprivileged service user, a name-constrained certificate authority, no SQL built from request values, strict manifest verification before an upgrade.

The pattern in the findings is consistent. The hub was tested hard against central's behaviour and against network and power failures. It was tested much less against a hostile or careless client on the LAN, and input validation at the trust boundary is where most defects sit. The second weak area is the control channel through central's dataStore, which the documents already describe as weak and which has grown more powerful with each feature.

## 2. Scope and method

### What was reviewed

Everything in the tree: about 43,000 lines of Go outside tests, 40,000 lines of Go tests, the Facility Hub app (8,100 lines of TypeScript), the Pi image, the Windows and macOS installers, the GitHub workflows, the Python client drivers and 4,200 lines of documents.

`docs/reviews/` and `docs/superpowers/`, which the README and many code comments cite, are not in this copy. I did not see the earlier reviews, so this one is independent of them; some findings may repeat what they said.

### How

1. **Reading the design.** README, the specification, FINDINGS, DEPLOYMENT, PERFORMANCE, TESTING, RECOVERY, NO-RTC and AGENTS.md.
2. **Building and running the project's own checks.** Details in section 4.
3. **Eight parallel code reviews**, one per area, each by a separate AI reviewer with a written brief and threat model: crypto and authentication; tracker and access control; sync and core; proxy, aggregate and store; admin pages, set-up and upgrade; the Facility Hub app; image, installers and CI; the test suite.
4. **Verification.** I re-checked the sub-reviews' most serious claims myself, by reading the cited lines and, where possible, by reproducing them on a hub I ran against the project's fake central.
5. **Black-box testing** of a running hub with TLS on: set-up, logins, offline work by four users with different access, reconnect, restart, and hostile requests.
6. **The live suite against real DHIS2 2.42 and 2.43** (`cmd/hubtest` provisioning the synthetic profile on clean instances from the instance broker).

### How to read the status of a finding

- **Reproduced:** I triggered it on a running hub and saw the effect.
- **Confirmed in code:** I read the lines that prove it; not executed.
- **Reported:** a sub-review traced the code path and I did not re-verify it. These are plausible and specific, but treat them as leads until checked.

### What was not assessed

- Real hardware: a Raspberry Pi, a TPM, USB sticks, Wi-Fi, power cuts.
- Windows and macOS at run time, including the installers.
- The Android Capture app and the real web apps in a browser (the Playwright and emulator tests were not run).
- The Sierra Leone and Laos profiles of the live suite; only the synthetic profile was run.
- The adversarial suite against a real DHIS2 (135 of its 144 tests need one).
- The Facility Hub app in a browser against a live instance; its own checks were run.
- The GitHub repository's settings (rulesets, environments, who may push tags).
- Load and performance; the numbers in PERFORMANCE.md were not re-measured.

## 3. What the project is

A single Go binary that runs on a small device on a health facility's LAN. Browsers and tablets use the hub's address (`https://dhis2.local`) instead of central DHIS2. While central is reachable the hub passes requests through with each user's own personal access token and caches GET responses per user. When it is not, the hub serves the cached responses, answers the tracker API and the Aggregate Data Entry app's data endpoints from an encrypted SQLite store, and queues writes. A sync worker pushes the queue as each author, pulls changes, reports refused changes, and sends a heartbeat.

Around that core the project has grown a good deal past the original specification: aggregate data entry, event programs, retention, a certificate authority per hub, a TPM-sealed device key, a USB stick mirror for recovery, network management on the Pi, a Windows service and installer, signed self-upgrade with staged rollout, a hub account approved from a DHIS2 app, and a procedure for central moving to a new address.

## 4. What was run, and the results

### 4.1 Build and static checks

| Check | Result |
|---|---|
| `go mod verify` | All modules verified. `golang.org/x/*` came from the GitHub mirrors' tags, since the Go proxy and checksum database are blocked in the sandbox; the hashes in `go.sum` match those tags. Not compared with `sum.golang.org`. |
| `go build ./...` | Clean. Stripped `linux/arm64` binary: 17.7 MB (README says about 16 MB). |
| `go vet ./...` | Clean for linux, `windows/amd64` and `darwin/arm64`. |
| `gofmt -l .` | Six files are not formatted: `internal/aggregate/lock_test.go`, `internal/e2e/retries_test.go`, `internal/fakecentral/fakecentral.go`, `internal/live/a15_metadata_test.go`, `internal/sync/sync.go`, `internal/tracker/search.go`. |
| staticcheck (current `master`) | 17 items: 6 unused identifiers, 5 uses of the deprecated `ReverseProxy.Director` in tests, 3 values never used (one in `internal/tracker/import.go:206`), 3 style notes. |

### 4.2 The Go test suite

`go test ./...` on a copy of the tree: all 32 packages with tests pass. The slow ones are `internal/live` against the fake (200 s) and `internal/e2e` (192 s).

Statement coverage, with every package's tests counted towards every package (`-coverpkg=./...`): **82.7 %** of the production packages (9,666 of 11,686 statements).

| Package | Coverage | Package | Coverage |
|---|---|---|---|
| `aggregate` | 93.3 % | `store` | 83.8 % |
| `central` | 91.1 % | `hubaccount` | 82.3 % |
| `compat` | 90.0 % | `mdns` | 81.4 % |
| `access` | 89.5 % | `stick` | 81.2 % |
| `proxy` | 89.3 % | `app` | 79.0 % |
| `auth` | 88.7 % | `ui` | 77.0 % |
| `core` | 88.1 % | `aggregateapi` | 74.8 % |
| `hubcrypto` | 88.0 % | `upgrade` | 71.9 % |
| `tracker` | 87.5 % | `config` | 63.3 % |
| `netsetup` | 86.3 % | `devkey` | 56.0 % |
| `trackerapi` | 85.3 % | `cmd/hub` | 24.8 % |
| `hubtls` | 84.9 % | | |
| `sync` | 84.3 % | | |

Two things qualify that number:

- Most of it comes from end-to-end tests. `internal/auth`, `internal/trackerapi`, `internal/aggregateapi` and `internal/app` have no tests of their own, and `internal/sync` has 16 lines.
- `internal/adversarial` runs 9 tests and skips 135 without a real DHIS2 (`HUB_MANIFEST`).

### 4.3 Scenario 11 (`scripts/test-upgrade.sh`)

**The script fails as shipped** (finding R-14). With two corrections in a scratch copy it passes: an unsigned release is ignored, a signed one installs with the data key handed to the new process, and a release that fails its health check is rolled back and reported in the heartbeat. So the upgrade code works; its only automated end-to-end check has gone stale.

### 4.4 The live suite against DHIS2 2.42 and 2.43

**DHIS2 2.42.7.1.** A clean instance, provisioned with the synthetic profile (37 users, 5 tracker programs, 76 tracked entities), then `go test ./internal/live/` against it.

**Result: pass**, in 1,486 seconds. 34 top-level tests passed and 89 were skipped: 60 need the fake central's fault injection (they ran in 4.2), 18 need the real client apps or release zips, and the rest need files, a slow-link set-up or a built app.

The differential read oracle (`TestOracle`, 751 s) compared hub and central for 37 users and 132 queries each, 4,884 answers:

| Verdict | Answers | Meaning |
|---|---|---|
| MATCH | 1,724 | Same answer |
| REFUSED | 2,976 | The hub answered "needs connection" or refused |
| DIFFERS | 74 | Known gap: central collates text by locale, the hub by bytes |
| MISSING | 70 | Known gap: events for a user without access to the tracked entity type |
| LENIENT | 36 | Known gap: the hub answers an `ACCESSIBLE` search with fewer attributes than the program requires, which central refuses |
| LEAK | 4 | Known gap: a paged list ordered by `scheduledAt` puts events without a date at the other end, so the first pages differ. Not a disclosure |

Every difference is one the test names and explains, and the test passes. Two remarks:

- 61 % of the answers are refusals. Refusing is safe, but the oracle has no floor on matches, so a hub that refused everything would also pass (section 10).
- The LENIENT gap means the program's "minimum attributes required to search" is not enforced offline.

The write oracle (`TestWriteOracle`) made 740 writes: 735 matched central, and in 5 the hub refused what central accepts (an event in an enrollment the sibling facility owns), which the test names as not a failure.

**DHIS2 2.43.2.1.** The same on a clean 2.43 instance: **pass**, in 1,240 seconds, 34 passed and 89 skipped as on 2.42. The read oracle gave 1,643 matches, 2,976 refusals, 161 differing, 52 missing and 52 lenient answers, and no leak; every difference is a named known gap. Two of those gaps are specific to 2.43: a program with an enrollment category combination, where central shows people and events the hub does not hold (52 answers), and an order by `scheduledAt` on an event program, which 2.43 refuses and the hub answers as 2.42 does (16 answers). The write oracle made 814 writes: 805 matched and 9 were refused by the hub where central accepts them.

The fake central is fixed at 2.42.6, so nothing in the default test run executes `internal/compat/v43.go`; 2.43 is covered only by a live run like this one.

### 4.5 The Facility Hub app's own checks

Run by the app sub-review in a scratch copy:

| Check | Result |
|---|---|
| `pnpm install --frozen-lockfile` | OK |
| Tests | 17 suites, 207 tests, all pass |
| `tsc --noEmit` | Clean. With `--strict`: 4 errors (`strict` is not set in `tsconfig.json`) |
| ESLint | Clean |
| Prettier | Fails on `pnpm-lock.yaml`, so `pnpm lint` fails; there is no `.prettierignore` |
| Production build | OK, 1.84 MB zip; the main chunk is 649 kB |
| `pnpm audit --prod` | 10 advisories, all in transitive dependencies of `@dhis2/ui` and `@dhis2/app-runtime` |

### 4.6 Black-box testing of a running hub

A hub with TLS on, set up against the fake central, with the users `hub_admin`, `nurse_a`, `nurse_b` (no access to stage 3), `clerk_c` (program read-only) and `outsider_d` (another facility).

What behaved correctly:

- **Response headers on `/hub/*`:** a content security policy with `frame-ancestors 'none'` and `form-action 'self'`, `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`, HSTS for seven days. TLS 1.2 and 1.3 only.
- **Host check:** requests with `Host: evil.example.com`, `10.1.2.3` or `8.8.8.8` get 421.
- **Cross-site posts:** `POST /hub/setup` and `/hub/ui/sync` without an `Origin`, or with a foreign one, get 403.
- **Session cookie:** 192 random bits, `HttpOnly; Secure; SameSite=Lax`, 12 hours.
- **Without a login:** `/api/*` answers 401, `/hub/ui` redirects to the login page.
- **Offline sharing and stage access:** `nurse_a` registered a person with stage 1 and stage 3 events. `nurse_b` found the person by a `like` search and saw only stage 1 through every path I tried: the list, the person with `fields=*`, the enrollment's nested events, `events?event=`, the single event (503 "needs connection") and `events?programStage=` (403, E1006). She could not update the hidden event.
- **Write access:** `clerk_c` got 409 with E1095. **Facility scope:** `outsider_d` got 403.
- **Injection attempts** in filter values, filter attributes and `order`: no effect.
- **Per-user cache:** offline, `nurse_b` got 503 for the API paths only `nurse_a` had fetched, and her own `/api/me`, not `nurse_a`'s.
- **Encryption at rest:** with the hub running and the write-ahead log present, no file in the data folder contained a patient value, a UID, a program or org unit identifier, a password or a token. The plaintext key file holds what the documents say: usernames, hub ID, central's address, version and time zone.
- **Offline login:** an unknown user and a wrong password get the same answer in about the same time.
- **Restart while offline:** the store is locked, old sessions are refused, an offline login unlocks it, the four pending objects are still there, and after reconnecting they reach central with `createdBy` set to `nurse_a`.

What did not is in section 6.

## 5. Architecture

### 5.1 Shape

```
cmd/hub ── app (wiring, router)
            ├─ ui            /hub/* pages, set-up
            ├─ auth          logins, key slots, throttle
            ├─ trackerapi    /api/tracker/*      ─┐
            ├─ aggregateapi  data entry endpoints ├─ core.Hub (shared state)
            ├─ proxy         everything else      │    ├─ store    (SQLite: key file + encrypted data file)
            ├─ sync          push, pull, heartbeat┘    ├─ tracker  (model, search, import, visibility)
            ├─ upgrade, stick, netsetup, mdns          ├─ central  (HTTP client, connectivity monitor)
                                                       └─ access, aggregate, compat, hubcrypto, hubtls …
```

The import graph is acyclic and reads well. `store` owns all SQL and depends only on `hubcrypto`. `tracker` knows nothing of HTTP. Version differences sit in `compat`, one file per DHIS2 version. `app` is the only place where everything meets.

### 5.2 What works well

- **A small dependency set.** Three direct modules (`go-sqlite3`, `go-tpm`, `x/crypto`) and the standard library. No web framework, no ORM.
- **One static binary, no C toolchain.** That keeps cross-builds and the upgrade path simple.
- **The storage split.** A small plaintext key file for what the hub must know while locked, and a page-encrypted data file for everything else, with sensitive payloads sealed again per row. The reasoning (FINDINGS item 23) is right: index columns identify patients.
- **Central decides access.** The hub stores what central showed each user, not a reimplementation of sharing, and it tests that claim against central with the oracle.
- **Fail closed as a habit.** Unknown or invisible objects get the same 503; unsupported parameters are refused and logged.
- **The rule in AGENTS.md** that a mechanism needs a measured problem. The history in PERFORMANCE.md shows mechanisms being removed again, which is rare.

### 5.3 Concerns

**1. Central's dataStore is the fleet's control channel, and it is not authenticated.** Hubs take from it which version to run, where central has moved, which refused saves to resend or throw away, how long to keep data, who the hub managers are, and whether the hub itself is retired or revoked. The only protection is DHIS2 sharing on each entry, which the hub checks in a way an entry's creator can pass (R-07). Each new feature has added another instruction to this channel. The signed release manifest shows the alternative: an instruction that carries its own proof. A message authentication code made with the move key each approved hub already holds, or a signature by a country key pinned at set-up, would remove the whole class.

**2. Input from clients is trusted more than input from central.** Central's answers are parsed strictly and compared by oracle. A client's tracker payload is merged into the store with hub-owned fields still in it (R-02), relationship ends are not type-checked (R-01), identifying fields can be changed by an update (R-10), and paths are forwarded and classified without being normalised (R-16). The hub is the trust boundary for every device on the facility Wi-Fi, so it needs one place that canonicalises paths and one that strips or overwrites every hub-owned field before anything is stored.

**3. `core.Hub` is a shared object that everything reaches through.** It exports `Store`, `Index`, `Central` and `Monitor`, and holds runtime state, object writes, trust decisions about central, and observability. The practical cost is that there is no transactional boundary: `sync_state` is written by the sync worker, the request handlers and the pull, each in its own commit, and validation of an import is separate from persisting it (R-33). Splitting out the code that decides what to believe from central (`account.go`, `move.go`, `tokens.go`, `central_config.go`) into its own small package would also make that code easier to audit.

**4. The hub's admin pages share an origin with proxied DHIS2 content.** `/hub/*` gets a strict content security policy; proxied responses get none. Script injected into anything central serves runs on the hub's origin and can drive the admin pages in a staff member's session. Serving `/hub/*` on a separate port would end the coupling.

**5. One data key protects everything.** Every key slot wraps the same key, and personal access tokens are sealed with it. One staff password plus the USB stick therefore yields every registered user's token, including users from above the facility who logged in once (R-25). Sealing tokens under a key derived from their owner's password would limit that, at the cost of pushes needing the author to have logged in since the last restart.

**6. Breadth.** A proof of concept that now includes a certificate authority, a TPM integration, an mDNS responder, a NetworkManager controller, a Windows installer and a DHIS2 app has a large attack surface, and each part has had less review than the core. Several medium findings sit in exactly those parts (set-up, USB restore, installer, image). For a pilot, consider shipping fewer of them.

**7. SQLite compiled to WebAssembly and translated to Go.** The choice gives a static binary and memory-safe execution of SQLite's C code. The costs are speed and memory against native SQLite, which PERFORMANCE.md measures, and dependence on one maintainer for the translator, the driver and the `vfs/xts` layer that the encryption at rest rests on. That layer deserves a review of its own before a national rollout.

## 6. Findings

Severity is judged for a proof of concept heading towards field pilots, against this threat model: someone on the facility LAN without a login; a logged-in staff member with little access; someone who takes the powered-off device, its disk or its USB stick; a spoofed central; and an ordinary DHIS2 user on central who can write dataStore entries.

### 6.1 High

#### R-01. A relationship that points at itself kills the hub, persistently

- **Status:** Reproduced.
- **Where:** `internal/tracker/import.go:608-624` (`validateRelationship`), `import.go:153-165` (`resolve`), `internal/tracker/index.go:404-406` (`CanRead`).
- **What happens:** the import only checks that both ends of a relationship resolve to something, not to what kind. A payload that lists the same relationship UID twice, the second time with `to` pointing at the relationship itself, is accepted. `CanRead` for a relationship asks `CanRead` of both ends, with no depth or cycle guard.
- **What I saw:** `clerk_c`, who has read-only access to the program, posted the payload offline and got 200. The relationship type was `"x"`, which does not exist. The next read of that person with relationships, by any user, hung. The hub's memory grew to 2.4 GB in 15 seconds, the log showed `runtime: goroutine stack exceeds 1000000000-byte limit` and `fatal error: stack overflow`, and the process exited. After a restart and a login, opening the person started the same recursion.
- **Why it matters:** a stack overflow is not a panic, so the recovery middleware cannot catch it. The row survives restarts. Android downloads every person in a program, so one tablet sync brings the hub down each time. Central will refuse the relationship on push, and refused objects stay on the hub.
- **Fix:** require each end's kind to match what the reference names (tracked entity, enrollment or event). Refuse relationship ends that are relationships in `CanRead` too, so rows already stored cannot crash a hub. Check that the relationship type exists and that the user may write it. Add a test with a self-referencing and a mutually referencing pair.

#### R-02. Client-supplied `deleted`, `hubStub` and `updatedAt` are trusted

- **Status:** Reproduced for `deleted`; the other two confirmed in code by the sub-review.
- **Where:** `internal/tracker/import.go:628-635` (`normalise`), `internal/tracker/slim.go:19-24`, `internal/store/objects.go:71` and `:224-225`, `internal/core/objects.go:177` and `:203`, `internal/sync/pull.go:351`.
- **What happens:** `normalise` only gives `deleted` a default, so a client's value stays. The store derives its `deleted` and `stub` columns from the object's JSON.
- **What I saw, offline:**
  1. `nurse_b` posted the seeded person with `"deleted": true` and got 200. The person then answered 404 "could not be found", was gone from every other user's lists, and a later edit was refused with E1114 "already deleted and can't be modified". The spec says deletes are refused offline.
  2. She posted another person with `"deleted": "x"` and got 200. A list with `includeDeleted=true` then answered 500 with `search: sql: Scan error on column index 16, name "deleted": sql/driver: couldn't convert "x" into type bool`. The ordinary list no longer included the person, with no error, and the single read answered 503.
- **Why it matters:** Android downloads with `includeDeleted=true`, so the first case tells tablets to drop the record and the second fails every tablet's download of the program. The 500 also sends SQL error text to the client. Reported, not reproduced: `"hubStub": true` in an online save turns the hub's enrollment into a stub, and a far-future `updatedAt` stops the pull from ever refreshing the row.
- **Fix:** treat `deleted`, `hubStub`, `createdAt`, `updatedAt`, `createdBy` and `updatedBy` as hub-owned in both the offline and the online path: strip or overwrite them before storing. Set the `deleted` and `stub` columns from typed Go fields, not from JSON. Make the pull re-fetch when the stored time differs, not only when it is older.

#### R-03. Event lists filter, and person lists sort, on attributes the caller may not read

- **Status:** Confirmed in code. Not reproduced: the fake central has one tracker program, and the synthetic profile holds no attribute values that belong to one program only. What central answers to the same requests was not checked.
- **Where:** `internal/tracker/search.go:895-903` (events, `filterAttributes`), `search.go:640-644` (person `order`), `internal/tracker/filter.go:378-401` (`attrSQL`).
- **What happens:** `te_attrs` holds every attribute of a person, from every program's pull merged. The person search checks each filter attribute with `Catalog.ReadableAttribute` (`search.go:632-637`). `ReadableAttribute` has only one other caller, the renderer. The same filters on events go into SQL with no such check, and `order=<attribute>` on persons sorts on all stored attributes.
- **Scenario:** a nurse who can read the outpatient program but not the HIV program asks for `GET /api/tracker/events?program=<OPD>&orgUnit=<F>&filterAttributes=<HIV attribute>:!null`. The answer lists the outpatients who have a value in an HIV-only attribute. With `sw:A`, `sw:AB` and so on she recovers the value. `order=<HIV attribute>:desc` on a person search puts those people first.
- **Why it matters:** this is the disclosure FINDINGS item 9 set out to prevent, and it is the one finding that touches the hub's first promise, that each user sees only what central lets them see.
- **Fix:** check `ReadableAttribute` for every `filterAttributes` and every `order` attribute before building the search. Add both parameters to the oracle's query generator, and give the synthetic profile program-only attribute values so the oracle can see the difference.
- **Added 11 November, after the oracle was extended:** central does the same. On 2.42.6 and 2.43.2, `syn_clerk`, who cannot read the TB program, gets 14 maternal events for `filterAttributes=<TB number>:!null` and 4 for `sw:TB-0001`; `order` on the TB number sorts people by it. The hub, with the fix, refuses these filters and sorts as for empty values, so it is stricter than central here. The oracle accepts the difference (`acrossPrograms` in `internal/live/oracle_test.go`). To be reported to DHIS2.

#### R-04. The login throttle counts after checking, and forgets

- **Status:** Reproduced.
- **Where:** `internal/auth/auth.go:60-67` and `146-155`, `internal/auth/throttle.go:33-72`. The same pattern guards the setup code (`internal/ui/setup.go:148-154`) and the take-over and move codes (`internal/ui/ui.go:477-504`).
- **What happens:** a request first asks whether the user and address are locked, then checks the password, then counts a failure. Nothing reserves an attempt.
- **What I saw:** with the hub offline, 60 parallel logins with wrong passwords for `clerk_c` all got 401, meaning all 60 were checked. The limit is 5 per user and address. Sequential attempts afterwards got 429.
- **Two more weaknesses in the same code:**
  - A counter that has not reached its limit is forgotten 15 minutes after its first failure (`throttle.go:52`). So 49 guesses per 15 minutes against the shared per-user limit of 50 never lock anything.
  - The per-address limits are avoided by changing address, which is easy on a LAN.
- **Why it matters:** offline, the only real bound on password guessing is the two Argon2 slots, about 7 guesses a second on a Pi. Each successful guess is a staff member's DHIS2 password. The same burst also queues behind those two slots and delays real logins. For the 6-digit setup code the arithmetic is worse than the comment in `internal/ui/setuptoken.go:20-23` says ("guessing takes years"): about 4,700 guesses a day without any parallelism, so an even chance in about 15 weeks, and much sooner with it.
- **Fix:** count on entry and clear on success, or hold a per-user lock across check and count. Keep a counter that reached its limit until a quiet period has passed. Bound the queue for Argon2 and answer 429 beyond it. Make the setup code 8 or more digits; it is read from a file. Add a concurrent test; `TestLoginThrottle` is sequential.

#### R-05. One wrong Basic password, from anyone, logs a user out and blocks their offline login

- **Status:** Reproduced.
- **Where:** `internal/auth/auth.go:162-178`.
- **What happens:** when central answers 401 to a Basic credential, the hub ends all of that user's sessions and sets `needs_login`. It does this also for a credential it has never seen succeed.
- **What I saw:** `nurse_a` was logged in on the web. Without any login, I sent `curl -u nurse_a:x https://<hub>/api/me` once and got 401. Her session then answered 401, the admin page showed "Needs online login: yes" for her, and after the uplink was cut her correct password was refused with "central refused this account; log in again online".
- **Why it matters:** usernames are not secret. One request per name, sent while the hub is online, locks every staff member out of offline work until each logs in online again; done before an outage, nobody can unlock the store. A mistyped password on one tablet does the same to its owner's web session. While the flag is set, that user's queued saves are not pushed.
- **Fix:** end sessions and set `needs_login` only when the refused credential is one the hub has cached as good. For any other, return central's 401 and count the failure.

#### R-06. The request log grows without bound, for anyone

- **Status:** Reproduced.
- **Where:** `internal/core/reqlog.go:71-99` (`Record`, `counts[k]++`) and `:103-108` (`Miss`); `cmd/hub/main.go:146` (no `MaxHeaderBytes`, so 1 MB).
- **What happens:** every proxied request, with or without a login, adds its method, path template and parameter names as a key to an in-memory map that is never trimmed. Only UID-shaped path segments are collapsed.
- **What I saw:** 300 anonymous requests with random 500 kB paths took 4 seconds and raised the hub's memory from 154 MB to 546 MB. It was still 546 MB after 20 idle seconds.
- **Why it matters:** about a thousand such requests exhaust a 1 GB Pi. The restart leaves the store locked until a known user logs in, so offline service is down for everyone.
- **Fix:** cap the maps (a few thousand keys, the rest counted as "other"), truncate paths and parameter names in the key, and set `MaxHeaderBytes` to about 64 kB.

#### R-07. Instructions in central's dataStore are trusted on a weak check, or none

- **Status:** Confirmed in code for the sharing check and for `applyRetries`, which I read; the move, the config keys and the app's write sequence were traced by the sub-reviews. That an entry's creator keeps write access while its public access is read-only is the project's own measurement (DEPLOYMENT.md, "Security operations"; `apps/facility-hub/src/hubs/adminEntry.ts:86-88`); I did not re-measure it.
- **Where:** `internal/core/central_config.go:58-68` (`writableByAll`), `internal/core/move.go:45-74`, `internal/sync/push.go:511-617` (`applyRetries`), `internal/core/reserved.go:26-35`, `internal/core/prune.go:49`, `apps/facility-hub/src/hubs/adminEntry.ts:36-51` and `:90-103`.
- **What happens:**
  1. `writableByAll` looks only at an entry's public access. A user who creates a missing entry and shares it read-only for everyone else passes the check and can still write it.
  2. `facility-hub-admin/retries` is not checked at all before it is applied. An instruction can discard every refused save of a kind, program and error code, or resend refused saves as another named user.
  3. `facility-hub-config/*` (retention, pool sizes, local reads, metadata reload) is outside the check, is not pre-created by the app, and its values are applied without bounds.
  4. The app, when the writer does not own an admin entry, deletes it, recreates it as `{}`, sets sharing and then writes the value. The hub stores "the instructions I have applied" as the IDs currently in the list. A hub that reads `{}` in that window forgets them all and later applies every instruction again, old discards included.
- **Scenarios:**
  - Before a hub manager first opens the app, any DHIS2 user creates `facility-hub-admin/central-move` pointing at their own server, shared read-only. When central next fails to answer as central for an hour, each hub that is not pinned sends its token to that server and then follows it.
  - Any DHIS2 user creates `facility-hub-config/retention` with `{"defaultMonths": 1}`. Every hub prunes its offline data at its next cycle.
  - Two managers alternate on the Refused changes page. A hub that syncs between the app's delete and its final write re-runs a discard from months ago against whatever is refused today. Refused saves exist only on the hub.
- **Why it matters:** these are fleet-wide effects from one write on central, by people the design does not intend to trust. The documents accept that unprotected entries are "only flagged"; for discards, resend-as and retention that acceptance looks wrong.
- **Fix:** authenticate instructions (section 5.3, point 1). Until then: have hubs ignore `retries`, `accounts` and every `facility-hub-config` key that fails the protection check, and check the owner as well as public access; keep the applied set as one that only grows; clamp retention and pool sizes; have the app never publish a placeholder and verify owner and sharing of every entry when it opens.

#### R-08. The release signing key is exposed by the workflow

- **Status:** Confirmed in the workflow file. Whether repository settings limit the damage is not visible from the tree.
- **Where:** `.github/workflows/release.yml:10-18`, `:101`, `:117-124`.
- **What happens:**
  - The "dry run" started by hand signs with the real key whenever the secret is set (`if [ -n "$RELEASE_SIGNING_KEY" ]; then …`). README.md says a dry run "signs with a throwaway key".
  - The key is a plain repository secret with no protected environment, so a workflow on any branch can read it.
  - `permissions: contents: write` applies to every job, including the ones that run tests and build the image with `sudo`.
  - `secrets: inherit` passes every secret to `image.yml`, which uses none.
  - Any `v*` tag on any commit starts a signed, published release.
  - Actions are pinned by tag, not by commit.
- **Why it matters:** a valid signature is all a hub checks before it replaces its own binary. Anyone with write access, or with a developer's stolen token, can obtain the key or a signed release of arbitrary code.
- **Fix:** move the signing secrets to a protected environment with required reviewers, used only by the publish job on `v*` tags. Never use the real key unless the event is a tag push. Set `contents: read` at the top and `contents: write` on publish only. Drop `secrets: inherit`. Check that the tagged commit is on the default branch. Pin actions by commit. Longer term, sign offline with a hardware key.

### 6.2 Medium

#### Set-up, upgrade and recovery

**R-09. An interrupted set-up leaves a hub that cannot be set up after a restart.** *Reproduced.* `internal/ui/setup.go:428-436` creates the data key and the data file, then makes several calls to central; the first key slot is written only at `:484`. I made central answer 500 to the program list during set-up ("Setup failed: reading programs"), restarted the hub, and every later attempt failed with "Setup failed: data file: sqlite3: file is not a database". The file is encrypted with a key that existed only in memory. A Pi has no console or SSH, so the box needs re-flashing. Fix: delete the data file when set-up fails before commit; in `SetDK`, delete an existing data file when the key file has no slots; write the setup user's key slot before setting the mode to active.

**R-12. A failed fetch of the signature file is recorded as a bad signature, for good.** *Confirmed in code.* `internal/upgrade/upgrade.go:306-312`: `if err != nil || !Verify(...)` sets `upgrade_refused`. A timeout, a 404 during the seconds when `release.json` is uploaded and `release.json.sig` is not, or a captive portal's page, pins the version as refused until the country edits its choice. The heartbeat then says "the release has no valid signature". At the last rollout stage the choice no longer changes, so such hubs stay behind silently. Fix: retry on network errors; refuse only when both files were fetched and verification failed.

**R-13. A release that rejects `hub.yaml` is never rolled back.** *Reproduced in part.* `cmd/hub/main.go:111-119` loads the config before `upgrade.CountStart`, and `internal/config/config.go:216` rejects unknown keys. A hub with `release_channel:` in its config (the key from the specification's own sample) exits with `unknown key "release_channel"` before the start is counted. A future release that drops or renames a key would exit at every start under systemd's `Restart=always`, and the rollback after three failed starts would never run. Separately, a migration that exceeds its two-minute limit marks the version as failed for ever (`upgrade.go:233-244`), though a slow card may only have needed more time. Fix: count the start before parsing strictly, or have the old binary run the new one with a "check config" flag before swapping; treat a migration timeout as "try again".

**R-14. `scripts/test-upgrade.sh` fails as shipped.** *Reproduced, three runs.*
  - The script waits 10 seconds for the hub to notice central is down. Since probes run every four ping intervals while online (`internal/central/central.go`, `nextDelay`) and two must fail, that takes 8 to 16 seconds with the script's settings; I measured 15.7.
  - Step 2 publishes a signed 0.2.0 after an unsigned 0.2.0 was refused. Since refused manifests are remembered until the choice changes (R-12's mechanism), the hub never fetches it again.

  No workflow runs the script, and FINDINGS lists scenario 11 as passing by it. Fix the script and run it in CI.

**R-15. Restoring from the USB stick bypasses the setup code and the central pin.** *Confirmed in code.* `internal/ui/setup.go:127-145`, `internal/stick/mirror.go:224-294`, `internal/core/core.go:290-295`. The restore step checks only that the posted password opens a key slot in `keys.db` on the stick, a file the stick's author wrote. The hub then takes central's address from that file, in preference to `central_url` in its config, and the certificate authority from `tls.rec`. Someone who prepares a stick with a hub of their own, swaps it into an unconfigured hub and posts the restore from the LAN gets a hub that looks set up, points at their server and uses an authority whose key they hold; staff then type their DHIS2 passwords into it. Fix: require the restored address to pass `CentralAllowed` and be https; require the setup code for a restore; show the copy's central address for confirmation.

**R-23. A forged "revoked" takes effect when the hub's token is next refused for any reason.** *Confirmed in code.* `internal/core/refresh.go:300-311` and `:436-447`. The hub distinguishes a real revocation from a forged registry entry by whether central still accepts its token. A token also stops working when it expires during a long outage or its owner is disabled. A planted `status: "revoked"` then makes the hub drop its tokens, and its unsent work "can no longer be sent" (`internal/app/app.go:243-248`). Fix: make revocation reversible, as "retired" is, or require an authenticated marker.

#### Tracker data integrity

**R-10. An update can move a record to another enrollment, stage, program or person.** *Reproduced.* `internal/tracker/import.go:324-375`, `:420-477`, `:566-585`. Only the tracked entity type is protected against change. Offline, `nurse_b` posted an existing event with another person's enrollment and another stage; the hub answered 200, and the event then read as belonging to the other patient. Write access is checked against the new stage only, so a user with write on stage 2 can take over a stage 1 event. If central refuses the push, the misfiled copy stays on the hub as a refused change. Fix: refuse a changed `program` or `trackedEntity` on an enrollment and a changed `programStage` or `enrollment` on an event, with central's error codes, and check access against the stored object as well.

**R-11. A dry run sent offline is stored and pushed as a real write.** *Reproduced.* `internal/trackerapi/trackerapi.go:382-391`, `:416-422`. `importMode=VALIDATE` is accepted and ignored: the object was readable afterwards and `/hub/status` showed one pending entry. Fix: validate only, or refuse with "needs connection".

**R-28. The hub's copy drifts from central in two ways.** *Reported. One simple attempt at the first item on 2.43 (a single enrollment read with `fields=enrollment,orgUnit,status`) did not reproduce it; the sub-review says it needs the merged data to differ from the stored copy.*
  - An online read with a narrow `fields` list rewrites the row's index columns from the response alone (`internal/trackerapi/trackerapi.go:146-160`, `internal/core/objects.go:111-144`). `GET /api/tracker/enrollments/<E>?fields=enrollment,orgUnit,status` can leave the enrollment without its person and program columns, so it drops out of offline lists until the pull sees a newer version.
  - Merging is additive for `attributes`, `dataValues` and `notes` (`internal/tracker/model.go:178-225`). A value cleared on central stays on the hub, and a client that saves the whole record offline sends it back.

  Fix: take columns from the merged object; replace `dataValues` and `notes` when the response is a full object.

**R-33. State changes that should be atomic are not.** *Reported.*
  - Imports validate under a shared lock and persist later (`internal/trackerapi/trackerapi.go:348-353`, `:412`), so two imports of the same new UID both validate as "create", and the unique-value and enrol-once checks can both pass.
  - `upsertCentral` reads `sync_state` and writes later (`internal/core/objects.go:68`, `:170`, `:227-236`); a local save in between is overwritten and marked synced. Combined with `internal/sync/push.go:125-127`, an edit can be finished as sent without having been sent.
  - `UpdateRuntime` is an unlocked read-modify-write called from login handlers and the worker (`internal/core/core.go:395-403`); a lost update can drop a new hub token.

  Fix: one import lock across validate and persist; a `WHERE sync_state <> 'pending'` guard inside the transaction; a mutex on `UpdateRuntime`.

#### Sync

**R-21. Several conditions stop the outbox, and with it everything else.** *Reported; the fixed timeouts are confirmed in code.*
  - An entry is sent as one request with fixed timeouts of 60 seconds to the first byte and 5 minutes in all (`internal/central/central.go:80-83`). A 6 MB upload from a tablet on a 20 kB/s link needs 300 seconds for the upload alone. Each attempt fails, marks the hub offline, and the same entry is first again. The cycle returns at the push, so the hub also stops pulling.
  - Any timeout on a proxied request marks central unreachable for everyone (`internal/proxy/proxy.go:77`, `central.go:194-198`). A slow analytics query can flip the hub offline at will.
  - An aggregate edit that gets a 500 ends every push at that entry, tracker entries included, with no day rule (`internal/sync/aggregate.go:339-342`). Any 2xx answer counts as stored without a look at the body (`:350-354`).
  - An entry that cannot be pushed, because its author must log in again or because it depends on one that does, keeps the whole hub answering locally for up to 7 days (`internal/core/core.go:201-211`).

  Fix: split large entries and scale the timeout with size; let the probe, not one request, decide that central is down; give aggregate pushes the tracker rules; count only entries the next push can send.

**R-22. A refused edit is replaced by the hub's own later pushes, and its issue is deleted.** *Reported.* `internal/core/objects.go:170-176` and `:528-538`, `internal/sync/push.go:336-355`. Central's version replaces a refused copy when its `updatedAt` is later than the refusal. A nurse's refused phone-number correction, followed by a visit that is accepted, is replaced in the same cycle, and one cycle later the issue is gone from central. FINDINGS says the values "remain in the issue on central". Fix: compare only fields the refused payload touched, and keep the issue as "superseded".

**R-34. The pull reads too much into some answers.** *Reported.* Any 4xx except 408 and 429 counts as "access withdrawn" (`internal/sync/pull.go:273-277`), so on a full pull a proxy's 403 page hides a whole program until the next successful full pull. `{}` counts as an empty listing (`internal/compat/v42.go:269-281`). Cursors use the newest `updatedAt` exactly, with no overlap (`pull.go:225-233`). Fix: accept only DHIS2 web messages with access codes as refusals, require the collection key, and subtract an overlap from the cursor.

#### Proxy and cache

**R-16. "Static, shared by all" is decided too broadly.** *Classification confirmed in code; the first item reported.*
  - `isStatic` (`internal/proxy/proxy.go:303-317`) treats any path outside `/api/` with a listed extension as an app file anyone may be served, without a login when offline (`:497-499`). The replay re-parses stored URIs with `url.Parse`, which reads `//h/api/x.json` as host `h` and path `/api/x.json` (`internal/proxy/replay.go:57-66`, `:164-177`). So if a user's request for `//h/api/…json` gets a cacheable answer, the next user's replay fetches the real `/api/…` path with that user's token and stores the answer under the static-class key. I checked that DHIS2 2.42 behind Tomcat alone answers such paths with 400, which blocks this; a front end that collapses double slashes before Tomcat would not.
  - `/api/apps/menu.json` is classed static, but central answers it per user (checked on 2.42).
  - `GetCacheAnyUser` returns the newest row whatever its status or owner (`internal/store/misc.go:123-134`), so a redirect to the login page cached for an anonymous request can be served to a logged-in user offline in place of another user's good copy.

  Fix: reject request paths that are not clean in the root handler; make `isStatic` a positive list; build replay requests from the stored path, not a re-parse; never serve another user's copy to a request without a session outside the login app's files.

**R-17. Client `X-Forwarded-*` headers reach central, and one user can break an app for the others.** *Reproduced on DHIS2 2.43.* `internal/proxy/proxy.go:35-42` and `:72-76` drop only hop headers and `X-Forwarded-For`. Central writes its base URL into an app's page from `X-Forwarded-Port`, with an unchanged `ETag`, and `revalidate` serves the newest copy from any user when central answers 304. Through a hub, one user requested `/apps/capture` with `X-Forwarded-Port: 9999` and `If-None-Match: "x"`; the page came back with `dhis2-base-url` set to central's host on port 9999. Another user who then opened `/apps/capture` without any header got the same page. The app's API calls then go to a dead address until someone gets a fresh 200 from central for that page. Fix: forward an allowlist of headers, or drop `X-Forwarded-*` and `Forwarded`.

**R-18. Several stores grow without a bound.** *Reported; the last item confirmed in code.*
  - `http_cache` is pruned by age only, and anonymous requests write to it (`internal/proxy/proxy.go:366-372`, `internal/store/misc.go:70`). A loop over `?x=N` on a login-app file fills the card.
  - Expired sessions leave the map only when looked up (`internal/core/sessions.go:68-89`).
  - Async import reports and bodies of up to 16 MB are kept for an hour, unbounded in number (`internal/trackerapi/trackerapi.go:163-180`, `:442-453`).
  - `multipart.ReadForm` temporary files are never removed (`internal/aggregateapi/aggregateapi.go:195`); parts above 1 MB land unencrypted in the temporary folder.

  Fix: a byte budget for the cache with a small one for anonymous entries; a periodic sweep of sessions; bounds on the job maps; `defer f.RemoveAll()`.

**R-19. `/hub/status` reads and decrypts the whole pending outbox.** *Confirmed in code.* `internal/ui/ui.go:130` calls `StuckOutbox`, which calls `PendingOutbox` (`internal/store/outbox.go:148-154`) only to count entries per user. The endpoint needs no login, and the start page on every device probes it. After a long outage a few parallel calls allocate the outbox several times over. Fix: count in SQL.

**R-20. One request can load far more than the device has.** *The page size and the uncapped reads are confirmed in code, and I observed `pageSize=100000000` accepted; the other two items are reported.*
  - `pageSize` has no upper bound and `paging=false` is honoured (`internal/tracker/query.go:138-159`). `paging=false` with nested fields renders a whole program into memory.
  - An event list with a data-value filter decodes the payload of every candidate event at once (`internal/tracker/search.go:910-934`). This is an ordinary working-list filter in Capture.
  - `fields` is parsed recursively with no limit on depth or length (`internal/tracker/fields.go:20-64`).
  - Answers from central are read whole with no size cap in eight places (for example `internal/central/central.go:247`, `internal/proxy/proxy.go:82`).

  Fix: cap `pageSize`, the length and depth of `fields`, and central's answers; filter data values in bounded chunks. Per AGENTS.md, measure the data-value filter at 150,000 events first.

#### Secrets and keys

**R-24. The data file's encryption key can end up in the log.** *Confirmed in code; the sub-review reproduced the error text with the same driver.* `internal/store/store.go:413` passes the key in the URI of `VACUUM INTO`. SQLite's error for a destination it cannot open quotes the URI. The upgrade logs that error (`internal/upgrade/upgrade.go:224-226`), and on the Pi `hub-diag` copies the last 150 log lines to the unencrypted boot partition every 30 minutes. Fix: keep the key out of the URI, or redact `hexkey=` before wrapping the error.

**R-25. Tokens outlive a reset, and one password opens all of them.** *The reset is confirmed in code; the rest is reported, and partly a design decision.*
  - Reset deletes only the hub's own token (`internal/app/app.go:285-287`). Users' tokens stay valid on central, and the old hub's folder stays on the USB stick with its key slots, tokens and the authority's key.
  - Every key slot wraps the same data key and tokens are sealed with it, so one staff member's password plus the stick yields every registered user's token. `Belongs` (`internal/core/refresh.go:51-61`) registers users assigned to any ancestor of the facility, so a district or national account that logged in once is included.

  Fix: delete users' tokens and the stick's folder on reset. Give users from above the facility pass-through only, or no stored token. State the insider case in DEPLOYMENT.md.

**R-29. Two short secrets guard the set-up.** *The throttle arithmetic is confirmed in code (R-04); the rest is reported.* The setup code has 6 digits. The setup Wi-Fi's password has 8 digits, which is found offline in minutes from one captured handshake, and before set-up that Wi-Fi is the only gate on making the hub join another network (`internal/ui/network.go:48-83`). Fix: longer values, and require the setup code for network changes before set-up.

#### Platforms and deployment

**R-26. On the Pi image the `hub` user can become root, and the service is not sandboxed.** *The files are confirmed; the dnsmasq path is reported and needs a Pi.*
  - `hub-diag` runs as root every 30 minutes and executes `/usr/local/lib/hub/hub version`, a file the `hub` user owns (`image/customize.sh:23`, `image/files/usr/local/sbin/hub-diag:9`).
  - The `hub` group can write NetworkManager's dnsmasq configuration folder (`image/customize.sh:28-29`), and the polkit rule allows the `hub` user every NetworkManager action.
  - `deploy/hub.service` sets a user and one capability, and nothing else: no `NoNewPrivileges`, `ProtectSystem`, `PrivateTmp`, `LimitCORE=0` or `UMask`.
  - `StateDirectory=hub` creates `/var/lib/hub` as 0755, and SQLite creates `hub.db` and its log as 0644 (I saw those modes). The key slots are then readable by any local account, though DEPLOYMENT.md says "readable only by the hub's account".
  - Nothing disables swap or core dumps, so the data key and decrypted records can reach the disk.

  Fix: run the version call as `hub`; replace the writable folder with a root helper; list the polkit actions needed; harden the unit and add `StateDirectoryMode=0700`, `UMask=0077`, `MemorySwapMax=0`, `LimitCORE=0`; have `image/check.sh` assert all of it.

**R-27. The Windows installer runs and trusts files a less privileged party controls.** *Reported; untested on Windows.* The elevated installer executes `hub.exe` from the data folder, which the service account can modify (`windows/installer.nsi:238` against `:140`, `:185`, `:209`, `:236`). The move from a per-user install brings `upgrade.json` along, whose paths the service then uses to overwrite its own binary. A moved certificate authority is added to the machine's trusted roots without checking its constraints (`:225`). The data key handover at upgrade writes a DPAPI-sealed file that is later only unlinked (`internal/upgrade/handover.go:30-57`), which probably defeats "a stolen hub reveals nothing without a staff password" on a PC without disk encryption. Fix: never execute the installed binary elevated; copy an allow-list of files on a move; check the authority's constraints; hand the key over in memory.

**R-31. Field reliability of the image.** *Reported; needs hardware.*
  - Unattended upgrades probably do not cover the kernel and firmware from Raspberry Pi's archive, and the automatic reboot at 03:00 is in the image's time zone, so mid-morning in Laos, with the hub coming back locked.
  - `hub-diag` rewrites a file on the FAT boot partition every 30 minutes, and `hub.service` requires that partition's mount. A power cut during a write can leave a Pi that waits in emergency mode with no console.
  - The udev rule for the stick matches on `ID_BUS=usb`, which can match the system disk on a Pi booted from USB, and passes a vfat option to exFAT.
  - The timer NO-RTC.md recommends for a Pi without a clock is not in the image.
  - Load-test mode ships enabled in the facility image.

#### The Facility Hub app

**R-30. Fleet-wide actions on partial or stale information.** *Reported.*
  - Group Resend and Discard act on every hub in the country, while the counts shown are for the manager's own org units (`apps/facility-hub/src/pages/RefusalsPage.tsx`, `src/hubs/refusals.ts:167-187`).
  - "Next stage" and "Stop" write a cached rollout choice without re-reading it (`src/pages/VersionPage.tsx:135`, `:146-152`), so one manager can undo another's Stop. Editing the pilot list drops pilots the user cannot see.
  - Changing the hub managers group re-shares three entries only (`src/pages/ApprovalsPage.tsx:160-166`). The old group keeps access to move keys and settings, and new managers cannot read existing move keys.
  - After the session expires, requests follow central's redirect to the login page and the app shows "No hubs" (`src/api/useApi.ts:22-46`). On 2.42 a request with `X-Requested-With: XMLHttpRequest` gets a 401 instead.
  - DataStore values are rendered without validation, so one malformed entry crashes a page for everyone who sees that hub.
  - Announcing a move, advancing a rollout and saving retention act on one click.

#### Testing and process

**R-32. The tests that carry the main claims do not run automatically.** *Confirmed for the workflows, the skip counts, the shared code and the drifted SQL; the rest reported.*
  - `test.yml`, `windows.yml` and `macos.yml` start by hand only. The only automatic run is on a release tag, without the race detector.
  - The read and write oracles, the access matrix and 135 of 144 adversarial tests need a real DHIS2 and run on one developer's machine. Their results are dated prose in the documents.
  - The fake central uses the hub's own `tracker.Flatten` and `tracker.AssignUIDs` (`internal/fakecentral/fakecentral.go:1061-1062`), so a bug there is mirrored. It ignores unknown parameters where DHIS2 answers 400, and validates imports far less.
  - Nothing in the default run covers 2.43.
  - The guard `TestQueryPlansUseIndexes` explains copies of the SQL, and one copy has drifted (`internal/tracker/plans_test.go:56` against `internal/store/objects.go:394`).
  - There are no fuzz tests, though the hub has several hand-written parsers of hostile input (fields, filters, mDNS packets).

### 6.3 Low

| # | Finding | Where | Status |
|---|---|---|---|
| R-35 | `sameOrigin` accepts `http://<host>` as the origin of the HTTPS site. A spoofed plain-HTTP page could pass in browsers that compare sites without the scheme | `internal/ui/ui.go:194` | Confirmed in code |
| R-36 | `/hub/login` has no origin check, and `/hub/logout` acts on a GET | `internal/ui/ui.go:200-238` | Confirmed in code |
| R-37 | `stick.Finish` deletes the live store first and removes its marker last. A power cut in between makes every later start delete the store again | `internal/stick/mirror.go:268-294` | Confirmed in code |
| R-38 | Set-up completion is not serialised; a double-submitted form runs it twice | `internal/ui/setup.go:102-107` | Reported |
| R-39 | Replaced and pruned key slots stay in the key file and its log until overwritten | `internal/store/users.go:119-121` | Reported |
| R-40 | A username reused on central inherits the previous user's cache, visibility and queued saves | `internal/store/users.go:115-132` | Reported |
| R-41 | The authority's name constraints include the machine's bare host name. A PC named `CLINIC` or `STORE` yields an authority allowed to sign for a top-level domain | `internal/hubtls/hubtls.go:207-212` | Reported |
| R-42 | A missing `device.key` on a TPM hub is silently replaced, and the hub then restarts for ever | `internal/devkey/devkey.go:75-92` | Reported |
| R-43 | Argon2 parameters are not stored with the slot, so they cannot be raised later without locking every user out | `internal/hubcrypto/hubcrypto.go:22-26` | Reported |
| R-44 | Transport errors carry the full URL, with the query string, into the log, the outbox's last error and the heartbeat. Aggregate values are sent in the query string | `internal/central/central.go:179-181`, `internal/aggregate/edit.go:146-172` | Query-string values confirmed in code; the rest reported |
| R-45 | `Store.Meta` returns an empty string on an error. `EnsureIdentity` would then give the hub a new ID and put it in setup mode | `internal/store/store.go:518-522`, `internal/app/app.go:50-57` | Reported |
| R-46 | `AddIssue` and the reserved-value writes bypass the store's write lock | `internal/store/misc.go:174`, `:353`, `:374` | Reported |
| R-47 | Discarding the refusals of a user who gets 404 from central turns existing records into tombstones | `internal/sync/discard.go:100-104` | Reported |
| R-48 | `go.mod` says `go 1.26.0` and every workflow builds with that file, so releases use 1.26.0 without later security fixes | `go.mod:3` | Confirmed in code |
| R-49 | Authenticode signatures have no timestamp. `macos/install.sh` checks the download against `release.json` but not the manifest's signature | `windows/sign.sh:21-22`, `macos/install.sh:76-80` | Reported |
| R-50 | The stick's key file is rewritten every minute while any device uses the hub, though RECOVERY.md says nothing is written when nobody saves | `internal/core/reach.go:56-59` | Reported |
| R-51 | An invalid `order` is silently ignored offline, where central answers 400 | Observed | Reproduced |
| R-52 | The release source `dhis2-utils/poc-data-proxy` is built into the binary, while the module is `github.com/dhis2/facility-hub` | `internal/upgrade/upgrade.go:53` | Confirmed in code |
| R-53 | The app: 8 strings containing ":" are missing from `en.pot`; `pnpm lint` fails; `strict` is off; a move code is printed from an empty key | `apps/facility-hub` | Reported |

## 7. Security, area by area

**Encryption at rest: good.** AES-256-XTS per page with a key derived by HKDF, the write-ahead log and temporary files included; AES-GCM per row with associated data that binds each ciphertext to its row; random nonces from `crypto/rand`. My grep of a live data folder found no plaintext. Weak points: R-24 (key in an error message), R-26 (file modes, swap), R-39.

**Authentication: good design, two serious flaws.** One password verifier (the key slot), a dummy derivation for unknown users, Argon2id with 64 MB, uniform answers. The flaws are R-04 and R-05. Two-factor authentication is not enforced offline, which FINDINGS item 13 states.

**Authorisation offline: good, with one hole.** Visibility needs both the user's bit and the current access list, single reads do not reveal existence, totals count only what the caller may see, and the oracle found no unexplained leak on 2.42 or 2.43. The hole is R-03.

**Input handling: the weakest area.** R-01, R-02, R-10, R-11, R-16, R-20. SQL is safe: every request value is bound, and the one place that inlines values escapes them correctly.

**Web pages: good.** `html/template` throughout with no unsafe casts, a strict content security policy, origin checks on state-changing posts, a Host allow-list. Minor: R-35, R-36.

**Trust in central: strict on transport, weak on content.** TLS is verified, redirects are never followed, credentials go only to the configured base. The control channel is the problem (R-07, R-23).

**Upgrade: good.** The signature is checked on the raw bytes before parsing, the binary's hash is bound by it, there is no downgrade, and a build without a key never upgrades. Problems are in recovery (R-12, R-13) and in how the key is kept (R-08).

**Device and image: mixed.** No SSH, no login users, a default-drop firewall and an unprivileged service are right. R-26 and R-31 are the gaps.

## 8. Functionality against the specification

| # | Scenario | Result in this review |
|---|---|---|
| 1 | Online pass-through, attributed to its author | Pass (project's e2e and live tests) |
| 2 | Offline sharing between two users | Pass (by hand, and the project's tests) |
| 2a | Mixed clients, generated value from the pool | Pass in the project's tests with a simulated Android client; no real device here |
| 3 | Stage access | Pass, on every read path I tried |
| 4 | Program read-only user cannot write | Pass (409, E1095) |
| 5 | A user from another facility | Pass (403) |
| 6 | Reconnect, in order, by author | Pass (by hand: `createdBy` on central) |
| 7 | Rejection, dependents blocked, issue reported | Pass (project's e2e test) |
| 8 | Reboot offline: locked until a known user logs in | Pass (by hand) |
| 9 | No plaintext on disk | Pass (by hand, with the log files present) |
| 10 | Set-up by a staff member with only a login | Pass. Note R-09 |
| 11 | Upgrade, rollback, unsigned release | The mechanism passes; the script fails as shipped (R-14) |

Functional observations beyond the findings:

- Any DHIS2 user can set up a fresh hub for an org unit they are assigned to. This is the documented design; with `central_url` pinned it costs nothing but a reset.
- Offline, the hub accepts an `ACCESSIBLE` search with fewer attributes than the program requires. The oracle lists this as a known gap; it removes a guard some programs rely on to prevent browsing.
- 61 % of the oracle's 4,884 answers were refusals. For the matrix users the hub often says "needs connection" where central answers. That is safe, and it also means offline coverage for users with unusual access is thinner than the headline suggests.

## 9. Code quality

**Strengths**

- 1,523 functions outside tests with a median length of 11 lines; 29 are longer than 100 lines and 3 longer than 200 (all three in test tooling).
- One `TODO` marker in the whole tree and seven `panic` calls outside tests.
- Comments are consistently about why: the measured reason, the DHIS2 behaviour, and the test that guards a rule. For its size the code is unusually well documented.
- Version differences, SQL and cryptography each live in one place.

**Weaknesses**

- **Errors from state writes and reads are widely dropped.** `SetMeta`, `SetNeedsLogin`, `NoteAttempt`, `setState`, `Store.Meta`, `HasCache`, `CountOutbox` and others return nothing or are ignored. On a full disk or a damaged store the hub continues on wrong assumptions (R-45).
- **A few long functions hold several policies each:** `upsertCentral` (194 lines), `FetchCatalog` (176), `pullAggGroup` (166), `pushEntry` (146), `Upgrader.check` (140), `handleSetup` (138). Most of the logic findings in sync and set-up sit inside these.
- **Paths are classified in four places, four ways** (`shared`, `isStatic`, `replayable`, the router). R-16 follows from that.
- **Comments that describe intent the code does not implement.** Examples: the throttle ("50, then one per 15 minutes"), `CountStart` ("before anything that may fail"), `GetCacheAnyUser` ("only used for static app files, which hold no user data"), the Basic-auth refusal ("disabled account or changed password"). Because the comments are otherwise so reliable, these mislead.
- **Stale text:** `internal/ui/templates/login.html` still mentions the hub setup user group; README.md says the health check includes central; DEPLOYMENT.md mentions `hub release -central-url`, which does not exist; `deploy/hub.example.yaml` says it is baked into the image with `central_url`.
- **Global mutable state** for test hooks: `upgrade.HandoverDir`, `ReleaseBase`, `ReleaseKey`, `stick.Removable`, `core.BasicRecheck`, `devkey.OpenDevice`. It also prevents parallel tests.

## 10. Testing

The test design is the project's strongest asset: roles instead of UIDs, a fake and a real central behind the same tests, central as the oracle, and ledger invariants checked per confirmed save. The gaps are in what runs when, and in what the oracles can miss.

- **Gating** (R-32). Put `go vet` and `go test` on every push, add `-race` to the release job, and add a nightly job that starts DHIS2 in Docker, provisions the synthetic profile, and runs the oracles, filter parity and the adversarial package on 2.42 and 2.43. Store the verdict counts with the commit.
- **The read oracle can pass while the hub refuses everything.** It fails on leaks, missing, differing and lenient answers, but has no minimum of matches. Add a per-role floor and a checked-in baseline for refusals.
- **The write oracle checks existence only,** and a save that was accepted offline, is absent on central and has no issue recorded is logged as "late", not failed (`internal/live/writes_test.go:155`, `:178`). Compare author and values, and fail on silent loss.
- **Negative access tests cover few paths.** The single event read, enrollment endpoints and relationships as a restricted user are not asserted by any default test. I checked them by hand and they hold; a table-driven test over every local read path and role would keep it so.
- **Scenario 9's test greps top-level files only and skips unreadable ones** (`internal/e2e/e2e_test.go:656-669`). Walk the tree and fail on read errors.
- **Missing tests that the findings point to:** hostile tracker payloads (R-01, R-02, R-10); concurrent logins (R-04); a wrong Basic password for a known user (R-05); a restart between a failed and a retried set-up (R-09); a failed signature fetch (R-12); cache classification with hostile paths (R-16); forged dataStore entries with owner access (R-07).
- **The Python client drivers** have no pinned Playwright version, 43 fixed waits and selectors on English text.

## 11. Documentation

The documents are unusually candid about what was measured, what was decided and what remains open, and they are the reason this review could go deep quickly.

- `docs/reviews/` and `docs/superpowers/` are cited by README.md, by `apps/facility-hub/README.md` and by about twenty code comments, and are not in this tree. If they were left out on purpose for this review, ignore this; otherwise the links are broken.
- README.md says `go mod verify` settles doubts about `go.sum`. It only compares the module cache with `go.sum`. The instruction should be to regenerate `go.sum` with the checksum database on and compare.
- Statements that the code does not bear out: "the dry run signs with a throwaway key" (R-08); "readable only by the hub's account" (R-26); the logs hold "no values" (they hold usernames, SSIDs and error URLs); the refused values "remain in the issue on central" (R-22); "guessing takes years" (R-04).
- RECOVERY.md says the stick is ext4; the image mounts only FAT and exFAT.

## 12. Recommendations, in order

**Before any pilot**

1. Fix the eight high findings. R-01, R-02, R-05 and R-06 are small changes. R-04 needs a restructured throttle. R-03 is two access checks and an oracle extension.
2. Validate tracker input at one boundary: strip hub-owned fields, type-check references, refuse changes to identifying fields, honour `importMode` (R-02, R-01, R-10, R-11).
3. Normalise request paths in the root handler and make the shared-cache rule a positive list (R-16).
4. Fix the set-up brick (R-09) and the two upgrade dead ends (R-12, R-13), and repair `scripts/test-upgrade.sh` (R-14).
5. Bound memory: page size, `fields`, answers from central, the status endpoint (R-19, R-20).
6. Protect the release key (R-08) and harden the service unit and file modes (R-26).
7. Turn on CI for every push, and add tests for each fix.

**Before a rollout beyond a pilot**

8. Authenticate dataStore instructions (R-07, R-23).
9. Make the push robust to large entries and slow links (R-21), and make state changes atomic (R-33).
10. Revisit what one password and the stick give (R-25), and what a reset leaves behind.
11. Review the Windows installer's privilege boundaries on a real PC (R-27) and the image's reliability items on real hardware (R-31).
12. Run the full live and adversarial suites nightly on 2.42 and 2.43, with fuzzing for the parsers. The adversarial suite was not run in this review.
13. Commission a focused review of the `vfs/xts` layer and the SQLite translation the storage depends on.

**Scope**

14. Decide which of the parts outside the core (Windows, TPM, USB restore, network management, central moves) a first pilot needs, and leave the rest out until each has had this level of review.

## 13. Left open after the fixes

Added 12 November. The fixes are on the branch `review-fable`. The findings below are not fixed, or fixed only in part:

- **R-20, the data-value filter.** Paging and the uncapped reads are bounded. The filter on data values reads every event of the program. Measure it at 150 000 events (`internal/perf`) before adding anything.
- **R-21.1, batching.** Pushes still go one object per request. Not measured as a problem yet.
- **R-26 and R-31, the Pi image.** Need a Pi:
  - the dnsmasq path and the polkit actions;
  - `MemorySwapMax` and `ProtectSystem` in `deploy/hub.service`;
  - the field-reliability items in R-31.
- **R-27, the Windows installer.** Not run on a real PC. The check of the self-signed installer is in TESTING.md, "The self-signed installer".
- **R-53c, strict TypeScript.** `strict` is still off in the app.
- **R-03 on the other profiles.** The oracle queries and baselines for R-03 exist for the synthetic profile only (`internal/live/testdata/oracle/synthetic-4{2,3}.json`). Run and record Sierra Leone and Laos.
- **Two DHIS2 bugs, to be reported:**
  - On 2.43 an event read by its ID answers 200 without program sharing; 2.42 answers 404.
  - Central filters and sorts on attributes of programs the user cannot read (see the note under R-03).

## Appendix: environment and traces

- **Toolchain:** Go 1.26.8 `linux/arm64`; staticcheck built from its `master`; four CPU cores.
- **DHIS2:** two clean instances, 2.42.7.1 (`agent-hubreview-42`) and 2.43.2.1 (`agent-hubreview-43`), created for this review through the instance broker, provisioned by `cmd/hubtest provision -profile synthetic`, and deleted afterwards.
- **The tree was not modified** apart from this file. Builds, test runs and the hubs used for probing ran on a copy outside the project folder.
- **Reproductions** used the project's own `cmd/fakecentral` and a hub built from this tree with `tls: true`, `tpm: false`. For R-09, a small proxy in front of the fake returned 500 for `/api/programs` during set-up.
- **Other instances:** the four stopped `agent-*` instances from earlier sessions of this project were not touched.
