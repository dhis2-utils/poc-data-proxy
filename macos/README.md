# The hub on a Mac

For the DHIS2 team to try the hub on their own Macs, against a test DHIS2: the same hub, app, staging and upgrades as in the field. It isn't meant for facilities, which run a Pi kit or a Windows PC (docs/DEPLOYMENT.md).

```bash
macos/install.sh                    # the latest release from GitHub (checked against its checksums)
macos/install.sh --hub ./hub        # a hub you built: CGO_ENABLED=0 GOOS=darwin go build -o hub ./cmd/hub
macos/install.sh --uninstall [--remove-data]
```

**What it does:**
- The hub runs as you, started at login and restarted if it stops (a launchd agent, `~/Library/LaunchAgents/org.dhis2.facility-hub.plist`).
- It runs from `~/Library/Application Support/DHIS2 Facility Hub/bin`, so it can replace itself when it upgrades. The script never puts an older version over a newer one.
- `hub init` chooses the settings:
  - free ports (443/80 when free; macOS lets any user bind them);
  - `dhis2.local`, answered by the hub itself;
  - set-up only on this Mac;
  - no USB stick copy.
- It trusts the hub's certificate authority in the System keychain (your password, once). The authority is limited to the hub's own names and local addresses, so it can't vouch for any other site. `--uninstall` removes it.

Open the address the script prints (`https://dhis2.local`, with a port if 443 was taken) to set the hub up. The log is `hub.log` in the data folder.

**macOS warnings:**
- A binary downloaded by the script isn't quarantined, so it runs.
- A binary you download in a browser is quarantined: the script clears that flag on its copy.
- Nothing is signed with an Apple Developer ID; it would only matter for a `.pkg` installer, which there isn't one of.

**Tested by** `.github/workflows/macos.yml` on every push:
- the Go tests on macOS;
- the install, checked: the agent runs and answers over HTTPS at `localhost` and `dhis2.local`, and the authority is trusted;
- the uninstall.
