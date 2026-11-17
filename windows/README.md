# The hub's Windows installer

`build.sh` builds `hub.exe` for windows/amd64 and wraps it in an NSIS installer. Both are built on Linux and signed with `osslsigncode`. `hub.exe` has no console window; on Windows the hub logs to `hub.log` in its data folder.

```bash
sudo apt-get install nsis osslsigncode p7zip-full   # once
windows/build.sh 0.3.0 out/          # out/dhis2-hub-0.3.0-setup.exe and out/hub-windows-amd64.exe
windows/check.sh out/dhis2-hub-0.3.0-setup.exe
```

**Releases** never ship an installer signed with the test certificate (security review S3). Without the repository secrets `SIGN_CERT` and `SIGN_KEY` (PEM), a tagged release has everything but the installer. To ship one, build and sign it where the certificate is, and attach it to the release. That works on Linux or a Mac (`brew install makensis osslsigncode sevenzip`):

```bash
SIGN_CERT=cert.pem SIGN_KEY=key.pem windows/build.sh 0.3.0 out/
```

**Signing.** No supplier code-signing certificate exists yet. Without `SIGN_CERT` and `SIGN_KEY` (PEM files), `sign.sh` signs with a self-signed test certificate. It makes the certificate once in `~/.cache/hub-signing`. Windows does not trust it, so SmartScreen warns. The real certificate's key must sit in hardware or a cloud signing service (docs/DEPLOYMENT.md, "Signing"): `sign.sh` is the one place to change for that.

**What the installer does** (installer.nsi). It installs either for the whole PC (`/AllUsers`, needs administrator rights) or for the current user (`/CurrentUser`). `/S` makes it silent.

| | Whole PC | Current user |
|---|---|---|
| Data and `bin\hub.exe` | `C:\ProgramData\DHIS2 Facility Hub` (only the service and administrators) | `%LOCALAPPDATA%\DHIS2 Facility Hub` |
| Runs as | the service `DHIS2FacilityHub` (`NT SERVICE\DHIS2FacilityHub`), started at boot, restarted after a crash or an upgrade | the user, started at their login |
| Settings | `hub init`: free ports (443/80, else 8443/8080), `hub.yaml`, the certificate authority | the same |
| Trust | the authority in the PC's trusted roots; `dhis2.local` → `127.0.0.1` in `hosts` | the authority in the user's trusted roots (Windows asks) |
| Firewall | the two ports and UDP 5353 (mDNS), for `hub.exe` | none: Windows may ask |
| Also | keeping the PC awake on mains power, a Microsoft Defender exclusion (both optional), a desktop shortcut to `https://dhis2.local` | the shortcut |

The installer:
- never puts an older `hub.exe` over a newer one;
- when installing for the whole PC, offers to move the installing user's own hub, so it keeps its ID, approval and authority.

Uninstalling (Windows' Apps list) removes the service, the firewall rules, the authority and the `hosts` line. It keeps the data unless "Remove the hub's data" is ticked, and the data folder holds the program (`bin\hub.exe`), since the hub upgrades itself there. An install over kept data never puts an older program over a newer one, which may have migrated the store: it keeps the newer program, says so in `install.log`, and shows that version in the Apps list.

No elevated step runs the program in the data folder, which the service's account may change (review R-27): the installer uses its own copy, and the uninstaller stops and removes the service with Windows' own tools.

Untested until a Windows machine runs it: see docs/TESTING.md, "Windows".
