#!/usr/bin/env python3
"""Set up an empty DHIS2 instance (2.43) for eRegisters through a facility hub, and
write the manifest the live suite reads (internal/live/eregisters_test.go).

usage: python3 -I eregisters_setup.py BASE_URL ADMIN:PASSWORD METADATA.json.gz APP.zip MANIFEST.json

METADATA is the eRegisters configuration export ({"metadata": {...}, "dataStore":
{"namespace", "entries"}}); APP is a build of github.com/HISP-Uganda/eregisters.
Neither is in this repository. The script:
- creates a small org unit tree with Uganda's level names: two facilities in one sub
  county and one in another district;
- imports the metadata with the program and data sets on those facilities, data
  sharing open, and references to what the export leaves out removed (org units on
  category options, custom attributes, data entry forms, indicators of data sets,
  one category combination whose option combinations do not match);
- adds the dataStore namespace, a hub setup group, a role and four users;
- installs the app.
"""
import base64
import gzip
import json
import sys
import urllib.error
import urllib.request
import uuid

base, cred, meta_path, app_path, manifest_path = sys.argv[1].rstrip("/"), sys.argv[2], *sys.argv[3:6]
AUTH = "Basic " + base64.b64encode(cred.encode()).decode()
PASSWORD = "Hub-Test-2026!"
PROGRAM = "ueBhWkWll5v"  # Medical Registers
SURNAME = "TWPNbc9O2nK"  # the surname attribute: searches and tests find clients by it


def call(method, path, body=None, ctype="application/json"):
    data = None if body is None else (body if isinstance(body, bytes) else json.dumps(body).encode())
    req = urllib.request.Request(base + path, data=data, method=method,
                                 headers={"Authorization": AUTH, "Content-Type": ctype, "Accept": "application/json"})
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


def report(name, status, res):
    res = res.get("response", res) if isinstance(res, dict) else res
    if not isinstance(res, dict):
        print(f"{name}: {status} {str(res)[:200]}")
        return
    print(f"{name}: {status} {res.get('status')} {res.get('stats')}")
    seen = {}
    for tr in res.get("typeReports") or []:
        for o in tr.get("objectReports") or []:
            for e in o.get("errorReports") or []:
                seen.setdefault((tr.get("klass", "").split(".")[-1], e.get("errorCode")), []).append(e.get("message"))
    for (klass, code), msgs in sorted(seen.items(), key=lambda x: -len(x[1])):
        print(f"   {len(msgs):5} {klass} {code}: {msgs[0][:160]}")


OU = {  # key: (uid, name, parent)
    "nat": ("ErgOuNat001", "Uganda", None), "reg": ("ErgOuReg001", "Test Region", "nat"),
    "dis": ("ErgOuDis001", "Test District", "reg"), "dlg": ("ErgOuDlg001", "Test DLG", "dis"),
    "sub": ("ErgOuSub001", "Test Sub County", "dlg"),
    "fac": ("ErgOuFac001", "Test HC IV", "sub"), "sib": ("ErgOuFac002", "Test HC III", "sub"),
    "dis2": ("ErgOuDis002", "Other District", "reg"), "dlg2": ("ErgOuDlg002", "Other DLG", "dis2"),
    "sub2": ("ErgOuSub002", "Other Sub County", "dlg2"), "far": ("ErgOuFac003", "Other HC IV", "sub2"),
}


def path(key):
    uid, _, parent = OU[key]
    return (path(parent) if parent else "") + "/" + uid


ous = []
for key, (uid, name, parent) in OU.items():
    o = {"id": uid, "name": name, "shortName": name, "openingDate": "2000-01-01"}
    if parent:
        o["parent"] = {"id": OU[parent][0]}
    ous.append(o)
report("org units", *call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE", {"organisationUnits": ous}))

d = json.load(gzip.open(meta_path))
m = d["metadata"]
facilities = [{"id": OU[k][0]} for k in ("fac", "sib", "far")]
OPEN = {"public": "rwrw----", "external": False, "users": {}, "userGroups": {}}
for objs in m.values():
    for x in objs if isinstance(objs, list) else []:
        x.pop("attributeValues", None)
for p in m["programs"]:
    p["organisationUnits"] = facilities
for x in m["dataSets"]:
    x["organisationUnits"] = facilities
    x.pop("dataEntryForm", None)
    x.pop("indicators", None)
for x in m.get("categoryOptions", []):
    x.pop("organisationUnits", None)
m["categoryOptionCombos"] = [c for c in m.get("categoryOptionCombos", [])
                             if (c.get("categoryCombo") or {}).get("id") not in ("QMCgb1k22LJ", "u6esSiHnWUn")]
for kind in ("programs", "programStages", "trackedEntityTypes", "dataSets", "categoryOptions"):
    for x in m.get(kind, []):
        x["sharing"] = OPEN
report("eRegisters metadata", *call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE&atomicMode=NONE", m))

GROUP, ROLE, ADMIN_ROLE = "ErgHubAdm01", "ErgRoleUsr1", "ErgRoleAdm1"
USERS = {  # role: (username, uid, user roles, org unit)
    "admin": ("ereg_admin", "ErgUsrAdm01", [ADMIN_ROLE], "fac"),
    "nurse": ("ereg_nurse1", "ErgUsrNur01", [ROLE], "fac"),
    "nurse2": ("ereg_nurse2", "ErgUsrNur02", [ROLE], "fac"),
    "outsider": ("ereg_outsider", "ErgUsrOut01", [ROLE], "far"),
}
users = [{"id": uid, "username": name, "password": PASSWORD, "firstName": name, "surname": "Test",
          "userRoles": [{"id": r} for r in roles], "organisationUnits": [{"id": OU[ou][0]}],
          "dataViewOrganisationUnits": [{"id": OU["dis"][0]}], "teiSearchOrganisationUnits": [{"id": OU["dis"][0]}]}
         for name, uid, roles, ou in USERS.values()]
report("roles, group, users", *call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE&atomicMode=NONE", {
    "userRoles": [
        {"id": ROLE, "name": "eRegisters user", "authorities": [
            "M_eregisters", "F_TRACKED_ENTITY_INSTANCE_SEARCH", "F_DATAVALUE_ADD", "F_DATAVALUE_DELETE"]},
        {"id": ADMIN_ROLE, "name": "eRegisters hub admin", "authorities": ["ALL"]},
    ],
    "userGroups": [{"id": GROUP, "name": "eRegisters hub admins", "users": []}],
    "users": users,
}))
report("group members", *call("POST", "/api/metadata?importStrategy=CREATE_AND_UPDATE", {
    "userGroups": [{"id": GROUP, "name": "eRegisters hub admins", "users": [{"id": USERS["admin"][1]}]}]}))

ns = d["dataStore"]["namespace"]
for key, value in d["dataStore"]["entries"].items():
    st, _ = call("POST", f"/api/dataStore/{ns}/{key}", value)
    if st == 409:
        st, _ = call("PUT", f"/api/dataStore/{ns}/{key}", value)
    print(f"dataStore {ns}/{key}: {st}")

boundary = uuid.uuid4().hex
body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"eregisters.zip\"\r\n"
        f"Content-Type: application/zip\r\n\r\n").encode() + open(app_path, "rb").read() + f"\r\n--{boundary}--\r\n".encode()
st, res = call("POST", "/api/apps", body, ctype=f"multipart/form-data; boundary={boundary}")
print(f"app install: {st} {res.get('version') if isinstance(res, dict) else str(res)[:200]}")

_, info = call("GET", "/api/system/info")
_, prog = call("GET", f"/api/programs/{PROGRAM}?fields=id,name,accessLevel,trackedEntityType[id],programStages[id]")
admin_user, admin_pw = cred.split(":", 1)
manifest = {
    "profile": "eregisters", "dhis2Version": info.get("version", ""), "centralUrl": base,
    "adminUser": admin_user, "adminPassword": admin_pw, "password": PASSWORD, "setupGroup": GROUP,
    "facility": {"id": OU["fac"][0], "name": OU["fac"][1], "path": path("fac")},
    "sibling": {"id": OU["sib"][0], "name": OU["sib"][1], "path": path("sib")},
    "district": {"id": OU["dis"][0], "name": OU["dis"][1], "path": path("dis")},
    "programs": [{"id": prog["id"], "name": prog["name"], "tet": prog["trackedEntityType"]["id"],
                  "accessLevel": prog.get("accessLevel", "OPEN"), "stages": [s["id"] for s in prog["programStages"]],
                  "searchAttr": SURNAME, "districtSees": True}],
    "users": {role: {"username": name, "uid": uid} for role, (name, uid, _, _) in USERS.items()},
}
json.dump(manifest, open(manifest_path, "w"), indent=2)
print(f"manifest: {manifest_path}; users {', '.join(u[0] for u in USERS.values())} / {PASSWORD}")
