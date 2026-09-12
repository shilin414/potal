"""聊天身份展示端到端验证：双方头像 + 真实名称（姓名（user_id））。

验证目标（本轮需求）：
    1. 用户侧不再显示「你」，而是 姓名（user_id），例如 吴志彬（19127920）
    2. 智能体侧不再显示「助手」，而是应用名（创作助手 / 问数小安）
    3. 双方都有头像：用户用飞书头像图片，智能体用上传的头像图片或 emoji 图标
    4. 旧会话（localStorage 里没有 display_id 的持久化 user）也要正确 —— 靠
       AppShell 的 GET /api/identity/session 同步补齐
    5. 首页「今天想做什么」的快捷入口卡片、智能体切换器（触发器 + 下拉项）、
       聊天空态大图标，都要显示智能体头像（第 4~6 步）

自包含：脚本自己向 Django 申请一个真实飞书用户（吴志彬）的 JWT + studio_session
cookie 注入浏览器，因此不需要密码、也不需要人工扫码。

前置：
    1. 后端：cd backend && .venv/Scripts/python.exe manage.py runserver 0.0.0.0:8000
    2. 前端：cd frontend && npx vite --port 3032 --strictPort
运行（backend/.venv 没有 playwright，用 py311）：
    cd <项目根>
    C:/software/miniconda3/envs/py311/python.exe gui-test-screenshots/e2e_chat_identity.py

本机坑（与 e2e_agent_market.py 相同）：
    - 必须用 http://localhost，不能用 127.0.0.1（vite 只监听 localhost/::1）
    - 运行前 unset http_proxy https_proxy
"""
import json
import pathlib
import subprocess
import sys

from playwright.sync_api import expect, sync_playwright

BASE = 'http://localhost:3032'
ROOT = pathlib.Path(__file__).resolve().parent.parent
BACKEND = ROOT / 'backend'
VENV_PY = BACKEND / '.venv' / 'Scripts' / 'python.exe'
SHOTS = pathlib.Path(__file__).parent

FEISHU_USERNAME = '吴志彬'
EXPECTED_USER_LABEL = '吴志彬（19127920）'
MAIN_AGENT_SLUG = 'creative-chat'
MAIN_AGENT_NAME = '创作助手'
IMAGE_AVATAR_AGENT_SLUG = 'wenshuxiaoan'
IMAGE_AVATAR_AGENT_NAME = '问数小安'


def step(text: str) -> None:
    print(f'[e2e] {text}', flush=True)


def issue_session(username: str) -> dict:
    """Ask Django for a real JWT + studio_session cookie for `username`."""
    code = (
        "import json\n"
        "from django.contrib.auth import get_user_model\n"
        "from rest_framework_simplejwt.tokens import RefreshToken\n"
        "from apps.identity.services import sign_session\n"
        f"u = get_user_model().objects.get(username='{username}')\n"
        "print('SESSION_JSON=' + json.dumps({\n"
        "    'access': str(RefreshToken.for_user(u).access_token),\n"
        "    'cookie': sign_session(u.pk),\n"
        "    'username': u.username,\n"
        "    'pk': u.pk,\n"
        "}))\n"
    )
    proc = subprocess.run(
        [str(VENV_PY), 'manage.py', 'shell', '-c', code],
        cwd=str(BACKEND), capture_output=True, text=True,
        encoding='utf-8', errors='replace')
    for line in (proc.stdout or '').splitlines():
        if line.startswith('SESSION_JSON='):
            return json.loads(line[len('SESSION_JSON='):])
    raise SystemExit(f'无法取得 {username} 的会话：\n{proc.stdout}\n{proc.stderr}')


def seed_browser(context, page, session: dict) -> None:
    """Cookie + a deliberately STALE persisted auth user.

    The persisted user has no display_name/display_id/avatar_url on purpose:
    that is exactly the state of an already-logged-in browser after this
    release, and the AppShell session sync must repair it.
    """
    context.add_cookies([{
        'name': 'studio_session', 'value': session['cookie'],
        'domain': 'localhost', 'path': '/', 'httpOnly': True, 'sameSite': 'Lax',
    }])
    stale_user = {
        'id': str(session['pk']), 'username': session['username'],
        'email': '', 'role': 'creator', 'created_at': '',
    }
    page.add_init_script(
        "window.localStorage.setItem('auth-storage', "
        f"{json.dumps(json.dumps({'state': {'user': stale_user, 'token': session['access'], 'refreshToken': None, 'isAuthenticated': True}, 'version': 0}, ensure_ascii=False))}"
        ");")


def avatar_reports(page) -> list:
    """Read the rendered participant line: name + whether an image rendered."""
    return page.eval_on_selector_all(
        '.run-chat-bubble',
        """els => els.map(el => {
            const name = el.querySelector('.chat-sender-name');
            const avatar = el.parentElement.querySelector('.run-chat-avatar');
            const img = avatar.querySelector('img');
            return {
                name: name ? name.textContent.trim() : null,
                avatarText: img ? null : avatar.textContent.trim(),
                avatarSrc: img ? img.getAttribute('src') : null,
                status: el.querySelector('.run-chat-dotting') ? 'streaming' : 'settled',
            };
        })""")


def main() -> int:
    session = issue_session(FEISHU_USERNAME)
    step(f'已取得 {session["username"]}(pk={session["pk"]}) 的真实会话凭据')

    failures: list = []
    errors: list = []
    with sync_playwright() as p:
        browser = p.chromium.launch(channel='chrome', headless=True)
        context = browser.new_context(viewport={'width': 1440, 'height': 900})
        page = context.new_page()
        page.set_default_timeout(25000)
        page.on('pageerror', lambda exc: errors.append(str(exc)))
        seed_browser(context, page, session)

        # ── 1) 智能体（emoji 头像）的会话 ────────────────────────────────
        page.goto(f'{BASE}/chat/{MAIN_AGENT_SLUG}')
        page.wait_for_selector('.chat-empty, .chat-messages')
        expect(page.locator('.chat-empty h3')).to_contain_text(MAIN_AGENT_NAME)
        step(f'进入 {MAIN_AGENT_NAME} 工作区（空态标题 = 应用名）')

        page.fill('.run-chat-textarea', '回复两个字：收到')
        page.keyboard.press('Enter')
        page.wait_for_selector('.run-chat-bubble')
        expect(page.locator('.run-chat-bubble')).to_have_count(2)

        reports = avatar_reports(page)
        step(f'消息渲染：{json.dumps(reports, ensure_ascii=False)}')

        user_line, agent_line = reports[0], reports[1]
        if user_line['name'] != EXPECTED_USER_LABEL:
            failures.append(f'用户标签应为 {EXPECTED_USER_LABEL}，实际 {user_line["name"]}')
        if agent_line['name'] != MAIN_AGENT_NAME:
            failures.append(f'智能体标签应为 {MAIN_AGENT_NAME}，实际 {agent_line["name"]}')
        if not user_line['avatarSrc'] or 'feishucdn' not in user_line['avatarSrc']:
            failures.append(f'用户头像应渲染飞书图片，实际 {user_line["avatarSrc"]}')
        if agent_line['avatarText'] != '✨':
            failures.append(f'智能体头像应回退 emoji ✨，实际 {agent_line["avatarText"]}')
        if not page.locator('.run-chat-avatar img').first.is_visible():
            failures.append('头像图片元素不可见')
        step('双方头像 + 真实名称断言通过（用户=飞书图片，智能体=emoji）')

        # 等流式结束，拿一张终态截图（失败气泡也带完整身份信息）。
        try:
            page.wait_for_function(
                "() => !document.querySelector('.run-chat-dotting')", timeout=45000)
            step('本轮 Run 已到终态')
        except Exception:
            step('Run 仍在进行（截图会显示「生成中…」，不影响身份断言）')
        page.screenshot(path=str(SHOTS / 'c1_chat_identity.png'))

        # ── 2) 历史回放：?conversation= 重新加载后身份仍正确 ─────────────
        conversation_url = page.url
        page.goto(conversation_url)
        page.wait_for_selector('.run-chat-bubble')
        expect(page.locator('.run-chat-bubble')).to_have_count(2)
        replay = avatar_reports(page)
        if replay[0]['name'] != EXPECTED_USER_LABEL:
            failures.append(f'历史回放用户标签错误：{replay[0]["name"]}')
        step(f'历史回放渲染：{json.dumps(replay, ensure_ascii=False)}')
        page.screenshot(path=str(SHOTS / 'c2_history_identity.png'))

        # ── 3) 上传过头像的智能体：头像应渲染图片 ───────────────────────
        page.goto(f'{BASE}/chat/{IMAGE_AVATAR_AGENT_SLUG}')
        page.wait_for_selector('.chat-empty, .chat-messages')
        if page.locator('.run-chat-bubble').count() == 0:
            page.fill('.run-chat-textarea', '回复两个字：收到')
            page.keyboard.press('Enter')
            page.wait_for_selector('.run-chat-bubble')
            try:
                page.wait_for_function(
                    "() => !document.querySelector('.run-chat-dotting')", timeout=45000)
            except Exception:
                pass
        second = avatar_reports(page)
        step(f'{IMAGE_AVATAR_AGENT_NAME} 渲染：{json.dumps(second, ensure_ascii=False)}')
        if second[1]['name'] != IMAGE_AVATAR_AGENT_NAME:
            failures.append(f'智能体标签应为 {IMAGE_AVATAR_AGENT_NAME}，实际 {second[1]["name"]}')
        if not (second[1]['avatarSrc'] or '').startswith('/api/v2/applications/'):
            failures.append(f'上传头像的智能体应渲染 <img>，实际 {second[1]}')
        page.screenshot(path=str(SHOTS / 'c3_uploaded_avatar_agent.png'))

        # ── 4) 首页「今天想做什么」：快捷入口卡片显示头像 ────────────────
        page.evaluate("() => window.localStorage.removeItem('workspace-storage')")
        page.goto(f'{BASE}/')
        # `.home-shortcuts` also exists in its "还没有可用的智能体" empty branch, so
        # wait for an actual card — the catalog is fetched on mount.
        page.wait_for_selector('.home-shortcut')
        cards = page.eval_on_selector_all('.home-shortcut', """els => els.map(el => {
            const icon = el.querySelector('.home-shortcut__icon');
            const img = icon ? icon.querySelector('img') : null;
            return {
                name: (el.querySelector('.home-shortcut__name') || {}).textContent?.trim(),
                kind: icon ? icon.dataset.agentAvatar : null,
                src: img ? img.getAttribute('src') : null,
            };
        })""")
        step(f'首页快捷入口：{json.dumps(cards, ensure_ascii=False)}')
        if not cards:
            failures.append('首页快捷入口没有渲染任何卡片')
        if not any(card['kind'] == 'image' for card in cards):
            failures.append(f'首页快捷入口没有任何头像图片：{cards}')
        if not any('/api/v2/applications/30011/avatar' in (card['src'] or '')
                   for card in cards):
            failures.append(f'「{IMAGE_AVATAR_AGENT_NAME}」的卡片未渲染上传头像：{cards}')
        if not all(card['kind'] in ('image', 'emoji') for card in cards):
            failures.append(f'有卡片既没有头像图片也没有 emoji 回退：{cards}')
        page.screenshot(path=str(SHOTS / 'c4_home_shortcuts_avatar.png'))

        # ── 5) 智能体切换器：触发器与下拉项都带头像 ─────────────────────
        page.goto(f'{BASE}/chat/{MAIN_AGENT_SLUG}')
        page.wait_for_selector('.application-switcher')
        trigger = page.eval_on_selector('.application-switcher', """el => {
            const face = el.querySelector('.application-switcher__icon');
            const img = face ? face.querySelector('img') : null;
            return { name: el.textContent.trim(), kind: face?.dataset.agentAvatar,
                     src: img ? img.getAttribute('src') : null };
        }""")
        step(f'切换器触发器：{json.dumps(trigger, ensure_ascii=False)}')
        if trigger['kind'] != 'emoji':
            failures.append(f'{MAIN_AGENT_NAME} 无上传头像，触发器应回退 emoji：{trigger}')

        page.click('.application-switcher')
        page.wait_for_selector('.ant-dropdown:not(.ant-dropdown-hidden) '
                               '.application-switcher__item')
        menu = page.eval_on_selector_all(
            '.ant-dropdown:not(.ant-dropdown-hidden) .application-switcher__item',
            """els => els.map(el => {
                const face = el.querySelector('.application-switcher__item-icon');
                const img = face ? face.querySelector('img') : null;
                return {
                    name: (el.querySelector('.application-switcher__item-name') || {})
                        .textContent?.trim(),
                    kind: face ? face.dataset.agentAvatar : null,
                    src: img ? img.getAttribute('src') : null,
                };
            })""")
        step(f'切换器下拉项：{json.dumps(menu, ensure_ascii=False)}')
        if not any(item['kind'] == 'image' for item in menu):
            failures.append(f'切换器下拉项没有任何头像图片：{menu}')
        if not any('/api/v2/applications/30011/avatar' in (item['src'] or '')
                   for item in menu):
            failures.append(f'切换器里「{IMAGE_AVATAR_AGENT_NAME}」未渲染上传头像：{menu}')
        page.screenshot(path=str(SHOTS / 'c5_switcher_avatar.png'))
        page.keyboard.press('Escape')

        # ── 6) 空态大图标 = 当前智能体的头像 ─────────────────────────────
        page.goto(f'{BASE}/chat/{IMAGE_AVATAR_AGENT_SLUG}')
        page.wait_for_selector('.chat-empty .chat-empty-icon')
        empty = page.eval_on_selector('.chat-empty-icon', """el => {
            const img = el.querySelector('img');
            return { kind: el.dataset.agentAvatar,
                     src: img ? img.getAttribute('src') : null };
        }""")
        step(f'空态大图标：{json.dumps(empty, ensure_ascii=False)}')
        if empty['kind'] != 'image' or '/api/v2/applications/30011/avatar' not in (
                empty['src'] or ''):
            failures.append(f'空态大图标应为 {IMAGE_AVATAR_AGENT_NAME} 的头像图片：{empty}')
        page.screenshot(path=str(SHOTS / 'c6_empty_state_avatar.png'))

        browser.close()

    ignored = [e for e in errors if 'BodyStreamBuffer was aborted' not in e]
    if ignored:
        step(f'页面 JS 错误：{ignored[:3]}')
    if failures:
        for item in failures:
            print(f'[FAIL] {item}')
        return 1
    print('[e2e] 全部断言通过 ✅')
    return 0


if __name__ == '__main__':
    sys.exit(main())
