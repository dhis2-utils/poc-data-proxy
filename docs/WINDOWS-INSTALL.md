# Installing the facility hub on a Windows PC

For the person at the facility, or the district focal point, putting the hub on a PC the facility already has. About 15 minutes.

**Choose the PC.** It should stay on while staff work: a records-room desktop, or a small server that is always on (for example the one for OpenMRS). It needs 64-bit Windows 11 (Windows 10 is untested), a few GB of free disk, and the facility's network (cable or Wi-Fi). A laptop that goes home in the evening takes the hub with it.

1. **Get the installer**, `dhis2-hub-<version>-setup.exe`: from the GitHub release, or from the USB stick you brought.

   Release 0.2.0 has no installer attached: there is no code-signing certificate yet (PENDING.md, item 37). Until there is, the team supporting the test builds one (`windows/README.md`). It is signed with a test certificate, so Windows warns of an unknown publisher: choose "More info", then "Run anyway".
2. **Run it** as an administrator (Windows asks). The hub runs as a service from start-up, whether or not anyone is logged in, and devices on the network can reach it. Someone with administrator rights on the PC must do this step.
3. **Leave "Keep this PC awake" ticked.** A sleeping PC serves no one.

   The last page says if the PC has no usable TPM (security chip). The hub works without one, but then only staff passwords protect its data if the PC is stolen. Most PCs sold with Windows 11 have one; if it is switched off, turn it on in the PC's firmware settings and restart: the hub uses it from then on.
4. **Set the hub up on this PC:** open "DHIS2 Facility Hub" on the desktop. Then:
   1. give central's address from the country's installation card;
   2. log in with a DHIS2 account assigned to this facility, and confirm the facility.

   Set-up only works on this PC.
5. **Put a USB stick in and leave it there** (optional, recommended): the hub keeps a copy on it, so a broken disk loses at most a minute of work. Then:
   1. log in to the hub's page;
   2. under "USB stick", choose that stick. The hub never uses a stick nobody chose; staff's own sticks are safe.
6. **Connect the devices.** On each tablet or laptop, open `http://dhis2.local` once and follow the steps to trust the hub; then use `https://dhis2.local`.
   - The hub's page on this PC shows the address with its port, if it has one, and the PC's IP address for devices where `dhis2.local` does not work.
   - On this PC itself, the desktop shortcut opens the same address.

**After a restart** (Windows Update, a power cut), the hub waits for the first staff member to log in through it, which also works offline. Until then it does not send saves to central.

**The firewall** opens the hub's ports on every network profile, including networks Windows calls "Public", because facility networks are often marked that way, but only to devices on the PC's own subnets (`LocalSubnet`), never from beyond a router. On a laptop that leaves the facility, devices on the same network can still reach the hub's sign-in, which protects it; switch the hub off there if that worries you.

**Other antivirus** than Microsoft Defender: exclude the folder `C:\ProgramData\DHIS2 Facility Hub` from scanning, or the hub slows down.

**Removing it:** in Windows' Settings › Apps, "DHIS2 Facility Hub" › Uninstall. Its data stays unless you tick "Remove the hub's data". Any saves not yet sent are lost with it.
