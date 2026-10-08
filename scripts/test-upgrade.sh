#!/usr/bin/env bash
# Scenario 11 (spec 13.3): hub self-upgrade against a local release channel.
#
#   1. An unsigned release is ignored.
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
go build -ldflags "-X main.version=0.1.0" -o "$WORK/hub-0.1.0" ./cmd/hub
go build -ldflags "-X main.version=0.2.0" -o "$WORK/hub-0.2.0" ./cmd/hub
go build -ldflags "-X main.version=0.3.0 -X main.healthFail=true" -o "$WORK/hub-0.3.0" ./cmd/hub
"$WORK/hub-0.1.0" keygen -out "$WORK/release"
PLAT="$(go env GOOS)/$(go env GOARCH)"

mkdir -p "$WORK/channel" "$WORK/run/bin"
cp "$WORK/hub-0.1.0" "$WORK/run/bin/hub"
cat > "$WORK/hub.yaml" <<EOF
central_url: http://127.0.0.1:$FC_PORT
setup_user_group: HubGrpSetup
listen: "127.0.0.1:$HUB_PORT"
data_dir: $WORK/run/data
sync_interval: 1h
ping_interval: 2s
tls: false
reserved_pool_size: 5
release_channel: http://127.0.0.1:$CH_PORT
release_public_key: $(cat "$WORK/release.pub")
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
sync_now() { curl -s -o /dev/null -b "$WORK/admin.jar" -X POST -H "Origin: $H" "$H/hub/ui/sync"; }
is() { [ "$(status "$1")" = "$2" ]; } # is <status field> <value>
wait_for() { # wait_for <seconds> <command...>
  local n=$1; shift
  for _ in $(seq $((n * 5))); do if "$@" >/dev/null 2>&1; then return 0; fi; sleep 0.2; done
  return 1
}

say "setting up the hub as hub_admin"
curl -s -c "$WORK/admin.jar" -o "$WORK/setup.html" -X POST "$H/hub/setup" --data-urlencode step=login \
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
wait_for 10 is mode offline || fail "hub did not go offline"
curl -s -b "$WORK/nurse.jar" -X POST "$H/api/tracker?async=false" -H 'Content-Type: application/json' -d '{"trackedEntities":[{"trackedEntity":"UpgTe000001","trackedEntityType":"nEenWmSyUEp","orgUnit":"DiszpKrYNg8","attributes":[{"attribute":"w75KJ2mc4zz","value":"Upgrade"}]}]}' | grep -q '"status":"OK"' || fail "offline write"
[ "$(status pending)" = 1 ] || fail "expected 1 pending object"

say "1. unsigned release 0.2.0 is ignored"
"$WORK/hub-0.1.0" release -dir "$WORK/channel" -version 0.2.0 -unsigned "$PLAT=$WORK/hub-0.2.0"
curl -s -X POST "$FC/fake/up" >/dev/null
wait_for 10 is mode online || fail "hub did not come back online"
sync_now
wait_for 15 is pending 0 || fail "outbox not pushed"
sync_now   # second cycle: heartbeat carries the upgrade result of the first
wait_for 15 sh -c "curl -s -u hub_admin:Hub-Test-2026! $FC/api/dataStore/facility-hub-status/$HUB_ID | grep -q ignored" || fail "no 'ignored' in heartbeat"
[ "$(status version)" = 0.1.0 ] || fail "unsigned release was installed"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"

say "an entry whose author central no longer accepts is stuck"
curl -s -X POST "$FC/fake/down" >/dev/null
wait_for 10 is mode offline || fail "hub did not go offline"
curl -s -b "$WORK/nurse_b.jar" -X POST "$H/api/tracker?async=false" -H 'Content-Type: application/json' -d '{"trackedEntities":[{"trackedEntity":"UpgTe000002","trackedEntityType":"nEenWmSyUEp","orgUnit":"DiszpKrYNg8","attributes":[{"attribute":"w75KJ2mc4zz","value":"Stuck"}]}]}' | grep -q '"status":"OK"' || fail "offline write by nurse_b"
curl -s -X POST "$FC/fake/revoke?user=nurse_b" >/dev/null
curl -s -X POST "$FC/fake/up" >/dev/null
wait_for 10 is mode online || fail "hub did not come back online"
sync_now
wait_for 15 is stuckOutbox 1 || fail "stuck entry not reported"

say "2. signed release 0.2.0 installs and passes the health check"
"$WORK/hub-0.1.0" release -key "$WORK/release.key" -dir "$WORK/channel" -version 0.2.0 "$PLAT=$WORK/hub-0.2.0"
sync_now
wait_for 30 is version 0.2.0 || fail "did not upgrade to 0.2.0"
kill -0 $HUB_PID || fail "hub process gone (re-exec should keep the PID)"
[ "$(status stuckOutbox)" = 1 ] || fail "stuck entry lost in the upgrade"
[ "$(status locked)" = False ] || fail "store locked after upgrade (data key hand-over failed)"
curl -sf -b "$WORK/nurse.jar" "$H/hub/status" >/dev/null
wait_for 10 sh -c "grep -q 'upgrade result=ok' $WORK/hub.log" || fail "no ok result logged"
sync_now
wait_for 15 sh -c "curl -s -u hub_admin:Hub-Test-2026! $FC/api/dataStore/facility-hub-status/$HUB_ID | grep -q '\"result\":\"ok\"'" || fail "heartbeat does not report ok"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"

say "3. signed release 0.3.0 fails its health check and rolls back"
"$WORK/hub-0.1.0" release -key "$WORK/release.key" -dir "$WORK/channel" -version 0.3.0 "$PLAT=$WORK/hub-0.3.0"
# Log in again: the admin session lived in the old process's memory.
curl -s -c "$WORK/admin.jar" -X POST "$H/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"hub_admin","password":"Hub-Test-2026!"}' >/dev/null
sync_now
wait_for 30 sh -c "grep -q 'health check failed' $WORK/hub.log" || fail "0.3.0 health check did not run"
wait_for 30 sh -c "grep -q 'upgrade result=failed' $WORK/hub.log" || fail "rollback not recorded"
wait_for 10 is version 0.2.0 || fail "not rolled back to 0.2.0"
[ "$(status locked)" = False ] || fail "store locked after rollback"
curl -s -c "$WORK/admin.jar" -X POST "$H/api/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"hub_admin","password":"Hub-Test-2026!"}' >/dev/null
sync_now
wait_for 15 sh -c "curl -s -u hub_admin:Hub-Test-2026! $FC/api/dataStore/facility-hub-status/$HUB_ID | grep -q '\"result\":\"failed\"'" || fail "heartbeat does not report the failure"
echo "heartbeat lastUpgrade: $(heartbeat_upgrade)"
grep -E "upgrad|rolling back|health" "$WORK/hub.log" | cut -c1-200

say "PASS: scenario 11"
