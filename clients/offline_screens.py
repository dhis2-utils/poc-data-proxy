#!/usr/bin/env python3
"""Open Capture's and the Data Entry app's screens through the hub as one user, in a
fresh browser (no app caches of its own), and record what the hub could not answer.

Run by internal/live/offline_screens_test.go, which passes:
  HUB_URL, USER, PASSWORD, ORG_UNIT, OUT (folder for screenshots), TAG (names them),
  MODE: screens (every screen below), front (Capture's front page only), login (the
    login only, no app screen)
  PROGRAMS (tracker programs, comma separated), EVENT_PROGRAMS, ENROLLMENT (program:
    enrollment, for a dashboard), DATASETS (dataSet:period, comma separated)
Screens: Capture's front page, each program's working list, a dashboard, each tracker
program's registration form, each event program's new event form, and each data set's
form in the Data Entry app.
Prints one JSON object: per screen, whether it showed what it should (rows, fields,
cells) and every hub answer of 400 or more except 404 (offline, 503 is "needs
connection"). Exits non-zero only if the browser itself fails.
"""
import json
import os
import time

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
USER, PASSWORD = os.environ["USER"], os.environ["PASSWORD"]
OU = os.environ["ORG_UNIT"]
OUT = os.environ.get("OUT", ".")
TAG = os.environ.get("TAG", USER)
MODE = os.environ.get("MODE", "screens")


def env_list(name):
    return [x for x in os.environ.get(name, "").split(",") if x]


PROGRAMS, EVENT_PROGRAMS, DATASETS = env_list("PROGRAMS"), env_list("EVENT_PROGRAMS"), env_list("DATASETS")
ENROLLMENT = os.environ.get("ENROLLMENT", "")

R = {"user": USER, "mode": MODE, "screens": {}, "errors": []}
CUR = {"screen": "login"}


def save():
    """The result so far, in OUT/TAG.json: the test reads it if the browser hangs."""
    with open(os.path.join(OUT, TAG + ".json"), "w") as f:
        json.dump(R, f)


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(channel=os.environ.get("PW_CHANNEL") or None, args=["--disable-dev-shm-usage"])
        try:
            ctx = browser.new_context(viewport={"width": 1400, "height": 900}, service_workers="block")
            P = {}

            def on_response(resp):
                if resp.url.startswith(HUB) and resp.status >= 400 and resp.status != 404:
                    s = R["screens"].setdefault(CUR["screen"], {})
                    s.setdefault("failed", []).append({"status": resp.status, "path": resp.url[len(HUB):][:300]})

            def new_page():
                # Also after a crash ("Page crashed" when memory runs short).
                page = ctx.new_page()
                page.on("response", on_response)
                page.on("pageerror", lambda e: R["errors"].append(f"{CUR['screen']}: {str(e)[:200]}"))
                P["page"] = page

            new_page()

            r = ctx.request.post(HUB + "/api/auth/login", data={"username": USER, "password": PASSWORD})
            R["login"] = r.status
            if r.status != 200 or MODE == "login":
                return

            def frame(name):
                # From 2.42 the app runs in a frame of the global shell, which may
                # appear after the page loaded; a shell that shows its login form
                # never gets one.
                page = P["page"]
                for _ in range(20):
                    for f in page.frames:
                        if f != page.main_frame and name in f.url:
                            return f
                    page.wait_for_timeout(500)
                return page.main_frame

            def count(app, selector, wait):
                # The app may replace its frame while loading (a detached frame):
                # look it up again until the wait is over.
                deadline = time.time() + wait / 1000
                f = frame(app)
                while time.time() < deadline:
                    try:
                        f.wait_for_selector(selector, timeout=max(1000, (deadline - time.time()) * 1000))
                        break
                    except Exception as e:
                        if "detached" not in str(e):
                            break
                        f = frame(app)
                P["page"].wait_for_timeout(1500)
                try:
                    return f, f.locator(selector).count()
                except Exception:
                    return f, -1

            def screen(name, url, app, selector, wait=30000, retry=True):
                CUR["screen"] = name
                s = R["screens"].setdefault(name, {})
                try:
                    try:
                        P["page"].goto(HUB + url, wait_until="networkidle", timeout=120000)
                    except Exception as e:
                        if "crash" in str(e) or "closed" in str(e):
                            raise
                        s["error"] = str(e)[:200]
                    f, s["shown"] = count(app, selector, wait)
                    try:
                        s["text"] = f.locator("body").inner_text(timeout=5000)[:300]
                    except Exception:
                        s["text"] = ""
                    P["page"].screenshot(path=os.path.join(OUT, f"{TAG}-{name.replace(':', '-')}.png"))
                except Exception as e:
                    s.setdefault("crashes", []).append(str(e)[:200])
                    try:
                        P["page"].close()
                    except Exception:
                        pass
                    new_page()
                    if retry:
                        screen(name, url, app, selector, wait, retry=False)
                save()

            cap = "/dhis-web-capture/index.html#"
            screen("front", cap + f"/?orgUnitId={OU}", "capture", "text=Get started with Capture app")
            if MODE == "front":
                return
            for p in PROGRAMS + EVENT_PROGRAMS:
                # A working list's rows, or a search-first programme's search form
                screen(f"list:{p}", cap + f"/?orgUnitId={OU}&programId={p}", "capture",
                       "table tbody tr, :text('Search for')")
            if ENROLLMENT:
                prog, enr = ENROLLMENT.split(":")
                screen("dashboard", cap + f"/enrollment?enrollmentId={enr}&orgUnitId={OU}&programId={prog}",
                       "capture", "text=Stages and Events")
            for p in PROGRAMS:
                screen(f"register:{p}", cap + f"/new?orgUnitId={OU}&programId={p}", "capture", "input")
            for p in EVENT_PROGRAMS:
                screen(f"event:{p}", cap + f"/new?orgUnitId={OU}&programId={p}", "capture", "input")
            for d in DATASETS:
                ds, pe = d.split(":")
                screen(f"form:{ds}", f"/apps/aggregate-data-entry#/?dataSetId={ds}&orgUnitId={OU}&periodId={pe}",
                       "aggregate-data-entry", "[data-test=dhis2-dataentryapp-dataentrycell]", 90000)
        finally:
            save()
            print(json.dumps(R), flush=True)
            browser.close()


if __name__ == "__main__":
    main()
