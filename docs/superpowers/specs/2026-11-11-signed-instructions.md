# Signed instructions from the Facility Hub app

Status: decided by Olav on 2026-11-11 (review R-07, R-23, R-30a); built on the same day.

## Why

Hubs obey entries in central's dataStore: `facility-hub-admin/*` (central-move, retries, release, accounts), `facility-hub-config/*` and the status in their own registry entry. They checked only an entry's public access. On 2.42 any user may create a missing entry, own it and share it read-only for everyone else, which passes that check. Sharing cannot tell the app's writes from anyone else's; a code made with a key only the hub and the hub managers hold can.

## The code

Each approved hub has a move key (32 bytes, `facility-hub-keys/<hub ID>`, shared with the hub managers group; the hub holds it sealed). The app adds, to each instruction it writes, one code per hub whose move key it can read:

- **Signed object:** a JSON object with `issued` (the time the app wrote it, ISO 8601 UTC) and `codes`: `{"<hub ID>": "<code>"}`.
- **Code:** base64 (standard, padded) of the first 16 bytes of HMAC-SHA256(move key, `"facility-hub instruction"` 0x00 hub ID 0x00 kind 0x00 canonical JSON of the object without `codes`).
- **Kind:** `<namespace>/<key>` of the entry: `facility-hub-admin/central-move`, `facility-hub-admin/retries`, `facility-hub-config/retention`, `facility-hub-registry/<hub ID>`, and so on. A code for one entry or one hub is worth nothing in another.
- **Canonical JSON:** object keys sorted, no spaces, strings escaped as `JSON.stringify` does, with U+2028 and U+2029 written ` ` and ` ` (as Go writes them), numbers as written.
- **Where:** the whole entry is the signed object for `facility-hub-admin/*` and `facility-hub-config/*`; the fields the hubs read are its top-level fields. In a registry entry, which the hub and others also write, the signed object is the field `signedStatus`: `{"status": "revoked" | "active", "by", "at", "issued", "codes"}`.
- **Test vector:** `TestInstructionCodeVector` (`internal/hubaccount/codes_test.go`) and `src/hubaccount/codes.test.ts` give the same code for the same input.

## What a hub accepts

- A valid code for itself, and `issued` no earlier than the last one it accepted for that kind (it keeps the last per kind). Equal is the same entry read again; earlier is a replay.
- Retries are signed as a whole entry; each instruction is applied once (the IDs applied only grow). A group Resend or Discard carries `orgUnits`, the org units of the manager who asked (the hubs their Refusals page counts): a hub applies it only if its facility is under one of them, and only to saves refused before the instruction's `at` (the age rule). An instruction for one refused change names its hub already. A group instruction without `orgUnits` is not applied.
- Anything else is ignored. The hub's admin page and its heartbeat (`unsigned`) name the entries it ignored; the app flags such hubs.

When an approved hub ignores an entry: no planned move; no retries applied; no upgrade from `release`; for `facility-hub-config/*` and `accounts`, the last value it accepted stays in force; the registry status does not change.

## Hubs without a move key

A hub not approved (running on a staff member's token) cannot check a code. It ignores `central-move`, `retries`, `metadataReload` and the `facility-hub-config` settings, for which it uses the built-in defaults, and reads `release` and `accounts` as before. A first pull before approval therefore uses the default retention (everything kept). `release` only chooses among releases signed by the supplier, which the hub verifies. `accounts` is needed at set-up, before any approval (the hub owners group for the registry entry's sharing; set-up is refused without one). A "revoked" in such a hub's registry entry is handled as before: taken only when central refuses the hub's token, undone when the entry no longer says it.

## Approvals and Revoke

- Approving a hub gives it a new move key. The app then adds the hub's code to each entry that already carries a valid code for another hub (content and `issued` unchanged, so other hubs see no change). An entry the approver may not write (`accounts`, superusers only) gets the new hub's code at the next save by a superuser; until then that hub reads `accounts` as an unapproved hub does.
- Revoke writes `signedStatus` revoked, deletes the hub's tokens, then disables the hub's own DHIS2 user (`POST /api/users/{id}/disabled`; a new random password if that is refused). Checked on 2.42.7.1 and 2.43.2.1 on 11 November 2026 with a hub manager's role (`F_USER_ADD`, `F_USER_VIEW`, `F_USER_GROUPS_READ_ONLY_ADD_MEMBERS`): both answer 204, the hub's user can no longer log in, and a user outside the manager's org units is refused (409). Making a revoked hub active again needs a newer signed `"active"`; the app does not offer it: reset the hub and approve it again.

## Remaining risks

- Every hub manager reads every move key (accepted 2026-10-19), so any manager can sign anything for any hub. The codes keep out other DHIS2 users, not managers.
- `accounts` and `release`, for unapproved hubs, and `accounts` for approved hubs that have not yet accepted a signed one, are trusted as before: on the entry's sharing, which a user who created a missing entry first can pass.
- `retired` in the registry is written by the replacing hub at its set-up, which has no key of the hub it replaces. It stays unsigned: a forged one stops a hub from serving offline until the entry says active again (the hub keeps its data and pushes its outbox).
- A revoked hub that was offline while it was revoked cannot read its registry entry afterwards (its tokens are deleted and its user disabled), so it does not drop what it holds by itself. Its data stays encrypted on the device and can no longer reach central.
- A code covers the entry's content, not who wrote it: a manager's honest mistake is signed like anything else.
