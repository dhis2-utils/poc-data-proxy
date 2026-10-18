#!/usr/bin/env python3
"""Adds demo hubs to a TEST DHIS2 (Sierra Leone database) for trying the Facility Hub app.

Writes registry entries, heartbeats with 90 days of history, and a few refused changes,
for hubs at Sierra Leone facilities, each showing a different state: healthy, silent,
refused changes and low disk, USB stick missing and a failed upgrade, retired. Their
hub IDs start with "demo-"; --remove deletes them again. Never run it against a real
central: the app would show these hubs to districts.

    scripts/demo-facility-hubs.py http://dhis2-agent-x:8080 admin district --test-instance
"""
import base64
import datetime as dt
import json
import os
import random
import sys
import urllib.error
import urllib.request

FACILITIES = [  # (facility, its state)
    ("GvFqTavdpGE", "healthy"),        # Agape CHP, Bo
    ("rx9ubw0UCqj", "silent"),         # Bandajuma MCHP, Bo
    ("agM0BKQlTh3", "refused"),        # Batkanu CHC, Bombali
    ("GHHvGp7tgtZ", "usb-upgrade"),    # Binkolo CHC, Bombali
    ("c41XRVOYNJm", "retired"),        # Baiima CHP, Kailahun
    ("uFp0ztDOFbI", "waiting"),        # Bendu CHC, Bonthe: waits for its own account
]

# The waiting hub's box key: the public half of 32 x 0x11 (a demo, not a secret). Its
# account code is printed, to try an approval in the app; no hub takes the account up.
WAITING_BOX_KEY = "e06Qm75//kTEZaIgA31gjuNYl9Me+XLwf3SJLLD3PxM="


def account_code(pub, hub, facility):
    """AccountCode of docs/FACILITY-HUB-APP.md."""
    import hashlib
    digest = hashlib.sha256(b"facility-hub account code\0" + hub.encode() + b"\0" + facility.encode() + b"\0" + pub).digest()
    code = base64.b32encode(digest).decode()[:16]
    return "-".join(code[i:i + 4] for i in range(0, 16, 4))


def main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    if len(args) != 3 or "--test-instance" not in sys.argv:
        sys.exit(__doc__)
    base, user, password = args
    auth = "Basic " + base64.b64encode(f"{user}:{password}".encode()).decode()

    def req(method, path, body=None):
        r = urllib.request.Request(base.rstrip("/") + "/api" + path, method=method,
                                   data=json.dumps(body).encode() if body is not None else None)
        r.add_header("Authorization", auth)
        r.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(r) as resp:
                return resp.status
        except urllib.error.HTTPError as e:
            return e.code

    def put(ns, key, value):
        if req("PUT", f"/dataStore/{ns}/{key}", value) == 404:
            req("POST", f"/dataStore/{ns}/{key}", value)

    now = dt.datetime.now(dt.timezone.utc)
    iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")
    rnd = random.Random(42)
    for i, (facility, state) in enumerate(FACILITIES):
        hub = f"demo-hub-{i + 1}"
        if "--remove" in sys.argv:
            for ns in ("facility-hub-registry", "facility-hub-status"):
                req("DELETE", f"/dataStore/{ns}/{hub}")
            for n in range(1, 4):
                req("DELETE", f"/dataStore/facility-hub-issues/{hub}-{n}")
            continue
        reg = {
            "hubId": hub, "facility": facility, "status": "retired" if state == "retired" else "active",
            "setupBy": "demo", "setupAt": iso(now - dt.timedelta(days=120)), "hubVersion": "0.1.0",
            "demo": True,
        }
        if state == "waiting":
            reg.update(accountRequested=True, boxKey=WAITING_BOX_KEY)
            print(f"{hub} waits for approval; its account code: "
                  f"{account_code(base64.b64decode(WAITING_BOX_KEY), hub, facility)}")
        put("facility-hub-registry", hub, reg)
        last = now - (dt.timedelta(days=3) if state == "silent" else dt.timedelta(minutes=12))
        history = []
        for d in range(90, 0, -1):
            day = (last - dt.timedelta(days=d - 1)).date().isoformat()
            off = rnd.choice([0, 0, 0, 15, 40, 180, 600])
            history.append({"date": day, "syncs": 90 - off // 20, "outages": 1 if off else 0,
                            "offlineMinutes": off, "longestOfflineMinutes": off,
                            "pendingPeak": rnd.randint(0, 40), "diskFreeMinBytes": 40_000_000_000})
        put("facility-hub-status", hub, {
            "hubId": hub, "facility": facility, "version": "0.1.0", "serverVersion": "2.42.6",
            "time": iso(last), "lastPush": iso(last), "lastSync": iso(last), "lastError": "",
            "pending": 12 if state == "silent" else 0, "pendingOutbox": 12 if state == "silent" else 0,
            "stuckOutbox": 0, "issues": 3 if state == "refused" else 0,
            "diskFreeBytes": 1_200_000_000 if state == "refused" else 41_000_000_000,
            "usb": {"present": state != "usb-upgrade", "writable": state != "usb-upgrade",
                    "lastCopy": iso(last)},
            "hubTokenOwner": "demo", "hubTokenExpires": iso(now + dt.timedelta(days=60)),
            "lastUpgrade": ({"result": "failed", "from": "0.1.0", "to": "0.2.0",
                             "reason": "health check failed after the swap; rolled back", "at": iso(last)}
                            if state == "usb-upgrade" else None),
            "needsUpgrade": False, "history": history, "demo": True,
        })
        if state == "refused":
            for n in range(1, 4):
                put("facility-hub-issues", f"{hub}-{n}", {
                    "hub_id": hub, "facility": facility, "user": "demo", "objectUid": f"DemoEvent0{n}",
                    "kind": "event", "outboxSeq": n, "createdAt": iso(last - dt.timedelta(hours=n)),
                    "error": {"message": "Value *** is not a valid option for data element",
                              "errorReports": [{"errorCode": "E1125",
                                                "message": "Value `***` is not a valid option code for data element `Hz6sDTFMbhn`"}]},
                })
    print("removed" if "--remove" in sys.argv else f"added {len(FACILITIES)} demo hubs")


if __name__ == "__main__":
    main()
