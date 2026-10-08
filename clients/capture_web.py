#!/usr/bin/env python3
"""Drive the real Capture web app through the hub, online then offline.

Run by internal/live/capture_web_test.go, which starts the hub and passes:
  HUB_URL, CONTROL_URL (GET /offline, /online), HUB_USER, HUB_PASSWORD,
  ORG_UNIT, PROGRAMS (comma separated), ENROLLMENT (one to open), OUT (folder).
Prints one JSON object: what loaded, rows seen, and every request the hub could
not answer offline. Exits non-zero only if the browser itself fails.
"""
import json
import os
import sys
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER, PASSWORD = os.environ["HUB_USER"], os.environ["HUB_PASSWORD"]
OU = os.environ["ORG_UNIT"]
PROGRAMS = [p for p in os.environ.get("PROGRAMS", "").split(",") if p]
ENROLLMENT = os.environ.get("ENROLLMENT", "")
OUT = os.environ.get("OUT", ".")


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=60).read()


def main():
    result = {"online": {}, "offline": {}}
    phase = {"name": "online"}
    failures = {"online": [], "offline": []}
    errors = {"online": [], "offline": []}
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--disable-dev-shm-usage"])
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

            def visit(tag):
                p = phase["name"]
                out = {}
                page.goto(HUB + "/dhis-web-capture/index.html", wait_until="networkidle", timeout=120000)
                out["app"] = page.title()
                for prog in PROGRAMS:
                    page.goto(f"{HUB}/dhis-web-capture/index.html#/?orgUnitId={OU}&programId={prog}",
                              wait_until="networkidle", timeout=120000)
                    page.wait_for_timeout(3000)
                    out[f"rows:{prog}"] = page.locator("table tbody tr").count()
                    page.screenshot(path=os.path.join(OUT, f"{tag}-{prog}.png"))
                if ENROLLMENT:
                    page.goto(f"{HUB}/dhis-web-capture/index.html#/enrollment?enrollmentId={ENROLLMENT}",
                              wait_until="networkidle", timeout=120000)
                    page.wait_for_timeout(3000)
                    out["dashboard"] = page.locator("body").inner_text()[:2000]
                    page.screenshot(path=os.path.join(OUT, f"{tag}-dashboard.png"))
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
