#!/usr/bin/env python3
"""Idempotent test setup for the facility hub POC on a DHIS2 2.42 Sierra Leone demo DB.

Usage:
    DHIS2_URL=http://dhis2-agent-facility-hub:8080 DHIS2_ADMIN=local_admin:district \
        python3 setup_instance.py

Creates (or updates) with fixed UIDs:
  - user role  "Facility hub tracker user"
  - user groups "Facility hub setup", "Facility hub stage 3"
  - users hub_admin, nurse_a, nurse_b, clerk_c, outsider_d (password Hub-Test-2026!)
  - sharing on TB program, its 3 stages and the Person tracked entity type
Then verifies login and effective access per user. stdlib only.
"""
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("DHIS2_URL", "http://dhis2-agent-facility-hub:8080").rstrip("/")
ADMIN = os.environ.get("DHIS2_ADMIN", "local_admin:district")
PASSWORD = "Hub-Test-2026!"

FACILITY = "DiszpKrYNg8"        # Ngelehun CHC (OU_559)
OTHER_FACILITY = "g8upMTyEZGZ"  # Njandama MCHP (sibling in Badjia chiefdom)
DISTRICT = "O6uvpzGd5pu"        # Bo district
PROGRAM = "ur1Edk5Oe2n"         # TB program
STAGES = ["EPEcjy3FWmI", "ZkbAXlQUYJG", "jdRD35YwbRH"]  # sortOrder 1,2,3 (3 = restricted)
TET = "nEenWmSyUEp"             # Person

ROLE = "HubRole0001"
GRP_SETUP = "HubGrpSetup"
GRP_STAGE3 = "HubGrpStag3"
USERS = {
    # uid: (username, firstName, surname, orgUnit, groups)
    "HubUsrAdmin": ("hub_admin", "Hub", "Admin", FACILITY, [GRP_SETUP]),
    "HubUsrNursA": ("nurse_a", "Nurse", "A", FACILITY, [GRP_STAGE3]),
    "HubUsrNursB": ("nurse_b", "Nurse", "B", FACILITY, []),
    "HubUsrClrkC": ("clerk_c", "Clerk", "C", FACILITY, []),
    "HubUsrOutsD": ("outsider_d", "Outsider", "D", OTHER_FACILITY, []),
}
ROLE_AUTHORITIES = [
    "M_dhis-web-capture",
    "M_dhis-web-dashboard",
    "M_dhis-web-messaging",
    "F_UNCOMPLETE_EVENT",
]

RWRW = "rwrw----"
RWR = "rwr-----"
PUBLIC = "r-------"  # metadata read, NO data access for the public

# sharing: object -> {userUid|groupUid: access}
PROGRAM_USERS = {"HubUsrAdmin": RWRW, "HubUsrNursA": RWRW, "HubUsrNursB": RWRW,
                 "HubUsrClrkC": RWR, "HubUsrOutsD": RWRW}
STAGE12_USERS = PROGRAM_USERS
STAGE3_USERS = {"HubUsrAdmin": RWRW}
STAGE3_GROUPS = {GRP_STAGE3: RWRW}
TET_USERS = {"HubUsrAdmin": RWRW, "HubUsrNursA": RWRW, "HubUsrNursB": RWRW,
             "HubUsrClrkC": RWRW, "HubUsrOutsD": RWRW}
OUR_UIDS = set(USERS) | {GRP_SETUP, GRP_STAGE3}


def call(method, path, body=None, auth=ADMIN, raw=False):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Basic " + base64.b64encode(auth.encode()).decode())
    req.add_header("Accept", "application/json")
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req) as r:
            txt = r.read().decode()
            status = r.status
    except urllib.error.HTTPError as e:
        txt = e.read().decode()
        status = e.code
    if raw:
        return status, txt
    try:
        return status, json.loads(txt) if txt else None
    except ValueError:
        return status, txt


def must(status, body, what):
    if status >= 300:
        print(f"FAILED {what}: {status} {json.dumps(body)[:2000]}")
        sys.exit(1)
    return body


def metadata():
    users = []
    for uid, (username, fn, sn, ou, _groups) in USERS.items():
        users.append({
            "id": uid, "username": username, "password": PASSWORD,
            "firstName": fn, "surname": sn,
            "userRoles": [{"id": ROLE}],
            "organisationUnits": [{"id": ou}],
            "dataViewOrganisationUnits": [{"id": ou}],
            "teiSearchOrganisationUnits": [{"id": DISTRICT}],
            "disabled": False,
        })
    groups = []
    for gid, name in [(GRP_SETUP, "Facility hub setup"), (GRP_STAGE3, "Facility hub stage 3")]:
        members = [{"id": u} for u, v in USERS.items() if gid in v[4]]
        groups.append({"id": gid, "name": name, "code": name.upper().replace(" ", "_"),
                       "users": members,
                       "sharing": {"public": "r-------", "users": {}, "userGroups": {}}})
    role = {"id": ROLE, "name": "Facility hub tracker user",
            "description": "Capture app data entry for the facility hub POC",
            "authorities": ROLE_AUTHORITIES}
    return {"userRoles": [role], "users": users, "userGroups": groups}


def import_metadata():
    st, body = call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE&atomicMode=ALL"
                    "&mergeMode=REPLACE", metadata())
    must(st, body, "metadata import")
    print("metadata import:", body.get("status"), (body.get("response") or body).get("stats"))
    if body.get("status") not in ("OK", "WARNING"):
        print(json.dumps(body, indent=1)[:4000])
        sys.exit(1)


def set_sharing(type_, uid, users, groups, public=None):
    st, cur = call("GET", f"/api/sharing?type={type_}&id={uid}")
    obj = must(st, cur, f"get sharing {type_} {uid}")["object"]
    ua = [a for a in obj.get("userAccesses", []) if a["id"] not in OUR_UIDS]
    ga = [a for a in obj.get("userGroupAccesses", []) if a["id"] not in OUR_UIDS]
    ua += [{"id": k, "access": v} for k, v in users.items()]
    ga += [{"id": k, "access": v} for k, v in groups.items()]
    new = {"object": {"publicAccess": public if public is not None else obj["publicAccess"],
                      "externalAccess": False,
                      "userAccesses": [{"id": a["id"], "access": a["access"]} for a in ua],
                      "userGroupAccesses": [{"id": a["id"], "access": a["access"]} for a in ga]}}
    st, body = call("POST", f"/api/sharing?type={type_}&id={uid}", new)
    must(st, body, f"set sharing {type_} {uid}")
    print(f"sharing {type_} {uid}: ok")


def apply_sharing():
    set_sharing("program", PROGRAM, PROGRAM_USERS, {}, PUBLIC)
    for s in STAGES[:2]:
        set_sharing("programStage", s, STAGE12_USERS, {}, PUBLIC)
    set_sharing("programStage", STAGES[2], STAGE3_USERS, STAGE3_GROUPS, PUBLIC)
    # Person TET is shared by all programs; keep its public access (rwrw---- in the demo DB)
    set_sharing("trackedEntityType", TET, TET_USERS, {})


def verify():
    ok = True
    fields = ("access[data[read,write]],programStages[id,sortOrder,access[data[read,write]]],"
              "trackedEntityType[access[data[read,write]]]")
    for uid, (username, *_rest) in USERS.items():
        auth = f"{username}:{PASSWORD}"
        st, me = call("GET", "/api/me?fields=id,username,organisationUnits[id]", auth=auth)
        if st != 200:
            print(f"  {username}: /api/me {st} {me}")
            ok = False
            continue
        st, p = call("GET", f"/api/programs/{PROGRAM}?fields={urllib.parse.quote(fields)}", auth=auth)
        if st != 200:
            print(f"  {username}: program GET {st}")
            ok = False
            continue
        d = lambda a: ("r" if a["data"]["read"] else "-") + ("w" if a["data"]["write"] else "-")
        stages = sorted(p["programStages"], key=lambda s: s["sortOrder"])
        print(f"  {username:11s} ou={me['organisationUnits'][0]['id']} program={d(p['access'])} "
              f"tet={d(p['trackedEntityType']['access'])} stages="
              + ",".join(d(s["access"]) for s in stages))
    # session login check (web login app endpoint)
    for uid, (username, *_rest) in USERS.items():
        req = urllib.request.Request(BASE + "/api/auth/login", method="POST",
                                     data=json.dumps({"username": username, "password": PASSWORD}).encode(),
                                     headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req) as r:
                res = json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            res = {"http": e.code, "body": e.read().decode()[:200]}
        if res.get("loginStatus") != "SUCCESS":
            ok = False
        print(f"  login {username}: {res}")
    return ok


if __name__ == "__main__":
    import_metadata()
    apply_sharing()
    print("verify:")
    sys.exit(0 if verify() else 1)
