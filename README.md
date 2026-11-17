# Facility hub (proof of concept)

> ![Maturity: Experimental](https://img.shields.io/badge/maturity-Experimental-orange)  
> Intended use: PoC hub that temporarily forwards/stores data of all users in one facility while offline, allowing share of data locally.  
> Maintainers: HISP Centre implementation team.

A small device on a facility's network that keeps working copies of the facility's DHIS2 data. While the internet is down, staff keep using DHIS2 through the hub, in Capture web, the Aggregate Data Entry app and eRegisters, and share their work with each other at the facility. When the internet is back, the hub sends their changes to the central DHIS2 server, each as its author. Each user sees only what central lets them see, online and offline.

**Status: an experiment.** It is being tested with country teams on test servers. Do not use it with production data or let patient care depend on it. What works offline and what does not is in [SUPPORTED.md](docs/SUPPORTED.md); what is still open, known weaknesses included, in [PENDING.md](docs/PENDING.md).

- DHIS2 **2.42 and 2.43**.
- Runs on a **Raspberry Pi** 4 or 5 (a ready image), a **Windows** PC (an installer), or any Linux machine.
- Releases: [GitHub releases](https://github.com/dhis2-utils/poc-data-proxy/releases). A hub upgrades itself to the version chosen on central, and checks each release's signature first.

## Trying it

- **On a Raspberry Pi:** [docs/user/SETUP-PI.md](docs/user/SETUP-PI.md), step by step, from flashing the image to testing offline.
- **On a Windows PC:** [docs/WINDOWS-INSTALL.md](docs/WINDOWS-INSTALL.md).
- **On central:** the Facility Hub app (`apps/facility-hub`) lists the hubs, approves their accounts and chooses their version ([FACILITY-HUB-APP.md](docs/FACILITY-HUB-APP.md)).

## How it works

- Online, every request passes through to central as the user, and the hub keeps copies on the way: each user's metadata, and the facility's tracker and aggregate data, pulled every 15 minutes.
- Offline, the hub answers from those copies, with central's access rules: logins (for users who logged in once online), working lists, searches, registrations, events and aggregate forms.
- Changes made offline wait in the hub's outbox, encrypted on its disk, with a copy on a USB stick, and go to central when it is back. What central refuses is kept and reported to its author and on central.
- Devices reach the hub at `https://dhis2.local`, with a certificate from the hub's own authority, installed once per device.

The design and the choices behind it: [DEPLOYMENT.md](docs/DEPLOYMENT.md). What building it taught us about DHIS2: [FINDINGS.md](docs/FINDINGS.md).

## Documents

| Document | What it covers |
|---|---|
| [SPEC-facility-hub-poc.md](docs/SPEC-facility-hub-poc.md) | The original specification, as it was asked |
| [SUPPORTED.md](docs/SUPPORTED.md) | What works offline through the hub, per app, and the endpoints and parameters it answers offline (generated from the code) |
| [FINDINGS.md](docs/FINDINGS.md) | What we learned about DHIS2, the Capture apps and the spec: answers to its open questions, where it was wrong, what the tests found |
| [PENDING.md](docs/PENDING.md) | What is open: decisions, work to build or fix, runs on hardware and against DHIS2, and ideas under consideration |
| [TESTING.md](docs/TESTING.md) | How the hub is tested: profiles, the live suite against real DHIS2, the differential oracles, phases |
| [PERFORMANCE.md](docs/PERFORMANCE.md) | How the hub scales: current results on Pi-class profiles, load on central, sizing, history |
| [HARDWARE-TESTING.md](docs/HARDWARE-TESTING.md) | Testing on real Raspberry Pis (Pi 4 1 GB, Pi 5 4 GB, SD card and SSD): set-up, calibration, ladder, full downloads, bytes written, power cuts |
| [ADVERSARIAL-TESTING.md](docs/ADVERSARIAL-TESTING.md) | Worst cases run against a real DHIS2 (central, network, power, users changing under offline work): each scenario and what must hold; results in FINDINGS.md |
| [INCREMENTAL-SYNC-FIX.md](docs/INCREMENTAL-SYNC-FIX.md) | Incremental syncs under load: the measured problem and its fix (built 23 Oct 2026) |
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | Rolling out hubs: hardware, image, setup at the facility, networks, reaching the hub securely, running the fleet; options under consideration |
| [FACILITY-HUB-APP.md](docs/FACILITY-HUB-APP.md) | The Facility Hub app on central: its screens, and what it must match on the hub's side (approving a hub's own account, move codes) |
| [RECOVERY.md](docs/RECOVERY.md) | Keeping unsynced data safe: backup and recovery options (ideas, not decisions) |
| [AGENTS.md](AGENTS.md) | How to change the code: keep the structure, guards, checks before committing. Reviews (`docs/reviews/`), which the code cites by name, are kept in a private notes repository |
| [CONFIGURATION.md](docs/CONFIGURATION.md) | The hub's settings (`hub.yaml`) and the dataStore keys it reads on central |
| [RELEASES.md](docs/RELEASES.md) | Versions, the release workflow, signing, and how a hub upgrades itself |
| [DEVELOPMENT.md](docs/DEVELOPMENT.md) | The code's layout, building, running and testing |

## Building and testing

You need Go 1.26 or newer, and no C toolchain:

```bash
go build -o hub ./cmd/hub
go test ./...
```

Details, cross-compiling and the code's layout: [DEVELOPMENT.md](docs/DEVELOPMENT.md).

## License

BSD 3-Clause. Copyright (c) 2026, University of Oslo. See [LICENSE](LICENSE).
