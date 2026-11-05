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
import shutil
import tempfile
import time
import urllib.request

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
CONTROL = os.environ["CONTROL_URL"].rstrip("/")
USER, PASSWORD = os.environ["HUB_USER"], os.environ["HUB_PASSWORD"]
OU, PROGRAM, ENROLLMENT = os.environ["ORG_UNIT"], os.environ["PROGRAM"], os.environ["ENROLLMENT"]
OUT = os.environ.get("OUT", ".")
APP = f"{HUB}/dhis-web-capture/index.html"

CONDITIONS = [  # label, link rate (bytes/s, 0 = unlimited), latency (ms), answered by the hub, cache cleared
    ("through to central, full speed", 0, 0, False, False),
    ("through to central, 3000 KB/s", 3_000_000, 200, False, False),
    ("through to central, 31 KB/s", 31_000, 200, False, False),
    ("answered by the hub", 31_000, 200, True, False),
    ("through to central, 31 KB/s, browser cache cleared", 31_000, 200, False, True),
    ("answered by the hub, browser cache cleared", 31_000, 200, True, True),
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
        # A profile on disk, as staff's browsers have: an in-memory profile drops the
        # app's 6.9 MB bundle from its cache and downloads it at every start.
        profile = tempfile.mkdtemp(prefix="slowlink-profile-")
        ctx = p.chromium.launch_persistent_context(profile, args=["--disable-dev-shm-usage"],
                                                   ignore_https_errors=True, viewport={"width": 1400, "height": 900})
        ctx.request.post(HUB + "/api/auth/login", data={"username": USER, "password": PASSWORD})
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        cdp = ctx.new_cdp_session(page)
        page.on("request", lambda r: count.__setitem__("n", count["n"] + 1) if r.url.startswith(HUB) else None)
        page.on("pageerror", lambda e: out["errors"].append(str(e)[:200]))
        # With DETAIL set, each request of the timed runs: path, status, body bytes,
        # milliseconds, and who answered (the hub's X-Hub-Cache header).
        detail = {"on": False, "list": []}

        def finished(r):
            if not detail["on"] or not r.url.startswith(HUB):
                return
            try:
                resp = r.response()
                t = r.timing
                detail["list"].append({"url": r.url[len(HUB):][:300], "status": resp.status if resp else None,
                                       "bytes": r.sizes()["responseBodySize"],
                                       "ms": round(t["responseEnd"] - t["requestStart"]) if t["responseEnd"] > 0 else None,
                                       "hub": (resp.headers.get("x-hub-cache") or "") if resp else "",
                                       "conditional": "if-none-match" in r.all_headers() or "if-modified-since" in r.all_headers()})
            except Exception as e:
                detail["list"].append({"url": r.url[len(HUB):][:300], "error": str(e)[:100]})

        page.on("requestfinished", finished)

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

        only = os.environ.get("CONDITIONS")  # indexes into CONDITIONS, e.g. "2,3"
        chosen = [c for i, c in enumerate(CONDITIONS) if not only or str(i) in only.split(",")]
        for label, rate, delay, local, cold in chosen:
            control(f"link?rate={rate}&delay={delay}")
            if local:
                control("offline")
            res = {}

            def timed(name, fn):
                count["n"] = 0
                detail["on"], detail["list"] = bool(os.environ.get("DETAIL")), []
                t = time.time()
                try:
                    fn()
                    res.setdefault(name, []).append({"s": round(time.time() - t, 2), "requests": count["n"]})
                    if detail["on"]:
                        res[name][-1]["detail"] = detail["list"]
                    detail["on"] = False
                except Exception as e:
                    res.setdefault(name, []).append({"s": None, "requests": count["n"], "error": str(e)[:200]})
                    page.screenshot(path=os.path.join(OUT, f"{label}-{name}.png".replace(" ", "_").replace(",", "")))

            for _ in range(int(os.environ.get("RUNS", "3"))):
                if cold:
                    cdp.send("Network.clearBrowserCache")
                timed("app start", start_app)
                for name, route, ready in SCREENS:
                    timed(name, lambda: screen(route, ready))
            if local:
                control("online")
            out["conditions"][label] = res
        control("link?rate=0&delay=0")
        ctx.close()
        shutil.rmtree(profile, ignore_errors=True)
    print(json.dumps(out))


if __name__ == "__main__":
    main()
