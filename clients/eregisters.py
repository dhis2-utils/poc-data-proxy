#!/usr/bin/env python3
"""Drive the eRegisters app through the hub: two devices, one registers a client
offline and pushes, the other pulls and finds them.

Run by internal/live/eregisters_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /offline, /online), USER_A, USER_B, HUB_PASSWORD,
  SURNAME (unique for the run), OUT (folder for screenshots).
Prints one JSON object: each step's outcome, the status of every push and pull, and
every request the hub could not answer offline. Exits non-zero only if the browser
itself fails.

SCENARIO=update (adversarial N24) runs main_update instead: the app updated on central
(CONTROL_URL/install) while the hub is offline. It also needs ZIP_OLD and ZIP_NEW, the
two builds, to tell which one a browser runs, and SURNAME2 for a second client.
"""
import json
import os
import re
import shutil
import urllib.request
import zipfile

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER_A, USER_B, PASSWORD = os.environ["USER_A"], os.environ["USER_B"], os.environ["HUB_PASSWORD"]
SURNAME = os.environ["SURNAME"]
OUT = os.environ.get("OUT", ".")
APP = HUB + "/api/apps/eregisters/index.html"
WAIT = 300_000  # first metadata sync of a fresh browser


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=60).read()


class Device:
    """One device: a browser profile of its own, so eRegisters keeps its local data
    when the browser is closed and opened again. One device runs at a time."""

    def __init__(self, pw, user, result):
        self.pw, self.user, self.result = pw, user, result
        self.profile = os.path.join(OUT, "profile-" + user)
        self.phase = "online"
        self.ctx = None

    def start(self):
        self.ctx = self.pw.chromium.launch_persistent_context(
            self.profile, viewport={"width": 1400, "height": 1000}, args=["--disable-dev-shm-usage"])
        self.page = self.ctx.pages[0] if self.ctx.pages else self.ctx.new_page()
        self.page.on("response", self._response)
        self.page.on("pageerror", lambda e: self.result["errors"].append(f"{self.user} {self.phase}: {str(e)[:300]}"))

    def close(self):
        if self.ctx:
            self.ctx.close()
            self.ctx = None

    def _response(self, resp):
        if not resp.url.startswith(HUB):
            return
        path = resp.url[len(HUB):]
        if "/tracker" in path.split("?")[0]:
            entry = {"user": self.user, "phase": self.phase, "method": resp.request.method,
                     "status": resp.status, "path": path[:200]}
            if resp.request.method == "GET" and resp.status == 200:
                try:  # what the pull returned: how many, and whether the run's client is among them
                    body = resp.text()
                    entry["count"] = len(json.loads(body).get("trackedEntities") or [])
                    entry["hasClient"] = SURNAME in body
                    with open(os.path.join(OUT, f"pull-{self.user}-{self.phase}.json"), "w") as f:
                        f.write(body)
                except Exception:
                    pass
            self.result["tracker"].append(entry)
        if resp.status >= 400 and resp.status != 404 and not path.endswith(".map"):
            self.result["failed"].append({"user": self.user, "phase": self.phase, "status": resp.status,
                                          "path": path[:300]})

    @property
    def app(self):
        # 2.43 opens apps inside its global shell (an iframe); a reopened app is served
        # straight from its own service worker, without the shell.
        if self.page.locator("iframe").count():
            return self.page.frame_locator("iframe").first
        return self.page

    def running(self):
        """The app's main script in the page: which build the browser runs."""
        for f in [self.page.main_frame, *self.page.frames]:
            if "/api/apps/eregisters/" not in f.url:
                continue
            try:
                srcs = f.evaluate("Array.from(document.scripts).map(s => s.src)")
            except Exception as e:
                return f"error {str(e)[:80]}"
            for src in srcs:
                m = re.search(r"assets/(main-[^/]+\.js)", src)
                if m:
                    return BUILDS.get(m.group(1), m.group(1))
        return None

    def shot(self, name):
        self.page.screenshot(path=os.path.join(OUT, f"{self.user}-{name}.png"), full_page=True)

    def open(self):
        self.start()
        r = self.ctx.request.post(HUB + "/api/auth/login", data={"username": self.user, "password": PASSWORD})
        if r.status != 200:
            raise RuntimeError(f"login {self.user}: {r.status}")
        self.page.goto(APP, wait_until="domcontentloaded", timeout=120_000)
        self.ready()

    def ready(self):
        """Wait until the app shows its start screen. The app's admin can ask every
        device to reload (a banner, then a reload): do it now rather than mid-form."""
        for _ in range(WAIT // 10_000):
            try:  # short waits: self.app is chosen again each time, once the shell's frame is there
                self.app.get_by_role("button", name="Register New Client").wait_for(timeout=10_000)
                self.page.wait_for_timeout(2000)
                reload = self.app.get_by_role("button", name="Reload now")
                if not (reload.count() and reload.first.is_visible()):
                    return
                reload.first.click()
            except Exception:  # the frame went away in a reload: wait for the new one
                pass
            self.page.wait_for_timeout(5000)
        raise RuntimeError(f"{self.user}: the app did not settle")

    def sync(self, button, method, timeout=120_000):
        """Press Pull Data or Push Data and wait for its tracker request."""
        def is_tracker(r):
            p = r.url.split("?")[0]
            return r.request.method == method and (p.endswith("/tracker") or p.endswith("/tracker/trackedEntities"))
        try:
            with self.page.expect_response(is_tracker, timeout=timeout) as info:
                self.app.get_by_role("button", name=button).first.click()
            status = info.value.status
        except Exception as e:  # no request: nothing to send, or the app did not try
            status = f"no request ({str(e)[:80]})"
        self.page.wait_for_timeout(5000)  # let the app finish the rest of the cycle
        return status

    def register(self, first, surname):
        app = self.app
        app.get_by_role("button", name="Register New Client").click()
        dialog = app.get_by_role("dialog")
        dialog.get_by_text("Registration Date").wait_for(timeout=30_000)

        def date(label, value):
            box = dialog.locator(".ant-form-item", has_text=label).locator("input").first
            box.click()
            box.fill(value)
            box.press("Enter")

        def text(label, value):
            dialog.locator(".ant-form-item", has_text=label).locator("input").first.fill(value)

        def radio(label, value):
            dialog.locator(".ant-form-item", has_text=label).get_by_text(value, exact=True).click()

        date("Registration Date", "2026-10-14")
        text("Surname", surname)
        text("First name", first)
        radio("Sex", "Female")
        date("Date of Birth", "1990-05-01")
        radio("Client Category", "National")
        self.shot("form")
        dialog.get_by_role("button", name="Register client", exact=True).click()
        self.page.wait_for_timeout(5000)
        self.shot("registered")

    def search(self, surname):
        app = self.app
        app.get_by_role("button", name="Clear").first.click()
        app.locator(".ant-form-item", has_text="Surname").locator("input").first.fill(surname)
        app.get_by_role("button", name="Search", exact=True).click()
        self.page.wait_for_timeout(3000)
        self.shot("search")
        # 1.1.5 lists results in table rows; 1.1.9's table is built otherwise: count
        # the cells showing the surname (the search box holds it as a value, not text).
        return max(app.locator("tr", has_text=surname).count(), app.get_by_text(surname, exact=True).count())


BUILDS = {}  # main script name: version (main_update)


def builds():
    for path in (os.environ.get("ZIP_OLD"), os.environ.get("ZIP_NEW")):
        if not path:
            continue
        with zipfile.ZipFile(path) as z:
            version = json.loads(z.read("manifest.webapp"))["version"]
            for name in re.findall(r"assets/(main-[^\"']+\.js)", z.read("index.html").decode()):
                BUILDS[name] = version


def main_update():
    """N24: two devices on the old build; the new build installed on central while the
    hub is offline; each device works offline and online across the update."""
    builds()
    for user in (USER_A, USER_B):  # fresh devices: a profile left from a run before holds the new build
        shutil.rmtree(os.path.join(OUT, "profile-" + user), ignore_errors=True)
    surname2 = os.environ["SURNAME2"]
    result = {"steps": {}, "tracker": [], "failed": [], "errors": [], "builds": BUILDS}
    steps = result["steps"]

    def step(name, d, fn):
        try:
            steps[name] = fn()
        except Exception as e:
            steps[name] = f"{type(e).__name__}: {str(e)[:300]}"
            d.shot("error-" + name.replace(" ", "-"))
        steps[name + ": build"] = d.running() if d.ctx else None

    with sync_playwright() as pw:
        a, b = Device(pw, USER_A, result), Device(pw, USER_B, result)
        try:
            b.open()
            step("B online, pull", b, lambda: b.sync("Pull Data", "GET"))
            b.close()
            a.open()
            step("A online, pull", a, lambda: a.sync("Pull Data", "GET"))
            a.close()

            control("offline")
            a.phase = b.phase = "offline"
            control("install")  # on central, which the hub cannot reach now
            a.open()
            step("A offline after the install, registers", a, lambda: a.register("Ama", SURNAME) or "done")
            step("A offline, push", a, lambda: a.sync("Push Data", "POST"))
            a.close()

            control("online")
            control("sync")  # the hub pushes A's client to central
            a.phase = b.phase = "online-new"
            b.open()
            step("B online after the install, pull", b, lambda: b.sync("Pull Data", "GET"))
            step("B online finds A's client", b, lambda: b.search(SURNAME) > 0)
            b.close()

            control("offline")
            a.phase = b.phase = "offline-new"
            b.open()
            step("B offline, registers", b, lambda: b.register("Abena", surname2) or "done")
            step("B offline, push", b, lambda: b.sync("Push Data", "POST"))
            b.close()
            a.open()
            step("A offline, pull", a, lambda: a.sync("Pull Data", "GET"))
            step("A offline finds B's client", a, lambda: a.search(surname2) > 0)
            a.close()

            control("online")
            a.phase = b.phase = "online-end"
            a.open()
            step("A online at the end, pull", a, lambda: a.sync("Pull Data", "GET"))
            step("A online finds both", a, lambda: a.search(SURNAME) > 0 and a.search(surname2) > 0)
            a.close()
        except Exception as e:
            result["error"] = f"{type(e).__name__}: {str(e)[:500]}"
            for d in (a, b):
                if d.ctx:
                    d.shot("error")
        finally:
            control("online")
            a.close()
            b.close()
    print(json.dumps(result))


def main():
    result = {"steps": {}, "tracker": [], "failed": [], "errors": []}
    steps = result["steps"]
    with sync_playwright() as pw:
        a, b = Device(pw, USER_A, result), Device(pw, USER_B, result)
        try:
            b.open()  # B has used the app online before
            steps["pull online B"] = b.sync("Pull Data", "GET")
            b.close()
            a.open()
            steps["pull online A"] = a.sync("Pull Data", "GET")

            control("offline")
            a.phase = b.phase = "offline"
            a.register("Ama", SURNAME)
            steps["push offline A"] = a.sync("Push Data", "POST")
            a.shot("pushed")
            a.close()
            b.open()  # offline: logs in to the hub, opens the app from its cache
            steps["pull offline B"] = b.sync("Pull Data", "GET")
            steps["B finds the client offline"] = b.search(SURNAME) > 0
            b.close()
        except Exception as e:  # report what happened so far, with a screenshot
            result["error"] = f"{type(e).__name__}: {str(e)[:500]}"
            for d in (a, b):
                if d.ctx:
                    d.shot("error")
        finally:
            control("online")
            a.close()
            b.close()
    print(json.dumps(result))


if __name__ == "__main__":
    main_update() if os.environ.get("SCENARIO") == "update" else main()
