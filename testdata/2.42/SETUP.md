# DHIS2 2.42.6 test instance setup (facility hub POC)

Instance: `http://dhis2-agent-facility-hub:8080` (Sierra Leone demo DB, 2.42.6, rev dd8bdbb). Superuser `local_admin` / `district`.
Rerun `python3 setup_instance.py` (stdlib only; env `DHIS2_URL`, `DHIS2_ADMIN`) to recreate everything idempotently. It uses fixed UIDs, `CREATE_AND_UPDATE` and merged sharing.

## Metadata

| What | UID | Notes |
|---|---|---|
| Facility | `DiszpKrYNg8` | Ngelehun CHC, code `OU_559` |
| Chiefdom (parent) | `YuQRtpLP10I` | Badjia |
| District | `O6uvpzGd5pu` | Bo (teiSearchOrganisationUnits for all test users) |
| Other facility (outsider_d) | `g8upMTyEZGZ` | Njandama MCHP, code `OU_167609`, sibling in Badjia |
| Program | `ur1Edk5Oe2n` | TB program, WITH_REGISTRATION, accessLevel OPEN, category combo default |
| Stage 1 | `EPEcjy3FWmI` | Lab monitoring, repeatable, no compulsory DEs (DE `Vk1tzSQxvOR` TRUE_ONLY) |
| Stage 2 | `ZkbAXlQUYJG` | TB visit, not repeatable, autoGenerateEvent, compulsory `D7m8vpzxHDJ` (P+/P-/PE), `HmkXnHJxcD1` (New/Relapse/Failure/TAI) |
| **Stage 3 (restricted)** | `jdRD35YwbRH` | Sputum smear microscopy test, repeatable, compulsory `yLIPuJHRgey` (INTEGER_POSITIVE), `zocHNQIQBIN` (NEG/POS) |
| Tracked entity type | `nEenWmSyUEp` | Person |
| Default categoryOptionCombo | `HllvX50cXC0` | (default categoryCombo `bjDvmb4bfuf`) |
| Relationship type seen in data | `tBeOL0DL026` | |

Program attributes:
- Mandatory: First name `w75KJ2mc4zz`, Last name `zDhUuAYrxNC`, Gender `cejWyOfXge6` (option codes `Male`/`Female`).
- **Unique and generated:** TB identifier `xs8A6tQJY0s`, pattern `RANDOM(##########)`. Unique ID `lZGmxYbs97q`, pattern `RANDOM(#######)`. Both have `orgunitScope=false` and neither uses ORG_UNIT_CODE, so no extra parameter is needed.
- **Unique but not generated:** National identifier `AuPLng5hLbE` (used for the conflict test: value `886649902` belongs to TE `LxMVYhJm3Jp`).
- Others: Age `spFvx9FndA4`, Address `VqEFza8wbwA`, City `FO4sWYJ64LQ`, Email `NDXw0cluzSw`, Phone `P2cwLGskgxn`, etc.

Useful existing data: TE `LxMVYhJm3Jp` (Berhane Yusef) has events in all 3 stages (enrollment `awZ5RHoJin5`, event `IQCiAZs7PrK`). TE `J8KD01PshMC` has a relationship.

### Tracked entities per tracker program at Ngelehun CHC (orgUnitMode=SELECTED, before fixtures)

| Program | UID | TEs |
|---|---|---|
| Child Programme | IpHINAT79UW | 36 |
| Malaria case diagnosis, treatment and investigation | qDkgAbB5Jlk | 32 |
| Malaria focus investigation | M3xtLkYBlKI | 7 |
| MNCH / PNC (Adult Woman) | uy2gU8kT1jF | 3 |
| Provider Follow-up and Support Tool | fDd25txQckK | 5 |
| TB program | ur1Edk5Oe2n | 63 (+2 HUBFIX TEs created by fixtures) |
| WHO RMNCH Tracker (PROTECTED) | WSGAb5XwJ3Y | 10 |

That is at most 156, and TEs can be enrolled in more than one program, so a full pull is small. TB events at the facility: 40 visible to nurse_a, 28 to nurse_b (without stage 3).

Why TB program: it is OPEN, has exactly 3 stages and has generated unique attributes. WHO RMNCH also has `lZGmxYbs97q`, but it is PROTECTED.

## Users, groups, role (password for all: `Hub-Test-2026!`)

| Username | UID | Capture OU | Groups |
|---|---|---|---|
| hub_admin | `HubUsrAdmin` | DiszpKrYNg8 | Facility hub setup |
| nurse_a | `HubUsrNursA` | DiszpKrYNg8 | Facility hub stage 3 |
| nurse_b | `HubUsrNursB` | DiszpKrYNg8 | – |
| clerk_c | `HubUsrClrkC` | DiszpKrYNg8 | – |
| outsider_d | `HubUsrOutsD` | g8upMTyEZGZ | – |

All users have dataView OU = their capture OU, teiSearch OU = Bo `O6uvpzGd5pu`, and role `HubRole0001`. No password expiry is set (`keyPasswordExpiryDays` empty). All 5 log in with `POST /api/auth/login` and get SUCCESS.

- Groups: "Facility hub setup" `HubGrpSetup`, "Facility hub stage 3" `HubGrpStag3`.
- Role "Facility hub tracker user" `HubRole0001` has: `M_dhis-web-capture`, `M_dhis-web-dashboard`, `M_dhis-web-messaging`, `F_UNCOMPLETE_EVENT`.
  - 2.42 has no `M_dhis-web-tracker-capture`.
  - `F_TRACKED_ENTITY_ADD`/`_UPDATE` are tracked entity type *metadata* authorities, not data authorities.
  - Tracker data writes need no authority, only sharing.
  - No authority exists for PATs.

### Sharing (data access, verified per user via /api/programs)

| | program | TET | stage1 | stage2 | stage3 |
|---|---|---|---|---|---|
| hub_admin | rw | rw | rw | rw | rw (direct user access) |
| nurse_a | rw | rw | rw | rw | rw (via group) |
| nurse_b | rw | rw | rw | rw | -- |
| clerk_c | r- (`rwr-----`) | rw | r- | r- | -- |
| outsider_d | rw | rw | rw | rw | -- |

- Program and stages: public access is `r-------` (metadata read, no data). The existing demo `_PROGRAM_*` group accesses are kept; none of the test users is in them.
- Person TET: public access is left at the demo default `rwrw----`, and explicit user accesses are added.

## PAT (personal access tokens): normal users CAN create their own

- `POST /api/apiToken` works as nurse_a with Basic auth, a session cookie, or even another PAT.
  - Minimal body: `{}`. Type defaults to `PERSONAL_ACCESS_TOKEN_V2` and expire defaults to now + 30 days.
  - Recommended body: `{"expire": <epoch ms>}`.
  - Optional `attributes`: `IpAllowedList`, `MethodAllowedList` and `RefererAllowedList`.
  - Invalid `type` gives HTTP 500. A past expire or 400 days is accepted without validation.
- Response is 201 `{"httpStatus":"Created","httpStatusCode":201,"status":"OK","response":{"key":"d2p_…(54 chars total)","uid":"<11>","klass":"org.hisp.dhis.security.apikey.ApiToken","errorReports":[],"responseType":"ObjectReport"}}`. The key appears only here.
- `GET /api/apiToken?fields=*` returns `{pager, apiToken:[…]}` with the caller's own tokens only. Each token has id, type, version, expire, attributes, user, and no key. Delete with `DELETE /api/apiToken/{uid}`, which returns 200.
- `Authorization: ApiToken <key>` works for `/api/me` and `/api/tracker/*`.
  - DHIS2 still sends `Set-Cookie: JSESSIONID_…` on a PAT request; ignore it.
  - Expired token: 401 "Failed to authenticate API token, token has expired."
  - Deleted token: 401 "The API token does not exists".
  - Malformed or unknown key: **400** "Checksum validation failed" with `WWW-Authenticate: ApiToken`.
- All test tokens were deleted.

## Login, session, logout

- Web login app (2.42), in order:
  1. `GET /dhis-web-login/`
  2. `GET /api/loginConfig` (unauthenticated)
  3. `POST /api/auth/login`, JSON `{"username","password"}`, plus `"twoFactorCode"` when needed.
  4. On SUCCESS, navigate to `redirectUrl`.
- Success: 200 `{"loginStatus":"SUCCESS","redirectUrl":"/"}` plus `Set-Cookie: JSESSIONID_agent-facility-hub=…; Path=/; HttpOnly; SameSite=Lax`. **The cookie name is instance-specific; default DHIS2 uses `JSESSIONID`.**
- Bad password or unknown user: 401 `{"httpStatus":"Unauthorized","httpStatusCode":401,"status":"ERROR","message":"Bad credentials"}`.
- 2FA (tested live with a temporary user that was then deleted):
  - Missing or wrong code: **200** `{"loginStatus":"INCORRECT_TWO_FACTOR_CODE_TOTP"}` with no cookie.
  - Correct code: SUCCESS.
  - Basic auth for a 2FA user: 401.
- Unauthenticated `/api/me` or `/api/tracker/trackedEntities`:
  - Without `X-Requested-With`: **302 Location `/dhis-web-login/`**, empty body.
  - With `X-Requested-With: XMLHttpRequest`: 401 JSON `{"httpStatus":"Unauthorized","httpStatusCode":401,"status":"ERROR","message":"Unauthorized"}`.
  - Bad Basic credentials: 401, same JSON.
- Logout:
  - The header bar uses `GET /dhis-web-commons-security/logout.action`, which returns 302 to `/` and invalidates the session. Its Set-Cookie clears plain `JSESSIONID`, not the custom name.
  - `/api/auth/logout` does not exist (404).
- `GET /api/system/ping` returns `pong` (not valid JSON) with Content-Type application/json.

## Tracker read shapes

- `/api/tracker/events` takes **`orgUnit` (singular)**. With `orgUnits` it returns 400 E1003 "At least one org unit is required for orgUnitMode: SELECTED…". trackedEntities and enrollments take `orgUnits`.
- Pager shapes:
  - Default paging: `pager{page,pageSize[,prevPage][,nextPage]}`. `nextPage` appears only when the page is full; `prevPage` only when page > 1. There is no total.
  - `totalPages=true`: `pager{page,pageSize,total,pageCount[,prevPage][,nextPage]}`.
  - `paging=false`: no `pager` key at all.
- Collection keys: `trackedEntities`, `enrollments`, `events`, `relationships`.
- Single trackedEntity keys: trackedEntity, trackedEntityType, createdAt, createdAtClient, updatedAt, orgUnit, inactive, deleted, potentialDuplicate, relationships, attributes, enrollments, programOwners.
- Stage access on reads:
  - nurse_b gets the same TE **without stage-3 events**, silently.
  - nurse_b asking for `programStage=jdRD35YwbRH` gets 403 E1006 "User has no access to program stage: jdRD35YwbRH".
- Not found: 404 `{httpStatus,httpStatusCode,status,message:"TrackedEntity with id X could not be found.",errorCode:"E1005"}`.
- Bad parameter value: 400 `{httpStatus,httpStatusCode,status,message:"Value 'abc' is not valid for parameter pageSize. …"}` with **no errorCode**.
  - Unknown program: 400 E1003.
  - Missing program/TET: 400 E1003.
- outsider_d can *read* Ngelehun TEs, because the search scope is Bo and the program is OPEN. Writes are refused.

## Tracker write (POST /api/tracker?async=false) shapes

- Top-level keys:
  - Success: `status`, `validationReport{errorReports,warningReports}`, `stats{created,updated,deleted,ignored,total}`, `bundleReport{typeReportMap{TRACKED_ENTITY,ENROLLMENT,EVENT,RELATIONSHIP:{trackerType,stats,objectReports[{trackerType,uid,errorReports}]}}}`.
  - Failure (409): no bundleReport.
- Each errorReport has `{message, errorCode, trackerType, uid, args}`.
- HTTP status:
  - Success: 200, status OK.
  - Any error with atomicMode=ALL (default): **409**, status ERROR, no bundleReport.
  - atomicMode=OBJECT with errors: **200 and status "OK"**. Errors show only in validationReport and stats.ignored.
  - Invalid UID anywhere: **400** JSON parse WebMessage with no errorCode.
  - async=true: 200 `{…,"message":"Tracker job added","response":{"id","location","responseType":"TrackerJob"}}`.
- Error codes:
  - Program data read-only (clerk_c): **E1095** "User: `HubUsrClrkC` has no data write access to ProgramStage: `EPEcjy3FWmI`."
  - nurse_b on stage 3: E1095.
  - outsider_d writing at Ngelehun: **E1000** "User: `HubUsrOutsD` has no write access to OrganisationUnit: `DiszpKrYNg8`."
  - Unique conflict: **E1064** "Non-unique attribute value `886649902` for attribute `AuPLng5hLbE`."
    - The nested enrollment gets **E5000** "…cannot be created because trackedEntity … could not be created."
    - The nested event gets no error report but is counted as ignored in its per-type stats.
  - Event pointing at a missing enrollment: there is no dedicated code. You get **E1079** (program differs from enrollment) plus **E1313** (event of an enrollment does not reference a TE).
  - Enrollment pointing at a missing TE: **E1068**.
  - CREATE of an existing event: **E1030** "Event: `X` already exists."
- Test data created (all values start with HUBFIX, all at Ngelehun):
  - TE `HubFxTe0001` / enrollment `HubFxEn0001` / event `HubFxEv0001` (stage 1).
  - TE `HubFxTe0002` / enrollment `HubFxEn0002` / events `HubFxEv0002` (stage 1), `HubFxEv0003` (stage 3), `HubFxEv0004` (stage 1).
  - All created by nurse_a; `createdBy` is nurse_a.

## Generated values

- `GET /api/trackedEntityAttributes/{uid}/generate` returns a single object `{ownerObject,ownerUid,key,value,created,expiryDate}` (+3 days).
- **`/requestValues?numberToReserve=N` returns 200 `{}` (empty) on 2.42.6 for everyone.** Use `/generateAndReserve?numberToReserve=N[&expiration=days]`, which is what the Android SDK calls. It returns a bare array of the same objects, with a default expiry of +60 days.
- Non-generated attribute: 400 "Attribute does not contain pattern.". Unknown attribute: 404 E1005.
- RANDOM values can have leading zeros; keep them as strings.

## Data store

- Namespace `facility-hub-poc`, key `hub-DiszpKrYNg8`.
- `POST` returns 201 "Key created". POST on an existing key returns 409 E1004.
- `PUT` returns 200 "Key updated". **PUT on a missing key creates it (201).**
- GET of a missing key returns 404 E1005.
- **Query filters are supported:** `GET /api/dataStore/<ns>?fields=.&filter=facility:eq:DiszpKrYNg8` returns `{pager{page,pageSize},entries:[{key,value}]}`. With named fields each entry is `{key,<fields…>}`. Without `fields` you get a bare array of keys.
- Default entry sharing lets any user (nurse_a, outsider_d) read and write. Protect the namespace for the setup group if needed.
