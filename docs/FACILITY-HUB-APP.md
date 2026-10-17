# Facility Hub app: what it must match

The Facility Hub app is a DHIS2 app on central, used by the national team and districts (to build; its other duties are in [DEPLOYMENT.md](DEPLOYMENT.md), section 6, "The Facility Hub app"). This note fixes the parts the hubs depend on exactly: approving a hub's own account, and move codes. `internal/hubaccount` is the reference implementation, used by the hub's tests; the app's tests must give the same values (below).

## Approving a hub's own account

A hub waiting for an account has, in `facility-hub-registry/<hub ID>`: `accountRequested: true`, `status: "active"`, `facility`, and `boxKey` (base64 of an X25519 public key, 32 bytes). Its admin page shows its account code.

1. List the entries with `accountRequested` and no `account`.
2. The approver calls the facility and asks for the code on the hub's page. Refuse unless `AccountCode(boxKey, hubID)` equals the code read out (compare without dashes or spaces, in upper case). Anyone in the hub owners group can write the entry; the code on the hub's screen is what ties the entry to the hub.
3. Look up the user `<hub ID>`:
   - none: create it, with a random password meeting DHIS2's rules, assigned to `facility` only for capture, data view and search, with the hub account role (no authorities);
   - one assigned to `facility` only: replace its password (`PATCH /api/users/{id}`, JSON patch on `/password`);
   - anything else: refuse.
4. Add it to the hub accounts group.
5. Make a move key (32 random bytes). Delete `facility-hub-keys/<hub ID>` if it exists, create it as `{"moveKey": "<base64>"}`, and share it with no one (`POST /api/sharing?type=dataStore&id=<its metaData id>`, `publicAccess "--------"`, no user or group accesses). Creating it afresh makes the approver its owner: whoever created it before would keep access as owner.
6. Seal `{"username": "<hub ID>", "password": "...", "moveKey": "<base64>"}` to `boxKey` (`hubaccount.Seal`: an ephemeral X25519 key, HKDF-SHA256 bound to the hub ID, AES-256-GCM; see `internal/hubaccount/box.go`).
7. Read the entry again; refuse if `boxKey` changed meanwhile; set `account: {"username": "<hub ID>", "box": "<base64>"}` and write it back, changing nothing else.

The hub takes the account up at its next refresh, replaces the password and token, and removes `account` and `accountRequested`.

## Move codes

After an unplanned move, the app makes one code per hub for central's new address: for each hub, `MoveCode(moveKey, address)` with the key from `facility-hub-keys/<hub ID>`. The facility enters the address and the code on the hub's page.

## The two codes

Both are base32 (RFC 4648 alphabet `A–Z2–7`, no padding), cut short and written in groups.

- `AccountCode(pub, hubID)`: the first 16 characters of base32(SHA-256(`"facility-hub account code"` 0x00 hubID 0x00 pub)), in groups of 4. 80 bits: the key in the entry is public, so a key matching the hub's code must be out of reach of an offline search.
- `MoveCode(moveKey, address)`: the first 10 characters of base32(HMAC-SHA256(moveKey, normalized address)), in groups of 5. 50 bits: it can only be tried on the hub, which throttles wrong codes.
- Normalized address: surrounding spaces and trailing slashes removed, scheme and host in lower case, the path as typed.

Test values (`internal/hubaccount/codes_test.go`):

| Input | Code |
|---|---|
| `AccountCode(32 × 0x01, "hub-Abc123XyZ90")` | `OUMC-LEW6-EOCO-KJ6V` |
| `MoveCode(32 × 0x02, "https://new.example.org/dhis")` | `IJ3O2-BXZPR` |
| `MoveCode(32 × 0x02, " HTTPS://New.Example.org/dhis/ ")` | `IJ3O2-BXZPR` |
