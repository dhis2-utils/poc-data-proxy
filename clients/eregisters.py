#!/usr/bin/env python3
"""Drive the eRegisters app through the hub: two devices, one registers a client
offline and pushes, the other pulls and finds them.

Run by internal/live/eregisters_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /offline, /online), USER_A, USER_B, HUB_PASSWORD,
  SURNAME (unique for the run), OUT (folder for screenshots).
Prints one JSON object: each step's outcome, the status of every push and pull, and
every request the hub could not answer offline. Exits non-zero only if the browser
itself fails.
"""
import json
import os
import urllib.request

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
            self.result["tracker"].append({"user": self.user, "phase": self.phase, "method": resp.request.method,
                                           "status": resp.status, "path": path[:200]})
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
        for _ in range(5):
            try:
                self.app.get_by_role("button", name="Register New Client").wait_for(timeout=WAIT)
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
        return app.locator("tr", has_text=surname).count()


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
    main()
