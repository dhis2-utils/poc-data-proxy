# Facility Hub app: what it must match

The Facility Hub app is a DHIS2 app on central, used by the national team and districts (first version built 2026-10-18 in [apps/facility-hub](../apps/facility-hub); its other duties, still to build, are in [DEPLOYMENT.md](DEPLOYMENT.md), section 6, "The Facility Hub app"). This note fixes the parts the hubs depend on exactly: approving a hub's own account, and move codes. `internal/hubaccount` is the reference implementation, used by the hub's tests; the app (`apps/facility-hub/src/hubaccount`) gives the same values (below).

## The app

- **Install:** the zip from `pnpm build` in App Management. Opening it needs the authority `M_facilityhub` (DHIS2 drops the dash of the app's key); give it to the national team's and districts' roles.
- **Hubs:** every hub under the user's org units (superusers: all; a user at the country's root sees all too), with its last heartbeat, pending and refused changes, free disk, USB stick, version and token owner, and flags: no heartbeat for 2 days; saves waiting more than a day, or for their user to log in again (after 7 days the hub refuses them, docs/RECOVERY.md); refused changes; under 2 GB free; USB stick missing or failing; housekeeping token expired; failed upgrade; needs an upgrade; dataStore entries it acts on that any user may write (`unprotected`). Filtering by org unit is done in the app, not by sharing: the namespaces hold no patient data. A hub's status is at most an hour old: hubs send their heartbeat hourly.
- **A hub:** its latest report with each flag explained, its addresses (for devices where `dhis2.local` does not work) and the fingerprint of the certificate devices install, its 90-day history, what it could not answer offline (the most frequent "needs connection" reads, by endpoint, since it started), its refused changes (masked), and "Approve" while it waits.
- **Approvals** (hub managers and superusers): the hubs waiting, approved with the code from the hub's page; and the settings, kept in `facility-hub-admin/accounts`, which only superusers change: the hub accounts user group and role, the hub managers group, and the hub owners group (`owners`), which each hub gives write access to its registry entry, with the hub accounts and hub managers groups (docs/DEPLOYMENT.md, "Protect the data store namespaces"). Naming the hub managers group shares the `facility-hub-admin` entries made before with it.
- **Central moves** (hub managers and superusers): a planned move (`facility-hub-admin/central-move`), and move codes for a new address, printable or as CSV.
- **Hub version** (hub managers and superusers): the version hubs run, from the supplier's GitHub releases or typed, started on chosen pilot hubs and moved on to 10 %, 50 % and all hubs with "Next stage"; "Stop" holds every hub where it is. The page counts, from the heartbeats, the hubs in the stage, on the version, not yet, and failed (with the reason). It writes `facility-hub-admin/release`: `{"version": "0.3.0", "pilotHubs": ["hub-…"], "percent": 10}`. A hub takes the version when it is newer than its own, has not failed there before, and the hub is a pilot hub or `fnv32a(hub ID) % 100 < percent` (`upgrade.InStage`; the app's `stage.ts` follows the same rule, checked against `internal/upgrade/testdata/stages.json`). The hub list filters by version.
- **A hub, for hub managers:**
  - **Revoke all tokens of this hub**, for a lost or stolen hub. It deletes every token the hub lists in its registry entry (`tokens`), then marks the entry `revoked` with who and when. On DHIS2 2.42 only a token's owner, or a group it is shared with, can delete it, and disabling the user does not stop it (FINDINGS.md, "Tokens outlive their user"). So hubs share their tokens with the hub managers group. The page counts the tokens it could not delete (made before 2026-10-20 and not yet renewed, or already gone) and reminds the manager that the hub's users change their passwords. A revoked hub that comes online drops what it holds and serves nothing until it is reset.
  - **Undo this replacement**, on a hub retired by another: the replacing hub is retired (`retiredBy: "undo"`) and this one made active again, with the history kept under `undone`. Refused when the replacing hub is itself retired or revoked. Everyone sees the replacement history.
- **Settings** (hub managers and superusers):
  - tracker retention (`facility-hub-config/retention`: months by default, by programme, by person type; 0 keeps everything);
  - aggregate retention (`facility-hub-config/aggregate`: periods by default and by data set);
  - token lifetime (`facility-hub-config/tokens`: `expiryDays`, 7–365).
  
  Hubs read them hourly. Missing keys show today's defaults. They are written with the same sharing as `facility-hub-admin`, replacing the Datastore Manager for these keys.
- **Refusals** (hub managers and superusers): the refusals of every hub in use, added up by kind, programme and error code from the heartbeats (`refused`). After central is fixed, **Resend** on a group appends `{id, kind, program, code, at, by}` to `facility-hub-admin/retries` (the last 50 kept). Each hub, once per instruction, sends its refused saves of that group, and what they blocked, again as their authors. Central's new answer stands.
  - **Send as user.** On a group with the code `NO_LOGIN` (saves whose author could not log in online for 7 days after central stopped accepting their token: disabled, deleted or token expired), Resend has a text field for the username to send them as; the instruction then carries `user`. The app has no list of each hub's users, so the name is typed; it says next to the field that the records then appear on central as created by that user. Each hub sends the group's saves as that user if it knows the user and holds a token for them; otherwise it applies nothing of that instruction and its heartbeat lists the instruction with the reason (`retriesNotApplied: [{id, reason}]`). The list of resends shows as whom each was sent, and the hubs that did not apply it, with their reasons. Central decides whether that user may save the records; a refusal comes back with its reason.
- **`facility-hub-admin`** must be writable by administrators only (a planned move there moves every hub). The app creates its entries empty, shares them as public read only (the planned move also read-write for the hub managers), and only then writes them: a new dataStore entry is public read and write until its sharing is set. An entry someone else created first is deleted and created again, since its owner keeps write access whatever the sharing says. A planned move is withdrawn by writing it empty, not by deleting it, which would let anyone create it again. On 2.42 any user may create a missing key, own it, and keep writing it while its public access is read only, which the hubs would trust: when a superuser or hub manager opens the app, it creates the missing entries (`accounts`, `central-move`, `release`, `retries`) so, with values the hubs read as not set (`ensureAdminEntries`). If one cannot be created, every page shows a warning with the error ("The hub settings could not be created"), until a later visit creates it.

## Hub managers

Approving hubs and making move codes need no superuser: members of a *hub managers* user group do it (measured on 2.42). Set up once, by a superuser:

- a user group, named on the Approvals page as the hub managers group;
- a role for its members with `F_USER_ADD` and `F_USER_VIEW` (to create and look up the hub's user), `F_USER_GROUPS_READ_ONLY_ADD_MEMBERS` (to put it in the hub accounts group without write access to that group) and `M_facilityhub`;
- read access for the hub managers group to the hub accounts group (sharing).

DHIS2 lets a manager create users only within their own org units: a district's hub manager approves the hubs of that district, and is refused for others. Its hub's user is named in the hub accounts group when it is created (the group's own member endpoint wants write access to the group, which managers do not get).

Trade-off, accepted 2026-10-19: every hub manager reads every hub's move key (they are shared with the group, not by org unit), so any manager can make move codes for any hub. A manager who wanted to move a hub to a server of theirs would still need central to be really down and staff at that facility to type their address in. Name as managers only people you would trust to approve a hub.

## Approving a hub's own account

A hub waiting for an account has, in `facility-hub-registry/<hub ID>`: `accountRequested: true`, `status: "active"`, `facility`, and `boxKey` (base64 of an X25519 public key, 32 bytes). Its admin page shows its account code.

1. List the entries with `accountRequested` and no `account`.
2. The approver calls the facility, on a number they already have (never a code from someone who calls them), and asks for the code on the hub's page. Refuse unless `AccountCode(boxKey, hubID, facility)` equals the code read out (compare without dashes or spaces, in upper case). Anyone in the hub owners group can write the entry; the code on the hub's screen is what ties the entry, its key and its facility to the hub.
3. Refuse if the hub account role has any authorities. Look up the user `<hub ID>`:
   - none: create it, with a random password meeting DHIS2's rules, assigned to `facility` only for capture, data view and search, with the hub account role, and in the hub accounts group (`userGroups` in the new user);
   - one the hub would take up (enabled, assigned to `facility` only for capture, and to nothing or `facility` for data view and search, with roles that have no authorities): replace its password and its groups (`PATCH /api/users/{id}`, JSON patch: `replace /password`, `add /userGroups` = the hub accounts group);
   - anything else: refuse, saying what to correct. Handing it over again would only have the hub refuse it again.
4. (Removed: the group is set in step 3. Adding members through `/api/userGroups/{id}/users/{user}` needs write access to the group, which hub managers do not have.)
5. Make a move key (32 random bytes). Delete `facility-hub-keys/<hub ID>` if it exists, create it empty (`{}`), share it with the hub managers group only (`POST /api/sharing?type=dataStore&id=<its metaData id>`, `publicAccess "--------"`, no user accesses, the managers group `rw------`; no group accesses if none is named), and only then write `{"moveKey": "<base64>"}`: a new entry is public until its sharing is set. Creating it afresh makes the approver its owner: whoever created it before would keep access as owner.
6. Seal `{"username": "<hub ID>", "password": "...", "moveKey": "<base64>"}` to `boxKey` (`hubaccount.Seal`: an ephemeral X25519 key, HKDF-SHA256 bound to the hub ID, AES-256-GCM; see `internal/hubaccount/box.go`).
7. Read the entry again; refuse if `boxKey` or `facility` changed meanwhile, or it has an `account` (another approval); set `account: {"username": "<hub ID>", "box": "<base64>"}` and write it back, changing nothing else.

The hub takes the account up at its next refresh, replaces the password and token, and removes `account` and `accountRequested`. An account it refuses (another name, other org units, authorities, a password central refuses) makes it ask again.

An approval that fails after step 5 leaves central with a move key the hub never got: approve the hub again before making move codes for it.

Approving and making move codes need a hub manager or a superuser: the move key's entry is shared with the hub managers group only, so only they, its owner and superusers read or replace it. One approver per hub at a time: two approvals of the same hub within seconds can leave the hub with a move key central no longer holds (the hub then needs approving again).

If central locks accounts after failed logins, someone could trigger it on a waiting hub's account (its name is the hub ID), and the hub would read the refusal as an account it cannot use and ask again: approve it again later.

## Move codes

After an unplanned move, the app makes one code per hub for central's new address: for each hub, `MoveCode(moveKey, address)` with the key from `facility-hub-keys/<hub ID>`. The facility enters the address and the code on the hub's page. A code stays valid for its address until the hub is approved again (a new move key); that matters only if an old address could later belong to someone else.

## The two codes

Both are base32 (RFC 4648 alphabet `A–Z2–7`, no padding), cut short and written in groups.

- `AccountCode(pub, hubID, facility)`: the first 16 characters of base32(SHA-256(`"facility-hub account code"` 0x00 hubID 0x00 facility 0x00 pub)), in groups of 4. 80 bits: the key in the entry is public, so a key matching the hub's code must be out of reach of an offline search.
- `MoveCode(moveKey, address)`: the first 10 characters of base32(HMAC-SHA256(moveKey, normalized address)), in groups of 5. 50 bits: it can only be tried on the hub, which throttles wrong codes.
- Addresses: `http(s)://host[:port][/path]` written with letters, digits and `- . _ ~ ! $ & ' ( ) * + , ; = / : @ [ ]` only, with a port given if there is a colon after the host; the app refuses any other (as the hub does), since such addresses have more than one written form.
- Normalized address: surrounding spaces and trailing slashes removed, scheme and host in lower case, the default port (`:443` for https, `:80` for http) removed, the path as typed.

Test values (`internal/hubaccount/codes_test.go`):

| Input | Code |
|---|---|
| `AccountCode(32 × 0x01, "hub-Abc123XyZ90", "DiszpKrYNg8")` | `VMC4-PMVY-I5K4-UMUO` |
| `MoveCode(32 × 0x02, "https://new.example.org/dhis")` | `IJ3O2-BXZPR` |
| `MoveCode(32 × 0x02, " HTTPS://New.Example.org/dhis/ ")` | `IJ3O2-BXZPR` |
| `MoveCode(32 × 0x02, "https://new.example.org:443/dhis")` | `IJ3O2-BXZPR` |

The sealed box has a test value too: `TestBoxVector` (`internal/hubaccount/box_test.go`) builds it for fixed inputs (ephemeral key 32 × 0x03, nonce 12 × 0x04, the hub's key from 32 × 0x05, hub ID `hub-x`, plaintext `{"username":"hub-x"}`), pins its bytes and opens it with the hub's `Open`; the app's `seal` gives the same bytes (`src/hubaccount/box.test.ts`).
