#!/bin/sh
# Runs the hub in a container constrained like a Raspberry Pi (docs/PERFORMANCE.md).
# Run on the Docker host, from the repository root:
#
#   perf/pi.sh build                build the image (hub, fake central, hubload, benchmarks)
#   perf/pi.sh ladder pi5|pi4|none  the scale ladder: per data size a fresh hub, filled, the
#                                   realistic 60-user load, the hammer run, memory and CPU
#                                   sampled; results, hub logs and summary.txt in perf/results/ladder-*
#   perf/pi.sh bench pi5|pi4|none   calibration: storage benchmarks and fio in the same limits
#   perf/pi.sh up pi5|pi4|none      start the fake central (generated data) and the constrained hub on dev-net
#   perf/pi.sh load [hubload flags] the load generator against a running hub (cmd/hubload)
#   perf/pi.sh stats                CPU and memory of both containers, once
#   perf/pi.sh logs                 follow the hub's log
#   perf/pi.sh down                 stop and remove the containers (the hub's data volume stays)
#   perf/pi.sh reset                down, and delete the hub's data volume
#
# up and load use TES, ENROLLMENTS, EVENTS, USERS (default the busy HC IV target,
# docs/PERFORMANCE.md: 300000, 500000, 1200000, 60). The ladder's sizes:
# SIZES="name:tes:enrollments:events ..." (default 30k, 100k, the HC IV target with
# retention by programme "hc4", the same HC IV with all 5 years as today's hub keeps them
# "hc4-5y", and the pessimistic one), DURATION of the realistic load (5m), HAMMER of the hammer run (2m).
#
# MEM overrides the profile's memory limit (e.g. MEM=2g).
#
# RUN=name runs alongside other runs: its own containers (hub-pi-<name>,
# perf-central-<name>), volume and results folder (ladder-<profile>-<name>-…). Each run
# uses CPU, memory and disk of the same Docker VM, so parallel runs slow each other.
#
# IO=0 skips the disk limits (for instance if Docker refuses the device). DEVICE is the
# block device the limits apply to: in Docker Desktop, the VM's disk, /dev/vda.
set -eu

IMAGE=facility-hub-perf
SUF=${RUN:+-$RUN}
HUB=hub-pi$SUF
CEN=perf-central$SUF
VOL=hub-pi-data$SUF
NET=dev-net
DEVICE=${DEVICE:-/dev/vda}
IO=${IO:-1}
TES=${TES:-300000}
ENROLLMENTS=${ENROLLMENTS:-500000}
EVENTS=${EVENTS:-1200000}
USERS=${USERS:-60}

# Profile limits. --cpus caps total CPU time, not per-core speed: calibrate with
# "bench" against a real Pi and adjust (docs/PERFORMANCE.md, "Mimicking a Pi").
limits() {
	case "$1" in
	pi4) # Pi 4, 1 GB, SD card
		set -- --cpus 0.8 --memory 1g --memory-swap 1g
		[ "$IO" = 1 ] && set -- "$@" --device-write-bps "$DEVICE:10mb" --device-read-bps "$DEVICE:40mb" \
			--device-write-iops "$DEVICE:500" --device-read-iops "$DEVICE:1500"
		;;
	pi5) # Pi 5, 4 GB, USB SSD: CPU and memory only
		set -- --cpus 1.6 --memory 4g --memory-swap 4g ;;
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
	docker rm -f "$CEN" "$HUB" >/dev/null 2>&1 || true
	# The fake central is not constrained: it stands in for a national server.
	docker run -d --name "$CEN" --network "$NET" --entrypoint fakecentral "$IMAGE" -listen :8090 \
		-tes "$TES" -enrollments "$ENROLLMENTS" -events "$EVENTS" -users "$USERS" >/dev/null
	# shellcheck disable=SC2086
	conf=""
	if [ -n "$SUF" ]; then # the image's config names hub-pi and perf-central
		mkdir -p perf/results
		sed "s/perf-central/$CEN/; s/hub-pi/$HUB/" perf/hub.yaml >"perf/results/.hub$SUF.yaml"
		conf="-v $PWD/perf/results/.hub$SUF.yaml:/etc/hub/hub.yaml:ro"
	fi
	# shellcheck disable=SC2086
	docker run -d --name "$HUB" --network "$NET" $lim $conf -v "$VOL:/data" "$IMAGE" >/dev/null
	echo "hub: http://$HUB:8080 on $NET (profile $1: $lim; $TES TEs, $ENROLLMENTS enrollments, $EVENTS events, $USERS users)"
}

# load_ OUT [hubload flags]: OUT is a path under perf/results.
load_() {
	out=$1
	shift
	mkdir -p "$(dirname "$out")"
	# The load generator runs outside the hub's limits, like the clients it stands in for.
	docker run --rm --network "$NET" -v "$PWD/perf/results:/results" --entrypoint hubload "$IMAGE" \
		-hub "http://$HUB:8080" -central "http://$CEN:8090" -users "$USERS" \
		-out "/results/${out#perf/results/}" "$@"
}

# sample OUT: the hub container's memory and CPU every 10 seconds, until killed.
sample() {
	while true; do
		docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' "$HUB" 2>/dev/null | sed "s/^/$(date +%T) /"
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
	sizes=${SIZES:-"30k:30000:50000:120000 100k:100000:167000:400000 hc4:300000:500000:1200000 hc4-5y:300000:1200000:3100000 pessimistic:700000:1200000:3100000"}
	dir="perf/results/ladder-$prof$SUF-$(date +%Y%m%d-%H%M)"
	mkdir -p "$dir"
	for s in $sizes; do
		name=${s%%:*}
		rest=${s#*:}
		TES=${rest%%:*}
		rest=${rest#*:}
		ENROLLMENTS=${rest%%:*}
		EVENTS=${rest#*:}
		echo "== $name: $TES tracked entities, $ENROLLMENTS enrollments, $EVENTS events"
		docker rm -f "$CEN" "$HUB" >/dev/null 2>&1 || true
		docker volume rm "$VOL" >/dev/null 2>&1 || true
		up_ "$prof"
		sample "$dir/stats-$name.txt" &
		spid=$!
		# The realistic run is a normal working day: Android devices downloaded before it
		# and only sync. The hammer run keeps every first download at once (deployment day).
		load_ "$dir/load-$name-realistic.txt" -setup -fill -duration "${DURATION:-5m}" -android-first none || echo "realistic run failed"
		load_ "$dir/load-$name-hammer.txt" -duration "${HAMMER:-2m}" -think 0 || echo "hammer run failed"
		kill "$spid" 2>/dev/null || true
		docker logs "$HUB" >"$dir/hub-$name.log" 2>&1 || true
		{
			echo "== $name: $TES tracked entities, $ENROLLMENTS enrollments, $EVENTS events, $USERS users, $prof"
			echo "peak hub memory: $(peak "$dir/stats-$name.txt"); killed for memory: $(docker inspect -f '{{.State.OOMKilled}}' "$HUB" 2>/dev/null || echo unknown)"
			for run in realistic hammer; do
				echo "-- $run"
				grep -E "registered|first pull|working list|search by name|dashboard|new event|first download|download page|incremental sync|people in|upload|reconnect|failed" \
					"$dir/load-$name-$run.txt" 2>/dev/null || echo "(no report)"
			done
			echo
		} >>"$dir/summary.txt"
	done
	docker rm -f "$CEN" "$HUB" >/dev/null 2>&1 || true
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
	docker stats --no-stream "$HUB" "$CEN"
	;;
logs)
	docker logs -f "$HUB"
	;;
down)
	docker rm -f "$CEN" "$HUB"
	;;
reset)
	docker rm -f "$CEN" "$HUB" >/dev/null 2>&1 || true
	docker volume rm "$VOL"
	;;
*)
	sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
