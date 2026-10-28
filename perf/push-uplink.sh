#!/usr/bin/env bash
# How fast the hub pushes a backlog to a real DHIS2 over a slow uplink, with and
# without staff saving meanwhile (docs/PERFORMANCE.md, "Push over a slow uplink").
# Runs internal/adversarial.TestPushOverSlowUplink once per case, one after another.
#
#   HUB_MANIFEST=testresults/synthetic-42/manifest.json perf/push-uplink.sh [case ...]
#
# Cases are link:saves, the link one of
#   lan    unlimited, no latency added
#   4g     3 Mbit/s each way (375 000 bytes/s), 100 ms per request
#   poor   1 Mbit/s each way (125 000 bytes/s), 300 ms per request
# and saves the clients' saves a second while the hub pushes (0: none). Default:
# lan:0 4g:0 4g:0.4 4g:1.2 4g:2 poor:0 poor:2. Results go to perf/results/push-<time>/.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${HUB_MANIFEST:?set HUB_MANIFEST to a hubtest manifest}"
cases=("$@")
[ ${#cases[@]} -gt 0 ] || cases=(lan:0 4g:0 4g:0.4 4g:1.2 4g:2 poor:0 poor:2)
out=perf/results/push-$(date +%Y%m%d-%H%M)
mkdir -p "$out"
for c in "${cases[@]}"; do
	link=${c%%:*} saves=${c#*:}
	case $link in
	lan) spec= ;;
	4g) spec=375000,100ms ;;
	poor) spec=125000,300ms ;;
	*) echo "unknown link $link" >&2; exit 2 ;;
	esac
	echo "== $c"
	HUB_PUSH=1 HUB_PUSH_LINK=$spec HUB_PUSH_SAVES=$saves \
		go test ./internal/adversarial/ -run 'TestPushOverSlowUplink$' -v -count=1 -timeout 40m 2>&1 |
		grep -E 'push_rate_test|^(--- |ok|FAIL)' | tee "$out/$link-$saves.txt"
done
echo "results in $out"
