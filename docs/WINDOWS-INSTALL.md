# Installing the facility hub on a Windows PC

For the person at the facility, or the district focal point, putting the hub on a PC the facility already has. About 15 minutes.

**Choose the PC.** It should stay on while staff work: a records-room desktop, or a small server that is always on (for example the one for OpenMRS). It needs 64-bit Windows 10 or 11, a few GB of free disk, and the facility's network (cable or Wi-Fi). A laptop that goes home in the evening takes the hub with it.

1. **Get the installer**, `dhis2-hub-<version>-setup.exe`: from the GitHub release, or from the USB stick you brought.
2. **Run it.**
   - **If you can, choose "for anyone who uses this computer"** (Windows asks for an administrator). The hub then runs as a service from start-up, whether or not anyone is logged in, and devices on the network can reach it.
   - **Without administrator rights, choose "only for me".** The hub then runs only while you are logged in, and Windows may ask to allow it on the network: say yes.
3. **Leave "Keep this PC awake" ticked.** A sleeping PC serves no one.
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

**Other antivirus** than Microsoft Defender: exclude the folder `C:\ProgramData\DHIS2 Facility Hub` from scanning, or the hub slows down.

**Removing it:** in Windows' Settings › Apps, "DHIS2 Facility Hub" › Uninstall. Its data stays unless you tick "Remove the hub's data". Any saves not yet sent are lost with it.
