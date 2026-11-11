# Facility Hub app

The DHIS2 app on central for the facility hubs: their state, approving their own
accounts, and central moves. What it must match on the hub's side is in
[docs/FACILITY-HUB-APP.md](../../docs/FACILITY-HUB-APP.md); the design is
`docs/superpowers/specs/2026-10-18-facility-hub-app-design.md`.

```sh
pnpm install
pnpm test                    # codes, sealing, approval steps, flags, visibility
pnpm exec eslint src && pnpm exec tsc --noEmit
pnpm start --proxy https://dhis2.example.org   # develop against an instance
pnpm build                   # build/bundle/facility-hub-<version>.zip for App Management
```

Users need the authority `M_facilityhub` to open it; hub managers (members of the
group named in Settings) and superusers also see Approvals and Central moves.
Setting up hub managers: docs/FACILITY-HUB-APP.md, "Hub managers". `scripts/demo-facility-hubs.py` adds demo hubs to a test
instance. The live test is `TestFacilityHubApp` (`internal/live`), which installs the
built zip.
