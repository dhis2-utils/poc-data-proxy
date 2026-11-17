# Setting up a facility hub on a Raspberry Pi

Step-by-step instructions for setting up and testing a hub on a Raspberry Pi 5, written for a country team testing the hub. The full design, and why each step is there, is in [DEPLOYMENT.md](../DEPLOYMENT.md), section 3.

The hub is a test build. Set it up against your own DHIS2 test instance, not the general DHIS2 demo server and not production. Do not leave patient work depending on it yet.

## What you need

- A Raspberry Pi 5 with its power supply, and an SSD (NVMe on an M.2 HAT, or USB 3). An SD card works for a test, but wears out.
- A USB stick, FAT or exFAT, empty. It stays in the hub.
- An Ethernet cable to a router with internet. (A hub can also run without one; see "No cable" below.)
- A computer to flash the disk, with [Raspberry Pi Imager](https://www.raspberrypi.com/software/).
- A laptop, or a phone with a USB adapter, that can read the USB stick.
- The image: `dhis2-hub-<version>-arm64.img.xz`, sent to you by the DHIS2 team.
- Your DHIS2 test instance ("central" below):
  - DHIS2 2.42 or 2.43, with your configuration (programs, data sets, eRegisters);
  - reachable from the hub's network over `https`, with a publicly trusted certificate (Let's Encrypt is fine; a self-signed one is refused);
  - a superuser for the one-time steps below;
  - test users assigned to the test facility, without two-factor login (a user with 2FA cannot use the hub yet, docs/DEPLOYMENT.md, appendix B), and not superusers.

## 1. Central, once

Do this once on your test instance. Later hubs on the same instance skip it.

1. Install the Facility Hub app (`facility-hub-<version>.zip`, from the same release as the image) in App Management.
2. Give the authority `M_facilityhub` (the app) to the roles of the people who manage hubs.
3. Create three user groups, for example "Hub owners", "Hub managers" and "Hub accounts", and a user role for the hubs' own accounts.
4. As a superuser, open the Facility Hub app, Settings, and name the groups and the role. Save. A hub refuses set-up until the hub owners group is named.
5. Add the person who will set the hub up to the hub owners group. They must also be assigned to the facility.

Check central's time zone (System Settings, or `serverTimeZoneId` in `/api/system/info`). The hub takes it, and its nightly update restart is at 03:00 in that zone.

## 2. Flash the disk

1. Open Raspberry Pi Imager. Device: Raspberry Pi 5. Operating system: "Use custom", and pick the `.img.xz` file. Storage: the SSD (or SD card).
2. When the Imager asks about OS customisation, choose **No**. The image has no user, no SSH and no Wi-Fi settings to fill in; everything is set up from the hub's own page.
3. Write. The Imager verifies the disk afterwards.

**Re-using a Pi that was a hub before** (such as the one from the 13 November demo): if it still works, first open its admin page, log in, and use **Reset hub** at the bottom (type `RESET`). That sends what it holds, retires it on central and clears its folder on the USB stick. If it no longer works, delete the folder `facility-hub` from its USB stick on a computer instead, or use a new stick; otherwise the new hub offers to restore the old one.

## 3. First start

1. Put the SSD and the USB stick in the Pi. Connect the Ethernet cable. Connect power last.
2. Wait 5 minutes. The first start makes the hub's ID, grows the disk and writes `facility-hub/hub.txt` to the USB stick.
3. On a laptop or phone on the same network, open **http://dhis2.local**. The hub's setup page opens.
   - If `dhis2.local` does not open: the router may block it, or isolate devices. Find the Pi's address in the router's list of connected devices, and open `http://<that address>`.

Nothing shows on the Pi itself: it has no screen and no keyboard.

## 4. Set the hub up

1. Take the USB stick out of the Pi, read `facility-hub/hub.txt` on the laptop or phone, and put the stick back. It holds the hub ID, the **setup code** (8 digits), and the setup Wi-Fi's name and password.
2. On the setup page, type central's address (`https://...`, the one people use in the browser) and the setup code. **Continue.** The hub accepts only `https` with a trusted certificate.
3. The page names the server it found. Check it is the right DHIS2, then log in with **your own** DHIS2 account (a member of the hub owners group, assigned to the facility). Do not use a superuser or an account that manages users: the hub refuses to store their passwords.
4. If you are assigned to more than one org unit, pick the facility. Pick the facility itself, not a district above it.
5. If central already has a hub for that facility (the old one), the page asks to replace it: confirm.
6. The page says "This hub now serves <facility>". The first download has started; the admin page shows its progress.

**Approve the hub's own account** (can wait until after testing). The hub's admin page shows a 16-character account code. A hub manager opens the Facility Hub app on central, finds the hub waiting, and enters that code. Until then the hub runs on the token of the person who set it up.

## 5. Each device, once

Every phone, tablet and laptop that will use the hub:

1. Open **http://dhis2.local** (plain `http`). The page shows steps for that device: download the hub's certificate and install it. Android needs a screen lock for this. On an iPhone, also switch on full trust (Settings › General › About › Certificate Trust Settings).
2. Reload the page. It opens DHIS2 through the hub at `https://dhis2.local`.
3. Never click through a certificate warning for the hub.

Use the browser: Capture web, Data Entry, eRegisters. The Android Capture app does not work with the hub yet (it does not trust the hub's certificate).

**A device that used central directly before:** sync everything to central first, while online. Then switch to the hub's address and use only that.

## 6. Each staff member, once, online

While the hub has internet:

1. Each staff member logs in through the hub (`https://dhis2.local`), on the device they will use. Only users who have done this can log in offline.
2. Still online, they open each app they will use, and in it each program, each working list and each data entry form, once. A screen nobody opened online through the hub does not work offline. What one staff member opens reaches the others at their next online login.
3. Wait for the first download to finish (the admin page shows "last pull" with a time).

## 7. Test offline

1. Unplug the internet from the router (or the router's uplink), not the hub's own cable: devices still need to reach the hub.
2. Log in again on a device, register a test person, and enter data.
3. On a second device, find the person you registered.
4. Plug the internet back in. Within 15 minutes the hub sends the changes (the admin page shows "last push", and the outbox empties). Check on central that the person is there.

What to expect offline:
- A screen or search the hub cannot answer offline shows as "An unknown error occurred (503)" in Capture web. It means "needs connection": note what you were doing and continue online.
- Photos and files cannot be added offline.
- A note added offline to an existing event or enrollment (the Notes box on the dashboard) shows at once, and reaches central with the next push, as its author's. Check it there once the hub is back online.
- An edit made offline can overwrite a newer change someone made on central meanwhile (the later save wins, as with the Android app).

## No cable: the setup Wi-Fi

A hub that has no network a minute after it starts makes its own Wi-Fi, named `Hub-…`, with the 8-digit password from `hub.txt`.

1. Join that Wi-Fi with a phone. The hub's page opens by itself (as a hotel Wi-Fi does). If it opens in a small sign-in window, close it and open `http://10.42.0.1` in Chrome or Safari.
2. Enter the setup code, then choose one:
   - **Join an existing Wi-Fi** (the facility's router or a MiFi): pick it and type its password. The hub leaves its own Wi-Fi and joins; put the phone back on that Wi-Fi too, and continue at step 4 above.
   - **Make this hub the facility's Wi-Fi**: devices then join the hub's Wi-Fi, with the longer password the page shows (also written to `hub.txt`). The hub needs an uplink for internet: a cable to a modem or router, or a USB 4G stick of the "router in a stick" kind.

This mode has had less testing than a cable. Tell us how it goes.

## If something goes wrong

The hub has no screen. Every 30 minutes, and 3 minutes after each start, it writes **`hub-diag.txt`** to its disk's boot partition. To read it: switch the Pi off (unplug power), take the SSD out, connect it to any computer, and open the drive called `bootfs`. Send the file to the DHIS2 team. It holds no patient data and no passwords.

Also send:
- the hub ID (in `hub.txt`, and on the hub's page);
- what you did, what you expected, and what happened, with the time;
- a screenshot of the hub's admin page, if it opens.

On central, the Facility Hub app shows each hub's last report, what it could not answer offline, and changes central refused.

**Updates.** A hub updates its own program to the version chosen on central, in the Facility Hub app's "Hub version" page, within a sync cycle of the choice. Leave the version alone unless the DHIS2 team asks you to set it. Changes to the image itself (the operating system's settings) come as a new image to flash. Data on the old disk that was not yet sent is lost when you reflash, so first use **Reset hub**, which sends it.
