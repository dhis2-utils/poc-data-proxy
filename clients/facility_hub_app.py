#!/usr/bin/env python3
"""Drive the Facility Hub app on central (apps/facility-hub) as one user.

Run by internal/live/hubapp_test.go, which installs the app and passes:
  CENTRAL_URL, APP_USER, APP_PASSWORD, ACTION, and per action:
    list      the hub IDs the user sees on the hubs page
    approve   HUB_ID, CODE: approve the hub in the Approvals page's dialog
    movecode  HUB_ID, ADDRESS: the move code shown for the hub and address
    screens   screenshots of every page into OUT (for a person to look at)
  OUT (folder for screenshots).
Prints one JSON object: the action's result, any page errors and failed requests.
Exits non-zero only if the browser itself fails.
"""
import json
import os
import re

from playwright.sync_api import sync_playwright

CENTRAL = os.environ["CENTRAL_URL"].rstrip("/")
USER, PASSWORD = os.environ["APP_USER"], os.environ["APP_PASSWORD"]
ACTION = os.environ["ACTION"]
OUT = os.environ.get("OUT", ".")
APP = CENTRAL + "/api/apps/facility-hub/index.html"

R = {"action": ACTION, "errors": [], "failed": []}


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--disable-dev-shm-usage"])
        ctx = browser.new_context(viewport={"width": 1500, "height": 1000})
        # The app's own login: a session cookie for central.
        r = ctx.request.post(CENTRAL + "/api/auth/login", data={"username": USER, "password": PASSWORD})
        R["login"] = r.status
        page = ctx.new_page()
        page.on("pageerror", lambda e: R["errors"].append(str(e)[:300]))
        page.on("response", lambda resp: resp.status >= 400 and resp.status != 404
                and R["failed"].append({"status": resp.status, "url": resp.url[len(CENTRAL):][:200]}))
        try:
            run(page)
        except Exception as e:  # report, do not crash: the Go test reads the JSON
            R["exception"] = str(e)[:500]
            page.screenshot(path=os.path.join(OUT, f"facility-hub-{ACTION}-failed.png"), full_page=True)
        browser.close()
    print(json.dumps(R))


def open_page(page, route):
    page.goto(f"{APP}#{route}", wait_until="domcontentloaded", timeout=120_000)


def run(page):
    if ACTION == "list":
        open_page(page, "/")
        page.wait_for_selector("[data-hub], text=No hubs", timeout=120_000)
        R["hubs"] = page.eval_on_selector_all("[data-hub]", "els => els.map(e => e.dataset.hub)")
    elif ACTION == "approve":
        hub = os.environ["HUB_ID"]
        open_page(page, "/approvals")
        row = page.locator("tr", has_text=hub)
        row.get_by_role("button", name="Approve").click(timeout=120_000)
        page.fill("input[name=code]", os.environ["CODE"])
        page.get_by_role("dialog").get_by_role("button", name="Approve").click()
        page.wait_for_selector("text=/^(Approved|Not approved)$/", timeout=120_000)
        R["approved"] = page.locator("text=Approved").count() > 0 and page.locator("text=Not approved").count() == 0
        R["dialog"] = page.get_by_role("dialog").inner_text()[:1000]
    elif ACTION == "movecode":
        hub = os.environ["HUB_ID"]
        open_page(page, "/moves")
        page.fill("input[name=address]", os.environ["ADDRESS"])
        page.get_by_role("button", name="Make codes").click(timeout=120_000)
        el = page.wait_for_selector(f"[data-hub='{hub}']", timeout=120_000)
        R["code"] = el.inner_text().strip()
    elif ACTION == "screens":
        for name, route in [("hubs", "/"), ("approvals", "/approvals"), ("moves", "/moves")]:
            open_page(page, route)
            page.wait_for_timeout(4000)
            page.screenshot(path=os.path.join(OUT, f"facility-hub-{name}.png"), full_page=True)
        hub = os.environ.get("HUB_ID")
        if hub:
            open_page(page, f"/hubs/{hub}")
            page.wait_for_timeout(4000)
            page.screenshot(path=os.path.join(OUT, "facility-hub-hub.png"), full_page=True)
        R["screens"] = sorted(f for f in os.listdir(OUT) if re.match(r"facility-hub-.*\.png", f))
    else:
        raise SystemExit(f"unknown ACTION {ACTION}")


if __name__ == "__main__":
    main()
