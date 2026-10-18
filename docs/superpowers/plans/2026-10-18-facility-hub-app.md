# Facility Hub app Implementation Plan

> **For agentic workers:** executed inline (superpowers:executing-plans), overnight 2026-10-18, by the author of the spec. Steps use checkbox syntax.

**Goal:** the first version of the Facility Hub app: hub list with flags, hub page, approvals, central moves.

**Architecture:** a DHIS2 App Platform app in `apps/facility-hub/`. A pure module `src/hubaccount/` mirrors the hub's `internal/hubaccount` (codes, sealing, approval steps over a small request interface); pure functions compute flags and visibility; React screens read the facility-hub namespaces through `useApiDataQuery`.

**Tech Stack:** TypeScript, React 18, `@dhis2/ui`, `@dhis2/app-runtime`, TanStack Query 4, React Router (hash), Vite, pnpm; `@noble/curves`, `@noble/hashes`, `@noble/ciphers`; Go and Python Playwright for the live test.

**Spec:** docs/superpowers/specs/2026-10-18-facility-hub-app-design.md, and the contract docs/FACILITY-HUB-APP.md.

## Global Constraints

- DHIS2 2.42 and 2.43; `minDHIS2Version: '2.42'`.
- Codes, addresses and sealing exactly as FACILITY-HUB-APP.md, with its test values.
- Superuser = `ALL` authority. Districts filtered in the app.
- All user-facing strings through `i18n.t()`; `displayName` for DHIS2 objects.
- `pnpm exec eslint` and `pnpm exec tsc --noEmit` clean after each task; the app's tests and the Go suite green before each commit.

## Review Focus

1. A hub whose heartbeat lacks fields (older hub versions, never reported): the list and page must render, flags must not throw.
2. A namespace that does not exist yet (404 on the listing): an empty list, not an error.
3. An approval that fails half-way: the dialog names the failed step; running it again must work (the reference is idempotent).
4. A user with org units at several levels, or none: visibility follows paths; no org units sees nothing.
5. Move codes for hundreds of hubs: one listing request for the keys, not one per hub.

---

### Task 1: Scaffold

- [ ] `pnpm create @dhis2/app@latest facility-hub --typescript --yes` in `apps/`; apply the skill's fixes (locales import, `@types/react@18`), TanStack Query 4, React Router, the `@` alias, `d2.config.js` (`minDHIS2Version: '2.42'`), sidebar layout with four routes (Hubs, Hub, Approvals, Central moves).
- [ ] `pnpm exec tsc --noEmit`, `pnpm exec eslint src`, `pnpm build` pass; commit.

### Task 2: Codes and addresses

Produces `accountCode(pub: Uint8Array, hubID: string, facility: string): string`, `moveCode(key: Uint8Array, address: string): string`, `checkAddress(raw: string): string | null` (error message or null), `normalizeAddress`, `normalizeCode`.
- [ ] Tests first with the FACILITY-HUB-APP.md values (`VMC4-PMVY-I5K4-UMUO`, `IJ3O2-BXZPR` three ways) and the refused addresses of `codes_test.go`; watch them fail; implement; pass; commit.

### Task 3: Sealing, pinned on both sides

Produces `seal(pub: Uint8Array, plaintext: Uint8Array, hubID: string, rnd?: {eph: Uint8Array, nonce: Uint8Array}): Uint8Array`.
- [ ] Go: `TestBoxVector` in `internal/hubaccount/box_test.go`: builds the box for fixed inputs (ephemeral private key 32 × 0x03, nonce 12 × 0x04, hub public key from private 32 × 0x05, plaintext `{"username":"hub-x"}`, hub ID `hub-x`) by the documented steps, pins its hex, and opens it with `Open`. Fails first on the placeholder hex; pin.
- [ ] TS: the same inputs give the same hex; fails first; implement with noble; pass; commit both.

### Task 4: Approval steps

Produces `approve(api: Api, req: {hubID, code}, opts: {group, role}, onStep: (step: string) => void): Promise<void>`, where `Api` is `{get, post, put, patch, del}` over relative API paths returning parsed JSON or throwing `ApiError{status, message}`. An adapter over the app runtime's data engine.
- [ ] Tests first against an in-memory fake API (a small TS mirror of the fake central's behaviour for these endpoints): approves a waiting hub (user created with facility scope and role, group add, key entry created empty, restricted, then written, box opens with the hub's private key via a test `open`, registry gets `account` and keeps other fields); refuses a wrong code; refuses a changed facility; refuses a role with authorities; refuses an existing disabled, wider or authority-carrying user; re-approves a fit existing user by patching the password; refuses when the entry gained an `account` meanwhile; reports the failing step. Watch fail; implement; pass; commit.

### Task 5: Flags and visibility

Produces `flags(status: Heartbeat | undefined, now: Date): Flag[]` and `visible(hubPath: string, user: {paths: string[], superuser: boolean}): boolean`.
- [ ] Tests first for each flag row of the spec, a missing heartbeat, missing fields, a retired hub; paths at several levels and none. Fail; implement; pass; commit.

### Task 6: Data hooks and screens

- [ ] Hooks: `useMe`, `useHubs` (registry + status listings with `fields`, org units by id with path and ancestors' names, 404 → empty), `useHub(id)` (full status, issues filtered by key prefix), `useMoveKeys` (superuser), mutations for planned move.
- [ ] Screens per the spec. Approve dialog runs `approve` with the data engine adapter, listing steps.
- [ ] tsc, eslint, build; run against a broker instance with a real hub (manual check through Playwright screenshot); commit.

### Task 7: Live end to end

- [ ] `clients/facility_hub_app.py`: log in as a user, open the installed app, act as asked by arguments (approve hub with code; read move code for an address; list visible hub IDs), print JSON.
- [ ] `internal/live/hubapp_test.go` `TestFacilityHubApp`: build or find the app zip (skip with a message if pnpm or the zip is missing), install it (`POST /api/apps`), set up a hub, run the client as superuser to approve with `AccountCode()`, refresh the hub (takes up account), read the move code and compare with `MoveCode`, run as a district user of another district (hub not listed) and of the hub's district (listed). Fails first (no client); implement; pass on synthetic 2.42; commit.

### Task 8: Docs and final checks

- [ ] FACILITY-HUB-APP.md screens section, TESTING.md app tests, DEPLOYMENT.md status; README pointer.
- [ ] Full Go suite with race; app tests, lint, tsc, build; live synthetic 2.42 and 2.43 (and Sierra Leone 2.42) through hubtest; fresh review on the most capable model; fix pass.
- [ ] Push the branch into the shared repository as `facility-hub-app`; leave a broker instance with the app installed and demo hubs for the morning.
