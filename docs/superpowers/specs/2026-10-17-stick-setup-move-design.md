# USB stick, setup code and central moves

Status: building 2026-10-17. The decisions are in docs/DEPLOYMENT.md (sections 1, 3 and 6) and docs/RECOVERY.md (option C2); this note records the technical choices.

## USB stick (RECOVERY.md C2)

- **Where:** `usb_dir` in `hub.yaml`, default `/media/hub-usb`. The image mounts the box's stick there (ext4). The hub uses it only when it is a mount point (another device than its parent), so a missing stick never fills the hub's own disk. Tests turn the mount check off.
- **Before set-up:** while the hub is in set-up mode, it writes `facility-hub/setup-code.txt` (the code and the hub ID) to any stick present, checking every minute. The code stays the same across reboots until set-up uses it.
- **Every minute, if anything is new:** the key file, the hub's state (users and their tokens, settings, catalog, hub token; sealed) and each pending save (sealed), each when it changed. No snapshot of the cached data: central sends it again (decided 2026-10-17).
- **Restore:** explicit, on the new disk's setup page, with a staff member's password checked against the copy; the hub restarts as the same hub with a new data file, puts back state and saves at the first unlock, and pulls the rest.
- **Health,** on the admin page and in the heartbeat (`usb`): present, writable (a write-and-read-back each minute), last copy, free space, last error.

## Setup code and central's address (DEPLOYMENT.md section 3)

- The setup page asks first for central's address and the setup code (from `setup-code.txt` on the stick). The address can be typed, or scanned from the installation card's QR code, where the browser can scan (the BarcodeDetector API).
- The hub accepts only `https` with a certificate the system trusts. Before anyone logs in, it shows the server's own name (`/api/loginConfig`, which needs no login) and host.
- `hub.yaml` needs no `central_url`: without it the hub asks at set-up. The pinned and allow-list modes stay for test and pilot set-ups.

## Central moves (DEPLOYMENT.md section 6)

Revised: the documented "registry secret" check cannot run before sending anything, because reading the dataStore needs a login. What is built:

1. **Announced:** `facility-hub-config/central-move` = `{"url": "https://new.example.org"}`, set by the national team. Hubs read it from the current central with their token, so the current central vouches for it. A hub switches when the current central stops answering and the new one answers with this hub's registry entry.
2. **Redirect:** the current address, reached over verified TLS, answers with a permanent redirect (301 or 308) to another host. The hub checks the new host as above, then switches.
3. **Typed in** (unplanned moves): checked with the hub's own account (`hub approve`), which reads the hub's move secret from the typed address; built 2026-10-17. After review: only after a real outage of `central_move_after`, with the staff member's password checked offline; a failed check locks typed-in moves until the hub has reached central again and replaced its account's password, token and move secret (which it also does right after taking the account up).

A hub that misses all of them keeps working offline, with its data.
