#!/usr/bin/env python3
"""Drive one web app for the central load measurement (docs/PERFORMANCE.md, "Central's
load from one facility, three ways"): app starts, patient visits and registrations,
each between two marks the test turns into one measured unit.

Run by internal/perf/central_work_test.go (TestCentralWork), which passes:
  BASE_URL (central through a Netcut, or a hub), CONTROL_URL (GET /begin?unit=, /end),
  APP (capture or eregisters), WHERE (prefix of the unit names), USER, PASSWORD,
  ORG_UNIT, PROGRAM, PEOPLE (JSON: people to visit, with TE, Enrollment, First,
  Surname), RUNS, OUT (folder: browser profile, screenshots), PULL_WAIT (seconds
  between a facility-day's saves and eRegisters' pull).

The browser keeps a profile on disk, as staff's do. Its first start is measured on its
own (the app's files, its metadata and, for eRegisters, every person of the facility);
then one visit is made, not measured, so the browser holds what it would on any day
after the first. Each measured app start begins with a fresh login. eRegisters is
pulled after a facility-day of other staff's saves on central (CONTROL_URL/day).

Prints one JSON object: each save's status by unit, and errors.
"""
import json
import os
import time
import urllib.parse
import urllib.request

from playwright.sync_api import sync_playwright

BASE = os.environ["BASE_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
APP, WHERE = os.environ["APP"], os.environ["WHERE"]
USER, PASSWORD = os.environ["USER"], os.environ["PASSWORD"]
OU, PROGRAM = os.environ["ORG_UNIT"], os.environ["PROGRAM"]
PEOPLE = json.loads(os.environ["PEOPLE"])
RUNS = int(os.environ.get("RUNS", "3"))
OUT = os.environ.get("OUT", ".")
TODAY = time.strftime("%Y-%m-%d")
R = {"saves": [], "errors": [], "seconds": {}}


def control(path):
    return urllib.request.urlopen(CONTROL + path, timeout=600).read()


def measured(unit, fn):
    """Run fn between the marks; record a save's status if fn returns one."""
    name = f"{WHERE}: {APP} {unit}"
    control("/begin?" + urllib.parse.urlencode({"unit": name}))
    t = time.time()
    try:
        status = fn()
    except Exception as e:
        status = None
        R["errors"].append(f"{unit}: {type(e).__name__}: {str(e)[:300]}")
    finally:
        control("/end")
    R["seconds"].setdefault(unit, []).append(round(time.time() - t, 1))
    if status is not None:
        R["saves"].append({"unit": unit, "status": status})


class Browser:
    def __init__(self, pw):
        self.ctx = pw.chromium.launch_persistent_context(
            os.path.join(OUT, "profile"), viewport={"width": 1400, "height": 1000}, args=["--disable-dev-shm-usage"])
        self.page = self.ctx.pages[0] if self.ctx.pages else self.ctx.new_page()

    def login(self):
        self.ctx.clear_cookies()
        r = self.ctx.request.post(BASE + "/api/auth/login", data={"username": USER, "password": PASSWORD})
        if r.status != 200:
            raise RuntimeError(f"login: {r.status}")

    def shot(self, name):
        try:
            self.page.screenshot(path=os.path.join(OUT, name + ".png"), full_page=True)
        except Exception:
            pass

    def settle(self, ms=1500):
        try:
            self.page.wait_for_load_state("networkidle", timeout=60_000)
        except Exception:
            pass
        self.page.wait_for_timeout(ms)


# --- Capture web ---------------------------------------------------------------------

class Capture(Browser):
    URL = BASE + "/dhis-web-capture/index.html"

    def app(self):
        for f in self.page.frames:  # from 2.42 inside the global shell's frame
            if f != self.page.main_frame and "capture" in f.url:
                return f
        return self.page.main_frame

    def start(self):
        self.login()
        self.page.goto("about:blank")
        self.page.goto(self.URL + "#/", wait_until="load", timeout=300_000)
        self.app().wait_for_selector("[data-test=headerbar-title]", state="attached", timeout=300_000)
        self.settle()

    def route(self, r):
        self.app().evaluate("r => { location.hash = r }", r)

    def row(self, label, has="input"):
        # The form row of a field: the nearest element around its label that holds an input.
        return self.app().locator(f"xpath=//*[normalize-space(text())='{label}']/ancestor::div[.//{has}][1]").first

    def fill(self, label, value):
        box = self.row(label).locator("input").first
        box.fill(value)
        box.press("Tab")

    def pick(self, label, option):
        self.row(label, "*[@data-test='dhis2-uicore-select-input']").locator("[data-test=dhis2-uicore-select-input]").click()
        self.app().locator("[data-test=dhis2-uicore-select-menu-menuwrapper]").get_by_text(option, exact=True).first.click()
        self.page.keyboard.press("Escape")

    def pick_one(self, label, option):
        self.row(label, "*[@role='combobox']").locator("[role=combobox]").first.click()
        self.app().get_by_role("option", name=option, exact=True).first.click()

    def save(self, button):
        a = self.app()
        with self.page.expect_response(lambda r: "/tracker" in r.url and r.request.method == "POST", timeout=120_000) as resp:
            a.locator("button", has_text=button).first.click(timeout=30_000)
        status = resp.value.status
        self.settle()
        return status

    def new_visit(self):
        """On a dashboard: a Medical Visit event, as a short outpatient visit."""
        a = self.app()
        a.locator("button", has_text="New event").first.click(timeout=60_000)
        a.locator("button", has_text="Medical Visit").first.click(timeout=60_000)
        a.get_by_placeholder("yyyy-mm-dd").first.fill(TODAY)
        a.get_by_placeholder("yyyy-mm-dd").first.press("Tab")
        self.pick("Service Type", "Outpatient")
        for label, value in (("Weight (kg)", "61.5"), ("Height / Length (cm)", "164"), ("Temperature(°C)", "37.4"),
                             ("Respiratory rate", "18"), ("Pulse rate", "78")):
            self.fill(label, value)
        return self.save("Save without completing")

    def visit(self, p):
        """Find the person by name, open their dashboard, add a visit, save."""
        a = self.app()
        self.route(f"/search?orgUnitId={OU}&programId={PROGRAM}")
        a.get_by_text("Search by attributes").first.wait_for(timeout=60_000)
        self.fill("First name", p["First"])
        self.fill("Surname", p["Surname"])
        a.locator("button", has_text="Search by attributes").last.click()
        a.locator("button", has_text="View dashboard").first.click(timeout=60_000)
        a.get_by_text("Stages and Events").first.wait_for(timeout=60_000)
        self.settle(500)
        return self.new_visit()

    def register(self, surname):
        """A new person enrolled, then their first visit."""
        a = self.app()
        self.route(f"/new?orgUnitId={OU}&programId={PROGRAM}")
        a.get_by_text("Date of Registration").first.wait_for(timeout=60_000)
        self.fill("Date of Registration", TODAY)
        self.fill("Surname", surname)
        self.fill("First name", "Akello")
        self.pick_one("Sex", "Female")
        self.fill("Date of Birth", "1990-05-01")
        self.pick_one("Client Category", "National")
        status = self.save("Save person")
        if status != 200:
            return status
        a.get_by_text("Stages and Events").first.wait_for(timeout=60_000)
        self.settle(500)
        return self.new_visit()


# --- eRegisters ----------------------------------------------------------------------

class ERegisters(Browser):
    URL = BASE + "/api/apps/eregisters/index.html"

    def app(self):
        for f in self.page.frames:
            if "/api/apps/eregisters/" in f.url:
                return f
        return self.page.main_frame

    def start(self):
        """Open the app; the first time it downloads the metadata and every person."""
        self.login()
        self.page.goto("about:blank")
        self.page.goto(self.URL, wait_until="domcontentloaded", timeout=300_000)
        for _ in range(60):  # the admin can ask devices to reload: do it now
            try:
                self.app().get_by_role("button", name="Register New Client").wait_for(timeout=10_000)
                reload = self.app().get_by_role("button", name="Reload now")
                if not (reload.count() and reload.first.is_visible()):
                    break
                reload.first.click()
            except Exception:
                pass
            self.page.wait_for_timeout(5000)
        self.settle()

    def pull(self):
        """Pull Data: what changed on central since the browser's last pull."""
        with self.page.expect_response(lambda r: "trackedEntities" in r.url and r.request.method == "GET",
                                       timeout=120_000) as resp:
            self.app().get_by_role("button", name="Pull Data").first.click()
        self.quiet()
        return resp.value.status

    def quiet(self, ms=3000):
        """Wait until the app has sent nothing for ms."""
        n = {"n": 0}
        self.page.on("request", lambda r: n.__setitem__("n", n["n"] + 1))
        last = -1
        while n["n"] != last:
            last = n["n"]
            self.page.wait_for_timeout(ms)

    def item(self, label):
        return self.app().locator(".ant-modal .ant-form-item", has_text=label).first

    def date(self, label, value):
        box = self.item(label).locator("input").first
        box.click()
        box.fill(value)
        box.press("Enter")

    def text(self, label, value):
        self.item(label).locator("input").first.fill(value)

    def radio(self, label, value):
        self.item(label).get_by_text(value, exact=True).first.click()

    def push_after(self, button):
        """Press a save button; the app pushes on its own, or Push Data does."""
        a = self.app()
        try:
            with self.page.expect_response(lambda r: r.url.split("?")[0].endswith("/tracker") and r.request.method == "POST",
                                           timeout=5_000) as resp:
                a.get_by_role("button", name=button, exact=True).first.click()
            status = resp.value.status
        except Exception:
            with self.page.expect_response(lambda r: r.url.split("?")[0].endswith("/tracker") and r.request.method == "POST",
                                           timeout=120_000) as resp:
                a.get_by_role("button", name="Push Data").first.click()
            status = resp.value.status
            R.setdefault("pushedByButton", []).append(button)
        self.quiet()
        return status

    def new_visit(self, kind="Re-attendance"):
        a = self.app()
        a.get_by_role("button", name="Add new visit").first.click(timeout=60_000)
        a.locator(".ant-modal").get_by_text("New Visit").first.wait_for(timeout=60_000)
        self.date("Visit Date", TODAY)
        sel = self.item("Service Type").locator(".ant-select").first
        sel.click()
        self.page.keyboard.type("Outpatient")
        self.page.keyboard.press("Enter")
        self.page.keyboard.press("Escape")
        for label, value in (("Weight (kg)", "61.5"), ("Height / Length (cm)", "164"), ("Temperature(°C)", "37.4"),
                             ("Respiratory rate", "18"), ("Pulse rate", "78")):
            self.text(label, value)
        a.locator(".ant-modal").get_by_text("Outpatient", exact=True).last.click()  # the section's tab
        self.radio("Type of Patient", kind)
        self.radio("OPD Outcomes", "Managed")
        return self.push_after("Save Visit")

    def visit(self, p):
        """Search the browser's copy by name, open the client, add a visit, save."""
        a = self.app()
        a.get_by_role("button", name="Clear").first.click()
        a.locator(".ant-form-item", has_text="First name").locator("input").first.fill(p["First"])
        a.locator(".ant-form-item", has_text="Surname").locator("input").first.fill(p["Surname"])
        a.get_by_role("button", name="Search", exact=True).click()
        # 1.1.9's result rows are not table rows: click the surname's cell.
        a.get_by_text(p["Surname"], exact=True).first.click(timeout=60_000)
        a.get_by_role("button", name="Add new visit").first.wait_for(timeout=60_000)
        status = self.new_visit()
        a.get_by_role("button", name="Back").first.click()
        return status

    def register(self, surname):
        a = self.app()
        a.get_by_role("button", name="Register New Client").click()
        a.locator(".ant-modal").get_by_text("Registration Date").first.wait_for(timeout=30_000)
        self.date("Registration Date", TODAY)
        self.text("Surname", surname)
        self.text("First name", "Akello")
        self.radio("Sex", "Female")
        self.date("Date of Birth", "1990-05-01")
        self.radio("Client Category", "National")
        status = self.push_after("Register client")
        if status != 200:
            return status
        a.get_by_role("button", name="Add new visit").first.wait_for(timeout=60_000)
        status = self.new_visit("New")
        a.get_by_role("button", name="Back").first.click()
        return status


def main():
    with sync_playwright() as pw:
        b = Capture(pw) if APP == "capture" else ERegisters(pw)
        try:
            def first():  # a new browser: the app's files, metadata, and for eRegisters every person
                b.start()
                if APP == "eregisters":
                    return b.pull()
            measured("first start, once per browser", first)
            st = b.visit(PEOPLE[-1])  # warm-up, not measured
            R["warm-up"] = st
            b.shot("warm-up")
            for i in range(RUNS):
                measured("app start", b.start)
                if APP == "eregisters":  # after a facility-day of changes on central
                    control("/day")
                    # The app re-reads 5 minutes before its last pull: wait them out, or
                    # the pull gets the previous run's day too (PULL_WAIT seconds).
                    time.sleep(int(os.environ.get("PULL_WAIT", "0")))
                    measured("pull", b.pull)
                measured("visit", lambda: b.visit(PEOPLE[i]))
                b.shot(f"visit-{i}")
                measured("registration", lambda: b.register(f"Zw{int(time.time() * 1000) % 10**8}"))
                b.shot(f"registration-{i}")
        except Exception as e:
            R["errors"].append(f"{type(e).__name__}: {str(e)[:500]}")
            try:  # eRegisters' error boundary hides its message behind a button
                show = b.app().get_by_text("Show Error")
                if show.count():
                    show.first.click()
                    R["errors"].append("the app shows: " + b.app().locator("body").inner_text()[:1500])
            except Exception:
                pass
            b.shot("error")
        finally:
            b.ctx.close()
    print(json.dumps(R))


if __name__ == "__main__":
    main()
