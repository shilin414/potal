"""验证：主页导航已下掉 技能 / 案例库 / 工作流 三个入口，且页面本身仍然可达。

跑法（本机 py311 有 playwright；必须用 channel="chrome"，缺 chromium_headless_shell）：
  C:\\software\\miniconda3\\envs\\py311\\python.exe gui-test-screenshots/verify_nav_trim.py

断言：
  1) .header-nav-item 文本里不含 技能/案例库/工作流，且其余 5 项都在
  2) 直接访问 /skills、/templates、/workflows 仍能渲染（页面与路由没删）
"""
import re
import sys
from playwright.sync_api import sync_playwright

BASE = "http://localhost:3030"
USERNAME = "demo"
PASSWORD = "Creator@2026"

REMOVED = ["技能", "案例库", "工作流"]
KEPT = ["对话", "智能体", "应用", "定时任务", "企业控制台"]

# 移动端抽屉导航（标签与桌面端不同：首项是「首页」，且本来就没有技能/案例库）
MOBILE_REMOVED = ["技能", "案例库", "工作流"]
MOBILE_KEPT = ["首页", "智能体", "应用", "定时任务", "企业控制台"]

failures = []


def check(cond, msg):
    print(("  OK   " if cond else "  FAIL ") + msg)
    if not cond:
        failures.append(msg)


def login(page):
    page.goto(BASE + "/auth/login", wait_until="networkidle")
    # 登录页默认是飞书唯一入口，本地账号要走「管理员登录」
    page.get_by_text(re.compile("管理员登录")).click()
    page.fill("input#admin-login_username", USERNAME)
    page.fill("input#admin-login_password", PASSWORD)
    page.keyboard.press("Enter")
    page.wait_for_url(lambda url: url.rstrip("/").endswith("3030"), timeout=30000)
    page.wait_for_load_state("networkidle")


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        login(page)

        page.goto(BASE + "/", wait_until="networkidle")
        page.wait_for_selector(".header-nav-item", timeout=20000)
        nav = page.eval_on_selector_all(
            ".header-nav-item", "els => els.map(e => e.textContent.trim())")
        print("\n主页导航项 =", nav)

        print("\n[1] 三个入口已下掉 / 其余仍在")
        joined = " | ".join(nav)
        for label in REMOVED:
            check(not any(label in item for item in nav), f"导航不含「{label}」")
        for label in KEPT:
            check(any(label in item for item in nav), f"导航仍含「{label}」")
        check(len(nav) == len(KEPT), f"导航项数 == {len(KEPT)}（实际 {len(nav)}）")
        print("  (原始文本: %s)" % joined)

        page.screenshot(path="gui-test-screenshots/nav_1_home_after_trim.png")

        print("\n[2] 页面本身仍然可达（东西没删）")
        for path, marker in [("/skills", "技能"), ("/templates", "案例库"),
                             ("/workflows", "工作流")]:
            page.goto(BASE + path, wait_until="networkidle")
            body = page.eval_on_selector("body", "e => e.innerText")
            crashed = ("加载失败" in body and len(body) < 200)
            check(len(body.strip()) > 0 and not crashed,
                  f"{path} 仍能渲染（正文 {len(body)} 字符）")
            page.screenshot(
                path="gui-test-screenshots/nav_2_page%s.png"
                     % path.replace("/", "_"))

        # 移动端（<=768px 走 MobileAppShell）：直接复用桌面上下文的会话，
        # 避免把 session 凭据落盘。
        print("\n[3] 移动端抽屉导航（viewport 390px）")
        state = page.context.storage_state()
        mctx = browser.new_context(
            viewport={"width": 390, "height": 844},
            is_mobile=True, has_touch=True, storage_state=state)
        mobile = mctx.new_page()
        mobile.goto(BASE + "/", wait_until="networkidle")
        mobile.wait_for_selector(".mobile-shell__bar", timeout=20000)
        mobile.click('button[aria-label="打开导航"]')
        mobile.wait_for_selector(".mobile-shell__nav-item", timeout=10000)
        mitems = mobile.eval_on_selector_all(
            ".mobile-shell__nav-item", "els => els.map(e => e.textContent.trim())")
        print("移动端抽屉导航项 =", mitems)
        for label in MOBILE_REMOVED:
            check(not any(label in it for it in mitems), f"移动端抽屉不含「{label}」")
        for label in MOBILE_KEPT:
            check(any(label in it for it in mitems), f"移动端抽屉仍含「{label}」")
        check(len(mitems) == len(MOBILE_KEPT),
              f"移动端导航项数 == {len(MOBILE_KEPT)}（实际 {len(mitems)}）")
        mobile.screenshot(
            path="gui-test-screenshots/nav_3_mobile_drawer_after_trim.png")
        mctx.close()

        browser.close()

    print("\n=== 结果 ===")
    if failures:
        print("失败 %d 项：" % len(failures))
        for f in failures:
            print("  -", f)
        sys.exit(1)
    print("全部通过")


if __name__ == "__main__":
    main()
