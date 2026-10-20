# Device secret, token revocation, settings in the app, and loose ends

Date: 2026-10-20. Status: decided with the user (choices below), to build.

## Why

The two security reviews of October left three gaps the hub could close itself:

- a stolen disk lets the thief guess staff passwords offline and impersonate `dhis2.local` (DEPLOYMENT.md, "What is not encrypted");
- a stolen hub's tokens cannot be revoked by anyone at central;
- country settings live in the Datastore Manager, and refusals can only be retried one at a time, by their author.

Plus the review's S13 and S14, and 17 minor findings deferred from the Pi and Windows reviews.

### Measured: tokens outlive their user on DHIS2 2.42.6

Tested on 2.42.6 (2026-10-20, `agent-facility-hub-demo`):

- Disabling a user, changing their password or expiring their account does **not** stop their personal access tokens. They keep working for reads and writes.
- A superuser cannot list, read or delete another user's token (404).
- The owner can share a token (`POST /api/sharing?type=apiToken&id=…`). A plain member of a user group it is shared with (`rw------`) can then delete it, and the token stops working at once (401). Sharing given in the create request is ignored.
- Listing (`GET /api/apiToken`) shows only one's own tokens, even shared ones: the deleter must know the ID.

2.43 is to check. FINDINGS.md records this. Until then the hub's own rules (the 5-minute Basic re-check) keep a disabled user out *through the hub*. Their tokens still work *against central* for whoever holds them.

## 1. Device secret (TPM 2.0), optional

**Decided:** used where present; never required; no unlock without a person.

- **Where:** Linux through `/dev/tpmrm0` (a Pi with a TPM board, x86 PCs); Windows through TBS. macOS and machines without a TPM work as today.
  - The Pi image enables the common SPI TPM overlay and gives the hub's user the `tss` group. Without a TPM board nothing appears.
- **The device key:** 32 random bytes made at the first start that finds a TPM. They are sealed by the TPM under its storage primary key (owner hierarchy, no password, no PCR policy), and the sealed blob is kept as `device.key` in the data folder. It opens only on that TPM.
  - No PCR policy: the Pi has no measured boot, and on Windows a firmware update would change PCR 7 and lock the hub out.
  - So the device key protects a disk or data folder taken **without** its TPM, not a stolen whole device. DEPLOYMENT.md says so.
- **What it protects:**
  1. **Secrets the hub needs before anyone logs in:**
     - `tls.key` and `ca.key`;
     - `setup-token`;
     - `setup-wifi` and `network.json`.
     
     These are written as `HUBDEV1` + AES-GCM under the device key, with the file name as AAD. The hub reads either form. A plain file found with a device key present is rewritten sealed at start. `tls.crt` and `ca.crt` stay plain: they are public.
  2. **Password key slots on the disk:** the slot's key is `HKDF(Argon2id(password) ‖ device key)`, so a disk taken alone gives no offline guessing.
     - A slot is bound at its user's next login. Old slots keep working until then.
     - The USB stick keeps password-only slots, so a hub can be restored on new hardware. The unbound wrap of each slot is kept *inside* the data file (sealed under the data key), never on the disk in the clear. The stick's `keys.db` is built from those wraps.
     - Restoring from the stick on a device with a TPM binds the slots again.
- **If the TPM fails or the disk moves to another machine:** the disk's slots and sealed files cannot be opened. The hub says so on its page ("This hub's security chip is missing or changed: restore it from its USB stick"), and the restore from the stick works as for a dead disk. Without a stick, unsent saves are lost. That is the price, and DEPLOYMENT.md states it.
- **Not built:** unlocking at boot without a person (RECOVERY.md option I). Decided against: the first login after a restart, by any client, unlocks the hub, and Android does it unasked.
- **Code:**
  - `internal/devkey` (TPM through `github.com/google/go-tpm`, plus a fake for tests);
  - `devkey.ReadFile`/`WriteFile` for the sealed files;
  - `hubcrypto` with a bound key derivation;
  - `store` with a `bound` column and the unbound wraps;
  - `stick` builds `keys.db` from the unbound wraps.
- **Tests:**
  - the fake device in unit and e2e tests (bound slots; the disk without its device; restore on another device);
  - the real TPM code against `swtpm` where available (CI on Linux);
  - Windows TBS on the VM checklist.

## 2. Revoking a hub's tokens

- After creating any token (a user's, or its own), the hub shares it with the hub managers group from `facility-hub-admin/accounts` (`rw------`). It lists the IDs of its live tokens in its registry entry (`tokens: [uid…]`). Token IDs are not secrets.
- **Revoke all tokens of this hub** on the hub page of the Facility Hub app (managers):
  - deletes each listed token;
  - sets the registry entry's status to `revoked`, with `revokedBy` and `revokedAt`;
  - says how many tokens it could not delete (made before this release and not yet renewed, or already gone);
  - tells the manager to have the hub's users change their passwords.
- **A revoked hub that connects again:** it stops serving offline data as a retired one does, deletes the tokens it still holds and pushes nothing more. Its unsent saves stay on its disk.
- Tokens made by older hubs are covered once they are renewed (renewal happens with a week left).

## 3. Undoing a mistaken replacement

On the page of a hub retired by a replacement (`retiredBy` = a hub ID), managers get **Undo this replacement**:

- **The old entry:** goes back to `active`, keeping `retiredBy` etc. as history under `undone: {by, at, retiredBy}`.
- **The new hub:** retired with `retiredBy: "undo"`, `retiredByUser` and `retiredAt`.

Both hubs pick it up within one cycle. Their housekeeping already reads only `status`.

## 4. Settings and bulk retries in the app

- **Settings page (managers).** It writes with the same sharing as `facility-hub-admin` entries (public read, managers write). Missing keys mean today's defaults.
  - **Tracker retention** (`facility-hub-config/retention`): months by default, by programme, by person type.
  - **Aggregate retention** (`facility-hub-config/aggregate`): periods by default and by data set.
  - **Token lifetime** (`facility-hub-config/tokens`, `{expiryDays}`):
    - read hourly with the catalog;
    - kept within 7–365 days;
    - `hub.yaml`'s `pat_expiry_days` stays the fallback;
    - new and renewed tokens take it, and a token with more life left than the setting is renewed early.
- **Refusals page (managers).** It adds up the heartbeats' `refusedGroups` (kind, programme, error code) across hubs.
  - **Resend** on a group appends `{id, kind, program, code, at, by}` to `facility-hub-admin/retries` (the last 50 kept).
  - At each cycle a hub applies each retry it has not applied yet: its refused saves of that kind, programme and code go back to pending, and are pushed as their authors. Central's new answer stands; a save refused again is a fresh issue.

## 5. S13 and S14

- **S13:** the release check never downloads inside the sync cycle. It starts one background download of the chosen binary to `bin/<name>.part` (idle timeout as now, no total cap), checks it against the signed manifest and renames it. A later cycle that finds the verified file upgrades.
- **S14:** `Hub.Unlock` locks the store again on every error path.

## 6. The deferred minors

**Pi image and releases:**
1. The setup Wi-Fi retries with backoff (10 s doubling to 10 min).
2. Versions compare by semantic-versioning precedence (`0.3.0-rc1` < `0.3.0`). The app's Hub version page leaves pre-releases out.
3. "Answer every name" is removed at start when the setup Wi-Fi is not running.
4. A firewall restart keeps NetworkManager's sharing: the hub's table is replaced by itself, not with `flush ruleset`.
5. `100.64.0.0/10` counts as a local network (firewall and private-address checks).
6. A USB stick with no partition table is mounted.
7. Release workflow: published as a draft, files uploaded, then made public.
8. The image is built from the release's own binary.
9. Workflow inputs reach shell commands only through environment variables.
10. The chrony comment no longer promises an RTC.

**Windows:**

11. Firewall rules limited to `LocalSubnet`. On a "Public" network that still reaches devices on the same LAN.
12. The service accepts pre-shutdown and reports progress while stopping.
13. The last-resort port pick cannot return the same port twice.
14. Paths in `hub.yaml` are quoted.
15. The per-user upgrade waits for the old process to release its ports.
16. The uninstaller writes `hosts` through a temporary file and keeps the original if the rename fails.
17. The installer pre-selects "for anyone who uses this computer" when the user can elevate. IPv6 link-local addresses count as the PC itself.

Each is fixed with a test where one can show it, or listed in the handover as checked by hand.

## 7. Afterwards: a KISS review

A fresh reviewer reads the whole codebase against AGENTS.md: mechanisms without a measured problem, duplicated paths, dead code, files to split or merge. It writes `docs/reviews/KISS-<date>.md` for the user, and nothing changes because of it before the user has read it.
