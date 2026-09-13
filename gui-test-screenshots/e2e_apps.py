# -*- coding: utf-8 -*-
"""
e2e_apps.py — 应用平台（固定应用 + 应用中心 + 权限收紧）浏览器端到端验证。

前置（与 e2e_go_chat.py 相同）：
  * Go API :8080 + vite :3030 运行中（迁移 0005 已应用，四个固定应用已种子）
  * `C:/software/miniconda3/envs/py311/python.exe`（playwright + 本机 Chrome）
  * 必须用 `localhost` 访问（vite 只监听 ::1）

覆盖：
  A. 管理员（demo）
     1. 首页「常用应用」出现四个种子固定应用
     2. 点击 条码信息查询 → /app/barcode-query 占位页，Shell 不刷新
     3. 「返回工作台」回到首页，Shell 仍不刷新
     4. 聊天工作区 @条码信息查询 → Shell 内打开固定应用；返回后回到原聊天工作区（§80）
     5. /apps 应用中心：卡片、分类栏、管理员 启用/公开 开关；停用→已停用徽标→恢复
     6. /agents：管理员可见「新建智能体」
  B. 普通用户（system）
     7. /agents：无「新建智能体」
     8. /apps：无管理开关，只有「打开」
     9. 首页快捷入口仍出现公开固定应用
"""
import json
import pathlib
import subprocess
import sys

from playwright.sync_api import sync_playwright

BASE = 'http://localhost:3030'
# 本脚本位于 <repo>/gui-test-screenshots/，比 backend-go/tests/ 的脚本浅一层。
ROOT = pathlib.Path(__file__).resolve().parent.parent
BACKEND_GO = ROOT / 'backend-go'
SHOTS = pathlib.Path(__file__).parent

FIXED_APPS = ['条码信息查询', 'OA账号解锁', '修改OA密码', '物料信息查询']


def step(text: str) -> None:
    print(f'[e2e] {text}', flush=True)


def issue_session(username: str) -> dict:
    out = subprocess.run(
        ['go', 'run', './cmd/testsession', '-user', username],
        cwd=str(BACKEND_GO), capture_output=True, text=True, timeout=120,
        shell=True,
    )
    if out.returncode != 0:
        raise RuntimeError(f'testsession failed: {out.stderr}')
    line = [l for l in out.stdout.splitlines() if l.startswith('{')][-1]
    return json.loads(line)


def make_context(browser, session: dict):
    context = browser.new_context(viewport={'width': 1366, 'height': 900})
    context.add_cookies([
        {'name': 'studio_session', 'value': session['token'], 'domain': 'localhost', 'path': '/'},
        {'name': 'studio_csrf', 'value': session['csrf'], 'domain': 'localhost', 'path': '/'},
    ])
    context.add_init_script(
        "localStorage.setItem('auth-storage', JSON.stringify({state: {"
        f"user: {json.dumps(session['user'], ensure_ascii=False)}, isAuthenticated: true"
        "}, version: 0}));"
    )
    return context


def main() -> int:
    failures = []

    def check(name: str, ok: bool, detail: str = '') -> None:
        tag = '✅' if ok else '❌'
        print(f'[e2e]   {tag} {name}' + (f' — {detail}' if detail and not ok else ''), flush=True)
        if not ok:
            failures.append(f'{name}: {detail}')

    admin_session = issue_session('demo')
    user_session = issue_session('system')
    step(f"demo is_staff={admin_session['user']['is_staff']}  system is_staff={user_session['user']['is_staff']}")

    with sync_playwright() as p:
        browser = p.chromium.launch(channel='chrome', headless=True)

        # ── A. 管理员视角 ───────────────────────────────────────────────
        admin = make_context(browser, admin_session)
        page = admin.new_page()
        page.set_default_timeout(20000)

        step('A1. 首页「常用应用」出现四个固定应用')
        page.goto(BASE)
        page.wait_for_selector('.home-shortcuts', timeout=15000)
        page.wait_for_timeout(1200)  # 等目录加载
        page.screenshot(path=str(SHOTS / 'apps_e2e_0_home.png'))
        section = page.locator('.home-shortcuts__group', has_text='常用应用').first
        section_names = section.locator('.home-shortcut__name').all_inner_texts()
        missing = [n for n in FIXED_APPS if not any(n in s for s in section_names)]
        check('首页常用应用齐全', not missing, f'missing={missing} got={section_names}')

        step('A2. 点击 条码信息查询 → Shell 内打开占位页')
        page.evaluate('window.__shellMarker = 1')
        section.locator('.home-shortcut', has_text='条码信息查询').first.click()
        page.wait_for_url('**/app/barcode-query', timeout=10000)
        page.wait_for_selector('.fixed-app-placeholder', timeout=10000)
        page.screenshot(path=str(SHOTS / 'apps_e2e_1_placeholder.png'))
        check('占位页渲染', page.locator('.fixed-app-placeholder__name').inner_text() == '条码信息查询')
        check('「功能开发中」提示', page.locator('.fixed-app-placeholder__tag').inner_text().find('功能开发中') >= 0)
        check('Shell 未刷新（__shellMarker 仍在）', page.evaluate('window.__shellMarker') == 1)

        step('A3. 返回工作台 → 回到首页且 Shell 不刷新')
        page.get_by_role('button', name='返回工作台').click()
        page.wait_for_timeout(800)
        check('回到首页', page.url.rstrip('/') == BASE, page.url)
        check('Shell 未刷新', page.evaluate('window.__shellMarker') == 1)

        step('A4. 聊天工作区 @条码信息查询 → 打开固定应用 → 返回原聊天（§80）')
        page.goto(f'{BASE}/chat/creative-chat')
        page.wait_for_selector('textarea', timeout=15000)
        page.evaluate('window.__shellMarker = 2')
        composer = page.locator('textarea').first
        composer.fill('@条码信息查询')
        composer.press('Enter')
        page.wait_for_url('**/app/barcode-query', timeout=10000)
        page.wait_for_selector('.fixed-app-placeholder', timeout=10000)
        check('@mention 打开固定应用', page.evaluate('window.__shellMarker') == 2)
        page.get_by_role('button', name='返回工作台').click()
        page.wait_for_url('**/chat/creative-chat', timeout=10000)
        page.wait_for_selector('textarea', timeout=15000)
        check('返回原聊天工作区', page.evaluate('window.__shellMarker') == 2)

        step('A5. 应用中心：卡片 / 分类栏 / 管理员开关 / 停用-恢复')
        page.goto(f'{BASE}/apps')
        page.wait_for_selector('.app-card', timeout=15000)
        page.screenshot(path=str(SHOTS / 'apps_e2e_2_appcenter_admin.png'))
        cards = page.locator('.app-card')
        check('应用中心 4 张卡', cards.count() == 4, f'got {cards.count()}')
        # 注意 has_text 是子串匹配，「全部应用」也含「应用」——必须带计数括号。
        check('分类栏出现「应用 (4)」',
              page.locator('.app-sidebar .ant-menu-item', has_text='应用 (').count() >= 1)
        first_card = cards.first
        switches = first_card.locator('.app-card-admin .ant-switch')
        check('管理员卡片有 启用/公开 开关', switches.count() == 2, f'got {switches.count()}')

        material = cards.filter(has_text='物料信息查询')
        material.locator('.app-card-admin .ant-switch').first.click()
        page.wait_for_selector('.app-card--disabled', timeout=10000)
        check('停用后卡片出现已停用降级', material.filter(has_text='已停用').count() == 1)
        material.locator('.app-card-admin .ant-switch').first.click()
        page.wait_for_timeout(1000)
        check('恢复启用', page.locator('.app-card--disabled').count() == 0)

        step('A6. /agents 管理员可见「新建智能体」')
        page.goto(f'{BASE}/agents')
        page.wait_for_selector('.agents-page', timeout=15000)
        check('新建智能体按钮可见', page.get_by_role('button', name='新建智能体').count() == 1)
        page.close()
        admin.close()

        # ── B. 普通用户视角 ─────────────────────────────────────────────
        step('B. 普通用户（system）视角')
        regular = make_context(browser, user_session)
        page = regular.new_page()
        page.set_default_timeout(20000)

        page.goto(f'{BASE}/agents')
        page.wait_for_selector('.agents-page', timeout=15000)
        check('B1. 普通用户无「新建智能体」', page.get_by_role('button', name='新建智能体').count() == 0)

        page.goto(f'{BASE}/apps')
        page.wait_for_selector('.app-card', timeout=15000)
        check('B2. 普通用户可见 4 张公开应用卡', page.locator('.app-card').count() == 4)
        check('B3. 无管理员开关', page.locator('.app-card-admin').count() == 0)
        check('B4. 有「打开」入口', page.locator('.app-card-open').count() == 4)

        page.goto(BASE)
        page.wait_for_selector('.home-shortcuts', timeout=15000)
        page.wait_for_timeout(1000)
        section = page.locator('.home-shortcuts__group', has_text='常用应用').first
        check('B5. 普通用户首页仍有常用应用', section.locator('.home-shortcut').count() >= 1)
        page.close()
        regular.close()

        browser.close()

    # 截图收尾（仅供人工复核，断言已在上面完成）
    print(f'\n[e2e] 结果: {"全部通过 ✅" if not failures else f"{len(failures)} 项失败 ❌"}')
    for f in failures:
        print(f'[e2e]   - {f}')
    return 0 if not failures else 1


if __name__ == '__main__':
    sys.exit(main())
