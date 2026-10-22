#!/usr/bin/env bash
# The Mac's half of the automatic power-cut test (powercut.ps1 install on the VM):
# starts the UTM virtual machine, waits a random 4-8 minutes (Windows starts, the
# start-up task verifies the last cycle and takes saves offline), then cuts it hard,
# and again, N times. Most cuts kill the VM's process outright; every fourth one is
# UTM's "Force shut down", like holding a PC's power button.
#
#   perf/windows/powercut-host.sh "<VM name as UTM shows it>" [cycles, default 10]
#
# At the end the VM is started once more, so it verifies the last cut; then, on the
# VM: powercut.ps1 status, and powercut.ps1 uninstall.
set -euo pipefail
VM=$1
N=${2:-10}
UTMCTL=${UTMCTL:-/Applications/UTM.app/Contents/MacOS/utmctl}
"$UTMCTL" status "$VM" >/dev/null # fails here if the name is wrong

for i in $(seq "$N"); do
  if [ "$("$UTMCTL" status "$VM")" != started ]; then
    "$UTMCTL" start "$VM"
  fi
  wait=$((240 + RANDOM % 241))
  how=--kill
  if [ $((i % 4)) -eq 0 ]; then how=--force; fi
  echo "$(date +%T) cycle $i of $N: cut in ${wait}s ($how)"
  sleep "$wait"
  "$UTMCTL" stop "$how" "$VM"
  sleep 15
done
"$UTMCTL" start "$VM"
echo "$(date +%T) done: the VM now verifies the last cut. On the VM: powercut.ps1 status"
