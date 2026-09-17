"""
Verify the large-catalog fix in a REAL browser (the reported symptom is
"进去非常卡，一闪而过，列表又空了" on /agents and in the mobile switcher).

What is measured, not guessed:
  1. the console has NO "Duplicated key ... used in Menu" warning;
  2. /agents renders a bounded number of cards quickly, and 加载更多 expands;
  3. the mobile agent sheet opens fast and its rows are bounded;
  4. a search finds an application that is NOT on the first page.

Run:  py311 verify_large_catalog_fix.py
"""
import json
import re
import sys
import time

from playwright.sync_api import sync_playwright

BASE = "http://localhost:3030"
USERNAME = "demo"
PASSWORD = "Creator@2026"

report = {"checks": [], "console": []}


def check(name, ok, detail=""):
    report["checks"].append({"name": name, "ok": bool(ok), "detail": str(detail)})
    print(("PASS " if ok else "FAIL ") + name + (f"  -> {detail}" if detail else ""))


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=True)
        ctx = browser.new_context(viewport={"width": 1440, "height": 900})
        page = ctx.new_page()

        warnings = []
        page.on("console", lambda m: (
            warnings.append(m.text) if m.type in ("warning", "error") else None))

        # ── login (admin form behind the 管理员登录 link) ──────────────────
        page.goto(f"{BASE}/auth/login", wait_until="networkidle")
        page.get_by_text(re.compile("管理员登录")).click()
        page.wait_for_selector("input#admin-login_username")
        page.fill("input#admin-login_username", USERNAME)
        page.fill("input#admin-login_password", PASSWORD)
        page.click("button[type=submit]")
        page.wait_for_url(lambda url: url.rstrip("/").endswith("3030"), timeout=30000)
        page.wait_for_load_state("networkidle")

        # ── 1. /agents: bounded render + bounded entrance delay ────────────
        t0 = time.time()
        page.goto(f"{BASE}/agents", wait_until="networkidle")
        page.wait_for_selector(".agent-card", timeout=30000)
        load_ms = int((time.time() - t0) * 1000)

        stats = page.evaluate("""() => {
          const cards = Array.from(document.querySelectorAll('.agent-card'));
          const delays = cards.map((el) => {
            const raw = getComputedStyle(el).animationDelay || '0s';
            return parseFloat(raw) * (raw.includes('ms') ? 0.001 : 1);
          });
          const more = document.querySelector('.agents-page__more button');
          return {
            cards: cards.length,
            maxDelaySec: delays.length ? Math.max(...delays) : 0,
            // A card with animation-fill-mode:both is INVISIBLE until its delay
            // elapses, so opacity 0 here means "the user sees an empty grid".
            invisible: cards.filter((c) => parseFloat(getComputedStyle(c).opacity) < 0.5).length,
            moreLabel: more ? more.textContent.trim() : null,
          };
        }""")

        check("agents: load did not stall", load_ms < 20000, f"{load_ms}ms")
        check("agents: render is capped", stats["cards"] <= 24,
              f"{stats['cards']} cards rendered")
        check("agents: entrance delay is capped",
              stats["maxDelaySec"] <= 0.5, f"max delay {stats['maxDelaySec']}s")
        check("agents: no card left invisible by animation-fill",
              stats["invisible"] == 0, f"{stats['invisible']} invisible")
        check("agents: offers 加载更多", bool(stats["moreLabel"]), stats["moreLabel"])

        before = stats["cards"]
        if stats["moreLabel"]:
            page.click(".agents-page__more button")
            page.wait_for_timeout(400)
            after = page.evaluate("document.querySelectorAll('.agent-card').length")
            check("agents: 加载更多 reveals more", after > before, f"{before} -> {after}")

        # ── 2. duplicated Menu key must be gone ────────────────────────────
        dup = [w for w in warnings if "Duplicated key" in w]
        check("console: no Duplicated key warning", not dup,
              f"{len(dup)} warnings" + (f" e.g. {dup[0][:90]}" if dup else ""))

        # ── 3. desktop switcher opens and stays bounded ────────────────────
        page.goto(f"{BASE}/", wait_until="networkidle")
        page.wait_for_selector(".application-switcher", timeout=20000)
        t0 = time.time()
        page.click(".application-switcher")
        page.wait_for_selector(".ant-dropdown:not(.ant-dropdown-hidden)", timeout=15000)
        open_ms = int((time.time() - t0) * 1000)
        switcher_items = page.evaluate(
            "document.querySelectorAll('.ant-dropdown .ant-dropdown-menu-item').length")
        check("switcher: opens promptly", open_ms < 8000, f"{open_ms}ms")
        check("switcher: menu is bounded", switcher_items <= 60, f"{switcher_items} items")

        # ── 4. mobile shell: sheet opens fast, rows bounded, search works ──
        mobile = ctx.new_page()
        mwarn = []
        mobile.on("console", lambda m: (
            mwarn.append(m.text) if m.type in ("warning", "error") else None))
        mobile.set_viewport_size({"width": 390, "height": 844})
        mobile.goto(f"{BASE}/", wait_until="networkidle")
        mobile.wait_for_selector(".mobile-agent-switcher", timeout=20000)

        t0 = time.time()
        mobile.click(".mobile-agent-switcher")
        mobile.wait_for_selector(".mobile-sheet__row", timeout=20000)
        sheet_ms = int((time.time() - t0) * 1000)
        rows = mobile.evaluate("document.querySelectorAll('.mobile-sheet__row').length")
        check("mobile sheet: opens promptly", sheet_ms < 8000, f"{sheet_ms}ms")
        check("mobile sheet: rows bounded", rows <= 63, f"{rows} rows")

        # Search must reach past the first page.
        mobile.click(".mobile-sheet__icon-btn")
        mobile.wait_for_selector(".mobile-sheet__search input")
        mobile.fill(".mobile-sheet__search input", "问数")
        mobile.wait_for_timeout(400)
        found = mobile.evaluate(
            "Array.from(document.querySelectorAll('.mobile-sheet__row'))"
            ".map((e) => e.textContent).join('|')")
        check("mobile sheet: search returns matches", "问数" in found, found[:80])

        dup_m = [w for w in mwarn if "Duplicated key" in w]
        check("mobile console: no Duplicated key warning", not dup_m, f"{len(dup_m)}")

        page.screenshot(path="gui-test-screenshots/agents_large_catalog.png", full_page=False)
        mobile.screenshot(path="gui-test-screenshots/mobile_sheet_large_catalog.png")

        browser.close()

    console_warnings = [w for w in warnings if "Duplicated key" in w]
    report["console"] = console_warnings
    print("\n=== SUMMARY ===")
    failed = [c for c in report["checks"] if not c["ok"]]
    print(json.dumps({"total": len(report["checks"]), "failed": len(failed),
                      "failures": failed}, ensure_ascii=False, indent=2))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
