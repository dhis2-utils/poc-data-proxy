#!/usr/bin/env python3
"""Drive the Aggregate Data Entry app through the hub with one shared browser, in one
of three scenarios (SCENARIO):

complete (the default): user A enters a value online, and B opens the app online once
  (each user must, as with Capture: the app's settings are cached per user); then
  offline (browser closed and opened again) A enters another value and marks the form
  complete; B opens the same browser offline; back online, B opens the app. Each value
  must reach central as A's.
drafts: A runs validation online; offline A enters VALUE_A (which breaks a validation
  rule) and runs validation again; the hub restarts; A reopens the form, changes row A
  to VALUE_C and clears row B, leaving the form incomplete; B opens it offline; back
  online A opens it again.
update: A enters VALUE_A online; offline, the app is updated on central (GET
  /update-app), A reopens the app and enters VALUE_B; online, A opens the app (twice:
  a service worker may serve the old one first) and enters VALUE_C; offline again A
  enters VALUE_D with the new app and B opens it. Each step records which build
  (main-*.js) the app frame loaded.

Run by internal/live/aggregate_client_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /offline, /online, /sync, /reboot, /update-app), USER_A,
  USER_B, HUB_PASSWORD, DATASET, ORGUNIT, PERIOD, ROW_A, ROW_B (row labels), COLUMN
  (1-based data cell), VALUE_A, VALUE_B (VALUE_C, VALUE_D), OUT (folder for
  screenshots), PROFILE (the browser profile; a local disk: Chromium's profile on a
  host-mounted folder starts unreliably).
Prints one JSON object: each step's outcome, every write the app sent and its status,
and what the app showed. Exits non-zero only if the browser itself fails.
"""
import json
import os
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
A, B, PASSWORD = os.environ["USER_A"], os.environ["USER_B"], os.environ["HUB_PASSWORD"]
DS, OU, PE = os.environ["DATASET"], os.environ["ORGUNIT"], os.environ["PERIOD"]
ROW_A, ROW_B, COLUMN = os.environ["ROW_A"], os.environ["ROW_B"], int(os.environ["COLUMN"])
VALUE_A, VALUE_B = os.environ["VALUE_A"], os.environ["VALUE_B"]
VALUE_C, VALUE_D = os.environ.get("VALUE_C", ""), os.environ.get("VALUE_D", "")
SCENARIO = os.environ.get("SCENARIO", "complete")
OUT = os.environ.get("OUT", ".")
PROFILE = os.environ.get("PROFILE") or os.path.join(OUT, "profile")  # one browser, shared by both users

R = {"steps": {}, "writes": [], "failed": [], "errors": []}


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=300).read()


class Browser:
    """The shared browser, opened for one user and one phase."""

    def __init__(self, pw, user, phase):
        self.user, self.phase = user, phase
        self.ctx = pw.chromium.launch_persistent_context(PROFILE, viewport={"width": 1500, "height": 1000},
                                                         args=["--disable-dev-shm-usage"])
        self.page = self.ctx.pages[0] if self.ctx.pages else self.ctx.new_page()
        self.page.on("response", self._response)
        self.page.on("pageerror", lambda e: R["errors"].append(f"{self.user} {self.phase}: {str(e)[:200]}"))

    def _response(self, r):
        if not r.url.startswith(HUB):
            return
        path = r.url[len(HUB):]
        if r.request.method in ("POST", "PUT", "DELETE"):
            body = ""
            try:
                body = r.text()[:300]
            except Exception:
                pass
            R["writes"].append({"user": self.user, "phase": self.phase, "method": r.request.method,
                                "path": path.split("?")[0], "status": r.status, "body": body})
        elif r.status >= 400 and r.status != 404:
            R["failed"].append({"user": self.user, "phase": self.phase, "status": r.status, "path": path[:200]})

    def login(self):
        r = self.ctx.request.post(HUB + "/api/auth/login", data={"username": self.user, "password": PASSWORD})
        R["steps"][f"login {self.user} {self.phase}"] = r.status

    @property
    def app(self):
        # 2.42+ opens the app inside its global shell (an iframe); a reopened app may
        # be served by its own service worker without the shell.
        for fr in self.page.frames:
            if "aggregate-data-entry/index.html" in fr.url or "/api/apps/aggregate-data-entry" in fr.url:
                return fr
        return self.page.main_frame

    def form(self):
        self.page.goto(f"{HUB}/apps/aggregate-data-entry#/?dataSetId={DS}&orgUnitId={OU}&periodId={PE}",
                       wait_until="domcontentloaded", timeout=120_000)
        for _ in range(180):
            if self.cell(ROW_A).count():
                break
            self.page.wait_for_timeout(1000)
        self.cell(ROW_A).wait_for(timeout=5_000)
        self.page.wait_for_timeout(4000)

    def cell(self, row):
        return self.app.locator(f"xpath=//tr[td[normalize-space()='{row}']]"
                                f"/td[@data-test='dhis2-dataentryapp-dataentrycell'][{COLUMN}]//input").first

    def enter(self, row, value):
        c = self.cell(row)
        c.click()
        c.fill(value)
        c.press("Tab")
        self.page.wait_for_timeout(3000)

    def complete(self):
        """Press "Mark complete"; return the completion request's status and what the
        app shows afterwards (bottom bar and any alert)."""
        status = "no request"
        try:
            with self.page.expect_response(lambda r: "dataSetCompletion" in r.url, timeout=30_000) as info:
                self.app.get_by_role("button", name="Mark complete").click()
            status = info.value.status
        except Exception as e:
            status = f"no request ({str(e)[:80]})"
        self.page.wait_for_timeout(3000)
        texts = []
        for sel in ['[data-test="dhis2-uicore-alertbar"]', '[data-test="bottom-bar"]', '[role="dialog"]']:
            for fr in (self.app, self.page.main_frame):
                try:
                    loc = fr.locator(sel)
                    for i in range(loc.count()):
                        texts.append(loc.nth(i).inner_text()[:300])
                except Exception:
                    pass
        return status, texts

    def clear(self, row):
        c = self.cell(row)
        c.click()
        c.fill("")
        c.press("Tab")
        self.page.wait_for_timeout(3000)

    def shown(self):
        """The texts of alerts, dialogs and the side panel (validation results)."""
        texts = []
        for sel in ['[data-test="dhis2-uicore-alertbar"]', '[role="dialog"]', "aside"]:
            for fr in (self.app, self.page.main_frame):
                try:
                    loc = fr.locator(sel)
                    for i in range(loc.count()):
                        texts.append(loc.nth(i).inner_text()[:300])
                except Exception:
                    pass
        return texts

    def validate(self):
        """Press "Run validation"; return the validation request's status and what the
        app shows."""
        status = "no request"
        try:
            with self.page.expect_response(lambda r: "/validation/dataSet/" in r.url, timeout=30_000) as info:
                self.app.get_by_role("button", name="Run validation").first.click()
            status = info.value.status
        except Exception as e:
            status = f"no request ({str(e)[:80]})"
        self.page.wait_for_timeout(4000)
        for _ in range(60):  # the app retries a failed request before it says so
            if "Running validation" not in " ".join(self.shown()):
                break
            self.page.wait_for_timeout(1000)
        return status, self.shown()

    def completion(self):
        """The completion button the bottom bar offers: "Mark complete" (incomplete) or
        "Mark incomplete"."""
        for name in ("Mark complete", "Mark incomplete"):
            if self.app.get_by_role("button", name=name, exact=True).count():
                return name
        return ""

    def bundle(self):
        """The build the app frame runs: its main-*.js."""
        try:
            return ",".join(self.app.evaluate(
                "[...document.querySelectorAll('script[src*=\"/assets/main-\"]')].map(s => s.src.split('/').pop())"))
        except Exception as e:
            return f"unknown ({str(e)[:80]})"

    def shot(self, name):
        self.page.screenshot(path=os.path.join(OUT, f"{name}.png"), full_page=True)

    def close(self):
        self.ctx.close()


OPEN = []  # the browser now open, for the error screenshot


def browse(pw, user, phase):
    """Open the shared browser as user, log in to the hub and open the form."""
    b = Browser(pw, user, phase)
    OPEN[:] = [b]
    b.login()
    b.form()
    return b


def done(b):
    b.close()
    OPEN.clear()


def complete(pw):
    s = R["steps"]
    b = browse(pw, A, "online")
    b.enter(ROW_A, VALUE_A)
    b.shot("1-online")
    done(b)
    done(browse(pw, B, "online"))  # B has opened the app online through the hub before

    control("offline")
    b = browse(pw, A, "offline")  # the browser was closed: log in to the hub again
    s["A offline sees its online value"] = b.cell(ROW_A).input_value()
    b.enter(ROW_B, VALUE_B)
    s["complete offline: status"], s["complete offline: shown"] = b.complete()
    b.shot("2-complete-offline")
    done(b)

    b = browse(pw, B, "offline")  # the same browser, another user
    s["B offline: row A"] = b.cell(ROW_A).input_value()
    s["B offline: row B"] = b.cell(ROW_B).input_value()
    b.shot("3-offline-b")
    done(b)

    control("online")
    control("sync")
    b = browse(pw, B, "reconnected")
    b.page.wait_for_timeout(20_000)  # anything queued in the browser would replay now
    b.shot("4-reconnected")
    done(b)


def drafts(pw):
    s = R["steps"]
    b = browse(pw, A, "online")
    s["validate online: status"], s["validate online: shown"] = b.validate()
    b.shot("1-validated-online")
    done(b)
    done(browse(pw, B, "online"))

    control("offline")
    b = browse(pw, A, "offline")
    b.enter(ROW_A, VALUE_A)  # breaks the rule
    s["validate offline: status"], s["validate offline: shown"] = b.validate()
    b.shot("2-validated-offline")
    done(b)

    control("reboot")  # the hub restarts, still offline
    b = browse(pw, A, "after restart")
    s["A after restart: row A"] = b.cell(ROW_A).input_value()
    s["A after restart: row B"] = b.cell(ROW_B).input_value()
    b.enter(ROW_A, VALUE_C)
    b.clear(ROW_B)
    s["A after restart: completion"] = b.completion()
    b.shot("3-draft-edited")
    done(b)

    b = browse(pw, B, "offline")
    s["B offline: row A"] = b.cell(ROW_A).input_value()
    s["B offline: row B"] = b.cell(ROW_B).input_value()
    s["B offline: completion"] = b.completion()
    b.shot("4-offline-b")
    done(b)

    control("online")
    control("sync")
    b = browse(pw, A, "reconnected")
    b.page.wait_for_timeout(10_000)
    s["A reconnected: row A"] = b.cell(ROW_A).input_value()
    s["A reconnected: row B"] = b.cell(ROW_B).input_value()
    s["A reconnected: completion"] = b.completion()
    b.shot("5-reconnected")
    done(b)


def update(pw):
    s = R["steps"]

    def opened(b, name):
        s[name + ": build"] = b.bundle()
        s[name + ": shown"] = b.shown()

    b = browse(pw, A, "online")
    opened(b, "online")
    b.enter(ROW_A, VALUE_A)
    b.shot("1-online")
    done(b)
    done(browse(pw, B, "online"))

    control("offline")
    control("update-app")  # on central, which the hub cannot reach
    b = browse(pw, A, "offline")
    opened(b, "offline after the update")
    b.enter(ROW_B, VALUE_B)
    b.shot("2-offline-after-update")
    done(b)

    control("online")
    control("sync")
    b = browse(pw, A, "online")
    opened(b, "online, first load")
    b.shot("3-online-first-load")
    b.page.wait_for_timeout(10_000)  # let a service worker take the new build
    b.page.goto("about:blank")
    b.form()
    opened(b, "online, reloaded")
    s["online, reloaded: row B"] = b.cell(ROW_B).input_value()
    b.enter(ROW_A, VALUE_C)
    b.shot("4-online-reloaded")
    done(b)

    control("offline")
    b = browse(pw, A, "offline")
    opened(b, "offline, new build")
    s["offline, new build: row A"] = b.cell(ROW_A).input_value()
    b.enter(ROW_B, VALUE_D)
    b.shot("5-offline-new-build")
    done(b)
    b = browse(pw, B, "offline")
    opened(b, "B offline, new build")
    s["B offline: row A"] = b.cell(ROW_A).input_value()
    s["B offline: row B"] = b.cell(ROW_B).input_value()
    b.shot("6-offline-b")
    done(b)
    control("online")
    control("sync")


def main():
    with sync_playwright() as pw:
        try:
            {"complete": complete, "drafts": drafts, "update": update}[SCENARIO](pw)
        except Exception as e:  # report what happened so far, with a screenshot
            R["error"] = f"{type(e).__name__}: {str(e)[:500]}"
            for b in OPEN:
                try:
                    b.shot("error")
                    b.close()
                except Exception:
                    pass
        finally:
            control("online")
    print(json.dumps(R))


if __name__ == "__main__":
    main()
