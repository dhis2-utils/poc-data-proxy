#!/bin/sh
# Runs the hub in a container constrained like a Raspberry Pi (docs/PERFORMANCE.md).
# Run on the Docker host, from the repository root:
#
#   perf/pi.sh build               build the image (hub, fake central, benchmarks)
#   perf/pi.sh up pi5|pi4|none     start the fake central and the constrained hub on dev-net
#   perf/pi.sh bench pi5|pi4|none  calibration: storage benchmarks and fio in the same limits
#   perf/pi.sh stats               CPU and memory of both containers, once
#   perf/pi.sh logs                follow the hub's log
#   perf/pi.sh down                stop and remove the containers (the hub's data volume stays)
#   perf/pi.sh reset               down, and delete the hub's data volume
#
# IO=0 skips the disk limits (for instance if Docker refuses the device). DEVICE is the
# block device the limits apply to: in Docker Desktop, the VM's disk, /dev/vda.
set -eu

IMAGE=facility-hub-perf
NET=dev-net
DEVICE=${DEVICE:-/dev/vda}
IO=${IO:-1}

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
	echo "$@"
}

cmd=${1:-}
case "$cmd" in
build)
	docker build -f perf/Dockerfile -t "$IMAGE" .
	;;
up)
	lim=$(limits "${2:-pi5}")
	docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET"
	docker rm -f perf-central hub-pi >/dev/null 2>&1 || true
	# The fake central is not constrained: it stands in for a national server.
	docker run -d --name perf-central --network "$NET" --entrypoint fakecentral "$IMAGE" -listen :8090
	# shellcheck disable=SC2086
	docker run -d --name hub-pi --network "$NET" $lim -v hub-pi-data:/data "$IMAGE"
	echo "hub: http://hub-pi:8080 on $NET (profile ${2:-pi5}: $lim)"
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
	sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
