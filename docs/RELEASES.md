# Releases and self-upgrade

Releases come from the supplier's GitHub releases (`dhis2-utils/poc-data-proxy`, tag `v<version>`), signed with an ed25519 key. The release source and the public key are built into the binary, not set in `hub.yaml`:

```bash
./hub keygen -out release        # release.key (the release workflow's secret), release.pub
go build -ldflags "-X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=$(cat release.pub)" ./cmd/hub
./hub release -key release.key -dir out -version 0.3.0 [-image dhis2-hub-0.3.0-arm64.img.xz] \
    linux/arm64=hub-linux-arm64 linux/amd64=hub-linux-amd64 windows/amd64=hub-windows-amd64.exe
```

**The version** is in `VERSION`: the next release, like an app's `package.json`. Every build reads it:
- a release is `VERSION` itself, tagged `v<VERSION>`; the release workflow refuses any other tag;
- CI builds (the Windows installer, macOS, a Pi image run by hand) are `<VERSION>-ci.<run number>`, which sort above the previous release and below this one, so each installs over the one before;
- a plain `go build` is `0.0.0-dev`, older than all of them.

After a release, the next commit raises `VERSION`.

**On GitHub**, `.github/workflows/release.yml` does this for a tag `v<version>` on a commit of the default branch (a tag elsewhere is refused):
1. tests;
2. builds `linux/arm64` and `linux/amd64` (`windows/amd64` joins with the Windows service);
3. builds and checks the Pi image (`image.yml`);
4. enters the GitHub environment `release` (tags `v*` only), and signs with its secret `RELEASE_SIGNING_KEY`;
5. publishes the release.

The signing key is a secret of the environment `release`, not of the repository, so a workflow on another branch cannot read it; only the publish job of a tag push enters that environment. The public key is the repository variable `RELEASE_PUBLIC_KEY`. Tags cannot be moved or deleted, so run the workflow by hand first: a dry run on the same commit, which publishes nothing and signs with a throwaway key (and the Windows installer with the test certificate), whatever secrets are set. Actions in the workflows are pinned by commit; to update one, take the commit of its new tag from `git ls-remote --tags https://github.com/actions/<name>`. The workflows build with the newest Go 1.26 patch (`go-version: '1.26'`, `check-latest`), not the 1.26.0 that `go.mod` names as the least version; a move to Go 1.27 changes both.

`out/` holds the release's assets: the binaries (`hub-linux-arm64`, …), the image if given, `release.json` (version, file names and SHA-256 per platform, the image) and `release.json.sig`. A build without a key never upgrades.

Each country chooses the version on central, in the Facility Hub app's "Hub version" page, which writes `facility-hub-admin/release`: `{"version": "0.3.0", "pilotHubs": [...], "percent": 10}`. After each online sync cycle a hub reads it; if the version is newer than its own, has not failed here before, and the hub is a pilot hub or its ID hashes under the percentage, it fetches that release's signed `release.json`, then:

1. downloads the binary in the background, beside the sync cycle, and verifies it against `release.json`;
2. at a later cycle, once the binary is there: pushes its outbox;
3. blocks tracker writes;
4. runs the new binary's migrations on a copy of the store;
5. swaps binary and store;
6. re-execs in place, handing the in-memory data key over a pipe so the store stays unlocked;
7. health-checks itself: the store and `/hub/status` (not central, so a slow or unreachable central does not roll a good release back).

If the health check fails, it restores the previous binary and store and re-execs again. The result (`ok`, `failed` or `ignored`) goes into the heartbeat as `lastUpgrade`.
