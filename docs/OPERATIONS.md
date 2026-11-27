# Running facility hubs

How the national team and the districts run a fleet of hubs once they are set up: monitoring, refused changes, upgrades, support, and the security tasks on central. How the hubs are rolled out is in [DEPLOYMENT.md](DEPLOYMENT.md). How each mechanism works inside the hub is in [ARCHITECTURE.md](ARCHITECTURE.md) and [SECURITY-DESIGN.md](SECURITY-DESIGN.md); this document says what to do.

Most of this work happens in the Facility Hub app on central ([FACILITY-HUB-APP.md](FACILITY-HUB-APP.md)), which the national team installs in App Management.

## Monitoring

Every hub writes a heartbeat to `facility-hub-status/<hub_id>` on central once an hour, at the end of a sync cycle, and at its first cycle after a start. It holds:

- the latest status: version, uptime, last push and pull, pending count, issue count, free disk and last upgrade result;
- a history of one summary per day for the last 90 days: sync cycles, outages and minutes offline, the longest outage, the peak of pending changes, the lowest free disk, saves passed straight to central while online (`savedOnline`), and saves stored on the hub and sent later (`savedOffline`).

The history shows how much time a hub spends offline, and how much staff work through it offline. What else the heartbeat carries is in [ARCHITECTURE.md](ARCHITECTURE.md), "Namespaces on central".

The Facility Hub app lists hubs from `facility-hub-registry` with their latest heartbeat. It also approves hubs' own accounts, gives out move codes, chooses and stages the hub version, holds the country's hub settings (retention, token lifetime, reserved-value pools), and resends or discards refused saves. It flags:

- no heartbeat for more than 2 days;
- pending data older than 1 day;
- refused changes;
- low disk;
- failed upgrades;
- "hub needs upgrade" (central's DHIS2 version is not supported by the hub's release);
- a hub token that no longer works ("The hub's own account" below);
- dataStore entries the hub acts on that any user may write (`unprotected`), and instructions the hub ignored for want of a valid code (`unsigned`) ("Protect the data store namespaces" below).

**Not available offline.** The heartbeat carries the 10 screens or requests most often answered "needs connection" offline, and their total (`offlineMisses`, `offlineMissesTotal`). The app lists them on the hub's page, and the hub's own `/hub/ui/endpoints` lists every one. A miss is now mostly a screen nobody at the facility opened online in the last 14 days, or a staff member who went offline within a minute or two of logging in. The hub's log says what it loaded for whom at each login (`replay after login user=… loaded=… kB=… s=…`).

**Before tightening metadata** (a new compulsory field, a removed option, a closed stage), check what hubs and tablets have captured offline under the old rules: those changes will be refused.

**Volume on central.** One heartbeat key per hub is overwritten hourly, with its 90-day history inside, so the namespace does not grow. A heartbeat is about 14 KB (20 KB with 100 refusal groups). For 500 hubs that is 0.14 writes a second, and reading all their heartbeats takes about 0.1 s. dataStore writes are not audited (measured on 2.42 and 2.43). Single-key reads and writes stay at 0.1 s up to a million entries in a namespace; browsing slows as a namespace grows, so only the issues namespace needs watching (below). No archiving is needed.

## Refused changes

A save central refuses stays on the hub, which told the client it was saved. Each refused change lands in `facility-hub-issues`, one key per change, with central's error codes and its messages masked: every value the hub sent is replaced by `***`. Central staff see which data element or attribute caused it, and nothing that was entered. Once resolved, the hub deletes the key. The mechanism is in [ARCHITECTURE.md](ARCHITECTURE.md), "Refused changes".

What is not a refusal:

- An answer from the way to central (a redirect, 408, 429, 5xx, an answer cut off): the save waits for the next cycle, without limit.
- An answer to a tracker save that is not an import report (a proxy's 413 page, a firewall's 403 page) also keeps it waiting, but after a day the hub refuses the entry with that answer, so it does not hold up the saves behind it.

What closes by itself:

- A change to a record central has deleted (E1082, E1113, E1114, E4017) can never pass, since DHIS2 cannot undo a deletion. The hub closes it after the push, keeps a tombstone so Android's next download drops the record, and keeps the outbox entry for 30 days.
- A fixed version of the record sent through the hub and accepted by central closes the refusal.

What waits for someone:

- **Single refusals.** Districts follow them up from the hub's page in the app. It shows the author, an "Open in Capture" link, and who closes the refusal by its error code: the author, central, or nobody. Staff see their own open refusals on the hub's page, under "Your sync errors".
- **A refusal whose record central changed afterwards** stays open as superseded (`"superseded": true`) until a hub manager resends or discards it.
- **Saves of a user central stopped accepting** (disabled, deleted, token expired) wait for that user's online login. After 7 days the hub refuses them itself, with the code `NO_LOGIN`.

**Refused in bulk** (for example hundreds of events after a metadata change). The heartbeat carries counts by kind, programme and error code (`refused`), so the app's Refusals page shows the pattern, not hundreds of rows.

- **Resend.** Fix central first (restore the option, relax the rule). Then press "Resend" on that group. Each hub sends its refused saves of that group, and what they blocked, again as their original authors, once, at its next sync. Central's new answer stands: a save refused again is a fresh issue. On a `NO_LOGIN` group, Resend can name another user of the hub to send the saves as. Central then records that user as their creator. A hub that does not know that user, or cannot send as them until they log in online there, applies nothing and says why in its heartbeat (`retriesNotApplied`).
- **Discard.** When central was changed on purpose and the waiting saves no longer fit (a new rule, a closed period, a duplicate), press "Discard" on the group, or on one change on the hub's page. Each hub throws those saves away once, with what they blocked, and keeps central's version of each record, or a tombstone of a record central never had. A record central had but does not show to the author stays refused, since central's 404 does not tell a deletion from no access. A hub that holds no token for the author keeps those saves refused and says why in its heartbeat. The discarded entry is kept 30 days.

A group Resend or Discard reaches only hubs under the org units of the manager who asked, and only saves refused before it. The app says how many saves on how many hubs it reaches, and asks for confirmation.

Keys of hubs that were reset or replaced stay in `facility-hub-issues`. Removing them after a grace period is for the Facility Hub app **(to build)**.

## Answering staff from the hub while online

Where the uplink is slow or unreliable, the national team can have hubs answer staff from their own copy even while central is reachable: Facility Hub app, Settings, "Answer from the hub while online", a list of programmes. Hubs read it within an hour. Off by default: with no programme listed, everything passes to central while online. How it works is in [ARCHITECTURE.md](ARCHITECTURE.md), "Local reads"; measurements are in [PERFORMANCE.md](PERFORMANCE.md), "A slow uplink", and [CONNECTIVITY.md](CONNECTIVITY.md).

With programmes listed, a hub answers from its copy the listed programmes' patient data (working lists, searches within the facility, a person and their records), and the metadata and settings the user's apps asked for before. Saves, reserved IDs and validation always go to central.

**How staff get changes.** A change made elsewhere reaches staff at the hub's next sync, 15 minutes by default. For metadata, a hub learns of changes:

- hourly, asking central what metadata changed;
- when the national team presses "Refresh metadata on all hubs" (Settings): every online hub within 15 minutes, an offline hub when it reconnects;
- when someone at the facility presses "Refresh metadata now" on the hub page;
- when a setting or data store entry is changed through the hub;
- for one user, when their access changes.

The hourly check does not see deletions, changes to which org units a programme or data set is assigned to, or changes to settings and app configuration. Those reach staff at their next online login within a day, or at once with either refresh button. **After deleting metadata, reassigning forms, or changing settings that staff should see at once, press "Refresh metadata on all hubs".**

## Upgrades

**Hub software.** The supplier publishes signed releases on GitHub (`dhis2-utils/poc-data-proxy`, tag `v<version>`). Each country chooses the version its hubs run on the Facility Hub app's "Hub version" page, and stages it:

1. 5 pilot hubs, listed by hub ID;
2. 10 %;
3. 50 %;
4. 100 %.

Each hub downloads the chosen version in the background, pushes its outbox, migrates a copy of its store, swaps, health-checks the new release, and rolls back on failure. A rollback loses nothing. A hub never moves to an older version, and does not retry a version that failed. Results show in the heartbeat, and the page counts them per stage. The mechanism is in [ARCHITECTURE.md](ARCHITECTURE.md), "Upgrades".

- **Staff using Capture web log in again after an upgrade**, as after any restart: sessions are kept in memory only. The hub comes back unlocked, so that login works offline too. The Android app carries on.
- **Put some of the largest hubs in the first stage**, found by the heartbeat's disk use. A migration that touches existing rows is timed at the largest size before a release (AGENTS.md), but a hub that cannot migrate stays on its old release, and with hundreds of hubs a stage could stall.
- **Reset test hubs from before the first release, do not upgrade them.** Until a hub is deployed, the store's base schema is edited in place instead of migrated. A store from an earlier build keeps old indexes and is slower. Its data returns from central at the next pulls.
- **Before a DHIS2 server upgrade,** run the live suite against the new version ([TESTING.md](TESTING.md)). If the hub needs changes, roll out that hub release first. A hub release supports only the DHIS2 versions it was tested against. Hubs on an unsupported version stay usable online, as pass-through only, and show "hub needs upgrade". Their queued saves stay in the store, and the upgrade to a release that supports central pushes them.
- **Operating system.** On a Pi, security updates install themselves (`unattended-upgrades`, with a reboot at 03:00 in central's time zone when needed). Re-image only for major OS changes, by shipping a pre-flashed SSD: the hub holds nothing that central does not, once its outbox is pushed.

## Support

- **From central first.** The district and the national team read the Facility Hub app: last heartbeat, pending and refused changes, disk, token. The hub's own status page opens only on the facility network.
- **On site, by the staff:** restart the hub, log in to unlock it after a restart, plug in a replacement.
- **No remote access** to hubs. A hub that does not come up writes what support needs to `hub-diag.txt` on its boot partition, readable on any computer (image/README.md).
- **Most faults** are fixed by moving the disk to a new board or sending a spare ([DEPLOYMENT.md](DEPLOYMENT.md), "Replacing or moving a hub"). Keep a spare per district.
- **Faults in the hub software** go from the national team to the supplier.

## Central's time zone

Set central's server time zone to the facilities' own. In a country with several zones, use the easternmost, so that central's date is never behind a facility's.

Central refuses an enrollment or incident date "in the future" (E1020, E1021) by its own date, while staff date records by their devices. With central in a zone to the west, every registration dated today is refused between local midnight and central's: in the Pi 5 soak, with central on UTC and the clients on UTC+1, 398 saves in one hour each night. Online, the staff member sees central's refusal at once. Offline, or while the hub still has a backlog to push, the hub refuses the same dates itself, with central's code and text. So the staff member sees the refusal at the save in both cases. A hub whose clock is behind leaves these dates to central and says so on its page; a hub whose clock is ahead lets them through and central refuses them at push ([ARCHITECTURE.md](ARCHITECTURE.md), "Clocks").

**Device clocks.** A device whose clock or time zone is ahead of central's dates its records in central's future. The hub's page lists each user whose Android device ran more than half an hour ahead of central at their latest upload, and the heartbeat counts them (`clientClocksAhead`). Set the date, time and time zone of those devices to central's. Capture web sends no device time, so the hub cannot check a browser's clock. A clock behind is not listed. The list is empty after a restart until each device sends again.

Allowing future enrollment and incident dates in a program also avoids E1020, but it changes what the program allows. It is a workaround, not the fix.

## Offline edits and newer changes on central

An edit saved at the hub while it is offline can replace a newer change made on central meanwhile, and nobody is told. Seen on 3 November 2026: a nurse changed a weight offline at 13:59, someone changed it on central at 14:00, the hub pushed at 14:06, and central kept the nurse's older value. Central keeps the last save it receives and does not compare versions.

The hub pushes each saved object as the app sent it: a person with its attributes, or an event with its data values. Where the app sends every attribute or value, not only the changed one, a stale copy also puts back fields the nurse never touched. In adversarial scenario C1 the person went with every attribute, and central's earlier change to the last name was undone. Central's change log keeps both edits.

Within the facility nothing new happens: every device saves to the same copy on the hub, so the later save wins, as online. The case needs one record edited both through the hub and on central (the district, another facility, a tablet still pointed at central) while the hub is offline. Android Capture on its own does the same: its SDK sends every changed record with all its values and makes no version check (read in `dhis2-android-sdk` at `3ff01d8`; not run on a tablet). The hub makes the case more common, because more people work offline, and for longer.

**What to tell staff and districts:** while a facility's hub is offline, do not edit that facility's records on central. Where someone must, check the record once the hub has pushed. The hub's page shows when that was.

**How often it happens is counted** (telemetry only; nothing is held back). Before pushing one offline edit of a central record in ten, the hub reads central's current `updatedAt` with the author's token, and counts the save if central changed the record since. The app shows the count and the recent cases on the hub's page (record, author, when it was saved, when central changed it; no values). Measured on 2.42.7.1, one read in ten adds about 5 % to central's work for updates. A country turns the count off on every hub with `facility-hub-config/overwrites` = `{"off": true}`, signed as the app writes its settings. After some months the count decides whether to keep it, remove it (`internal/store/overwrites.go` says how), or build one of these options (not built):

- Before pushing, read central's `updatedAt` for the records in each batch. Where central's version is newer than the one the edit started from, hold the edit as a refusal ("changed on central meanwhile"), with Resend and Discard as for any refusal.
- Send only the values that changed, so only an edit of the same field replaces a newer one. First check on 2.42 and 2.43 that central keeps the attributes and data values an update leaves out.

## Security operations

The defences themselves are in [SECURITY-DESIGN.md](SECURITY-DESIGN.md). This section lists what the national team does.

### Protect the data store namespaces

Do this on central before the first hub goes out ([DEPLOYMENT.md](DEPLOYMENT.md), "On central, before the first hub"). By default any user can write the `facility-hub-*` namespaces, and a forged registry entry can retire a hub or block set-up of a facility.

1. A superuser creates, once:
   - the **hub owners** group: the facility focal points and whoever else may set up or take over a hub;
   - the **hub accounts** group and its role: write sharing on the `facility-hub-*` namespaces except `facility-hub-admin`, read on programme metadata;
   - the **hub managers** group and its role ([FACILITY-HUB-APP.md](FACILITY-HUB-APP.md), "Hub managers").
2. The superuser names the three groups in the Facility Hub app's Settings. Until a hub owners group is named, hubs refuse set-up and take-over.
3. The national team or the districts add the people who own hubs to the hub owners group. Membership is then the list of hub owners.

Any member of the hub owners group can write any hub's entries, so sharing alone is weaker than "admins only". The Facility Hub app therefore signs each instruction it writes for each approved hub, and hubs ignore unsigned ones ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Central's dataStore"). Watch the app's flags: `unsigned` means someone wrote an instruction the hub ignored; `unprotected` means an entry the hub acts on is writable by any user. A hub not yet approved still reads `release` and `accounts` on their sharing, and a "retired" mark stays unsigned.

### The hub's own account

Each hub needs an account of its own for its housekeeping (heartbeat, issue reports, registry, reserved values, upgrade checks), so that it depends on no staff member. Patient data never goes with it. Until the account is approved, the hub uses the token of whoever set it up: if that person is disabled on central, the hub goes silent and offline registrations that need generated IDs fail once its pool runs out.

**Approve each hub.** A hub manager approves waiting hubs in the Facility Hub app, for hubs in their own org units:

1. Call the facility on a number you already have. Never use a code from someone who calls you.
2. Ask for the 16-character account code on the hub's page, and enter it in the app. The app refuses a code that does not match the hub's key and facility.

The app creates the user `<hub ID>`, assigned to the facility only, with no authorities, and hands the credentials to the hub encrypted. The hub then changes the password and makes its own token, so whoever saw the credentials on the way is shut out. How this works is in [SECURITY-DESIGN.md](SECURITY-DESIGN.md), "The hub's own account". To approve a hub again, enable its user in the Users app first.

**Until it is approved, or if its account was revoked,** any member of the hub owners group assigned to the facility can take the hub over on its admin page ("Use my account", with their password). The hub swaps to a token of theirs and deletes the old one. On a hub with a working account of its own, a take-over is refused until a hub manager revokes that account. The heartbeat reports whose token the hub uses and when it expires (`hubTokenOwner`, `hubTokenExpires`), so the app can list the hubs that depend on one person. Set hubs up with a facility account, not with an account that can manage users: "Use my account" refuses such accounts, but a hub set up by one holds its token until the hub's own account is approved.

**Revoke a hub** with "Revoke all tokens of this hub" in the app. It deletes the hub's tokens and disables the user `<hub ID>` (or gives it a new random password where disabling is refused). A hub manager may do this within their org units.

### Tokens and passwords

- **Token lifetime** is the country's setting in the app (7 to 365 days, default 90). Tokens renew while the hub is online.
- **Disable users who leave** on central. The hub refuses their next login within minutes. A user already working goes on for up to an hour, and an access change can take about two hours to reach the hub, because central caches a token's login. To end it at once, delete the user's tokens on central. On 2.42 their tokens keep working against central after the user is disabled ([FINDINGS.md](FINDINGS.md), "Tokens outlive their user"), so revoke a lost hub's tokens in the app.
- **Hubs show users central refuses** as "needs online login".
- **Do not reuse usernames.** A hub knows its users by username. A new account with a deleted account's username inherits the old one's place on the hub once it logs in there: what it may see offline, its cached pages and its queued saves. DHIS2 practice is to disable accounts and keep them.
- **Enforce central's password policy for hub users.** Whoever holds a hub's disk or USB stick can test password guesses offline against each user's key slot. The weakest password among the hub's users sets the bar. The hub removes slots of users absent 90 days.
- **Use a TPM where there is one** (recommended, not required; decided 17 November 2026). A TPM add-on board costs about USD 25 for a Pi; most Windows 11 PCs have one built in. With it, a disk taken on its own is useless. A TPM does nothing against a staff password shared, written down or phished, which is the more likely way to the data. The heartbeat and the app show whether a hub's keys are sealed (`deviceKey`). Its limits are in [SECURITY-DESIGN.md](SECURITY-DESIGN.md), "The TPM".
- **Physical security.** Once a staff member has logged in, the whole facility's data is decrypted in the hub's memory. A powered-on hub that is moved keeps its key until it loses power.

### A stolen hub, an insider, and accounts that manage users

**A stolen hub, disk or stick.** Treat it as a possible password leak:

1. Ask the hub's users to change their passwords.
2. Press "Revoke all tokens of this hub" on its page in the Facility Hub app. On 2.42, disabling a user or changing their password does not stop their tokens, and only the owner or the hub managers can delete them. Hubs share the tokens they make with the hub managers and list them in their registry entry.
3. Without a TPM, remove the hub's certificate authority from the facility's devices, and install the new hub's.

A revoked hub that comes online again drops what it still holds when it next makes a token, and serves nothing offline until it is reset or its entry no longer says "revoked". It takes "revoked" as real only once central refuses its own token. If a token was refused for another reason (expired during a long outage, its owner disabled), a forged "revoked" can still pass: set the entry's `status` back to `active` on central, then a staff login makes tokens again and "Use my account" gives the hub a token of its own. Unsent saves are kept; their authors log in online again so they can be sent.

**An insider with the disk or the stick.** Every key slot opens the same data key, and users' tokens are sealed with it. One staff member's password, together with the hub's disk or its stick, therefore opens every registered user's token, including those of district or national accounts that logged in at this hub once. Treat such a case as a leak of all of them: revoke the hub's tokens and ask its users to change their passwords.

**Accounts that manage users work through the hub online only.** A superuser (`ALL`), or an account that can create, change, delete, replicate or impersonate users or change user roles (`F_USER_ADD`, `F_USER_DELETE`, their `_WITHIN_MANAGED_GROUP` forms, `F_USERROLE_PUBLIC_ADD`, `F_USERROLE_PRIVATE_ADD`, `F_REPLICATE_USER`, `F_IMPERSONATE_USER`), gets no key slot and no stored token on any hub. Such a token, taken from a hub, would let its holder make or take over any account on central. These users pass through to central online and cannot log in offline. Facility staff, and the accounts they use on the hub, should not hold these authorities.

### Moving central

A hub follows central to a new address only when the current central vouches for it ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Moving central"). A hub must never be stranded by a move, nor follow a server that is not the same DHIS2. There are three ways:

1. **Announced.** Before a planned move, announce it in the Facility Hub app. It writes `facility-hub-admin/central-move` (`{"url": "https://new.example.org", "from": "2026-11-01T06:00:00Z"}`, `from` optional), signed for each approved hub. Hubs read it at each catalog refresh. `facility-hub-admin` must be writable by administrators only, so that the hub owners group cannot move every hub. A hub not yet approved uses no announcement.
2. **Redirect.** The current address, over verified https, answers with a permanent redirect (301 or 308) to the new host, three times in a row. A redirect from plain http (a captive portal, say) never counts.
3. **Typed in,** for an unplanned move. A hub manager has the app make one 10-character move code per hub for the new address, as a list to send to facilities. While the old address does not answer, a staff member enters the new address and the code on the hub's page. The hub checks the code itself, sends nothing for it, and moves only to the address the code was made for. Only an approved hub has a move key.

For an announced or redirected move, the old address must have stopped answering for an hour, so a brief outage during a planned move does not move hubs early. A hub whose `hub.yaml` pins `central_url` never moves; one limited to `central_hosts` moves only within them. The new address survives restarts. A hub that misses all three keeps working offline, with its data, waiting for an address. The admin page's "Simulate uplink cut", shown only with `uplink_switch: true` in `hub.yaml`, is for demonstrations and does not count as central not answering.

### Host names

If devices reach the hub by a name other than its host name, `dhis2.local` or its IP (for example a router's DNS name), list it in `hub_hosts`: the hub refuses unknown `Host` headers. A name must name one host: at least three labels (`hub.clinic.example.org`), or two under `.local`, `.lan`, `.internal` or `.home.arpa`. Wider names would let the hub's authority vouch for a whole domain, and are left out with a warning. Set it before the hub's first boot: the authority permits the names in `hub_hosts` when it is made, so a name added later is not covered until the hub makes a new authority, which every device installs again.

### Signing releases

Hub binaries and the release manifest are signed with the supplier's ed25519 release key, which hubs check before upgrading ([SECURITY-DESIGN.md](SECURITY-DESIGN.md), "Releases").

The Windows installer and `hub.exe` are also Authenticode-signed, with a code-signing certificate issued to the supplier's organisation. Its key must sit in hardware or a cloud signing service, so CI signs through one (for example Azure Trusted Signing, about USD 10 a month, or DigiCert KeyLocker), with `signtool`, or `jsign` from Linux. SmartScreen warns until the certificate has built up download reputation. No such certificate exists yet, so releases carry no Windows installer ([PENDING.md](PENDING.md), item 37).

**Rotating the release key.** Every hub binary embeds the supplier's release public key. A hub runs only a release whose manifest (`v<version>/release.json`) verifies under that key; a manifest that does not verify is ignored, and not fetched again until the country's release choice changes. To rotate the key:

1. Publish one intermediate release whose manifest is signed with both the old and the new key, and whose binary trusts both.
2. Let hubs take it through the normal staging.
3. Publish a later release that drops the old key.

A hub that missed the intermediate release stays on its current release until a release signed by a key it trusts is chosen. Never re-sign an existing version with a new key: hubs that already refused or ran that version will not look at it again.
