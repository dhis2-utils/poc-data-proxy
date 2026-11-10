#!/usr/bin/env bash
# Scenario 11 (spec 13.3): hub self-upgrade, from the version chosen on central
# (facility-hub-admin/release) and a local release source laid out like GitHub's
# (v<version>/release.json, its signature and the binaries).
#
#   1. A release whose signature cannot be fetched is tried again; one with a bad
#      signature is ignored.
#   2. A signed release: the hub pushes its outbox, migrates a store copy, swaps the
#      binary, re-execs, passes the health check and reports "ok" in its heartbeat.
#      An outbox entry whose author central no longer accepts does not hold it back.
#   3. A signed release that fails its health check: the hub rolls back to the previous
#      binary and store and reports "failed" in its heartbeat.
#
# Runs against cmd/fakecentral; needs go, curl and python3. Usage: scripts/test-upgrade.sh
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
WORK=$(mktemp -d)
FC_PORT=${FC_PORT:-18090}
CH_PORT=${CH_PORT:-18091}
HUB_PORT=${HUB_PORT:-18443}
PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT
say() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*"; echo "--- hub log"; tail -40 "$WORK/hub.log"; exit 1; }

say "building (work dir $WORK)"
go build -o "$WORK/fakecentral" ./cmd/fakecentral
go build -o "$WORK/hub-tool" ./cmd/hub
"$WORK/hub-tool" keygen -out "$WORK/release"
UP=github.com/dhis2/facility-hub/internal/upgrade
REL="-X $UP.ReleaseKey=$(cat "$WORK/release.pub") -X $UP.ReleaseBase=http://127.0.0.1:$CH_PORT"
go build -ldflags "-X main.version=0.1.0 $REL" -o "$WORK/hub-0.1.0" ./cmd/hub
go build -ldflags "-X main.version=0.2.0 $REL" -o "$WORK/hub-0.2.0" ./cmd/hub
go build -ldflags "-X main.version=0.3.0 -X main.healthFail=true $REL" -o "$WORK/hub-0.3.0" ./cmd/hub
PLAT="$(go env GOOS)/$(go env GOARCH)"

mkdir -p "$WORK/channel" "$WORK/run/bin"
cp "$WORK/hub-0.1.0" "$WORK/run/bin/hub"
cat > "$WORK/hub.yaml" <<EOF
central_url: http://127.0.0.1:$FC_PORT
listen: "127.0.0.1:$HUB_PORT"
data_dir: $WORK/run/data
sync_interval: 1h
ping_interval: 2s
tls: false
reserved_pool_size: 5
EOF

"$WORK/fakecentral" -listen "127.0.0.1:$FC_PORT" > "$WORK/fc.log" 2>&1 & PIDS+=($!)
(cd "$WORK/channel" && exec python3 -m http.server "$CH_PORT" --bind 127.0.0.1) > "$WORK/channel.log" 2>&1 & PIDS+=($!)
"$WORK/run/bin/hub" serve -config "$WORK/hub.yaml" > "$WORK/hub.log" 2>&1 & HUB_PID=$!; PIDS+=($HUB_PID)
H=http://127.0.0.1:$HUB_PORT
FC=http://127.0.0.1:$FC_PORT
for _ in $(seq 50); do curl -sf "$H/hub/status" >/dev/null && break; sleep 0.2; done

status() { curl -s "$H/hub/status" | python3 -c "import json,sys; print(json.load(sys.stdin)['$1'])"; }
heartbeat_upgrade() {
  curl -s -u hub_admin:Hub-Test-2026! "$FC/api/dataStore/facility-hub-status/$HUB_ID" |
    python3 -c "import json,sys; u=json.load(sys.stdin).get('lastUpgrade') or {}; print(u.get('result',''), u.get('to',''), u.get('reason',''))"
}
choose() { # choose <version> [pilot hub]: the country's choice on central, every hub
  local m=PUT pilots=${2:+\"$2\"}
  curl -sf -o /dev/null -u hub_admin:Hub-Test-2026! "$FC/api/dataStore/facility-hub-admin/release" || m=POST
  curl -sf -o /dev/null -u hub_admin:Hub-Test-2026! -X $m -H 'Content-Type: application/json' \
    "$FC/api/dataStore/facility-hub-admin/release" -d "{\"version\":\"$1\",\"pilotHubs\":[$pilots],\"percent\":100}"
}
publish() { "$WORK/hub-tool" release -key "$WORK/release.key" -dir "$WORK/channel/v$1" -version "$1" "$PLAT=$WORK/hub-$1"; }
admin_login() {
  curl -s -c "$WORK/admin.jar" -X POST "$H/api/auth/login" -H 'Content-Type: application/json' \
    -d '{"username":"hub_admin","password":"Hub-Test-2026!"}' >/dev/null
}
sync_now() { curl -s -o /dev/null -b "$WORK/admin.jar" -X POST -H "Origin: $H" "$H/hub/ui/sync"; }
is() { [ "$(status "$1")" = "$2" ]; } # is <status field> <value>
wait_for() { # wait_for <seconds> <command...>
  local n=$1; shift
  for _ in $(seq $((n * 5))); do if "$@" >/dev/null 2>&1; then return 0; fi; sleep 0.2; done
  return 1
}

say "setting up the hub as hub_admin"
curl -s -c "$WORK/admin.jar" -o "$WORK/setup.html" -X POST -H "Origin: $H" "$H/hub/setup" --data-urlencode step=login \
  --data-urlencode "central_url=$FC" --data-urlencode username=hub_admin --data-urlencode 'password=Hub-Test-2026!'
grep -q "This hub now serves" "$WORK/setup.html" || fail "setup"
HUB_ID=$(status hubId)
curl -s -c "$WORK/nurse.jar" -X POST "$H/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"nurse_a","password":"Hub-Test-2026!"}' | grep -q SUCCESS || fail "nurse_a login"
curl -s -c "$WORK/nurse_b.jar" -X POST "$H/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"nurse_b","password":"Hub-Test-2026!"}' | grep -q SUCCESS || fail "nurse_b login"
echo "hub $HUB_ID version $(status version)"

say "queueing offline data so the upgrade has an outbox to push"
curl -s -X POST "$FC/fake/down" >/dev/null
wait_for 30 is mode offline || fail "hub did not go offline"
curl -s -b "$WORK/nurse.jar" -X POST "$H/api/tracker?async=false" -H 'Content-Type: application/json' -d '{"trackedEntities":[{"trackedEntity":"UpgTe000001","trackedEntityType":"nEenWmSyUEp","orgUnit":"DiszpKrYNg8","attributes":[{"attribute":"w75KJ2mc4zz","value":"Upgrade"}]}]}' | grep -q '"status":"OK"' || fail "offline write"
[ "$(status pending)" = 1 ] || fail "expected 1 pending object"

say "1. release 0.2.0 without its signature is tried again; with a bad one, ignored"
publish 0.2.0
rm "$WORK/channel/v0.2.0/release.json.sig"
curl -s -X POST "$FC/fake/up" >/dev/null
choose 0.2.0
wait_for 30 is mode online || fail "hub did not come back online"
sync_now
wait_for 15 is pending 0 || fail "outbox not pushed"
# A signature that is not there (yet) is not a bad one: nothing is recorded, and the
# next check fetches it again (review R-12).
wait_for 15 sh -c "grep -q 'release.json.sig: HTTP 404' $WORK/hub.log" || fail "missing signature not reported in the log"
grep -q 'upgrade result=' "$WORK/hub.log" && fail "a missing signature was recorded as a result"
head -c 64 /dev/urandom | base64 > "$WORK/channel/v0.2.0/release.json.sig"
sync_now
# The hub logs the result at once; the heartbeat carries it within the hour (here no
# restart sends one sooner, as after steps 2 and 3).
wait_for 15 sh -c "grep -q 'upgrade result=ignored' $WORK/hub.log" || fail "no 'ignored' result logged"
[ "$(status version)" = 0.1.0 ] || fail "badly signed release was installed"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"

say "an entry whose author central no longer accepts is stuck"
curl -s -X POST "$FC/fake/down" >/dev/null
wait_for 30 is mode offline || fail "hub did not go offline"
curl -s -b "$WORK/nurse_b.jar" -X POST "$H/api/tracker?async=false" -H 'Content-Type: application/json' -d '{"trackedEntities":[{"trackedEntity":"UpgTe000002","trackedEntityType":"nEenWmSyUEp","orgUnit":"DiszpKrYNg8","attributes":[{"attribute":"w75KJ2mc4zz","value":"Stuck"}]}]}' | grep -q '"status":"OK"' || fail "offline write by nurse_b"
curl -s -X POST "$FC/fake/revoke?user=nurse_b" >/dev/null
curl -s -X POST "$FC/fake/up" >/dev/null
wait_for 30 is mode online || fail "hub did not come back online"
sync_now
wait_for 15 is stuckOutbox 1 || fail "stuck entry not reported"

say "2. signed release 0.2.0 installs and passes the health check"
publish 0.2.0
# A refused manifest is not fetched again until the country's choice changes (G3):
# republished, the release is chosen again, this time naming the hub as a pilot.
choose 0.2.0 "$HUB_ID"
sync_now
wait_for 30 is version 0.2.0 || fail "did not upgrade to 0.2.0"
kill -0 $HUB_PID || fail "hub process gone (re-exec should keep the PID)"
[ "$(status stuckOutbox)" = 1 ] || fail "stuck entry lost in the upgrade"
[ "$(status locked)" = False ] || fail "store locked after upgrade (data key hand-over failed)"
curl -sf -b "$WORK/nurse.jar" "$H/hub/status" >/dev/null
wait_for 10 sh -c "grep -q 'upgrade result=ok' $WORK/hub.log" || fail "no ok result logged"
admin_login # the admin session lived in the old process's memory
sync_now
wait_for 15 sh -c "curl -s -u hub_admin:Hub-Test-2026! $FC/api/dataStore/facility-hub-status/$HUB_ID | grep -q '\"result\":\"ok\"'" || fail "heartbeat does not report ok"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"

say "3. signed release 0.3.0 fails its health check and rolls back"
publish 0.3.0
choose 0.3.0
admin_login # the admin session lived in the old process's memory
sync_now
wait_for 30 sh -c "grep -q 'health check failed' $WORK/hub.log" || fail "0.3.0 health check did not run"
wait_for 30 sh -c "grep -q 'upgrade result=failed' $WORK/hub.log" || fail "rollback not recorded"
wait_for 10 is version 0.2.0 || fail "not rolled back to 0.2.0"
[ "$(status locked)" = False ] || fail "store locked after rollback"
admin_login
sync_now
wait_for 15 sh -c "curl -s -u hub_admin:Hub-Test-2026! $FC/api/dataStore/facility-hub-status/$HUB_ID | grep -q '\"result\":\"failed\"'" || fail "heartbeat does not report the failure"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"
grep -E "upgrad|rolling back|health" "$WORK/hub.log" | cut -c1-200

say "PASS: scenario 11"
