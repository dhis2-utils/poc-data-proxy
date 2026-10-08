#!/bin/sh
# Runs the hub in a container constrained like a Raspberry Pi (docs/PERFORMANCE.md).
# Run on the Docker host, from the repository root:
#
#   perf/pi.sh build                build the image (hub, fake central, hubload, benchmarks)
#   perf/pi.sh ladder pi5|pi4|none  the scale ladder: per data size a fresh hub, filled, the
#                                   realistic 50-user load, the hammer run, memory and CPU
#                                   sampled; results and summary.txt in perf/results/ladder-*
#   perf/pi.sh bench pi5|pi4|none   calibration: storage benchmarks and fio in the same limits
#   perf/pi.sh up pi5|pi4|none      start the fake central (generated data) and the constrained hub on dev-net
#   perf/pi.sh load [hubload flags] the load generator against a running hub (cmd/hubload)
#   perf/pi.sh stats                CPU and memory of both containers, once
#   perf/pi.sh logs                 follow the hub's log
#   perf/pi.sh down                 stop and remove the containers (the hub's data volume stays)
#   perf/pi.sh reset                down, and delete the hub's data volume
#
# up and load use TES, ENROLLMENTS, EVENTS, USERS (default the plan's target: 100000,
# 150000, 500000, 50). The ladder's sizes: SIZES="pct:tes:enrollments:events ..." (default
# 10, 25, 50, 100 and 200 % of the target), DURATION of the realistic load (5m), HAMMER of
# the hammer run (2m).
#
# MEM overrides the profile's memory limit (e.g. MEM=6g for pi5 in an 8 GB Docker VM).
#
# IO=0 skips the disk limits (for instance if Docker refuses the device). DEVICE is the
# block device the limits apply to: in Docker Desktop, the VM's disk, /dev/vda.
set -eu

IMAGE=facility-hub-perf
NET=dev-net
DEVICE=${DEVICE:-/dev/vda}
IO=${IO:-1}
TES=${TES:-100000}
ENROLLMENTS=${ENROLLMENTS:-150000}
EVENTS=${EVENTS:-500000}
USERS=${USERS:-50}

# Profile limits. --cpus caps total CPU time, not per-core speed: calibrate with
# "bench" against a real Pi and adjust (docs/PERFORMANCE.md, "Mimicking a Pi").
limits() {
	case "$1" in
	pi4) # Pi 4, 4 GB, SD card
		set -- --cpus 0.8 --memory 4g --memory-swap 4g
		[ "$IO" = 1 ] && set -- "$@" --device-write-bps "$DEVICE:10mb" --device-read-bps "$DEVICE:40mb" \
			--device-write-iops "$DEVICE:500" --device-read-iops "$DEVICE:1500"
		;;
	pi5) # Pi 5, 8 GB, USB SSD: CPU and memory only
		set -- --cpus 1.6 --memory 8g --memory-swap 8g ;;
	none) set -- ;;
	*) echo "profile must be pi4, pi5 or none" >&2; exit 2 ;;
	esac
	if [ -n "${MEM:-}" ]; then # a smaller memory limit, e.g. when the Docker VM itself is small
		out=""
		while [ $# -gt 0 ]; do
			case "$1" in
			--memory | --memory-swap) out="$out $1 $MEM"; shift ;;
			*) out="$out $1" ;;
			esac
			shift
		done
		echo "$out"
		return
	fi
	echo "$@"
}

up_() {
	lim=$(limits "$1")
	docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET"
	docker rm -f perf-central hub-pi >/dev/null 2>&1 || true
	# The fake central is not constrained: it stands in for a national server.
	docker run -d --name perf-central --network "$NET" --entrypoint fakecentral "$IMAGE" -listen :8090 \
		-tes "$TES" -enrollments "$ENROLLMENTS" -events "$EVENTS" -users "$USERS" >/dev/null
	# shellcheck disable=SC2086
	docker run -d --name hub-pi --network "$NET" $lim -v hub-pi-data:/data "$IMAGE" >/dev/null
	echo "hub: http://hub-pi:8080 on $NET (profile $1: $lim; $TES TEs, $ENROLLMENTS enrollments, $EVENTS events, $USERS users)"
}

# load_ OUT [hubload flags]: OUT is a path under perf/results.
load_() {
	out=$1
	shift
	mkdir -p "$(dirname "$out")"
	# The load generator runs outside the hub's limits, like the clients it stands in for.
	docker run --rm --network "$NET" -v "$PWD/perf/results:/results" --entrypoint hubload "$IMAGE" \
		-hub http://hub-pi:8080 -central http://perf-central:8090 -users "$USERS" \
		-out "/results/${out#perf/results/}" "$@"
}

# sample OUT: the hub container's memory and CPU every 10 seconds, until killed.
sample() {
	while true; do
		docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' hub-pi 2>/dev/null | sed "s/^/$(date +%T) /"
		sleep 10
	done >"$1"
}

# peak FILE: the highest memory use in a sample file, in MiB.
peak() {
	awk '{ v = $2; u = v; sub(/^[0-9.]+/, "", u); sub(/[A-Za-z]+$/, "", v)
		m = (u == "GiB") ? v * 1024 : (u == "KiB" ? v / 1024 : v + 0); if (m > p) p = m }
		END { printf "%.0f MiB", p }' "$1"
}

cmd=${1:-}
case "$cmd" in
build)
	docker build -f perf/Dockerfile -t "$IMAGE" .
	;;
up)
	up_ "${2:-pi5}"
	;;
load)
	shift
	out="perf/results/load-$(date +%Y%m%d-%H%M%S).txt"
	load_ "$out" "$@"
	echo "saved to $out"
	;;
ladder)
	prof=${2:-pi5}
	limits "$prof" >/dev/null
	sizes=${SIZES:-"10:10000:15000:50000 25:25000:37500:125000 50:50000:75000:250000 100:100000:150000:500000 200:200000:300000:1000000"}
	dir="perf/results/ladder-$prof-$(date +%Y%m%d-%H%M)"
	mkdir -p "$dir"
	for s in $sizes; do
		pct=${s%%:*}
		rest=${s#*:}
		TES=${rest%%:*}
		rest=${rest#*:}
		ENROLLMENTS=${rest%%:*}
		EVENTS=${rest#*:}
		echo "== $pct %: $TES tracked entities, $ENROLLMENTS enrollments, $EVENTS events"
		docker rm -f perf-central hub-pi >/dev/null 2>&1 || true
		docker volume rm hub-pi-data >/dev/null 2>&1 || true
		up_ "$prof"
		sample "$dir/stats-$pct.txt" &
		spid=$!
		load_ "$dir/load-$pct-realistic.txt" -setup -fill -duration "${DURATION:-5m}" || echo "realistic run failed"
		load_ "$dir/load-$pct-hammer.txt" -duration "${HAMMER:-2m}" -think 0 || echo "hammer run failed"
		kill "$spid" 2>/dev/null || true
		{
			echo "== $pct %: $TES tracked entities, $ENROLLMENTS enrollments, $EVENTS events, $USERS users, $prof"
			echo "peak hub memory: $(peak "$dir/stats-$pct.txt"); killed for memory: $(docker inspect -f '{{.State.OOMKilled}}' hub-pi 2>/dev/null || echo unknown)"
			for run in realistic hammer; do
				echo "-- $run"
				grep -E "registered|first pull|working list|search by name|dashboard|new event|full download|download page|upload|reconnect|failed" \
					"$dir/load-$pct-$run.txt" 2>/dev/null || echo "(no report)"
			done
			echo
		} >>"$dir/summary.txt"
	done
	docker rm -f perf-central hub-pi >/dev/null 2>&1 || true
	echo "summary: $dir/summary.txt"
	;;
bench)
	lim=$(limits "${2:-pi5}")
	mkdir -p perf/results
	out="perf/results/bench-${2:-pi5}-$(date +%Y%m%d-%H%M%S).txt"
	# shellcheck disable=SC2086
	docker run --rm $lim -v hub-pi-bench:/bench -e TMPDIR=/bench --entrypoint sh "$IMAGE" -c '
		echo "== storage benchmarks"; store.bench -test.run "^$" -test.bench . -test.benchtime 2000x
		echo "== fio 4k random write"; fio --name=w --directory=/bench --rw=randwrite --bs=4k --size=256m --runtime=30 --time_based --direct=1 --group_reporting | grep -E "IOPS|BW="
		echo "== fio 4k random read"; fio --name=r --directory=/bench --rw=randread --bs=4k --size=256m --runtime=30 --time_based --direct=1 --group_reporting | grep -E "IOPS|BW="
		rm -f /bench/*' | tee "$out"
	echo "saved to $out"
	;;
stats)
	docker stats --no-stream hub-pi perf-central
	;;
logs)
	docker logs -f hub-pi
	;;
down)
	docker rm -f perf-central hub-pi
	;;
reset)
	docker rm -f perf-central hub-pi >/dev/null 2>&1 || true
	docker volume rm hub-pi-data
	;;
*)
	sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
