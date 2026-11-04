# Facility hub: running without a real-time clock

A study of what the hub does when its own clock is wrong, written 4 November 2026 on the `ready2` branch (491b1ad, with the future-date check F4 and the device-clock warning). DEPLOYMENT.md ("Clock") requires an RTC. This is what going without one implies. Nothing here changes the hub; the fixes at the end are proposals.

## Where the clock starts

A Pi 4 has no RTC. A Pi 5 without its battery is the same. After a power cut with no network, a Pi on the hub's image does not start at the time it last saved. It starts at a fixed date, 6 October 2026, 00:27 UTC, whatever the date is.

This comes from reading the 22 October image's root file system, not from a Pi:

- `fake-hwclock` is not installed.
- Installing `chrony` (image/customize.sh) removed `systemd-timesyncd` (dpkg: "deinstall ok config-files"). Nothing therefore updates `/var/lib/systemd/timesync/clock`.
- At boot, systemd 257 moves a clock that is earlier than an epoch up to that epoch. The epoch is the later of its build date and the time stamp of `/var/lib/systemd/timesync/clock` (both names are in `libsystemd-shared-257.so`). In the image that file is dated 6 October 2026 00:27:36 UTC, the base image's date (`BASE_DATE` in image/build.sh).
- chrony runs with `-F 1` and `makestep 1 3`, without `-s`. It does not set the clock from its drift file. It steps the clock at its first measurement once it reaches `pool.ntp.org` over UDP port 123.

So "behind" on this image is not the length of the outage. On 7 November it is 32 days. It grows by a day each day the image is in use. A new image moves the date to its own base. F1 on Saturday 7 November checks this (windows/out/session-b/SESSION-B.md, part 4).

A clock ahead does not happen on a Pi after a power cut. It happens on a PC with a wrong clock (the Windows hub), or when someone sets a clock by hand.

## Summary

Each row is one group of uses of the hub's own clock in `internal/` and `cmd/hub`. The outcome classes are: harmless; wrong answer shown (to staff or managers); wrong value on central; data not sent (lost to central, or held until someone acts); locked out (staff cannot do the task offline). "Online" in the fifth column means once the hub reaches central and NTP has set its clock.

| Group | Behind by hours | Behind by days to weeks | Ahead | Corrects itself online | How sure |
|---|---|---|---|---|---|
| Offline login and key unlock | harmless | harmless | harmless | n/a | code |
| Sessions, login throttle, sync and probe timers, heartbeat interval, stick copy cadence | harmless | harmless | harmless, also across the jump when NTP arrives | n/a | code (Go's monotonic clock) |
| The hub's own TLS certificate | harmless | harmless (renewed earlier); with the epoch over 397 days back, a new certificate is already expired for devices | locked out, if issued while ahead by more than an hour | no: kept until its renewal or a new address; an authority made ahead stays invalid until the true time passes its start | test |
| Reaching central (verifying central's certificate) | harmless, unless central's certificate is younger than the offset | data not sent: the hub stays offline while its clock is before central's certificate's start | data not sent, past the certificate's end | yes, once chrony reaches NTP; never if UDP 123 is blocked | test; hardware for the order of events |
| PAT and hub token expiry | harmless | wrong value on central: a token made while behind expires that much earlier; with a token life under the offset it is born expired and its user must log in online | harmless: renewed at once after the jump | yes, at the next renewal | code; hardware |
| Aggregate periods and expiry (`CentralNow`) | wrong answer, only within hours of a period's start or expiry | locked out of the current month's form; expired periods open, and central refuses their values at push (data not sent) | locked out of periods that expire early; next period open early, refused at push | yes, at the next pull; a pull with the clock wrong drops the right forms until then | test |
| Tracker program expiry (E1047) | as aggregate | edits of expired events taken, refused at push (data not sent) | locked out: edits central would take are refused | yes | test |
| Future-date check (F4, `checkEnrollmentDates`) | harmless since the fix below: more than 10 minutes behind the newest time central has shown, the check leaves dates to central at push; under that, today's registrations refused in the minutes after midnight | harmless since the fix (before it: every registration dated today refused, E1020 and E1021) | future dates let through, refused by central at push | yes | test |
| Device-clock warning (`NoteClientClock`) | harmless since the fix below: off while the hub's clock is behind central's newest time, and the hub's page says its own clock is behind | as hours | wrong answer: devices ahead not listed | at each device's next upload | test |
| Android incremental syncs (W1: `hubClockBehind`, `updatedFloor`) | harmless | wrong answer between 1 day and about 1.5 days behind: other devices miss the newest offline records; beyond that, harmless (everything written on the hub is re-sent) | harmless (more re-sent) | only after the records are pushed and pulled back; after the jump, records stamped while behind are left out until then | test |
| Refusal times | tracker: harmless (central's `Date` header) | aggregate: data not sent, a manager's resend of a refused edit can close as "replaced by a later change" unsent | aggregate: wrong value on central, a resend can undo a later change on central | no: the edit keeps its time | code |
| Reserved values | harmless | data not sent, rarely: a value whose reservation central let go can be given to another device, and one save is refused as a duplicate | locked out over 60 days ahead: the whole pool is dropped; under that, values wasted | yes, the pool refills | test |
| Retention and pruning (tracker retention, offline page cache 30 days, key slots 90 days, sent outbox 30 days) | harmless | harmless (keeps more) | harmless by days; over 30 or 90 days, an online cycle deletes the offline page cache or key slots, and in the next outage screens and offline logins are missing | the page cache refills screen by screen, key slots at each online login | code |
| Outbox order and waits (`failing_since` 24 hours, `loginWait` 7 days) | harmless | harmless; a jump forward after a stamp shortens the wait | harmless | n/a | code |
| Stored intervals (weekly full pull, daily aggregate full pull, hourly ownership check) | harmless | harmless: run at once after the jump | harmless: postponed by the offset | yes | code |
| Heartbeat, hub page, logs, history, stick copy times | wrong answer | wrong answer: the Facility Hub app shows the hub as silent until its next heartbeat, up to an hour after the jump | wrong answer: a silent hub not flagged | for new entries; old ones keep their times | code; hardware |
| Offline copies' `createdAt` and `updatedAt` | wrong answer (offline only) | wrong answer (offline only) | wrong answer (offline only) | yes: central's times come back with the pull; the push sends the client's payload, not the hub's times | code |
| Upgrades | harmless | harmless (downloads need TLS, as central) | harmless | n/a | code; Windows: a clock jump within the 2-minute handover leaves the hub locked until someone logs in |
| Central move (`central_next_from`) | harmless | harmless: an announced move waits | harmless: moves early, only after the old address is silent for an hour | yes | code |

The tests are `TestNoRTC*` in `internal/tracker`, `internal/aggregate`, `internal/hubtls`, `internal/central`, `internal/core` and `internal/store`. They pass on today's code and describe its behaviour, so a fix changes them. Each injects time where the code takes it: the importer's `Now`, the period functions' `now`, the certificate code's `env.now`, a reserved-value cut-off. Where the code reads `time.Now()` itself, the test moves the other side instead: central's certificate starts in the future, the device's times run ahead, the watermark runs ahead. Go ignores libfaketime and the sandbox has no root for time namespaces, so no test runs a whole hub with a wrong clock.

DEPLOYMENT.md ("Central's time zone") and FINDINGS.md (central's time zone, "Reversed 4 November") said that a hub clock behind central's lets a future date through and a clock ahead refuses a correct date. The code does the opposite (`TestNoRTCEnrollmentDateCheck`): behind refuses correct dates, ahead lets future dates through. The decision of 3 November had it the right way. Both texts are corrected in this commit. DEPLOYMENT.md ("Clock") also names PAT expiry as needing the right time after a power cut. Central decides a PAT's expiry by its own clock; the hub's clock only sets how long a new one lasts.

## The cases that matter most

### Registrations refused while the hub is behind

The future-date check came in on 4 November. It compares an enrollment date with the hub's today, in central's time zone. With the hub's clock on 6 October, every registration dated 7 November is "in the future" and refused at the save with E1020 and E1021. This applies to every program that does not allow future dates. That is DHIS2's default, and Child Programme on the demo central is one. Staff cannot register anyone offline until the network returns and NTP sets the hub's clock. Hours behind gives the same refusals between midnight and the hub's midnight. Before F4 the hub took these saves and central accepted them at push, so on a board without an RTC this check turns harmless saves into refusals.

Recommendation: fix, before F4 reaches a hub without an RTC. The hub can tell that its clock is wrong without root: its today is earlier than the newest time central has shown it (the `Date` header of central's answers, which the push already reads). When that holds, the check lets the date through and central decides at push, as before F4. The same comparison can drive a line on the hub's page ("the hub's clock is behind central's last answer") and keep the device-clock warning from listing every tablet. This is one stored time and one comparison. The measured problem is `TestNoRTCEnrollmentDateCheck`.

Fixed 4 November, before the check reached main: central's answers report their `Date` header to the hub (`central.Client.SawDate`), which keeps the newest (`MetaCentralDateSeen`, stored at most every 10 minutes, so it survives a restart without network). When the hub's clock is more than 10 minutes behind it (`Hub.ClockBehind`), the future-date check leaves the dates to central, the device-clock warning is off, and the hub's page says "The hub's clock is behind". Guards: `TestEnrollmentFutureDatesClockBehind` and `TestHubClockBehindLeavesDatesToCentral` (central three days ahead of the hub; fails without the fix). The aggregate window (below) does not use it yet.

### The clock starts at the image's date

Every case in the table that depends on the size of the offset is worse with a fixed epoch than with a saved clock. With the epoch, a Pi 4 hub one month after its image's base date starts a month behind after every power cut without network. With a saved clock it starts behind by the length of the power cut.

Recommendation: fix, in the image only. A root timer that runs `touch /var/lib/systemd/timesync/clock` every 15 minutes, and once at shutdown, gives systemd's existing epoch check a recent time. No hub code changes. This needs F1 on hardware first, to confirm the epoch, and once more after the change. The touched file is on the system partition. If the system partition becomes read-only (DEPLOYMENT.md, "Options under consideration"), it must move to the data partition, as that section already says.

### The month's aggregate form offline

Offline, the hub's clock decides which periods are open (`aggregate.Window`). With the clock on 6 October and the true date 4 November, a monthly data set with 10 expiry days offers September, which central has locked, and not October, which staff need to report (`TestNoRTCAggregateWindow`). Values entered for September are refused at push. A sync cycle that runs between the network's return and chrony's step also pulls by the wrong window and drops the stored October forms until the next cycle.

Recommendation: with the saved clock above, the offset is the outage, and this happens only across a month's end. Then warn: the hub's page and the Data Entry app's lock already show "locked". With the comparison from the first case, the hub can also use central's last time as a lower bound for its own when it decides periods. That keeps the forms right whenever the hub is behind the last time it was online.

### No NTP, no central

The hub verifies central's certificate by its own clock. Let's Encrypt certificates last 90 days and are renewed after about 60. A hub behind the start of central's current certificate cannot complete a TLS handshake, so it stays offline: nothing is pushed or pulled (`TestNoRTCProbeFailsOnCertificateNotYetValid`). chrony steps the clock at its first answer from NTP; how soon that comes after the network returns is for F1 to show. Where the facility's network blocks UDP port 123, or the uplink drops NTP, the clock is never set and the hub never comes online. The log says why, once a minute: `central still unreachable probe="… x509: certificate has expired or is not yet valid: current time … is before …"`. The demo central's current certificate ends 19 December, so it started about 20 September, and a hub at 6 October still reaches it until the certificate is renewed in late November.

Recommendation: warn, and add a requirement. DEPLOYMENT.md should say that the hub needs NTP (UDP 123 to `pool.ntp.org`, or a local server in `chrony` sources) from the facility's network. The hub's page and heartbeat should say "the hub's clock is wrong" when the probe fails with this x509 error, so support sees the cause without the log. The hub cannot set the clock itself: it runs without `CAP_SYS_TIME`, and should stay that way.

### The hub's certificate issued while ahead

The hub dates its server certificate an hour before its own now. A PC whose clock is two days ahead, restarting on a new address, issues a certificate that every device with the right time refuses as not yet valid. Once the PC's clock is right, the hub keeps that certificate: it renews only 60 days before the end or on a change of names or addresses (`TestNoRTCCertificateIssuedAhead`). Every device is locked out of the hub until the true time passes the certificate's start. An authority made at first boot with the clock ahead has the same problem, and no new server certificate helps (`TestNoRTCAuthorityMadeAhead`).

Recommendation: fix the server certificate, accept the authority. One more condition in `Certs.fits`: a certificate whose start is more than an hour after now is replaced. The check runs every 30 seconds, so the hub serves a valid certificate within a minute of its clock being right. The authority is made at first boot, at the depot with a network, so an authority made ahead is rare. Replacing it would make every device install a new one, so leave it.

### Others

- Aggregate refusals: `aggregateReplaced` compares central's `lastUpdated` with the edit's time on the hub's clock. The tracker side uses central's `Date` header for the same question (`changedSinceRefused`). Proposal: record central's `Date` header with an aggregate refusal too (`AddIssue` gets it for tracker refusals already), and compare with that. This belongs with the refused-changes plan.
- Android incremental syncs between 1 day and about 1.5 days behind leave out the newest offline records for other devices (`TestNoRTCIncrementalSyncWindow`). With the image's epoch the offset is weeks, so this window is rarely hit today. With a saved clock it becomes the length of a power cut of that size. Accept; with the lower bound from the first case, the hub could stamp its writes with the later of its clock and central's last time.
- Reserved values and pruning go wrong only with a clock more than 30 to 90 days ahead. Accept.

## What only hardware can tell

- Whether the Pi 4 starts at 6 October 00:27 UTC, as read from the image, or at some saved time.
- How long chrony takes to set the clock after the uplink returns, against how long the hub takes to go online (one probe every 15 seconds) and to run a sync cycle (within a random 5 minutes, or the 15-minute timer). If the hub syncs first, the cycle runs with the clock a month behind: tokens renewed with early expiry, the heartbeat dated 6 October, aggregate forms pulled by September's window.
- Whether e2fsck at boot, seeing the last mount time in the future, adds to the boot time on the SD card.
- What the Data Entry app and Android Capture show for the refusals above, on a tablet. Session B has no tablet; F4 with the hub behind needs an image built from `ready2`, and session B's image is built from dad9c4d, before F4.
- On Windows, whether the Windows Time service steps a clock days off, and how fast (F2, session B part 2.3).
