# USB stick, setup code and central moves

Status: building 2026-10-17. The decisions are in docs/DEPLOYMENT.md (sections 1, 3 and 6) and docs/RECOVERY.md (option C2); this note records the technical choices.

## USB stick (RECOVERY.md C2)

- **Where:** `usb_dir` in `hub.yaml`, default `/media/hub-usb`. The image mounts the box's stick there (ext4). The hub uses it only when it is a mount point (another device than its parent), so a missing stick never fills the hub's own disk. Tests turn the mount check off.
- **Before set-up:** while the hub is in set-up mode, it writes `facility-hub/setup-code.txt` (the code and the hub ID) to any stick present, checking every minute. The code stays the same across reboots until set-up uses it.
- **Every minute, if anything is new:**
  - a copy of the key file (key slots, identity, settings: small), written to a temporary file and renamed;
  - each offline save not yet copied: its outbox entry, still sealed with the data key, one file per entry.

  Copying runs on its own timer, never in a client's save.
- **Daily** (`usb_snapshot_interval`, default 24 h): a snapshot of the whole store (the encrypted copy the hub already writes before upgrades), written to a temporary folder and renamed. Saves it contains are then dropped from the per-minute copies. The stick needs room for two snapshots while one is being written.
- **Restore:** a hub that starts with no store, and finds a snapshot on the stick, takes it with the newer key file copy. It starts as the same hub (same ID, same key slots), locked. At the first staff login it puts back the saves copied after the snapshot, as pending changes, and pushes them. Records central changed since come back with the next pull.
- **Health,** on the admin page and in the heartbeat (`usb`): present, writable (a write-and-read-back each minute), last copy, last snapshot, free space, last error.

## Setup code and central's address (DEPLOYMENT.md section 3)

- The setup page asks first for central's address and the setup code (from `setup-code.txt` on the stick). The address can be typed, or scanned from the installation card's QR code, where the browser can scan (the BarcodeDetector API).
- The hub accepts only `https` with a certificate the system trusts. Before anyone logs in, it shows the server's own name (`/api/loginConfig`, which needs no login) and host.
- `hub.yaml` needs no `central_url`: without it the hub asks at set-up. The pinned and allow-list modes stay for test and pilot set-ups.

## Central moves (DEPLOYMENT.md section 6)

Revised: the documented "registry secret" check cannot run before sending anything, because reading the dataStore needs a login. What is built:

1. **Announced:** `facility-hub-config/central-move` = `{"url": "https://new.example.org"}`, set by the national team. Hubs read it from the current central with their token, so the current central vouches for it. A hub switches when the current central stops answering and the new one answers with this hub's registry entry.
2. **Redirect:** the current address, reached over verified TLS, answers with a permanent redirect (301 or 308) to another host. The hub checks the new host as above, then switches.
3. **Typed in** (unplanned moves): waiting for the user's decision.

A hub that misses all of them keeps working offline, with its data.
