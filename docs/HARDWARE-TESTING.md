# Testing on real hardware

The performance results so far come from containers on a Mac that approximate a Raspberry Pi (PERFORMANCE.md, "Running the tests"). They cap CPU time on cores two to three times faster than a Pi 5's, and Docker Desktop may not apply disk limits at all. This plan replaces those approximations with measurements on the boards and storage a deployment would use, and calibrates the container profiles so later container runs mean something.

## Results so far

2026-10-26: a Pi 5 (4 GB) on a SATA SSD (Intenso SATA III High Performance, 120 GB; StarTech USB 3.1 Gen 2 to SATA cable) at 100 k and 300 k, and a Pi 4 (1 GB, no heatsink) on a microSD card (SanDisk High Endurance, 64 GB) at 10 k, 30 k and 60 k, over Wi-Fi at 20 °C, with the image's load-test mode (PERFORMANCE.md, "On Raspberry Pis"). Not done yet: the Pi 4 on an SSD, the Pi 5 on a card, sizes above 300 k, the calibration of the container profiles, bytes written per day (sampled from the next runs on), and power cuts.

## What is being tested

| Board | Storage | Container profile it checks |
|---|---|---|
| Raspberry Pi 4, 1 GB | microSD card | `pi4` |
| Raspberry Pi 4, 1 GB | SSD over USB 3 | `pi4` with `IO=0` (roughly) |
| Raspberry Pi 5, 4 GB | microSD card | none |
| Raspberry Pi 5, 4 GB | SSD (NVMe on an M.2 HAT, or USB) | `pi5` |

Note the exact models used (card, SSD, USB adapter or HAT, power supply, case and cooling) with every result: a budget SSD, a fake card or a throttling CPU changes the numbers more than the board does.

Questions the runs answer:

1. Up to which facility size each combination serves a working day well (PERFORMANCE.md, "Sizing for a busy urban HC IV").
2. How much of the speed comes from memory and how much from the disk, for the choice between more memory and an SSD.
3. Whether the SD card's speed and endurance are acceptable for a pilot.
4. How fast a first pull and an Android or eRegisters first download are on the real thing.
5. Whether confirmed saves survive power cuts on each storage (later, see "Power cuts").

## Set-up

### The board

- 64-bit Raspberry Pi OS Lite, up to date. The official power supply (15 W for the Pi 4, 27 W for the Pi 5), a heatsink or the active cooler, and wired Ethernet: Wi-Fi adds noise to every number.
- Boot from the storage under test where possible. Otherwise boot from a small card and put the hub's data folder on the storage under test; say which in the results.
- Before testing, check the board does not throttle: `vcgencmd get_throttled` should print `throttled=0x0`, under load too.
- For USB SSDs on the Pi 4: `lsusb -t` should show `Driver=uas`, and `dmesg` should stay free of resets under load.
- The clock: the Pi 5's RTC battery fitted, or an RTC module on the Pi 4 (DEPLOYMENT.md, "Clock"). Check that it keeps time across a power cut with the network unplugged: `timedatectl` after power-on shows the right time before any network comes up.

### The hub on the board

Build on the Mac and copy one binary and one file:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o hub-linux-arm64 ./cmd/hub
scp hub-linux-arm64 pi@<pi>:/home/pi/hub
```

`/home/pi/hub.yaml` on the board, with the Mac's LAN address:

```yaml
central_url: http://<mac>:8090
listen: ":8080"
data_dir: /home/pi/hubdata    # on the storage under test
tls: false
sync_interval: 1h
log_requests: false
```

Start it with `./hub serve -config hub.yaml`. The hub alone runs on the board, so nothing else competes for its memory, CPU and disk.

### Central and the clients on the Mac

The fake central and the load generator run on the Mac, from the performance image (`sh perf/pi.sh build`), and reach the board over the LAN as a real central and real devices would. Allow incoming connections to port 8090 in the Mac's firewall.

```sh
# central, at one facility size (here 30 k people)
docker run --rm -p 8090:8090 --entrypoint fakecentral facility-hub-perf \
  -listen :8090 -tes 30000 -enrollments 50000 -events 120000 -users 20

# the load: set up the hub, fill it, a working day offline, then reconnect
docker run --rm -v "$PWD/perf/results:/results" --entrypoint hubload facility-hub-perf \
  -hub http://<pi>:8080 -central http://host.docker.internal:8090 -users 20 \
  -setup -fill -duration 5m -android-first none -out /results/<board>-<storage>-30k-realistic.txt

# then the hammer run against the same hub
docker run --rm -v "$PWD/perf/results:/results" --entrypoint hubload facility-hub-perf \
  -hub http://<pi>:8080 -central http://host.docker.internal:8090 -users 20 \
  -duration 2m -think 0 -out /results/<board>-<storage>-30k-hammer.txt
```

The uplink cut is the fake central refusing connections (`hubload` asks it to), so the hub sees a real network failure.

Between sizes: stop the hub, delete its data folder, restart the fake central with the next size, start the hub again.

While a run is going, sample the board every 10 seconds into a file, for memory, CPU, disk and temperature:

```sh
while true; do echo "$(date +%T) $(ps -o rss=,pcpu= -C hub) $(vcgencmd measure_temp) $(vcgencmd get_throttled)"; sleep 10; done > samples-<size>.txt
```

A `device` mode in `perf/pi.sh` that does the resets, runs and sampling over SSH is a possible next step **(to build)**; until then the steps above are run by hand.

## The tests, in order

### 1. Calibration (each board, each storage)

Storage benchmarks as the hub uses the disk, and raw random reads and writes:

```sh
# on the Mac: the store benchmarks for the board
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o store.bench ./internal/store
scp store.bench pi@<pi>:
# on the board, in a folder on the storage under test
TMPDIR=$PWD ./store.bench -test.run '^$' -test.bench . -test.benchtime 2000x
sudo apt install fio
fio --name=w --directory=. --rw=randwrite --bs=4k --size=256m --runtime=30 --time_based --direct=1 --group_reporting
fio --name=r --directory=. --rw=randread --bs=4k --size=256m --runtime=30 --time_based --direct=1 --group_reporting
```

Compare with `sh perf/pi.sh bench pi4` and `bench pi5` on the Mac, and set the container profiles' CPU and disk limits from the difference (PERFORMANCE.md, profile table), so container runs match the boards.

### 2. The ladder

For each size: the realistic run (a working day: Android devices downloaded before and only sync) and the hammer run (every device's first download at once, no pauses).

| Board, storage | Users | Sizes (people) |
|---|---|---|
| Pi 4, SD card | 20 | 10 k, 30 k, 60 k |
| Pi 4, SSD | 20 | 30 k, 60 k, 100 k |
| Pi 5, SD card | 60 | 30 k, 100 k |
| Pi 5, SSD | 60 | 100 k, 300 k (`hc4`) |

The sizes' enrollments and events are in PERFORMANCE.md ("Sizing for a busy urban HC IV"). Stop a size that runs out of memory or takes more than an hour to fill: that is the result for that combination.

Record per run: first pull time, the latencies the report prints (p50, p95, p99 for working list, search, dashboard, new event, Android pages and syncs), the reconnect steps, peak memory, CPU, temperature and throttling from the samples. The pass criteria proposed in PERFORMANCE.md apply to the realistic run.

### 3. A device's full download

eRegisters downloads every person of every program (FINDINGS.md, "eRegisters through the hub"). In process it takes about 4 minutes at HC IV size; on the boards it is to be measured. Run `TestFullDownload` against a filled hub on each board, or time a `hubload` Android user with `-android-limit 0`, at the largest size each combination handled in step 2.

### 4. Bytes written per day

For the SD card's endurance and the SSD's rated total writes: read the storage's write counter before and after a paced run at the per-hour rates in PERFORMANCE.md (a working day compressed into a few hours), and scale to a day:

```sh
cat /sys/block/mmcblk0/stat   # SD card; /sys/block/sda/stat for a USB SSD, nvme0n1 for NVMe
```

The 7th field is sectors written (512 bytes each). The paced load spreads one day's volume of a busy HC IV (560 new people with an enrollment, 2 600 events, 27 000 reads) evenly over `-duration`:

```sh
docker run --rm -v "$PWD/perf/results:/results" --entrypoint hubload facility-hub-perf \
  -hub http://<pi>:8080 -central http://host.docker.internal:8090 -users 20 -setup -fill \
  -paced -duration 3h -out /results/<board>-<storage>-<size>-paced.txt
```

`-day-people`, `-day-events` and `-day-reads` change the day's volume. The writes happen offline (the uplink is cut for the run), and the reconnect at the end pushes them, so both the local write and the push are counted.

### 5. Power cuts (later)

Cut power under load 50 to 100 times per storage, and after each restart check that the data file opens and that every save the hub confirmed is still there. `hubload -confirmed FILE` appends every write the hub confirmed (a 200 with nothing ignored) to FILE; `hubload -verify FILE` cuts central off, so the hub must answer from its own store, and reads every one of them back as its author, reporting the missing ones and exiting with an error if there are any.

```sh
# a load that logs confirmed saves; cut the board's power while it runs
docker run --rm -v "$PWD/perf/results:/results" --entrypoint hubload facility-hub-perf \
  -hub http://<pi>:8080 -central http://host.docker.internal:8090 -users 20 \
  -paced -duration 30m -reconnect=false -confirmed /results/confirmed.tsv
# power back on, the hub started: every confirmed save must still be there
docker run --rm -v "$PWD/perf/results:/results" --entrypoint hubload facility-hub-perf \
  -hub http://<pi>:8080 -central http://host.docker.internal:8090 -verify /results/confirmed.tsv
```

Run the first command with `-setup -fill` the first time only. Keep one confirmed file per storage across the cuts: each verify checks everything confirmed so far. Tried in the sandbox with the hub killed hard (`kill -9`) in the middle of an offline load: 85 of 85 confirmed records were there after the restart. A killed process is not a power cut (the operating system still writes what the hub handed it), so the boards' runs are the real test. Test with and without a UPS or supercapacitor module, if one is part of the kit (DEPLOYMENT.md, options under consideration).

Against a real central, `cmd/hubdrive` does the same end to end: `hubdrive run` saves through the hub (HTTPS, `-ca` the hub's `ca.crt`) as staff users, as Capture web and Android clients, through power cuts and uplink cuts, and writes every save to a ledger; `hubdrive check` then reads each one on central, as `internal/adversarial` does, and reports what is lost, partial, duplicated or wrong. Its defaults are Ngelehun CHC's Child Programme on the demo central. `TestDriveThroughFaults` runs it against a hub process with the uplink cut and a `kill -9`. With the synthetic 2.42 profile, 168 saves were confirmed (138 of them by the hub offline), 0 were lost, and one event deleted on central was reported lost.

### 6. Real clients (optional)

The scripted Capture web, Android and eRegisters runs (TESTING.md) against a board, with the board's hub in place of the test kit's: the same flows, at real speed, with real devices on the LAN.

### 7. The TPM board (when one is fitted)

An Infineon SLB 9670 board on SPI (the image enables its overlay):
1. `ls -l /dev/tpmrm0` shows group `tss`; the hub's log says "device key made and sealed by the TPM" at the first start.
2. `tls.key` and `ca.key` in `/var/lib/hub` start with `HUBDEV1`; staff log in, offline too, after a restart.
3. Take the TPM board off: the start page says the security chip is missing, and nothing else is served. Put it back: the hub works again.
4. Without a board, the image boots as before, with no TPM driver errors beyond the probe.

## Results

Keep results under `perf/results/<board>-<storage>-…`, and add a summary to PERFORMANCE.md:

| Board, storage | Size | First pull | Working list p50 / p95 | Dashboard p50 / p95 | Android sync | Hammer p50 | Peak memory | Throttled |
|---|---|---|---|---|---|---|---|---|
| Pi 4, SD | 10 k | | | | | | | |
| … | | | | | | | | |
