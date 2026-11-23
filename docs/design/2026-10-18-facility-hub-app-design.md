# Facility Hub app, first version (design)

Status: agreed in conversation 2026-10-17/18, scope "A. Operations core". Changed 2026-10-19: hub managers, not only superusers, approve hubs and make move codes (see "Hub managers" below). The contract with the hubs is [docs/FACILITY-HUB-APP.md](../FACILITY-HUB-APP.md); the app's other duties, left for later, are in DEPLOYMENT.md section 6.

## Purpose and users

The national team and districts see the hubs from central, and do the two things hubs depend on: approving a hub's own account, and giving out move codes after an unplanned move. Success: a district person spots a silent or struggling hub without calling the facility; a superuser approves a hub, or makes move codes for every hub, in minutes.

- **Districts** see the hubs under their org units: the list, each hub's page, its refused changes.
- **Superusers** (the `ALL` authority) see all hubs, and also approvals, move codes and planned moves.
- Filtering by org unit is done in the app, not by sharing: the namespaces hold no patient data (refused values are masked), and per-district sharing would need hubs to maintain it. Decided 2026-10-18.

Left for later: editing country hub settings (retention, aggregate), choosing and staging hub versions, bulk retry instructions.

## Stack and place

`apps/facility-hub/`, a DHIS2 App Platform app (`d2.config.js`, TypeScript, React 18, `@dhis2/ui`, `@dhis2/app-runtime`, TanStack Query 4, React Router with hash routing, Vite), DHIS2 2.42 and 2.43 (`minDHIS2Version: '2.42'`). Cryptography with `@noble/curves`, `@noble/hashes` and `@noble/ciphers` (pure JavaScript, audited; the browser's own X25519 is too recent for government PCs). pnpm. The build is the zip installed in App Management.

## Data

- **Reads:** `facility-hub-registry` and `facility-hub-status` (namespace listings with `fields` naming the values used, so the list does not load each hub's 90-day history, about 14 KB per hub); `/api/me` (org units with paths, authorities); `/api/organisationUnits` for the hubs' facilities (name, path, and the path's names for the district). A hub's page reads its full heartbeat and its refused changes (`facility-hub-issues`, keys `<hub ID>-<n>`).
- **Visible hubs:** a hub whose facility path contains one of the user's org units; all for a superuser.
- **Writes (superusers):** an approval, exactly as FACILITY-HUB-APP.md (users, user group, `facility-hub-keys`, sharing, the registry entry's `account`); a planned move, `facility-hub-admin/central-move` = `{"url", "from"}` (set or delete). Move codes write nothing.
- **Empty or missing namespaces** (no hub yet) show an empty list. A failed action shows the step that failed.

## Flags

Fixed in the app, computed from the heartbeat and the time:

| Flag | When |
|---|---|
| Silent | last heartbeat (`time`) more than 2 days ago |
| Pending | `pendingOutbox` > 0 and the oldest waiting save (`oldestPending`) made more than 1 day ago, or `stuckOutbox` > 0 (saves waiting for their user to log in). Hubs before 0.1.6 send no `oldestPending`; for them the last push (`lastPush`) counts, which then moved whenever the outbox was empty |
| Refused | `issues` > 0 |
| Low disk | `diskFreeBytes` under 2 GB |
| USB stick | `usb` reported and not present, not writable, or with an error |
| Token | `hubTokenExpires` in the past |
| Upgrade failed | `lastUpgrade.result` = `failed` |
| Needs upgrade | `needsUpgrade` |

A hub with no heartbeat yet shows "Never reported". A retired hub (`registry.status` = `retired`) is shown greyed, without flags.

## Screens

Left menu; the last two only for superusers.

1. **Hubs:** a row per visible hub: facility and district, last heartbeat, pending, refused, free disk, USB, version, token owner, and the flags as tags. Flagged first; "flagged only" switch; search by facility name. Superusers see "N hubs waiting for approval".
2. **Hub:** the latest heartbeat, each flag explained in a sentence; the 90-day history as a table, newest day first (syncs, outages, minutes offline, longest outage, pending peak, lowest free disk); refused changes, read-only (when, kind, programme, error codes, the masked message); "Approve" for a superuser if the hub waits.
3. **Approvals:** waiting hubs (facility, hub ID, set up by, when). "Approve" opens a dialog: call the facility on a number you already have, enter the code from the hub's page; the steps run and show as they complete, or the step that failed.
4. **Central moves:** the current planned move, set or clear (address, optional "not before"); move codes for a new address: every hub with its facility and code, "no code: approve first" for hubs without a move key, printable and downloadable as CSV.

## The hub account module

`src/hubaccount/`, mirroring `internal/hubaccount` function by function: `accountCode`, `moveCode`, `checkAddress`, `normalizeAddress`, `normalizeCode`, `seal`, `newPassword`, `approve` (the seven steps, reporting each). `seal` gives the hub's format: the ephemeral X25519 public key (32 bytes), a 12-byte nonce, AES-256-GCM ciphertext and tag; key = HKDF-SHA256(shared secret, no salt, info `"facility-hub account box " + hubID`, 32 bytes); authenticated data `"hub-account:" + hubID`. Random values from `crypto.getRandomValues`. Move keys are read only to compute codes, never shown or kept.

## Testing

- **Unit (the app's runner):** the code and address values from FACILITY-HUB-APP.md; a sealed box for fixed inputs (ephemeral key, nonce) equal to the Go test's bytes; the flag rules; the org-unit filter.
- **Go:** a test pins the same sealed box and opens it with the hub's `Open`, so the app's sealing cannot drift from the hub's.
- **End to end, live** (`internal/live`, run by `hubtest` on all databases): set up a real hub; install the built app; `clients/facility_hub_app.py` (Playwright) logs in as superuser, finds the hub waiting, approves it with the hub's code, and reads the move code for a test address. The Go test checks the hub takes up its account, the code shown equals the hub's own `MoveCode`, and a district user sees the hub only if it is in their district.

## Docs

FACILITY-HUB-APP.md gains the screens; TESTING.md how to run the app's tests; DEPLOYMENT.md marks the app built, except settings, version staging and bulk retries.

## Hub managers (2026-10-19)

A hub manager might not be a superuser. Members of a hub managers group, named once by a superuser on the Approvals page, see Approvals and Central moves and do everything there except change the settings. Measured on 2.42: a user with `F_USER_ADD`, `F_USER_VIEW`, `F_USER_GROUPS_READ_ONLY_ADD_MEMBERS` and read access to the hub accounts group creates the hub's user in that group (named in the new user; the group's member endpoint wants write access), replaces a hub user's password and groups, and reads, replaces and deletes move keys shared with the group; DHIS2 confines them to their own org units. Move keys and the planned move are shared with the group. Accepted trade-off: every manager can make move codes for every hub. Details in docs/FACILITY-HUB-APP.md, "Hub managers".
