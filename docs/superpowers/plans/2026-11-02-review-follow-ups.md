# Review follow-ups: what to fix, decided 2 November 2026

The review of this branch (`open-findings-plan`) found one bug and a few gaps beyond the fixed findings. Olav decided each on 2 November: the pool watermark is fixed by making the arithmetic exact (no minimum of 5 is needed, and an explicit 0 turns an attribute's offline pool off); the probe cadence is B1 (ping_interval while offline, four times that while online). This plan lists the work that follows, so any agent can pick it up.

## How to work

- One commit per item (C1–C7 below, in that order; C6 only if C5's oracle run says so). The fix, its test, and the FINDINGS.md or DEPLOYMENT.md wording land together.
- Before each commit, the checks in AGENTS.md ("Before committing"): `go vet ./...`, `go test ./...`, `go test -race -timeout 40m` on `internal/e2e`, `internal/live`, `internal/tracker`, `internal/store`. Where central decides the behaviour (C5, C6), also `go run ./cmd/hubtest run -profile <synthetic|sierra-leone|laos> -version 42` (docs/TESTING.md).
- The Facility Hub app commits also run tsc, eslint and jest (`apps/facility-hub`).
- No item touches the store's schema: no migration, so no `HUB_PERF_MIGRATION` timing is due.
- Commit messages state the numbers they claim, as the D6 and T2/T4 ones do.

## C1 — the pool watermark is exact for every target; 0 turns an attribute's pool off

The watermark comparison divided first (`have >= target/poolLowWatermark`), so a target below 4 never refilled, even empty: the app allows any size from 1, and the setting JSON can be written on central by hand. An explicit 0 will mean "no offline pool for this attribute" — the setting is `{"default": N, "attributes": {"<attrId>": N}}`, and absent must stay distinct from 0.

- `internal/core/reserved.go`, `PoolTarget`: a presence check (`if n, ok := r.Attributes[attr]; ok { return n }`), so an explicit 0 or less is returned instead of falling through. `Default` keeps its `> 0` check: with `omitempty` an explicit 0 and absent are indistinguishable, and the app's default field never writes one.
- `internal/sync/sync.go`, `RefillPool`: `have*poolLowWatermark >= target` skips, replacing the division. Every target behaves: 1–4 refill at empty, 5 at 1 or less, a quarter of any larger one. A disabled attribute (target 0) needs no case of its own: `have*4 >= 0` is always true. Extend the `poolLowWatermark` comment with why the multiply form (the division once truncated 1–3 to "never").
- `internal/trackerapi/trackerapi.go`, `ServeGenerated`: after `generatedReadable` and before `BeginLocalWrite`, a target of 0 or less answers needs-connection ("generated values for attribute … are not reserved for offline use"), so leftovers of a pool disabled later are not handed out either (before the branch, `max(1, min(n, 100, 0))` drained them one per request). Online stays pass-through, as today. The `target/2` cap per request stays: a deliberate floor of 1, not the watermark.
- App (`apps/facility-hub/src/hubs/settings.ts`): `limits.pool` stays `{min: 1, max: 100000}` for the default field; attribute rows gain `limits.poolRow = {min: 0, max: 100000}`, used by `poolValue`'s `mapOf`, and `poolForm`'s `rowsOf` keeps `n >= 0` so a stored 0 round-trips as `'0'`. The help text says 0 turns an attribute's offline pool off.
- Tests: `TestReservedPoolSetting` gains a target of 3 (refills after sync — red before, 3 never refilled; drains one per request offline, refills again at empty) and an explicit 0 (no refill while online; offline `generateAndReserve` answers 503 with leftovers in the pool — red before the `ServeGenerated` branch). `settings.test.ts`: 0 round-trips per attribute, -1 stays invalid, the default field still rejects 0.
- Docs: README's `reserved_pool_size` row, `deploy/hub.example.yaml`'s comment and FINDINGS.md item 33 record 0 = no offline pool and the watermark fix.

## C2 — FINDINGS.md item 29 says "release choice", not "release list"

The refusal memory is cleared only when the hashed choice (`version`, `pilotHubs`, `percent`) changes; the manifest is not part of it, so a republished or re-signed release for the same chosen version is never fetched again. Replace "fetches it no more until the release list (the country's choice) changes" with "fetches it no more until the country's release choice changes (its version, pilot hubs or percentage). The manifest is not part of the choice: a republished or re-signed release for the same chosen version is not fetched again (DEPLOYMENT.md, section 2, release key rotation)". Docs only; DEPLOYMENT.md already says it right.

## C3 — DEPLOYMENT.md records the squashed base schema

A new bullet in "Upgrades", after "Migrations on full hubs":

> **Stores from before the first release.** Until a hub is deployed, the store's base schema is edited in place instead of migrated; the 30 October tie-order change did this to the list indexes. A store created by an earlier build keeps the old indexes and is never rebuilt: it pays a sort per page and the plan tests' assumptions no longer hold. A test hub still holding such a store is reset, not upgraded; its data returns from central at the next pulls.

FINDINGS.md item 34 gains a parenthetical pointing there. Docs only.

## C4 — FINDINGS.md item 31 records I9c's cost

While the store is locked, a Basic request from a user the hub has no key slot for is no longer cached, so each costs one central login until the store unlocks — usually minutes; a known user's login unlocks the store and caches at once. One sentence, appended to item 31. Docs only.

## C5 — the oracle judges order: scheduledAt, attribute and createdAt orders, sequences compared

`Judge` compares membership only, and `pageBoundary` excuses a tie boundary: nothing in the oracle compares sequence, so the tie-order fix's parity with central is guarded only by the e2e test, which encodes the measured behaviour rather than checking live central. The commit message for 32a1290 claims central breaks ties by `created_at DESC` for any order column; the hub's Go path (`sortObjs`) only does that for `occurredAt`.

- `internal/testkit/oracle.go`, `Judge`: when the membership verdict is `Match` and the request's URL carries an `order` parameter, also compare the two item lists' UID sequences (the full list when unpaged, the page when paged). A mismatch is `Differs` with the first ~10 UIDs of each side in the detail. No `Query` change; the existing `occurredAt:desc` paged query becomes sequence-checked too, which would have caught the original finding.
- `internal/testkit/oracle.go`, the query builders: `EventQueries` gains `order=scheduledAt:asc` and `:desc`, and `order=createdAt:asc`, in the existing paged shape; `Queries` gains `order=<SearchAttr>:asc` and `:desc` (persons sharing a first name tie on it) and `order=createdAt:asc`/`:desc` for tracked entities.
- `internal/live/oracle_test.go`, gated by `e.Live()`: seed the ties before `e.Sync()` — five events of the event program sharing a `scheduledAt`, created a second apart, and four persons sharing the search attribute's value, written through one client.
- If a divergence is measured and accepted: a case in `knownGap` naming the order field, so it is reported but does not fail the run.
- Gates: `hubtest` on synthetic (and sierra-leone, laos if available), the live oracle against 2.42.7.1, and the new check count in the commit message.

## C6 — sortObjs follows central on the remaining orders (only if C5 says so)

If C5 finds central ties `scheduledAt` and attribute orders by `created_at DESC` then `uid DESC`, as the general claim says: in `internal/tracker/query.go`, `sortObjs` appends `createdAt desc` whenever `keys[0].field != "createdAt"` (replacing the `occurredAt`-only condition), which is exactly the rule the SQL path (`simplePlan`) already applies. `TestEventListTieOrderMatchesCentral` gains a `scheduledAt` tie case; FINDINGS.md item 34 notes the widened coverage. If central orders differently, C5's `knownGap` records the measured behaviour as accepted and no code changes.

## C7 — the probe cadence is B1: ping_interval while offline, four times that while online

Decided 2 November (B1 of the alternatives reviewed). The load on central comes from successful probes only — a failing probe never completes a TLS handshake — so the offline cadence stays `ping_interval` (recovery stays fast and free) and only the online cadence backs off: one TLS handshake a minute at the default 15 s instead of four. Idle online→offline notice grows to two failures at 60 s; that is covered by the per-request guard (finding 22) at the first client request, and a failing cycle reveals it otherwise.

- `internal/central/central.go`: `const probeOnlineEvery = 4`; `Monitor.Run` moves from `Ticker` to `Timer`, resetting with `nextDelay()` after each probe; `nextDelay()` returns `Interval * probeOnlineEvery` while `m.online` (under `mu`), else `Interval`. A forced-offline hub (the admin switch) still probes at `ping_interval`: cheap, and recovery detection stays quick — say so in the comment. Update the `probeClient` comment's cost sentence to the new cadence.
- Tests (`internal/central/central_test.go`): `TestProbeCadenceFollowsTheState` drives `Probe` against a server that fails on demand and asserts `nextDelay()`: `Interval` after the two failures that flip offline, `Interval * 4` after a success. No sleeps. `TestProbeOpensANewConnectionEachTime` stays as is (the D6 fix must not regress).
- Before committing: testkit runs hubs at `ping_interval: 2s`, so an online hub probes every 8 s and going offline takes ~16 s instead of ~4 s. Sweep the adversarial and live tests that wait on connectivity flips and check their timeouts; if any are tight, expose the multiplier as a `Monitor` field (as `Client` and `forced` already are) and let testkit set it to 1.
- Docs: README's probe row ("15s while offline, 60s while online"), `deploy/hub.example.yaml`'s `ping_interval` comment, RECOVERY.md's "probes central every 15 seconds" sentence, FINDINGS.md item 30. The commit message states the rate and the detection latencies, and a sandbox or Pi measurement if one is reachable when this lands.
