#!/usr/bin/env bash
# Runs remote-load.sh for several sizes one after another, unattended, against a hub
# machine left running stress.ps1 -Queue: it starts a fresh hub for each size when
# that size's fake central comes up, and stops it when the fake central goes away.
#
#   perf/remote-queue.sh <hub URL> "<sizes>" [users] [duration] [flap]
#   caffeinate -dims perf/remote-queue.sh http://192.168.64.5:18090 "100k 300k 500k 700k" 60 1h 10m/5m
#
# A size that fails is noted and the queue goes on with the next. Each size's results
# are in perf-results/<size>-<time>/; perf-results/queue-<time>.txt collects their
# summaries as they finish. For several machines at once, give each queue its own
# LABEL and PORT (its own fake central):
#   LABEL=rpi5 PORT=8091 perf/remote-queue.sh http://rpi5.local "100k 300k" 60 1h 10m/5m
set -uo pipefail
HUB=${1:?hub URL}
SIZES=${2:?sizes, e.g. "100k 300k 500k 700k"}
shift 2
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p perf-results
LOG=perf-results/queue-${LABEL:+$LABEL-}$(date +%Y%m%d-%H%M%S).txt
echo "queue against $HUB: $SIZES; remote-load.sh options: $*" | tee "$LOG"

up() { curl -sf -m 10 "$HUB/hub/status" >/dev/null; }

for size in $SIZES; do
	# The previous size's hub must be gone first, or this size would set it up.
	for _ in $(seq 120); do up || break; sleep 15; done
	if up; then
		echo "$(date +%FT%T) the hub at $HUB still answers 30 minutes after the last run: queue stopped" | tee -a "$LOG"
		exit 1
	fi
	echo "$(date +%FT%T) $size: started" | tee -a "$LOG"
	if "$HERE/remote-load.sh" "$HUB" "$size" "$@"; then status=finished; else status="failed (exit $?)"; fi
	echo "$(date +%FT%T) $size: $status" | tee -a "$LOG"
	dir=$(ls -dt perf-results/"${LABEL:+$LABEL-}$size"-* 2>/dev/null | head -1)
	[ -n "$dir" ] && [ -f "$dir/summary.txt" ] && { echo "--- $dir"; cat "$dir/summary.txt"; echo; } >>"$LOG"
done
echo "$(date +%FT%T) queue done" | tee -a "$LOG"
