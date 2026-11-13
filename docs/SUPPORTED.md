# What works offline through the hub

This page says what each client can do while the hub is offline, and what needs a connection. Online, the hub passes every request to central as the user, so everything central supports works; the hub keeps copies and data on the way. The first part is per app and written by hand from the tests named in it. The second part is generated from the tables the hub decides by, and `TestSupportedDocUpToDate` fails when it no longer matches the code.

Supported central versions: DHIS2 2.42 and 2.43. One hub serves one facility (one org unit): records owned by org units below it stay online only.

Before a hub is used offline, each staff member logs in once through it while it is online, on the device they will use. The hub keeps what they open online and, at each online login, loads what anyone at the facility opened online in the last 14 days. A screen nobody has opened online is not available offline (DEPLOYMENT.md, section 3, step 4). The login page is kept as soon as the hub reaches central.

## By app

| App | Works offline | Needs a connection | Tests |
| --- | --- | --- | --- |
| Capture web (tracker and event programs) | Login; working lists; search among the facility's people; registration with generated IDs from the hub's pool; enrollments, events and relationships, saved as their author; dashboards | Screens nobody opened online; search outside the facility; deletes; file and image uploads; central's validation | `TestCaptureWeb`, `TestCaptureWebUsers`, `TestCaptureWebRefusals`, `TestCaptureWebFiles`, `TestOfflineScreens` (live, real app) |
| Android Capture | Login; metadata and data sync with the hub; search; registration and events, uploaded to the hub and sent on as their author; reserved values topped up from the hub's pool | What the hub needs a connection for, as above; the download of event programs is covered by the oracle only | `TestCaptureAndroid` (live, emulator) |
| eRegisters | Login; its pull of the facility's people per program and its pushes, so devices at the facility share data through the hub; its settings in the data store (read) | Aggregate reports through `dataValueSets`; its reports service; data store writes; deletes | `TestERegisters`, `TestERegistersUpdate` (live, 2.43) |
| Aggregate Data Entry | Each user's forms for the held periods, edits and completion, sent as their author; the period lock, worked out offline | Forms outside the held periods (shown as locked); file values; min-max edits; validation before completing | `TestAggregateDataEntryApp` (live, real app), `TestAggregate*` |

Notes:

- **Search scope.** Offline, a search covers the facility's people only. A user whose search scope is wider (a district officer) gets "needs connection" for searches beyond it. The hub says so in the `X-Hub-Scope` header; the apps do not show it yet.
- **Status.** Apps that check the connection by asking the hub (eRegisters, Data Entry) show "Online" while the hub is offline, because the hub answers. `/hub/status` says whether central is reachable.
- **Refused changes.** A change central refuses later, on push, is kept as an issue for its author, shown on the hub's page (FINDINGS.md, "Refused changes").
- **What the hub does not run offline:** central's validation rules, program rules on the server, analytics and reports, messages, and user management.

## Endpoints and parameters (generated)

<!-- generated: go test ./internal/catalog -update -->

### Tracker reads

Answered offline from the hub's store, for the facility's data. The API version may be in the path (`/api/42/tracker/…`). Any other parameter gets 400 "not supported offline: <name>" and is counted on the hub's endpoints page. Ignored, as central ignores it: `pageNumber`.

| Endpoint | Parameters accepted offline |
| --- | --- |
| `GET /api/tracker/trackedEntities` | `enrollmentEnrolledAfter`, `enrollmentEnrolledBefore`, `enrollmentStatus`, `fields`, `filter`, `includeDeleted`, `order`, `orgUnit`, `orgUnitMode`, `orgUnits`, `ouMode`, `page`, `pageSize`, `paging`, `program`, `programStatus`, `skipPaging`, `totalPages`, `trackedEntities`, `trackedEntity`, `trackedEntityType`, `updatedAfter`, `updatedBefore` |
| `GET /api/tracker/enrollments` | `enrolledAfter`, `enrolledBefore`, `enrollmentStatus`, `enrollments`, `fields`, `includeDeleted`, `order`, `orgUnit`, `orgUnitMode`, `orgUnits`, `ouMode`, `page`, `pageSize`, `paging`, `program`, `programStatus`, `skipPaging`, `status`, `totalPages`, `trackedEntity`, `updatedAfter`, `updatedBefore` |
| `GET /api/tracker/events` | `enrollment`, `enrollmentStatus`, `enrollments`, `event`, `events`, `fields`, `filter`, `filterAttributes`, `includeDeleted`, `occurredAfter`, `occurredBefore`, `order`, `orgUnit`, `orgUnitMode`, `orgUnits`, `ouMode`, `page`, `pageSize`, `paging`, `program`, `programStage`, `programStatus`, `scheduledAfter`, `scheduledBefore`, `skipPaging`, `status`, `totalPages`, `trackedEntity`, `updatedAfter`, `updatedBefore` |
| `GET /api/tracker/relationships` | `enrollment`, `event`, `fields`, `includeDeleted`, `order`, `page`, `pageSize`, `paging`, `skipPaging`, `totalPages`, `trackedEntity` |
| `GET /api/tracker/{trackedEntities,enrollments,events,relationships}/{uid}` | `fields`, `includeDeleted`, `orgUnitMode`, `ouMode`, `program` |

`orgUnitMode` offline: `SELECTED` with the facility; `DESCENDANTS` or `CHILDREN` of the facility; `ACCESSIBLE` or `CAPTURE` only for a user whose scope is the facility alone. Other modes and other org units: "needs connection".

### Tracker writes

`POST /api/tracker` is stored on the hub and queued for central, sent later as its author. Parameters accepted offline: `async`, `atomicMode`, `flushMode`, `idScheme`, `importMode`, `importStrategy`, `reportMode`, `skipPatternValidation`, `skipRuleEngine`, `skipSideEffects`, `validationMode`; `importStrategy` `CREATE`, `UPDATE` or `CREATE_AND_UPDATE` (not `DELETE`), `importMode` `COMMIT` only, `idScheme` `UID` only. `GET /api/tracker/jobs/{id}` and `…/report` answer an offline import made with `async=true`.

### Aggregate data entry

Answered offline from the hub's copy of each user's forms: `DELETE /api/dataValues`, `GET /api/dataEntry/dataValues`, `POST /api/dataEntry/dataSetCompletion`, `POST /api/dataValues`. Other methods on these paths (file values, min-max values) get 409 "needs connection".

### Everything else: copies of what was opened online

Other reads (metadata, settings, the apps menu, data store entries) are answered offline from the user's own copy of central's answer to the same request: one they made online through the hub, or one the hub loaded for them at their online login because someone at the facility opened it online in the last 14 days. A request nobody opened online gets "needs connection".

Never loaded at login (the first part of the path after `/api/`): `analytics`, `apiToken`, `audits`, `completeDataSetRegistrations`, `dataAnalysis`, `dataEntry`, `dataValueSets`, `dataValues`, `documents`, `enrollments`, `events`, `fileResources`, `interpretations`, `messageConversations`, `ping`, `relationships`, `trackedEntities`, `trackedEntityInstances`, `tracker`, `userLookup`, `users`, `validation`, `validationResults`; also `/api/me` (kept at login), `generate` and `requestValues` (they reserve values), and requests naming another user.

Shared between users, since central answers anyone the same: the login page and its files (`/dhis-web-login/`, `/login/`, `/dhis-web-commons/flags/`, `/dhis-web-commons/security/`, kept in the public file and served even while the hub is locked), `loginConfig`, `staticContent`, `locales` under `/api/`, and the apps' static files.

Always "needs connection" offline: central's validation (`/api/validation/…`), and every other write (`POST`, `PUT`, `PATCH`, `DELETE`), data store entries included.

<!-- end of generated part -->
