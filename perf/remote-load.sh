#!/usr/bin/env bash
# The Mac's half of a load test against a hub on another machine: the Windows PC
# (perf/windows/stress.ps1 -Central), later a Pi. The fake central and the users run
# here, so they take no CPU, memory or disk from the machine under test.
#
#   perf/remote-load.sh <hub URL> <size> [users] [duration] [flap]
#   perf/remote-load.sh http://192.168.64.5:18090 300k 60 2h 10m/5m
#
# Start this first: the fake central needs a while to generate a large facility. Then
# start the hub, pointed at the address this prints. The script waits for the hub,
# then:
#   1. sets it up and times the first pull of all users (online);
#   2. a working day: web users with pauses, Android devices already downloaded and
#      syncing every 5 minutes, while the uplink goes down and up (flap: down/up,
#      starting down); every save the hub confirms is logged;
#   3. the reconnect at the end, timed;
#   4. a hammer run: every user at once, no pauses, 2 minutes offline;
#   5. every confirmed save read back from the hub, offline, as its author.
# Results go to perf-results/<size>-<time>/ in the current folder. Sizes are those of
# stress.ps1: people, 5/3 enrollments and 4 events per person (300k = HC IV).
#
# fakecentral and hubload are taken from next to this script (the kit's mac folder),
# else built from this repository with Go.
set -euo pipefail
HUB=${1:?hub URL, e.g. http://192.168.64.5:18090}
SIZE=${2:?size: 10k 30k 60k 100k 300k 500k 700k}
USERS=${3:-60}
DURATION=${4:-1h}
FLAP=${5:-10m/5m}
PORT=${PORT:-8090}

case $SIZE in
10k) PEOPLE=10000 ;; 30k) PEOPLE=30000 ;; 60k) PEOPLE=60000 ;; 100k) PEOPLE=100000 ;;
300k) PEOPLE=300000 ;; 500k) PEOPLE=500000 ;; 700k) PEOPLE=700000 ;;
*) echo "unknown size $SIZE" >&2; exit 2 ;;
esac
ENR=$((PEOPLE * 5 / 3))
EV=$((PEOPLE * 4))

HERE=$(cd "$(dirname "$0")" && pwd)
OUT=perf-results/$SIZE-$(date +%Y%m%d-%H%M%S)
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
BIN=$HERE
if [ ! -x "$BIN/fakecentral" ] || [ ! -x "$BIN/hubload" ]; then
	BIN=$OUT/bin
	(cd "$HERE/.." && go build -o "$BIN/fakecentral" ./cmd/fakecentral && go build -o "$BIN/hubload" ./cmd/hubload)
fi

"$BIN/fakecentral" -listen ":$PORT" -tes "$PEOPLE" -enrollments "$ENR" -events "$EV" -users "$USERS" \
	>"$OUT/central.log" 2>&1 &
CENTRAL_PID=$!
trap 'kill $CENTRAL_PID 2>/dev/null || true' EXIT
CENTRAL=http://127.0.0.1:$PORT

# The address the hub reaches this Mac by: UTM's shared network is bridge100.
MAC=$(ipconfig getifaddr bridge100 2>/dev/null || ipconfig getifaddr en0 2>/dev/null || echo "<this Mac's address>")
echo "fake central: $PEOPLE people, $ENR enrollments, $EV events, $USERS users (generating)"
echo "start the hub now, with its central at http://$MAC:$PORT"
echo "  Windows: stress.ps1 -Size $SIZE -Central http://$MAC:$PORT   (administrator PowerShell)"
until curl -sf "$CENTRAL/api/system/ping" >/dev/null; do sleep 2; done
echo "$(date +%T) fake central ready"
until curl -sf "$HUB/hub/status" >/dev/null; do sleep 5; done
echo "$(date +%T) hub answering at $HUB"

load() { "$BIN/hubload" -hub "$HUB" -central "$CENTRAL" -users "$USERS" "$@"; }
{
	echo "remote load test: $SIZE ($PEOPLE people), $USERS users, $DURATION working day, uplink $FLAP, $(date +%FT%T)"
	echo "hub $HUB; $(curl -s "$HUB/hub/status")"
} >"$OUT/summary.txt"

echo "$(date +%T) 1-3. set-up, first pull, working day with the uplink $FLAP"
load -setup -fill -duration "$DURATION" -android-first none -flap "$FLAP" \
	-confirmed "$OUT/confirmed.tsv" -out "$OUT/workday.txt" 2>&1 | tee "$OUT/workday.log"
echo "$(date +%T) 4. hammer"
load -duration 2m -think 0 -out "$OUT/hammer.txt" 2>&1 | tee "$OUT/hammer.log"
echo "$(date +%T) 5. confirmed saves read back"
load -verify "$OUT/confirmed.tsv" 2>&1 | tee "$OUT/verify.log"

cat "$OUT/workday.txt" "$OUT/hammer.txt" >>"$OUT/summary.txt"
tail -1 "$OUT/verify.log" >>"$OUT/summary.txt"
echo "results: $OUT (stop the hub; on Windows press Enter in stress.ps1 for its own results)"
