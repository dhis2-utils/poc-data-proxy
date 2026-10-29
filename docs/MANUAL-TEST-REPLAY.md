# Manual test: screens offline for staff who never opened them online

What this checks: since branch `followup`, a staff member's online login makes the hub load, in the background and with that person's own token, the metadata and settings requests the facility's apps were seen making (proxy/replay.go; FINDINGS.md, "Shared metadata cache"). Staff then open Capture's and the Data Entry app's screens offline without having opened each of them online first. Run it on the Pi 5 with the real apps in a browser against the demo central, before `followup` is merged.

## What you need

- **A hub built from branch `followup` (wave 3 with replay).** Either a Pi image (`sudo image/build.sh hub-linux-arm64 <version> out/`, image/README.md) or the binary copied over the one on the Pi 5:
  ```sh
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o hub-linux-arm64 ./cmd/hub
  scp hub-linux-arm64 pi@<pi>:/tmp/hub && ssh pi@<pi> 'sudo systemctl stop hub && sudo install -m 0755 /tmp/hub /usr/local/lib/hub/hub && sudo systemctl start hub'
  ```
  The branch adds a store migration (5) (a column on the HTTP cache). After it, a hub binary from `main` refuses the store ("store schema 5 is newer than this binary"): to go back, reset the hub or restore a copy of `/var/lib/hub` taken before.
- **The hub set up for Ngelehun CHC** on the demo central (https://implement.im.dhis2.org/facility-hub), online. `ngelehun1`, `ngelehun2` and `ngelehun3` (password `HubDemo-2026!`) have each logged in through it at least once before, or not at all: either works.
- **`uplink_switch: true`** in `/etc/hub/hub.yaml` (then `sudo systemctl restart hub`), for "Simulate uplink cut" on the hub's page; or be ready to unplug the uplink cable.
- **A browser where each user starts clean**: one Chrome profile per user, or a new incognito window for each step below (close all incognito windows between steps). The browser's own caches, Capture's IndexedDB and the Data Entry app's service worker otherwise answer screens the hub never sees, and the test would pass for the wrong reason.
- A terminal on the Pi: `journalctl -u hub -f | grep -E "replay after login|online login|offline login"`.

## 1. Online: ngelehun1 opens every screen

In a fresh window at `https://dhis2.local` (or the hub's address), log in as `ngelehun1` and open:

1. Capture: the front page with Ngelehun CHC selected.
2. Each programme's working list (pick each programme in the top bar).
3. One patient's dashboard (any row in a tracker programme's list).
4. Each tracker programme's registration form ("Create new" / "New person" with the programme selected); do not save.
5. One event programme's new event form; do not save.
6. The Data Entry app (`/apps/aggregate-data-entry`): each data set ngelehun1 has at Ngelehun CHC, last month's period. Do not enter values.

Log out. Write down the programmes and data sets you opened: steps 4 and 5 repeat them.

**Look for:** the journal shows `replay after login user=ngelehun1 loaded=…`: usually `loaded=0` on a hub where nobody has opened anything yet. A hub's earlier use counts: it loads what anyone opened through it in the last 14 days.

## 2. Online: ngelehun2 only logs in

In a new fresh window, log in as `ngelehun2` and open nothing; close the window after the apps menu appears.

**Look for:** within a few minutes, `replay after login user=ngelehun2 loaded=<n> kB=<size> s=<seconds> err=<nil>`. Write down n, kB and s. On the 2.43 synthetic test it was 58 requests, 52 kB and 15 s after the nurse had opened 16 screens; the Sierra Leone demo has more programmes and metadata, so expect more (at most 300). `err` must be `<nil>`; `loaded=0` means the hub found nothing to load (check that step 1 went through the hub's address, not central's). Wait for this line before step 4.

Log in as `ngelehun2` again in another window: no second `replay after login` line for ngelehun2 today (once a day per user).

## 3. Online: ngelehun3 opens Capture's front page once

In a new fresh window, log in as `ngelehun3`, open Capture's front page, close the window. Wait for `replay after login user=ngelehun3 …`.

## 4. Before going offline: the misses so far

Log in to the hub's page (`/hub/login`, any staff member) and open `/hub/ui/endpoints`. Note the "not available offline" list and its total (it counts since the hub started; it may already hold some).

## 5. Offline: each user opens every screen

Cut the uplink ("Simulate uplink cut" on the hub's page, or unplug it). Wait until the hub's page says offline.

For `ngelehun1`, then `ngelehun2`, then `ngelehun3`, each in a new fresh window: log in (offline login), then open the screens of step 1, items 1 to 6, the same programmes, patient and data sets. For each screen note:

- whether it shows what it showed online for ngelehun1 (rows, a dashboard with stages, a registration form with fields, an event form, a data entry form with cells);
- any error or "needs connection" Capture or the Data Entry app shows;
- in the browser's developer tools (Network, filter `status-code:503`), any 503 from the hub. The hub's answer names the path: `needs connection: /api/… was not cached while online`.

Expected: ngelehun2 and ngelehun3 open every screen ngelehun1 opens, with no 503 except those ngelehun1 also gets. The patient lists may differ by user only where their access differs.

Things that should still say "needs connection" offline, for all three: running validation in the Data Entry app ("Run validation"), and anything that was never opened by anyone online.

## 6. The hub's count of misses

Back on `/hub/ui/endpoints` (the hub is still offline; the page works): the "not available offline" total should have grown only by what ngelehun1 also met. If it grew by more, the new endpoints are what the replay missed: write them down with their counts.

Reconnect the uplink. In the Facility Hub app on central, the hub's heartbeat carries the same top 10 (`offlineMisses`) after the next sync.

## 7. Language stays per user (optional, 5 minutes)

Before step 2, on central as `robot`: set `ngelehun2`'s interface language to French (Users app, or `ngelehun2` in their own profile settings). In step 5, ngelehun2's Capture shows programme names, labels and the app's own texts in French where the demo database has translations, as central answers ngelehun2 online, while ngelehun1's and ngelehun3's stay in English. Set the language back afterwards.

## What to send back

The journal lines from steps 1 to 3, the misses before and after step 5 (step 4 and 6), and for each user the screens that did not open offline, with a screenshot and the 503 paths.
