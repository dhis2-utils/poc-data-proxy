#!/usr/bin/env python3
"""Time Capture web screens through the hub on a slow uplink: passed through to
central, and answered by the hub (docs/PERFORMANCE.md, "A slow uplink").

Run by internal/live/slowlink_test.go (TestSlowLinkScreens), which starts the hub and
passes HUB_URL, CONTROL_URL (GET /link?rate=&delay=, /offline, /online), HUB_USER,
HUB_PASSWORD, ORG_UNIT, PROGRAM, ENROLLMENT, OUT. Staff move between screens inside
the app, so after the app's start each screen is reached by changing the route, not
by loading the page again. The browser keeps the app's files from a first visit.
Prints one JSON object: per condition and screen, the seconds until the screen is
ready and the requests it made to the hub, for each of three runs.
"""
import json
import os
import time
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER, PASSWORD = os.environ["HUB_USER"], os.environ["HUB_PASSWORD"]
OU, PROGRAM, ENROLLMENT = os.environ["ORG_UNIT"], os.environ["PROGRAM"], os.environ["ENROLLMENT"]
OUT = os.environ.get("OUT", ".")
APP = f"{HUB}/dhis-web-capture/index.html"

CONDITIONS = [  # label, link rate (bytes/s, 0 = unlimited), latency (ms), answered by the hub
    ("through to central, full speed", 0, 0, False),
    ("through to central, 3000 KB/s", 3_000_000, 200, False),
    ("through to central, 31 KB/s", 31_000, 200, False),
    ("answered by the hub", 31_000, 200, True),
]
SCREENS = [  # label, route, what shows when it is ready
    ("working list", f"/?orgUnitId={OU}&programId={PROGRAM}", "table tbody tr"),
    ("dashboard", f"/enrollment?enrollmentId={ENROLLMENT}&orgUnitId={OU}&programId={PROGRAM}", "text=Stages and Events"),
    ("registration form", f"/new?orgUnitId={OU}&programId={PROGRAM}", "button:has-text('Save')"),
]


def control(what):
    urllib.request.urlopen(CONTROL + "/" + what, timeout=120).read()


def main():
    out = {"conditions": {}, "errors": []}
    count = {"n": 0}
    with sync_playwright() as p:
        browser = p.chromium.launch(args=["--disable-dev-shm-usage"])
        ctx = browser.new_context(ignore_https_errors=True, viewport={"width": 1400, "height": 900})
        ctx.request.post(HUB + "/api/auth/login", data={"username": USER, "password": PASSWORD})
        page = ctx.new_page()
        page.on("request", lambda r: count.__setitem__("n", count["n"] + 1) if r.url.startswith(HUB) else None)
        page.on("pageerror", lambda e: out["errors"].append(str(e)[:200]))

        def frame():
            for f in page.frames:
                if f != page.main_frame and "capture" in f.url:
                    return f
            return page.main_frame

        def start_app():
            page.goto(APP + "#/", wait_until="load", timeout=300_000)
            frame().wait_for_selector("[data-test=headerbar-title]", state="attached", timeout=300_000)
            page.wait_for_load_state("networkidle", timeout=300_000)

        def screen(route, ready):
            f = frame()
            f.evaluate("r => { location.hash = r }", route)
            f.wait_for_selector(ready, timeout=300_000)
            try:
                page.wait_for_load_state("networkidle", timeout=60_000)
            except Exception:
                pass

        # Warm up online at full speed: the browser keeps the app's files, and the hub
        # sees each screen once online, as staff must before working offline.
        control("link?rate=0&delay=0")
        start_app()
        for _, route, ready in SCREENS:
            screen(route, ready)

        for label, rate, delay, local in CONDITIONS:
            control(f"link?rate={rate}&delay={delay}")
            if local:
                control("offline")
            res = {}

            def timed(name, fn):
                count["n"] = 0
                t = time.time()
                try:
                    fn()
                    res.setdefault(name, []).append({"s": round(time.time() - t, 2), "requests": count["n"]})
                except Exception as e:
                    res.setdefault(name, []).append({"s": None, "requests": count["n"], "error": str(e)[:200]})
                    page.screenshot(path=os.path.join(OUT, f"{label}-{name}.png".replace(" ", "_").replace(",", "")))

            for _ in range(3):
                timed("app start", start_app)
                for name, route, ready in SCREENS:
                    timed(name, lambda: screen(route, ready))
            if local:
                control("online")
            out["conditions"][label] = res
        control("link?rate=0&delay=0")
        browser.close()
    print(json.dumps(out))


if __name__ == "__main__":
    main()
