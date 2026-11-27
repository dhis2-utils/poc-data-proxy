# Facility hub (proof of concept)

> ![Maturity: Experimental](https://img.shields.io/badge/maturity-Experimental-orange)  
> Intended use: PoC hub that temporarily forwards/stores data of all users in one facility while offline, allowing share of data locally.  
> Maintainers: HISP Centre implementation team.

A small device on a facility's network that keeps working copies of the facility's DHIS2 data. While the internet is down, staff keep using DHIS2 through the hub, in Capture web, the Aggregate Data Entry app and eRegisters, and share their work with each other at the facility. When the internet is back, the hub sends their changes to the central DHIS2 server, each as its author. Each user sees only what central lets them see, online and offline.

**Status: an experiment.** It is being tested with country teams on test servers. Do not use it with production data or let patient care depend on it. What works offline and what does not is in [SUPPORTED.md](docs/SUPPORTED.md); what is still open, known weaknesses included, in [PENDING.md](docs/PENDING.md).

- DHIS2 **2.42 and 2.43**.
- Runs on a **Raspberry Pi** 4 or 5 (a ready image), a **Windows** PC (an installer, not yet attached to releases: it awaits a code-signing certificate), or any Linux machine.
- Releases: [GitHub releases](https://github.com/dhis2-utils/poc-data-proxy/releases). A hub upgrades itself to the version chosen on central, and checks each release's signature first.

## Trying it

- **On a Raspberry Pi:** [docs/user/SETUP-PI.md](docs/user/SETUP-PI.md), step by step, from flashing the image to testing offline.
- **On a Windows PC:** [docs/WINDOWS-INSTALL.md](docs/WINDOWS-INSTALL.md).
- **On central:** the Facility Hub app (`facility-hub-<version>.zip` in each release, installed in App Management) lists the hubs, approves their accounts and chooses their version ([FACILITY-HUB-APP.md](docs/FACILITY-HUB-APP.md)).

## How it works

- Online, every request passes through to central as the user, and the hub keeps copies on the way: each user's metadata, and the facility's tracker and aggregate data, pulled every 15 minutes.
- Offline, the hub answers from those copies, with central's access rules: logins (for users who logged in once online), working lists, searches, registrations, events and aggregate forms.
- Changes made offline wait in the hub's outbox, encrypted on its disk, with a copy on a USB stick, and go to central when it is back. What central refuses is kept and reported to its author and on central.
- Devices reach the hub at `https://dhis2.local`, with a certificate from the hub's own authority, installed once per device.

A short overview for decision-makers and implementers: [OVERVIEW.md](docs/OVERVIEW.md). The rollout proposal: [DEPLOYMENT.md](docs/DEPLOYMENT.md). What building it taught us about DHIS2: [FINDINGS.md](docs/FINDINGS.md).

## Documents

**For country teams**

| Document | What it covers |
|---|---|
| [user/SETUP-PI.md](docs/user/SETUP-PI.md) | Setting up and testing a hub on a Raspberry Pi, step by step |
| [WINDOWS-INSTALL.md](docs/WINDOWS-INSTALL.md) | Installing the hub on a Windows PC |
| [SUPPORTED.md](docs/SUPPORTED.md) | What works offline, per app, and what needs a connection |
| [FACILITY-HUB-APP.md](docs/FACILITY-HUB-APP.md) | The Facility Hub app on central |

**The design**

| Document | What it covers |
|---|---|
| [OVERVIEW.md](docs/OVERVIEW.md) | The whole picture in about eight pages: what the hub does, rollout, running hubs, security, what is tested and what is open |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | How the hub works inside, for developers: routing a request, logins and tokens, tracker and aggregate data, the outbox, the store, the sync cycle, the code map |
| [SECURITY-DESIGN.md](docs/SECURITY-DESIGN.md) | The threat model, encryption at rest, who sees what, the network, accounts and tokens on central, not losing data, and the known gaps |
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | The rollout proposal: who gets a hub, hardware, the image, set-up, networks, reaching the hub securely, pointing devices at the hub |
| [OPERATIONS.md](docs/OPERATIONS.md) | Running the fleet: monitoring, refused changes, upgrades, support, central's time zone, security tasks on central |
| [RECOVERY.md](docs/RECOVERY.md) | Keeping unsent data safe: backup and recovery |
| [CONNECTIVITY.md](docs/CONNECTIVITY.md) | How facilities are connected, and what that means for the hub |
| [design/](docs/design/) | The design of each larger feature, as built (the code cites them); studies such as a hub without a real-time clock and two-factor login; deployment options not decided or not chosen |

**What we learned, and what is open**

| Document | What it covers |
|---|---|
| [FINDINGS.md](docs/FINDINGS.md) | What building and testing the hub taught us about DHIS2 and the Capture apps, and the hub's bugs found and fixed |
| [PENDING.md](docs/PENDING.md) | What is open: decisions, work, runs, known weaknesses, ideas under consideration |
| [PERFORMANCE.md](docs/PERFORMANCE.md) | How the hub scales, the load on central, and sizing |

**Testing and development**

| Document | What it covers |
|---|---|
| [TESTING.md](docs/TESTING.md) | How the hub is tested: profiles, the live suite against real DHIS2, the oracles |
| [ADVERSARIAL-TESTING.md](docs/ADVERSARIAL-TESTING.md) | The worst cases tested, and what must hold in each |
| [HARDWARE-TESTING.md](docs/HARDWARE-TESTING.md) | Testing on Raspberry Pis |
| [DEVELOPMENT.md](docs/DEVELOPMENT.md) | The code's layout, building, running, testing, the settings, and releases |
| [AGENTS.md](AGENTS.md) | How to change the code: keep the structure, guards, checks before committing |

The original brief is [SPEC-facility-hub-poc.md](docs/SPEC-facility-hub-poc.md); code comments cite its sections ("spec 9.2"), and FINDINGS.md says where it turned out wrong. Reviews (`docs/reviews/`), which the code also cites, and the test sessions' records are kept in a private notes repository.

## Building and testing

You need Go 1.26 or newer, and no C toolchain:

```bash
go build -o hub ./cmd/hub
go test ./...
```

Details, cross-compiling and the code's layout: [DEVELOPMENT.md](docs/DEVELOPMENT.md).

## License

BSD 3-Clause. Copyright (c) 2026, University of Oslo. See [LICENSE](LICENSE).
