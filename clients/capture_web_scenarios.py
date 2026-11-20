#!/usr/bin/env python3
"""Drive the real Capture web app through the hub for the adversarial scenarios
(docs/ADVERSARIAL-TESTING.md, wave 2).

Run by internal/live/capture_web_scenarios_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /<step>: the test changes the hub or central and answers
  when done), ACTION, OUT (folder for screenshots), PASSWORD, ORG_UNIT, PROGRAMS
  (comma separated, the first is the one used for dashboards), and per action:
    refusals  USER, ENROLLMENT, STAGE_NAME, NOTE, WEIGHT_UPGRADING, WEIGHT_REFUSED (I1c): what
              Capture shows when the hub answers "needs connection", "upgrading" and
              refuses a save itself, and whether the refused data appears saved after
              the page is opened again
    users     USERS (comma separated), ENROLLMENTS (program:enrollment:name, comma
              separated) (I2c): users in turn on one browser, online and offline,
              logging out between, and each later one alone in a fresh browser
    update    USER, ENROLLMENT (I10c, N18): Capture updated on central while the hub is
              offline; the steps "install" and "offline"/"online" come from the test
    files     USER, ENROLLMENT, EVENT, STAGE_NAME, IMAGE_LABEL, IMAGE_PATH (FINDINGS.md,
              "Images and files"): an event's image seen online and offline, and an
              image picked for a new event online and offline
Prints one JSON object. Exits non-zero only if the browser itself fails.
"""
import json
import os
import re
import time
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
ACTION = os.environ["ACTION"]
OUT = os.environ.get("OUT", ".")
PASSWORD = os.environ["PASSWORD"]
OU = os.environ["ORG_UNIT"]
PROGRAMS = [p for p in os.environ.get("PROGRAMS", "").split(",") if p]

R = {"action": ACTION, "steps": {}, "failed": [], "errors": [], "requests": []}
PHASE = {"name": "start"}


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=300).read()
    PHASE["name"] = what


class Capture:
    """One browser tab on Capture, through the hub."""

    def __init__(self, ctx):
        self.ctx = ctx
        self.page = ctx.new_page()
        self.page.on("response", self._response)
        self.page.on("pageerror", lambda e: R["errors"].append({"phase": PHASE["name"], "error": str(e)[:300]}))

    def _response(self, resp):
        if not resp.url.startswith(HUB):
            return
        path = resp.url[len(HUB):]
        req = resp.request
        if req.method != "GET" or resp.status >= 400 and resp.status != 404:
            R["requests"].append({"phase": PHASE["name"], "method": req.method, "status": resp.status,
                                  "path": path[:300]})
        if resp.status >= 400 and resp.status != 404:
            R["failed"].append({"phase": PHASE["name"], "status": resp.status, "path": path[:300]})

    def login(self, user):
        r = self.ctx.request.post(HUB + "/api/auth/login", data={"username": user, "password": PASSWORD})
        return r.status

    def logout(self):
        # The header bar's "Logout" (2.42): GET logout.action, which ends the session.
        self.page.goto(HUB + "/dhis-web-commons-security/logout.action", wait_until="load", timeout=60000)

    def app(self):
        # From 2.42 the app runs in a frame inside the global shell; earlier, in the page.
        for f in self.page.frames:
            if f != self.page.main_frame and "capture" in f.url:
                return f
        return self.page.main_frame

    def open(self, route, settle=3000):
        self.page.goto(f"{HUB}/dhis-web-capture/index.html#{route}", wait_until="networkidle", timeout=120000)
        self.page.wait_for_timeout(settle)
        return self.app()

    def text(self):
        try:
            return self.app().locator("body").inner_text(timeout=10000)
        except Exception as e:
            return f"(no text: {e})"[:300]

    def alerts(self):
        """The alert bars Capture shows (saved, could not save, ...)."""
        try:
            return self.app().locator("[data-test=dhis2-uicore-alertbar]").all_inner_texts()
        except Exception:
            return []

    def shot(self, name):
        self.page.screenshot(path=os.path.join(OUT, name + ".png"), full_page=True)

    def working_list(self, prog):
        a = self.open(f"/?orgUnitId={OU}&programId={prog}", settle=1000)
        try:  # the list appears after its own queries
            a.wait_for_selector("table tbody tr", timeout=30000)
        except Exception:
            pass
        self.page.wait_for_timeout(1000)
        rows = [r.inner_text().replace("\n", " | ")[:200] for r in a.locator("table tbody tr").all()]
        return rows

    def dashboard(self, enrollment, program=None):
        a = self.open(f"/enrollment?enrollmentId={enrollment}&orgUnitId={OU}&programId={program or PROGRAMS[0]}")
        try:
            a.wait_for_selector("text=Stages and Events", timeout=30000)
        except Exception:
            pass
        self.page.wait_for_timeout(2000)
        return self.text()

    def stage_events(self, stage_name):
        """The event count Capture shows on the dashboard for a stage ("2 events")."""
        m = re.search(re.escape(stage_name) + r"\s+(\d+) events?", self.text())
        return int(m.group(1)) if m else -1

    def app_version(self):
        """The version of the Capture the frame runs, from the manifest next to its index.html."""
        a = self.app()
        try:
            return a.evaluate("""async () => {
                const r = await fetch(new URL('manifest.webapp', location.href), {credentials: 'include', cache: 'no-store'});
                return r.ok ? (await r.json()).version : 'status ' + r.status }""")
        except Exception as e:
            return f"unknown: {e}"[:200]

    def save_event(self, weight, tag):
        """A new event in the first stage, as staff enter it: the report date, a weight,
        then "Save without completing". Returns the hub's answer and what Capture shows."""
        a = self.app()
        out = {}
        a.locator("button", has_text="New event").first.click(timeout=15000)
        a.locator("[data-test=program-stage-selector-button]").first.click(timeout=15000)
        a.get_by_placeholder("yyyy-mm-dd").first.fill(time.strftime("%Y-%m-%d"))
        a.get_by_placeholder("yyyy-mm-dd").first.press("Tab")
        field = a.locator("text=Synthetic weight").locator("xpath=following::input[1]")
        field.fill(weight)
        field.press("Tab")
        try:
            with self.page.expect_response(lambda r: "/api/" in r.url and "tracker" in r.url
                                           and r.request.method == "POST", timeout=60000) as resp:
                a.locator("button", has_text="Save without completing").first.click(timeout=15000)
            out["status"] = resp.value.status
            out["answer"] = resp.value.text()[:800]
        except Exception as e:
            out["status"] = 0
            out["answer"] = f"no request: {e}"[:300]
        self.page.wait_for_timeout(3000)
        out["alerts"] = self.alerts()
        self.shot(tag)
        out["shows"] = self.text()[:2500]
        return out

    def add_note(self, note, tag):
        """A note on the enrollment, in the dashboard's notes widget."""
        a = self.app()
        out = {}
        box = a.get_by_placeholder(re.compile("Write a note", re.I)).first
        box.click(timeout=15000)
        box.fill(note, timeout=15000)
        try:
            with self.page.expect_response(lambda r: "/note" in r.url and r.request.method == "POST",
                                           timeout=60000) as resp:
                a.locator("button", has_text=re.compile("Save note", re.I)).first.click(timeout=15000)
            out["status"] = resp.value.status
            out["answer"] = resp.value.text()[:800]
        except Exception as e:
            out["status"] = 0
            out["answer"] = f"no request: {e}"[:300]
        self.page.wait_for_timeout(3000)
        out["alerts"] = self.alerts()
        self.shot(tag)
        out["shows"] = self.text()[:2500]
        return out

    def view_event(self, event, tag):
        """An event's page: which of its images the browser could show."""
        n = len(R["requests"])
        a = self.open(f"/enrollmentEventEdit?eventId={event}&orgUnitId={OU}", settle=2000)
        try:
            a.wait_for_selector("text=" + os.environ.get("IMAGE_LABEL", "Image"), timeout=60000)
        except Exception:
            pass
        self.page.wait_for_timeout(5000)
        out = {"images": self.app().evaluate(
            "() => [...document.images].filter(i => /image|blob:/.test(i.src)).map("
            "i => ({src: i.src.replace(location.origin, '').slice(0, 160), shown: i.complete && i.naturalWidth > 0}))"),
            "requests": [r for r in R["requests"][n:] if re.search(r"/image|/file|fileResources", r["path"])]}
        out["alerts"] = self.alerts()
        self.shot(tag)
        out["shows"] = self.text()[:1500]
        return out

    def pick_image(self, stage, label, path, tag):
        """A new event in stage with an image picked in the field labelled label, saved
        without completing: what the upload and the save get, and what Capture shows."""
        a = self.app()
        out = {}
        a.locator("button", has_text="New event").first.click(timeout=90000)  # a large program loads slowly
        a.locator("[data-test=program-stage-selector-button]", has_text=stage).first.click(timeout=15000)
        a.get_by_placeholder("yyyy-mm-dd").first.fill(time.strftime("%Y-%m-%d"))
        a.get_by_placeholder("yyyy-mm-dd").first.press("Tab")
        field = a.locator(f"text={label}").first.locator("xpath=following::input[@type='file'][1]")
        try:
            with self.page.expect_response(lambda r: "fileResources" in r.url and r.request.method == "POST",
                                           timeout=60000) as resp:
                field.set_input_files(path, timeout=15000)
            out["upload"] = resp.value.status
            out["uploadAnswer"] = resp.value.text()[:300]
        except Exception as e:
            out["upload"] = 0
            out["uploadAnswer"] = f"no request: {e}"[:300]
        self.page.wait_for_timeout(3000)
        out["alertsAfterPick"] = self.alerts()
        self.shot(tag + "-picked")
        out["fieldShows"] = self.text()[:2000]
        try:
            with self.page.expect_response(lambda r: "/api/" in r.url and "tracker" in r.url
                                           and r.request.method == "POST", timeout=60000) as resp:
                a.locator("button", has_text="Save without completing").first.click(timeout=15000)
            out["save"] = resp.value.status
            out["saveAnswer"] = resp.value.text()[:300]
        except Exception as e:
            out["save"] = 0
            out["saveAnswer"] = f"no request: {e}"[:300]
        self.page.wait_for_timeout(3000)
        out["alerts"] = self.alerts()
        self.shot(tag + "-saved")
        out["shows"] = self.text()[:1500]
        return out


def files(ctx):
    """Images: an event's image online and offline, and a new image picked online and
    offline (FINDINGS.md, "Images and files")."""
    user, enr, ev = os.environ["USER"], os.environ["ENROLLMENT"], os.environ["EVENT"]
    stage, label, img = os.environ["STAGE_NAME"], os.environ["IMAGE_LABEL"], os.environ["IMAGE_PATH"]
    c = Capture(ctx)
    R["login"] = c.login(user)
    PHASE["name"] = "online"
    c.dashboard(enr)
    R["steps"]["online-view"] = c.view_event(ev, "files-online-view")
    c.dashboard(enr)
    R["steps"]["online-pick"] = c.pick_image(stage, label, img, "files-online-pick")
    control("offline")
    R["steps"]["offline-view"] = c.view_event(ev, "files-offline-view")
    c.dashboard(enr)
    R["steps"]["offline-pick"] = c.pick_image(stage, label, img, "files-offline-pick")
    control("online")


def refusals(ctx):
    """I1c: each refusal the hub gives a save, as Capture shows it."""
    user, enr, program = os.environ["USER"], os.environ["ENROLLMENT"], PROGRAMS[0]
    c = Capture(ctx)
    R["login"] = c.login(user)
    PHASE["name"] = "online"
    R["steps"]["online"] = {"rows": len(c.working_list(program)), "dashboard": c.dashboard(enr)[:1500]}
    c.shot("i1c-online-dashboard")

    control("offline")
    R["steps"]["offline"] = {"dashboard": c.dashboard(enr)[:1500]}
    c.shot("i1c-offline-dashboard")

    # A note on the enrollment: the hub takes it offline and sends Capture's request
    # to central at the push, as its author (not a refusal since 17 November 2026).
    note = os.environ["NOTE"]
    s = c.add_note(note, "i1c-note-offline")
    c.page.wait_for_timeout(20000)  # any retry Capture makes (1 s, 5 s, 15 s)
    s["reopened"] = c.dashboard(enr)
    c.shot("i1c-note-offline-reopened")
    R["steps"]["note-offline"] = s

    # "Upgrading": the hub refuses local writes while an upgrade copies its store.
    w, stage = os.environ["WEIGHT_UPGRADING"], os.environ["STAGE_NAME"]
    c.dashboard(enr)
    before = c.stage_events(stage)
    control("freeze")
    s = c.save_event(w, "i1c-upgrading")
    control("thaw")
    c.page.wait_for_timeout(30000)  # Capture's retries, if any, after the upgrade
    s["reopened"] = c.dashboard(enr)
    s["events-before"], s["events-reopened"] = before, c.stage_events(stage)
    c.shot("i1c-upgrading-reopened")
    R["steps"]["upgrading"] = s

    # "Refused": central made the stage non-repeatable after Capture cached its
    # metadata; the hub, synced since, refuses a second event in it (E1039).
    w = os.environ["WEIGHT_REFUSED"]
    control("refuse")
    c.dashboard(enr)
    before = c.stage_events(stage)
    s = c.save_event(w, "i1c-refused")
    c.page.wait_for_timeout(20000)
    s["reopened"] = c.dashboard(enr)
    s["events-before"], s["events-reopened"] = before, c.stage_events(stage)
    c.shot("i1c-refused-reopened")
    R["steps"]["refused"] = s


def indexeddb_names(c):
    try:
        return c.app().evaluate("async () => (await indexedDB.databases()).map(d => d.name)")
    except Exception as e:
        return [f"unknown: {e}"[:200]]


def stage_counts(text):
    """{stage: events} as the dashboard lists them ("Synthetic ANC visit", "2 events")."""
    lines = [ln.strip() for ln in text.splitlines() if ln.strip()]
    out = {}
    for i, ln in enumerate(lines):
        m = re.fullmatch(r"(\d+) events?", ln)
        if m and i > 0:
            out[lines[i - 1]] = int(m.group(1))
    return out


def look_as(c, user, tag, enrollments):
    """What user sees: each program's working list, and each enrollment's dashboard
    (the person's name shown or not, and the events per stage)."""
    PHASE["name"] = tag
    st = {"user": user, "login": c.login(user)}
    for prog in PROGRAMS:
        st[f"rows:{prog}"] = c.working_list(prog)
        c.shot(f"i2c-{tag}-{prog}")
    for prog, enr, name in enrollments:
        text = c.dashboard(enr, prog)
        st[f"dashboard:{enr}"] = {"name": name in text, "stages": stage_counts(text), "text": text[:1200]}
        c.shot(f"i2c-{tag}-{enr}")
    st["indexeddb"] = indexeddb_names(c)
    R["steps"][tag] = st
    c.logout()


def users(ctx, browser):
    """I2c: users in turn on one browser. The first works online then offline; each
    later one logs in offline first (never online in this browser), then online, then
    offline again. Each later user's view online in a fresh browser is what central
    shows them."""
    names = os.environ["USERS"].split(",")
    enrollments = [tuple(x.split(":")) for x in os.environ["ENROLLMENTS"].split(",") if x]  # program:enrollment:name
    c = Capture(ctx)
    look_as(c, names[0], f"{names[0]}-online", enrollments)
    control("offline")
    look_as(c, names[0], f"{names[0]}-offline", enrollments)
    for user in names[1:]:
        look_as(c, user, f"{user}-offline-first", enrollments)
        control("online")
        look_as(c, user, f"{user}-online", enrollments)
        control("offline")
        look_as(c, user, f"{user}-offline", enrollments)
    control("online")
    for user in names[1:]:
        fresh = browser.new_context(viewport={"width": 1400, "height": 900})
        look_as(Capture(fresh), user, f"{user}-fresh", enrollments)
        fresh.close()


def update(ctx):
    """I10c, N18: Capture updated on central while the hub is offline."""
    user, enr, program = os.environ["USER"], os.environ["ENROLLMENT"], PROGRAMS[0]
    c = Capture(ctx)
    R["login"] = c.login(user)

    def look(tag):
        rows = c.working_list(program)
        c.shot(f"update-{tag}-list")
        st = {"rows": len(rows), "version": c.app_version(), "frame": c.app().url[len(HUB):][:200]}
        st["dashboard"] = c.dashboard(enr)[:1500]
        c.shot(f"update-{tag}-dashboard")
        st["dashboard-ok"] = "program stages and events" in st["dashboard"].lower()
        R["steps"][tag] = st

    PHASE["name"] = "online-before"
    look("online-before")
    control("offline")
    look("offline-before")
    control("install")  # central gets the new Capture; the hub, offline, cannot see it
    PHASE["name"] = "offline-installed"
    look("offline-installed")
    control("online")
    look("online-new")
    control("offline")
    look("offline-new")
    control("online")


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(channel=os.environ.get("PW_CHANNEL") or None, args=["--disable-dev-shm-usage"])
        try:
            ctx = browser.new_context(viewport={"width": 1400, "height": 900})
            try:
                if ACTION == "users":
                    users(ctx, browser)
                else:
                    {"refusals": refusals, "update": update, "files": files}[ACTION](ctx)
            except Exception as e:  # report, do not crash: the Go test reads the JSON
                R["exception"] = f"{PHASE['name']}: {e}"[:1500]
                try:
                    R["inputs"] = [f.evaluate("() => [...document.querySelectorAll('input,textarea,button')].map("
                                              "e => e.tagName + ' ' + (e.placeholder || e.innerText || '').slice(0, 60))")
                                   for p in ctx.pages for f in p.frames]
                except Exception:
                    pass
                for p in ctx.pages:
                    p.screenshot(path=os.path.join(OUT, f"{ACTION}-exception.png"), full_page=True)
        finally:
            browser.close()
    print(json.dumps(R))


if __name__ == "__main__":
    main()
