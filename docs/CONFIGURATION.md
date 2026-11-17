# Configuration

The hub has no facility-specific configuration. It reads one small image-defaults file, the same for every hub in a country (spec 6.1). See [`deploy/hub.example.yaml`](../deploy/hub.example.yaml):

| Key | Meaning |
|---|---|
| `central_url` | Central DHIS2 base URL; optional. Without it (the generic image), set-up asks for central's address and the setup code first (in `facility-hub/hub.txt` on the USB stick, or `data_dir/setup-token`), checks the server (https, a trusted certificate) and shows its name before anyone logs in. With it and without `central_hosts`, set-up always uses it |
| `central_hosts` | Hosts setup may point a hub at (`hmis.example.org, *.hmis.example.org`); setup then asks for an `https://` URL on them. With neither key, setup needs the token in `data_dir/setup-token` (printed on the label) |
| `network` | `networkmanager` (the Pi image): the hub manages its network through NetworkManager, with the setup Wi-Fi and case C (`internal/netsetup`). Empty (default): the machine's network as it is |
| `mdns` | `true`: the hub answers `dhis2.local` itself (Windows, macOS; the Pi image has avahi). Default `false` |
| `setup_local_only` | `true`: set-up only from the machine itself, and no setup code (Windows, macOS: being at the PC proves presence at the facility). Default `false` |
| `uplink_switch` | `true`: "Simulate uplink cut" on the admin page, for demonstrations. Default `false`: in the field a hub that looks offline lets staff type in a new central |
| `tpm` | Seal the hub's device key with the machine's TPM 2.0 where it has one (`internal/devkey`). Default `true`. `false` keeps a new hub off a TPM that misbehaves; it cannot take a hub that already has `device.key` off its TPM, whose sealed files then cannot be opened |
| `password_fallback` | Store a user's central password, sealed, when a PAT cannot be created (spec 7.1). Default `false` |
| `listen` | Listen address, e.g. `:8443`; `:443` on the image, for `https://dhis2.local` |
| `http_listen` | Plain-HTTP address for the start page and the hub's certificate (`:80` on the image, for `http://dhis2.local`); off when empty (the default). Every other request there gets the start page, which moves on to HTTPS once the device trusts the hub |
| `data_dir` | Store (`hub.db` key file, `hub.db.data` encrypted data file), TLS certificates (the hub's authority `ca.crt` for devices, also served at `/hub/ca.crt`, with its key `ca.key`; the server's `tls.crt` and `tls.key`, renewed by the hub), request log, upgrade files |
| `sync_interval`, `ping_interval`, `ping_timeout` | Sync cadence (default 15m, with ±10 % jitter) and connectivity probe (15s while offline, 60s while online; 5s timeout; spec 5.2) |
| `reconnect_delay` | After an outage, wait up to this long at random before syncing, so hubs do not reach central together (default 5m). Until the outbox is pushed, the hub keeps answering tracker and aggregate requests itself |
| Retention by program and tracked entity type | Central's dataStore key `facility-hub-config/retention`, read by every hub: `{"defaultMonths": 0, "programs": {"<program>": 12}, "trackedEntityTypes": {"<type>": 6}}`. Months of data kept, 0 or no entry keeps everything. Event programs keep events by event date. Tracker programs keep active enrollments in full and closed ones until their last change is older than the window, then as hidden stubs (for the "enrol once" check). Tracked entity types have no default: people are kept unless their type is named; an entity of a type with a window goes when it is old and has nothing left in full |
| `access_refresh_interval` | How often each user's access list is re-read from central, besides at login (default 1h) |
| `pat_expiry_days` | Lifetime of the personal access tokens the hub creates |
| `tls` | `true` (default): at first boot the hub creates a CA limited to its own names and private addresses, issues its server certificate, and keeps the CA's key (`ca.key`) to renew it; both private keys are sealed with the device key where the machine has a TPM. `false`: plain HTTP for development |
| `reserved_pool_size` | Default reserved values kept per generated attribute (default 500); the Facility Hub app can override it per attribute in `facility-hub-config/reserved` (an explicit 0 there turns an attribute's offline pool off). At most 5000 per attribute, here and in the app. A pool refills only when it falls below a quarter of its target |
| `usb_dir` | Where the box's USB stick is mounted (none by default; the image uses `/media/hub-usb`). Before set-up the hub writes its setup code there; after, each minute, what a restore needs: the pending saves, the key file and the hub's state (docs/RECOVERY.md, C2). Used only when it is a mount point |
| `full_pull_interval` | How often a full, non-incremental pull runs per access group (default 168h, a week). A changed access list gives a full pull at the next sync anyway |
| `ownership_check_interval` | How often the hub checks which of its people the facility still owns, to catch transfers out (default 1h) |
| `offline_search_facility` | Answer offline searches with `orgUnitMode=ACCESSIBLE` from the facility's data instead of refusing them (default `true`; FINDINGS.md) |
| `log_requests` | Log every request (method, path with IDs replaced, parameter names, status, time) to `hub.log` and append it to `data_dir/requests.log` (spec 13.1). No parameter values: they can name a patient |
| `hub_hosts` | Extra host names clients may use for the hub (comma separated; no wildcard). The hub answers 421 to any other `Host`; its own names and addresses are always accepted |
| `session_idle`, `session_max` | End a hub session after this long without requests (default 1h) or after login (default 12h) |
| `key_slot_max_age` | Remove the key slot of a user who has not logged in for this long (default 2160h, 90 days); the three most recently used slots are always kept |
