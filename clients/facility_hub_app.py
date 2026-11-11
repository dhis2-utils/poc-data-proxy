#!/usr/bin/env python3
"""Drive the Facility Hub app on central (apps/facility-hub) as one user.

Run by internal/live/hubapp_test.go, which installs the app and passes:
  CENTRAL_URL, APP_USER, APP_PASSWORD, ACTION, and per action:
    list      the hub IDs the user sees on the hubs page
    approve   HUB_ID, CODE: approve the hub in the Approvals page's dialog
    movecode  HUB_ID, ADDRESS: the move code shown for the hub and address
    announce  ADDRESS: announce a planned move to it
    withdraw  withdraw the planned move
    stage     HUB_ID, VERSION: start VERSION with HUB_ID as the only pilot hub
    owners    OWNERS: name the hub owners group (its display name) in Settings and save
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
        # Central's answers only: the version page also asks GitHub for releases.
        page.on("response", lambda resp: resp.status >= 400 and resp.status != 404
                and resp.url.startswith(CENTRAL)
                and R["failed"].append({"status": resp.status, "url": resp.url[len(CENTRAL):][:200]}))
        try:
            run(page)
        except Exception as e:  # report, do not crash: the Go test reads the JSON
            R["exception"] = str(e)[:500]
            page.screenshot(path=os.path.join(OUT, f"facility-hub-{ACTION}-failed.png"), full_page=True)
        browser.close()
    print(json.dumps(R))


def open_page(page, route):
    """Opens the app at route and returns its frame: 2.42+ shows apps inside the
    global shell, in an iframe; older versions, or a cached app, without it."""
    page.goto(f"{APP}#{route}", wait_until="domcontentloaded", timeout=120_000)
    for _ in range(240):
        for fr in page.frames:
            if "/api/apps/facility-hub/" in fr.url and fr is not page.main_frame:
                fr.wait_for_load_state("domcontentloaded")
                return fr
        if "/api/apps/facility-hub/" in page.url and page.locator("iframe").count() == 0 \
                and page.locator("main").count() > 0:
            return page.main_frame
        page.wait_for_timeout(500)
    return page.main_frame


def run(page):
    if ACTION == "list":
        app = open_page(page, "/")
        app.locator("[data-hub]").or_(app.get_by_text("No hubs", exact=True)).first.wait_for(timeout=120_000)
        R["hubs"] = app.eval_on_selector_all("[data-hub]", "els => els.map(e => e.dataset.hub)")
    elif ACTION == "approve":
        hub = os.environ["HUB_ID"]
        app = open_page(page, "/approvals")
        row = app.locator("tr", has_text=hub)
        row.get_by_role("button", name="Approve").click(timeout=120_000)
        app.fill("input[name=code]", os.environ["CODE"])
        app.get_by_role("dialog").get_by_role("button", name="Approve").click()
        app.wait_for_selector("text=/^(Approved|Not approved)$/", timeout=120_000)
        R["approved"] = app.locator("text=Not approved").count() == 0
        R["dialog"] = app.get_by_role("dialog").inner_text()[:1000]
    elif ACTION == "movecode":
        hub = os.environ["HUB_ID"]
        app = open_page(page, "/moves")
        app.fill("input[name=address]", os.environ["ADDRESS"])
        app.get_by_role("button", name="Make codes").click(timeout=120_000)
        el = app.wait_for_selector(f"[data-hub='{hub}']", timeout=120_000)
        R["code"] = el.inner_text().strip()
    elif ACTION == "announce":
        app = open_page(page, "/moves")
        app.fill("input[name=planned]", os.environ["ADDRESS"])
        app.get_by_role("button", name="Announce").click(timeout=120_000)
        # Asked again first; the confirmation also names the address, so wait for it to
        # close and for the announcement on the page.
        dialog = app.get_by_role("dialog")
        dialog.get_by_role("button", name="Announce").click(timeout=120_000)
        dialog.wait_for(state="detached", timeout=120_000)
        address = os.environ["ADDRESS"].strip().rstrip("/").lower()
        shown = app.get_by_text(f"Announced – {address}", exact=False).first
        shown.wait_for(timeout=120_000)
        R["shown"] = shown.inner_text()
    elif ACTION == "withdraw":
        app = open_page(page, "/moves")
        app.get_by_role("button", name="Withdraw").click(timeout=120_000)
        app.get_by_text("No move announced").wait_for(timeout=120_000)
        R["shown"] = "No move announced"
    elif ACTION == "stage":
        hub, version = os.environ["HUB_ID"], os.environ["VERSION"]
        app = open_page(page, "/version")
        app.fill("input[name=version]", version, timeout=120_000)
        app.locator("[data-test=pilot-hubs] [data-test=dhis2-uicore-select-input]").click()
        app.locator("[data-test=dhis2-uicore-select-menu-menuwrapper] input:not([type=checkbox])").first.fill(hub)
        app.locator("[data-test=dhis2-uicore-select-menu-menuwrapper]").get_by_text(hub, exact=False).first.click()
        app.locator("[data-test=dhis2-uicore-layer]").last.click(position={"x": 5, "y": 5})  # close the menu
        app.get_by_role("button", name="Start with pilot hubs").click()
        # The app writes "–", not ":", in shown strings (review R-53a).
        app.get_by_text(f"Version {version} – Pilot hubs only").wait_for(timeout=120_000)
        R["shown"] = f"Version {version} – Pilot hubs only"
    elif ACTION == "owners":
        name = os.environ["OWNERS"]
        app = open_page(page, "/settings")
        app.locator("[data-test=owners-group] [data-test=dhis2-uicore-select-input]").click(timeout=120_000)
        app.locator("[data-test=dhis2-uicore-select-menu-menuwrapper] input:not([type=checkbox])").first.fill(name)
        app.locator("[data-test=dhis2-uicore-select-menu-menuwrapper]").get_by_text(name, exact=True).first.click()
        save = app.get_by_role("button", name="Save").first
        save.click()
        # Named: the field is locked once central has the group.
        app.get_by_text("Change who is in this group in the Users app, not the group itself").nth(1).wait_for(timeout=120_000)
        R["shown"] = "saved"
    elif ACTION == "screens":
        for name, route in [("hubs", "/"), ("approvals", "/approvals"), ("moves", "/moves"), ("version", "/version")]:
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
