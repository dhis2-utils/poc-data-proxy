#!/usr/bin/env python3
"""A15: does Capture web reload metadata offline, and is it shown a stale form?

Run by internal/live/a15_metadata_test.go with:
  HUB_URL, CONTROL_URL (GET /midpoint: the test renames the program on central and
  cuts the uplink; GET /online restores it), HUB_USER, HUB_PASSWORD, ORG_UNIT,
  PROGRAM (id), OLD_NAME (the program's name before the change), NEW_NAME (after),
  OUT (folder for screenshots and result.json).

Phase "online": log in through the hub, open the program's working list and a
new-registration form; record every response (path, status, X-Hub-Mode) and the
program name the app shows. Then GET CONTROL_URL/midpoint. Phase "offline": reload
the app, open the same screens again; record the same. Prints one JSON object.
"""
import json
import os
import time
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER, PASSWORD = os.environ["HUB_USER"], os.environ["HUB_PASSWORD"]
OU = os.environ["ORG_UNIT"]
PROGRAM = os.environ["PROGRAM"]
OLD_NAME = os.environ["OLD_NAME"]
NEW_NAME = os.environ["NEW_NAME"]
OUT = os.environ.get("OUT", ".")


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=120).read()


def main():
    reqs = {"online": [], "offline": []}
    shown = {"online": {}, "offline": {}}
    errors = {"online": [], "offline": []}
    phase = {"name": "online"}
    result = {"login": 0}
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--disable-dev-shm-usage"])
        try:
            ctx = browser.new_context(viewport={"width": 1400, "height": 900})
            page = ctx.new_page()

            def on_response(resp):
                if resp.url.startswith(HUB):
                    path = resp.url[len(HUB):]
                    reqs[phase["name"]].append({"path": path[:300], "status": resp.status,
                                                "hub": resp.headers.get("x-hub-mode", "")})

            page.on("response", on_response)
            page.on("pageerror", lambda e: errors[phase["name"]].append(str(e)[:300]))

            r = ctx.request.post(HUB + "/api/auth/login", data={"username": USER, "password": PASSWORD})
            result["login"] = r.status
            if r.status != 200:
                print(json.dumps({"result": result, "reqs": reqs}))
                return

            def app():
                for f in page.frames:
                    if f != page.main_frame and "capture" in f.url:
                        return f
                return page.main_frame

            def visit(tag):
                out = {}
                page.goto(HUB + "/dhis-web-capture/index.html", wait_until="networkidle", timeout=120000)
                page.goto(f"{HUB}/dhis-web-capture/index.html#/?orgUnitId={OU}&programId={PROGRAM}",
                          wait_until="networkidle", timeout=120000)
                try:
                    app().wait_for_selector("table tbody tr", timeout=30000)
                except Exception:
                    pass
                page.wait_for_timeout(2000)
                body = app().locator("body").inner_text()
                out["shows-old-name"] = OLD_NAME in body
                out["shows-new-name"] = NEW_NAME in body
                out["rows"] = app().locator("table tbody tr").count()
                page.screenshot(path=os.path.join(OUT, f"a15-{tag}-list.png"), full_page=True)
                # A registration form loads the program's stages, attributes and option sets.
                try:
                    app().locator("button", has_text="New").first.click(timeout=15000)
                    page.wait_for_timeout(4000)
                    form = app().locator("body").inner_text()
                    out["form-shows-old-name"] = OLD_NAME in form
                    out["form-shows-new-name"] = NEW_NAME in form
                    out["form-length"] = len(form)
                    page.screenshot(path=os.path.join(OUT, f"a15-{tag}-form.png"), full_page=True)
                except Exception as e:
                    out["form-error"] = str(e)[:200]
                shown[tag] = out
                result[phase["name"]] = out

            visit("online")
            control("midpoint")  # rename the program on central, cut the hub's uplink
            phase["name"] = "offline"
            visit("offline")
            control("online")
        finally:
            browser.close()
    result["reqs"] = reqs
    result["errors"] = errors
    with open(os.path.join(OUT, "result.json"), "w") as f:
        json.dump(result, f, indent=1)
    print(json.dumps(result))


if __name__ == "__main__":
    main()
