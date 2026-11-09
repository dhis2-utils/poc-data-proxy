#!/usr/bin/env python3
"""Seed an eRegisters instance (clients/eregisters_setup.py) for the central load
measurement (internal/perf/central_work_test.go, docs/PERFORMANCE.md "Central's load
from one facility, three ways").

usage: python3 -I central_load_seed.py MANIFEST.json PEOPLE

- adds staff so the facility has ten (ereg_nurse1..10, nurse3..10 new), each allowed
  to open both eRegisters and Capture web;
- imports PEOPLE people in Medical Registers at the facility, each enrolled once with
  1 to 7 Medical Visit events over the last two years (13 values each, as a short
  outpatient visit), 100 people per request, without program rules (only the seed);
- writes into the manifest the staff (users "staff1".."staff10") and what a new
  registration and a visit carry (the program's requiredAttrs and eventValues).

PEOPLE 0 only adds the staff and rewrites the manifest.
"""
import base64
import datetime
import json
import random
import sys
import urllib.error
import urllib.request

manifest_path, people = sys.argv[1], int(sys.argv[2])
m = json.load(open(manifest_path))
base = m["centralUrl"].rstrip("/")
AUTH = "Basic " + base64.b64encode(f"{m['adminUser']}:{m['adminPassword']}".encode()).decode()
PROGRAM, TET, FAC = m["programs"][0]["id"], m["programs"][0]["tet"], m["facility"]["id"]
VISIT_STAGE = "K2nxbE9ubSs"  # Medical Visit
rnd = random.Random(20261109)


def call(method, path, body=None, auth=AUTH):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Authorization": auth, "Content-Type": "application/json",
                                          "Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=1200) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw[:1] in (b"{", b"[") else raw)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw[:300]


ALNUM = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"


def uid():
    return rnd.choice(ALNUM[:52]) + "".join(rnd.choice(ALNUM) for _ in range(10))


# Staff: ten at the facility, all with eRegisters and Capture web.
CAPTURE_ROLE = "ErgRoleCap1"
st, res = call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE", {"userRoles": [
    {"id": CAPTURE_ROLE, "name": "Capture web user", "authorities": [
        "M_dhis-web-capture", "M_dhis-web-aggregate-data-entry", "F_TRACKED_ENTITY_INSTANCE_SEARCH"]}]})
print("capture role:", st)
_, existing = call("GET", "/api/users?filter=username:like:ereg_nurse&fields=id,username,userRoles[id]&paging=false")
staff = []
for n in range(1, 11):
    username = f"ereg_nurse{n}"
    found = [u for u in existing.get("users", []) if u["username"] == username]
    uid_ = found[0]["id"] if found else f"ErgUsrNur{n:02d}"
    roles = {r["id"] for r in found[0]["userRoles"]} if found else {"ErgRoleUsr1"}
    roles.add(CAPTURE_ROLE)
    user = {"id": uid_, "username": username, "password": m["password"], "firstName": f"Nurse{n}",
            "surname": "Test", "userRoles": [{"id": r} for r in sorted(roles)],
            "organisationUnits": [{"id": FAC}], "dataViewOrganisationUnits": [{"id": m["district"]["id"]}],
            "teiSearchOrganisationUnits": [{"id": m["district"]["id"]}]}
    st, res = call("PUT" if found else "POST", f"/api/users/{uid_}" if found else "/api/users", user)
    if st >= 300:
        print(username, st, str(res)[:300])
    staff.append({"username": username, "uid": uid_})
print("staff:", len(staff))

# Option codes for the visit's values.
_, ps = call("GET", f"/api/programStages/{VISIT_STAGE}?fields=programStageDataElements[dataElement[id,valueType,optionSet[options[code]]]]")
des = {d["dataElement"]["id"]: d["dataElement"] for d in ps["programStageDataElements"]}


def first_code(de, prefer=None):
    codes = [o["code"] for o in des[de]["optionSet"]["options"]]
    return prefer if prefer in codes else codes[0]


DIAGNOSES = [o["code"] for o in des["GdWHQHW4cyT"]["optionSet"]["options"]][:200]


def visit_values():
    return [
        {"dataElement": "mrKZWf2WMIC", "value": "Outpatient"},  # Service Type
        {"dataElement": "GdWHQHW4cyT", "value": rnd.choice(DIAGNOSES)},  # Outpatient Diagnosis
        {"dataElement": "tRXiL9yZ7kL", "value": str(round(rnd.uniform(36.0, 39.5), 1))},  # temperature
        {"dataElement": "mpgSAfAuJjJ", "value": str(rnd.randint(95, 160))},  # systolic
        {"dataElement": "jDU285iO105", "value": str(rnd.randint(60, 100))},  # diastolic
        {"dataElement": "scpPwoNsS27", "value": str(round(rnd.uniform(8, 90), 1))},  # weight
        {"dataElement": "uIFJ94mZt0S", "value": str(rnd.randint(70, 185))},  # height
        {"dataElement": "qjSB1DKCPzM", "value": str(rnd.randint(60, 120))},  # pulse
        {"dataElement": "KaDtw0YFA60", "value": str(rnd.randint(12, 30))},  # respiratory rate
        {"dataElement": "bSh6ltk6YUN", "value": first_code("bSh6ltk6YUN", "No")},  # admitted
        {"dataElement": "EzGu4kzZZTz", "value": first_code("EzGu4kzZZTz", "No")},  # referral in
        {"dataElement": "JYu2Q6Le6Kb", "value": first_code("JYu2Q6Le6Kb")},  # screened for TB
        {"dataElement": "iVk5VVNj8qr", "value": first_code("iVk5VVNj8qr")},  # service location
    ]


# What the measurement's registrations and visits carry (testkit.Manifest.NewPerson,
# NewEvent): the mandatory attributes and one visit's values.
prog = m["programs"][0]
prog["requiredAttrs"] = {"KSq9EyZ8ZFi": "Akello", "Y3DE5CZWySr": "1990-05-01", "bqliZKdUGMX": "Female",
                         "N6Y4aCbmHHt": "National"}
prog["eventValues"] = {VISIT_STAGE: {v["dataElement"]: v["value"] for v in visit_values()}}
for n, s in enumerate(staff, 1):
    m["users"][f"staff{n}"] = s
json.dump(m, open(manifest_path, "w"), indent=2)

FIRST = ["Akello", "Apio", "Auma", "Nakato", "Babirye", "Kato", "Mukasa", "Okello", "Opio", "Wasswa",
         "Namubiru", "Nansubuga", "Ssemanda", "Tumusiime", "Byaruhanga", "Atim", "Achieng", "Kintu",
         "Nabukenya", "Musoke", "Odongo", "Ochieng", "Namatovu", "Kyomuhendo", "Asiimwe"]
SURNAMES = [a + b for a in ["Ka", "Mu", "Na", "Ssa", "Tu", "Bya", "O", "A", "Ki", "Lu", "Wa", "Ny"]
            for b in ["bazi", "gisha", "kasa", "mbale", "ruhanga", "tebe", "wenda", "yinda", "kumbi",
                      "sigye", "ndera", "goma", "lwanga", "zinga", "bende", "rungi", "kiza", "wuta"]]
today = datetime.date.today()


def day(ago):
    return (today - datetime.timedelta(days=ago)).isoformat()


def person():
    enrolled_ago = rnd.randint(30, 730)
    visits = sorted({rnd.randint(1, enrolled_ago) for _ in range(rnd.randint(1, 7))}, reverse=True)
    dob = day(rnd.randint(365, 80 * 365))
    return {
        "trackedEntity": uid(), "trackedEntityType": TET, "orgUnit": FAC,
        "attributes": [
            {"attribute": "KSq9EyZ8ZFi", "value": rnd.choice(FIRST)},
            {"attribute": "TWPNbc9O2nK", "value": rnd.choice(SURNAMES)},
            {"attribute": "Y3DE5CZWySr", "value": dob},
            {"attribute": "bqliZKdUGMX", "value": rnd.choice(["Male", "Female"])},
            {"attribute": "N6Y4aCbmHHt", "value": "National"},
            {"attribute": "hPGgzWsb14m", "value": "07" + "".join(rnd.choice("0123456789") for _ in range(8))},
        ],
        "enrollments": [{
            "enrollment": uid(), "program": PROGRAM, "orgUnit": FAC, "status": "ACTIVE",
            "enrolledAt": day(enrolled_ago), "occurredAt": day(enrolled_ago),
            "events": [{"event": uid(), "program": PROGRAM, "programStage": VISIT_STAGE, "orgUnit": FAC,
                        "status": "COMPLETED", "occurredAt": day(v), "dataValues": visit_values()} for v in visits],
        }],
    }


# As the facility's hub admin: the superuser has no org unit to capture in.
IMPORTER = "Basic " + base64.b64encode(f"{m['users']['admin']['username']}:{m['password']}".encode()).decode()
done = events = 0
while done < people:
    batch = [person() for _ in range(min(100, people - done))]
    st, res = call("POST", "/api/tracker?async=false&importStrategy=CREATE&skipRuleEngine=true&skipSideEffects=true",
                   {"trackedEntities": batch}, auth=IMPORTER)
    if st != 200:
        print("import:", st, json.dumps(res)[:800])
        sys.exit(1)
    done += len(batch)
    events += sum(len(p["enrollments"][0]["events"]) for p in batch)
    print(f"{done} people, {events} visits", flush=True)
