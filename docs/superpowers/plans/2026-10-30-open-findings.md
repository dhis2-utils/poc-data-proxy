# Open findings: what to fix, decided 30 October 2026

Olav decided each open finding in FINDINGS.md on 30 October 2026 ("Open" sections, and "A week offline at HC IV size" in PERFORMANCE.md). This plan lists the work that follows, so any agent can pick it up. W1, the Android incremental sync, is already fixed (e3e956e; FINDINGS.md, "Found by adversarial testing" 21).

## How to work

- One commit per finding: the fix, its test, and the finding moved from "Open" to the numbered list in FINDINGS.md ("Found by adversarial testing"), as for items 1–21.
- Most findings have a test gated by `finding(t, …)` that runs only with `HUB_FINDINGS=1` (`internal/adversarial`). The fix removes the gate and makes the test assert the fixed behaviour.
- Before each commit, run the checks in AGENTS.md ("Before committing"). Where central decides the behaviour, also run `go run ./cmd/hubtest run -profile synthetic -version 42`.
- AGENTS.md applies: no new mechanism without a measured problem. Each fix below is the small change named, nothing more.
- From a fresh clone in the sandbox, proxy.golang.org is unreachable. Copy `~/work/hub/go.work` (it replaces x/crypto and x/sys with `~/gomods`) and never commit it.

## Fix now

| Finding | The fix | Test |
|---|---|---|
| S1, concurrent logins leave tokens on central (soak) | A lock per user around `core.EnsurePAT`, called from `auth.registerOnline`, so logins of one user within the same second make one token. | `TestM1ConcurrentLoginsMakeOneToken` |
| S2, a person saved during a full pull is hidden from its author offline (soak) | `RemoveVisibilityExcept` (`sync.pullGroup`) keeps visible what the hub wrote or passed on to central after the listing started. Compare with the listing's start time, or the outbox sequence at its start. | `TestM1SaveDuringFullPullStaysVisible` |
| A10, program expiry not read | Program expiry fields (`expiryDays`, `expiryPeriodType`, `completeEventsExpiryDays`) in the catalog. An offline E1047 check that lets users with `F_EDIT_EXPIRED` through. | `TestA10HubLockFollowsProgramExpiry` |
| B5, a lost authority not applied offline | Refuse reopening a completed event offline (E1083) when the user's stored authorities lack `F_UNCOMPLETE_EVENT`. Do it in the same place as A10's check. | `TestB5HubRefusesAfterSync` |
| B3, Android's old password works online for 5 minutes | When central refuses a user's web login, drop that user's cached credentials. | `TestB3OldPasswordOnlineThroughCache` |
| G6 / N23, a reset rewrites the registry entry | The reset edits the entry (`EditRegistry`) instead of replacing it, so `tokens` and the other fields the Facility Hub app shows stay. | `TestG6ResetWithRefusedChanges` (N23 is checked in it) |
| G3, a refused release recorded every cycle | Remember a mislabelled or refused manifest once, as for unsigned ones, and fetch it no more until the release list changes. | `TestG3G4ReleasesRefused` |
| D6, the hub flaps while name lookup fails | The connectivity probe opens a new connection each time instead of reusing a kept-alive one. Olav asked that this not add load on central: one TLS handshake per probe is the cost. Measure the probe's rate, and if it is more than about one a minute while online, say so in the commit. | `TestD6DNSFails` |
| I9c, a new device is not registered after a locked start | Don't cache a pass-through session made while the store is locked. Or, if simpler, register the device once the store unlocks. | `TestI9cNewDeviceWhileStoreLocked` |
| F5, forms open and close to the second on the hub, by the day on central | Compare data input periods at day level, as central does. | `TestF5InputPeriodPassesWhileOffline` |
| T2 / T4, reserved values run out offline (Android tests; see "To confirm with Olav") | Size the pool per generated attribute from the facility's own rate: about a week of registrations at the hub's observed rate, with `reserved_pool_size` (config, default 150) as the minimum. Hand a tablet up to Android's own 100 per request when the pool allows it. A busy HC IV registers about 560 people a day, so 150 does not last a day offline. | New: a pool filled from a measured rate; a tablet gets 100 |

## Documentation only

- **G11:** when all staff passwords change while the hub is locked, the old password still unlocks it offline. Add this to RECOVERY.md and to the hub page's help.
- **N8:** document release key rotation (DEPLOYMENT.md, "The hub image and releases").
- **T1:** generated values with `ORG_UNIT_CODE` are a limitation, by decision. The hub's pool is made with the facility's code. A hub that serves an org unit with children (set-up allows any level) would hand children the facility's code offline. Record it in FINDINGS.md, under the decided limitations next to H4, and in DEPLOYMENT.md where set-up lets the org unit be chosen at any level.

## Accepted as limitations (record in FINDINGS.md, no code)

- **I8c:** a million-digit number is confirmed offline, then refused at push and kept.
- **J4:** a queued completion skips validation, because central's own completion endpoint does not validate. Raise it with DHIS2, next to J1 and C2.

## Measure first

- **A15:** cached metadata replayed offline after central changed it. First find out whether Capture web and Android reload metadata offline at all, as a scenario in the sandbox with the real apps. Fix only if a client is shown a stale form.

## To confirm with Olav before starting

Answers given on 30 October; no decision recorded yet:

- **T2 / T4, pool size:** the proposal is above. Olav raised it ("an OPD number in a big facility that is offline for a week, far more than 150 might be needed"). Confirm per attribute and rate-based, or only a higher default.
- **T3, misleading message on a first login offline:** first logins offline are not supported, so this is only about the wording. The user sees "The URL provided is not a DHIS2 server." because the cached `/api/system/info` is per user. Check whether anything in it really differs per user. If not, serve the hub's copy to anyone, and Android then shows its usual login failure. Proposed: fix later.
- **The oracle's tie order** (events sharing a date): the hub breaks ties by UID, central by creation time. Changing the hub means the paged search in `internal/tracker/search.go`, guarded in AGENTS.md, and probably a new index migrated at HC IV size. Proposed: keep it accepted in the oracle unless staff would notice.
