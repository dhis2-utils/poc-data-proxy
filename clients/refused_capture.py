#!/usr/bin/env python3
"""Fix a refused change in Capture web through the hub, as its author.

Run by internal/adversarial/refused_capture_test.go, which starts the hub with the
author's refused change of a person and passes:
  HUB_URL, HUB_USER, HUB_PASSWORD, ATTRIBUTE (the refused attribute), VALUE (the
  corrected value), OUT (folder for screenshots).
On the hub page the author follows "Open in Capture", corrects the attribute in the
person's profile and saves; then opens the hub page again.
Prints one JSON object. Exits non-zero only if the browser itself fails.
"""
import json
import os

from playwright.sync_api import sync_playwright

HUB = os.environ["HUB_URL"].rstrip("/")
ATTR, VALUE, OUT = os.environ["ATTRIBUTE"], os.environ["VALUE"], os.environ.get("OUT", ".")
R = {"requests": []}


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--disable-dev-shm-usage"])
        try:
            ctx = browser.new_context(viewport={"width": 1400, "height": 1000})
            R["login"] = ctx.request.post(HUB + "/api/auth/login", data={
                "username": os.environ["HUB_USER"], "password": os.environ["HUB_PASSWORD"]}).status
            page = ctx.new_page()

            def on_response(resp):
                if resp.url.startswith(HUB) and "/tracker" in resp.url:
                    R["requests"].append({"method": resp.request.method, "status": resp.status,
                                          "mode": resp.headers.get("x-hub-mode", ""), "path": resp.url[len(HUB):][:200]})
            page.on("response", on_response)

            def app():
                for f in page.frames:
                    if f != page.main_frame and "capture" in f.url:
                        return f
                return page.main_frame

            page.goto(HUB + "/hub/ui", wait_until="load", timeout=60000)
            page.screenshot(path=os.path.join(OUT, "1-hub-page.png"), full_page=True)
            link = page.locator("#own-refusals a", has_text="Open in Capture")
            R["links"] = link.count()
            if R["links"] == 0:
                return
            R["href"] = link.first.get_attribute("href")
            link.first.click()
            page.wait_for_load_state("networkidle", timeout=180000)
            page.wait_for_timeout(5000)
            page.screenshot(path=os.path.join(OUT, "2-capture-opens.png"), full_page=True)
            a = app()
            a.locator("[data-test=profile-widget]").locator("button", has_text="Edit").first.click(timeout=30000)
            page.wait_for_timeout(2000)
            field = a.locator(f"[data-test=form-field-{ATTR}] input").first
            R["shown"] = field.input_value(timeout=10000)
            page.screenshot(path=os.path.join(OUT, "3-profile-form.png"), full_page=True)
            field.fill(VALUE)
            field.press("Tab")
            try:
                with page.expect_response(lambda r: "/tracker" in r.url and r.request.method == "POST",
                                          timeout=60000) as resp:
                    a.locator("button", has_text="Save changes").first.click(timeout=15000)
                R["save"] = resp.value.status
                R["saveQuery"] = resp.value.url.split("?", 1)[-1]
            except Exception as e:
                R["save"] = 0
                R["saveError"] = str(e)[:300]
            page.wait_for_timeout(4000)
            page.screenshot(path=os.path.join(OUT, "4-saved.png"), full_page=True)
            page.goto(HUB + "/hub/ui", wait_until="load", timeout=60000)
            page.screenshot(path=os.path.join(OUT, "5-hub-page-after.png"), full_page=True)
            R["linksAfter"] = page.locator("#own-refusals a", has_text="Open in Capture").count()
        finally:
            browser.close()


if __name__ == "__main__":
    try:
        main()
    finally:
        print(json.dumps(R))
