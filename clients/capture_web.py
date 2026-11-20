#!/usr/bin/env python3
"""Drive the real Capture web app through the hub, online then offline.

Run by internal/live/capture_web_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /offline, /online), HUB_USER, HUB_PASSWORD,
  ORG_UNIT, PROGRAMS (comma separated), ENROLLMENT (one to open), OUT (folder),
  WEIGHT (saves a new event on that enrollment offline, with this weight).
Prints one JSON object: what loaded, rows seen, and every request the hub could
not answer offline. Exits non-zero only if the browser itself fails.
"""
import json
import os
import sys
import time
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER, PASSWORD = os.environ["HUB_USER"], os.environ["HUB_PASSWORD"]
OU = os.environ["ORG_UNIT"]
PROGRAMS = [p for p in os.environ.get("PROGRAMS", "").split(",") if p]
ENROLLMENT = os.environ.get("ENROLLMENT", "")
OUT = os.environ.get("OUT", ".")
WEIGHT = os.environ.get("WEIGHT", "")  # set: save an event offline with this weight


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=60).read()


def main():
    result = {"online": {}, "offline": {}}
    phase = {"name": "online"}
    failures = {"online": [], "offline": []}
    errors = {"online": [], "offline": []}
    with sync_playwright() as pw:
        browser = pw.chromium.launch(channel=os.environ.get("PW_CHANNEL") or None, args=["--disable-dev-shm-usage"])
        try:
            ctx = browser.new_context(viewport={"width": 1400, "height": 900})
            page = ctx.new_page()

            def on_response(resp):
                if resp.url.startswith(HUB) and resp.status >= 400 and resp.status != 404:
                    path = resp.url[len(HUB):]
                    failures[phase["name"]].append({"status": resp.status, "path": path[:300]})

            page.on("response", on_response)
            page.on("pageerror", lambda e: errors[phase["name"]].append(str(e)[:300]))

            r = ctx.request.post(HUB + "/api/auth/login", data={"username": USER, "password": PASSWORD})
            result["login"] = r.status
            if r.status != 200:
                print(json.dumps(result))
                return

            def app():
                # From 2.42 the app runs in a frame inside the global shell; earlier, in the page.
                for f in page.frames:
                    if f != page.main_frame and "capture" in f.url:
                        return f
                return page.main_frame

            def save_event(out):
                # A new event in the first stage, saved offline as staff would: the
                # report date, a weight the test looks for on central, then "Save
                # without completing". The hub's answer to the save is recorded.
                a = app()
                a.locator("button", has_text="New event").first.click(timeout=15000)
                a.locator("[data-test=program-stage-selector-button]").first.click(timeout=15000)
                a.get_by_placeholder("yyyy-mm-dd").first.fill(time.strftime("%Y-%m-%d"))
                a.get_by_placeholder("yyyy-mm-dd").first.press("Tab")
                a.locator("text=Synthetic weight").locator("xpath=following::input[1]").fill(WEIGHT)
                a.locator("text=Synthetic weight").locator("xpath=following::input[1]").press("Tab")
                with page.expect_response(lambda r: "/api/" in r.url and "tracker" in r.url and r.request.method == "POST",
                                          timeout=60000) as resp:
                    a.locator("button", has_text="Save without completing").first.click(timeout=15000)
                out["save-status"] = resp.value.status
                page.wait_for_timeout(5000)
                page.screenshot(path=os.path.join(OUT, "offline-saved.png"), full_page=True)
                out["after-save"] = a.locator("body").inner_text()[:1500]

            def visit(tag):
                p = phase["name"]
                out = {}
                page.goto(HUB + "/dhis-web-capture/index.html", wait_until="networkidle", timeout=120000)
                out["app"] = page.title()
                for prog in PROGRAMS:
                    page.goto(f"{HUB}/dhis-web-capture/index.html#/?orgUnitId={OU}&programId={prog}",
                              wait_until="networkidle", timeout=120000)
                    try:  # the list appears after its own queries
                        app().wait_for_selector("table tbody tr", timeout=30000)
                    except Exception:
                        pass
                    page.wait_for_timeout(1000)
                    out[f"rows:{prog}"] = app().locator("table tbody tr").count()
                    page.screenshot(path=os.path.join(OUT, f"{tag}-{prog}.png"))
                if ENROLLMENT:
                    page.goto(f"{HUB}/dhis-web-capture/index.html#/enrollment?enrollmentId={ENROLLMENT}&orgUnitId={OU}&programId={PROGRAMS[0]}",
                              wait_until="networkidle", timeout=120000)
                    page.wait_for_timeout(3000)
                    out["dashboard"] = app().locator("body").inner_text()[:2000]
                    page.screenshot(path=os.path.join(OUT, f"{tag}-dashboard.png"))
                    if tag == "offline" and WEIGHT:
                        save_event(out)
                result[p] = out

            visit("online")
            control("offline")
            phase["name"] = "offline"
            visit("offline")
            control("online")
        finally:
            browser.close()
    result["failed"] = failures
    result["errors"] = errors
    print(json.dumps(result))


if __name__ == "__main__":
    main()
