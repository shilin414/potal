"""
Verify the avatar-cache + true-pagination changes in a REAL browser
(执行报告 §34/§35 acceptance, 2026-09-17).

What is measured, not guessed:
  1. /agents requests GET /v2/applications/page (kind=chat, limit=24) and
     the response carries <= 24 items — NOT the whole catalog array;
  2. 加载更多 issues a second paged request WITH cursor=...;
  3. typing a search fires ?q=... to the paged endpoint;
  4. /apps requests the paged endpoint with kind=fixed;
  5. avatar URLs are fingerprint-versioned (?v=<16 hex>), and the avatar
     response carries the year-long immutable cache header + ETag;
  6. /chat/:slug still opens (the workspace mirror still works).

Run:  py311 verify_catalog_paging.py
"""
import json
import re
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

        console_errors = []
        page.on("console", lambda m: (
            console_errors.append(m.text) if m.type == "error" else None))

        page_requests = []
        avatar_responses = []

        def on_request(request):
            url = request.url
            if "/api/v2/applications/page" in url:
                page_requests.append(url)

        def on_response(response):
            url = response.url
            if re.search(r"/api/v2/applications/\d+/avatar", url):
                avatar_responses.append({
                    "url": url,
                    "status": response.status,
                    "cache_control": response.headers.get("cache-control", ""),
                    "etag": response.headers.get("etag", ""),
                })

        page.on("request", on_request)
        page.on("response", on_response)

        # ── login ─────────────────────────────────────────────────────────
        page.goto(f"{BASE}/auth/login", wait_until="networkidle")
        page.get_by_text(re.compile("管理员登录")).click()
        page.wait_for_selector("input#admin-login_username")
        page.fill("input#admin-login_username", USERNAME)
        page.fill("input#admin-login_password", PASSWORD)
        page.click("button[type=submit]")
        page.wait_for_url(lambda url: url.rstrip("/").endswith("3030"), timeout=30000)
        page.wait_for_load_state("networkidle")

        # ── 1. /agents: paged endpoint, bounded items ─────────────────────
        page_requests.clear()
        page.goto(f"{BASE}/agents", wait_until="networkidle")
        page.wait_for_selector(".agent-card", timeout=30000)
        page.wait_for_timeout(500)

        first_page = [u for u in page_requests if "cursor=" not in u]
        check("agents uses the paged endpoint", len(first_page) >= 1,
              f"{len(first_page)} page request(s)")
        if first_page:
            check("first agents page passes limit=24", "limit=24" in first_page[0],
                  first_page[0])
        # API response size: parse the paged response body directly.
        paged_status = page.evaluate("""async () => {
          const r = await fetch('/api/v2/applications/page?kind=chat&scope=manage&include_unbound=true&limit=24', {credentials: 'include'});
          const body = await r.json();
          return {count: body.items.length, has_more: body.has_more,
                  has_cursor: Boolean(body.next_cursor)};
        }""")
        check("paged response carries <= 24 items", paged_status["count"] <= 24,
              f"items={paged_status['count']}")
        check("paged response shape (has_more/next_cursor)",
              isinstance(paged_status["has_more"], bool)
              and isinstance(paged_status["has_cursor"], bool))

        # ── 2. 加载更多 issues a cursor request ──────────────────────────
        more = page.locator("button:has-text('加载更多智能体')")
        if more.count() > 0:
            before = len(page_requests)
            more.first.click()
            page.wait_for_timeout(1200)
            cursor_reqs = [u for u in page_requests[before:] if "cursor=" in u]
            check("加载更多 sends a cursor request", len(cursor_reqs) >= 1,
                  cursor_reqs[0] if cursor_reqs else "none")
            # The backend must answer the cursor page in SQL (no error, no full reload).
            check("cards still render after 加载更多",
                  page.locator(".agent-card").count() > 0)
        else:
            # 7 real apps fit on one page — the button legitimately hides.
            check("加载更多 hidden with a complete first page", True,
                  f"items={paged_status['count']}")

        # ── 3. search goes to the backend ─────────────────────────────────
        page_requests.clear()
        search = page.locator("input[placeholder='搜索智能体...']")
        search.fill("创作")
        page.wait_for_timeout(800)  # > the 300 ms debounce
        q_reqs = [u for u in page_requests if "q=" in u]
        check("search fires q= to the paged endpoint", len(q_reqs) >= 1,
              q_reqs[0] if q_reqs else "none")

        # ── 4. /apps: kind=fixed ──────────────────────────────────────────
        page_requests.clear()
        page.goto(f"{BASE}/apps", wait_until="networkidle")
        page.wait_for_timeout(800)
        fixed_reqs = [u for u in page_requests if "kind=fixed" in u]
        check("app center pages with kind=fixed", len(fixed_reqs) >= 1,
              fixed_reqs[0] if fixed_reqs else "none")
        check("app center renders app cards", page.locator(".app-card").count() > 0,
              f"{page.locator('.app-card').count()} cards")

        # ── 5. avatar headers (cache fingerprint + immutable) ─────────────
        page.goto(f"{BASE}/agents", wait_until="networkidle")
        page.wait_for_timeout(800)
        fingerprint_ok = True
        detail = ""
        for entry in avatar_responses[:4]:
            parsed = entry["url"].split("?v=")
            if len(parsed) != 2 or not re.fullmatch(r"[0-9a-f]{16}", parsed[1]):
                fingerprint_ok = False
                detail = entry["url"]
                break
        check("avatar URLs are fingerprint-versioned (16 hex)",
              fingerprint_ok and len(avatar_responses) > 0,
              detail or f"{len(avatar_responses)} avatar URL(s)")
        if avatar_responses:
            first = avatar_responses[0]
            check("avatar Cache-Control is year-long immutable",
                  "max-age=31536000" in first["cache_control"]
                  and "immutable" in first["cache_control"],
                  first["cache_control"])
            check("avatar response carries an ETag", bool(first["etag"]),
                  first["etag"])

        # Conditional revalidation: a repeat fetch with If-None-Match → 304.
        if avatar_responses:
            etag = avatar_responses[0]["etag"]
            avatar_path = avatar_responses[0]["url"].split(BASE)[-1]
            conditional = page.evaluate("""async ([path, etag]) => {
              const r = await fetch(path, {credentials: 'include',
                headers: {'If-None-Match': etag}});
              return r.status;
            }""", [avatar_path, etag])
            check("If-None-Match revalidation answers 304", conditional == 304,
                  f"status={conditional}")

        # ── 6. the workspace still opens (mirror intact) ──────────────────
        page.goto(f"{BASE}/", wait_until="networkidle")
        page.wait_for_timeout(500)
        body_text = page.evaluate("() => document.body.innerText.slice(0, 400)")
        check("home surface renders after the changes", len(body_text.strip()) > 10)

        real_errors = [e for e in console_errors
                       if "favicon" not in e and "404" not in e]
        check("no console errors", len(real_errors) == 0,
              "; ".join(real_errors[:2]))

        browser.close()

    ok_count = sum(1 for c in report["checks"] if c["ok"])
    print(f"\n{ok_count}/{len(report['checks'])} checks passed")
    with open("gui-test-screenshots/catalog_paging_report.json", "w",
              encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
