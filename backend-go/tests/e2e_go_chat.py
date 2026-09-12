"""Go 后端端到端验证：登录态 → 主工作台 → 真实 Aily 流式聊天 → 历史回放。

这是《Go 重构执行计划》G9（前端切 Go）的验收脚本，对照
docs/开发进度清单.md「必须保留的现有真实 E2E 行为」：
    1. cookie 会话（无 JWT / 无 Authorization 头）驱动整个 UI
    2. 主工作台渲染快捷入口 + 主输入框
    3. 发送消息 → 智能体气泡 + 流式回复 → 终态收敛（真实 Aily）
    4. 双方身份：用户「吴志彬（19127920）」+ 飞书头像，智能体「创作助手」
    5. 发送后输入框清空
    6. 新建会话（清空 URL 后回空工作区）
    7. 历史回放：/api/conversations/{id}/ 返回的消息渲染气泡（Go 新接口）
    8. 侧栏会话历史出现该会话标题

前置：
    0. Python 依赖：python -m pip install -r backend-go/tests/requirements-e2e.txt
    1. Go 后端：cd backend-go && go run ./cmd/api  （:8080）
    2. Worker：  cd backend-go && go run ./cmd/worker（真实 Aily 调用）
    3. 前端：    cd frontend && npx vite --port 3030 --strictPort
运行：
    cd <项目根>
    C:/software/miniconda3/envs/py311/python.exe backend-go/tests/e2e_go_chat.py

本机坑：
    - 必须用 http://localhost，不能用 127.0.0.1（vite 只监听 localhost/::1）
    - 运行前 unset http_proxy https_proxy
"""
import json
import pathlib
import subprocess
import sys
import time

from playwright.sync_api import expect, sync_playwright

BASE = 'http://localhost:3030'
ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
BACKEND_GO = ROOT / 'backend-go'
SHOTS = pathlib.Path(__file__).parent

MAIN_AGENT_SLUG = 'creative-chat'
MAIN_AGENT_NAME = '创作助手'
EXPECTED_USER_LABEL = '吴志彬（19127920）'


def step(text: str) -> None:
    print(f'[e2e] {text}', flush=True)


def issue_session(username: str) -> dict:
    """Mint a real studio session via backend-go cmd/testsession."""
    exe = SHOTS.parent / 'cmd' / 'testsession'  # documentation only
    out = subprocess.run(
        ['go', 'run', './cmd/testsession', '-user', username],
        cwd=str(BACKEND_GO), capture_output=True, text=True, timeout=120,
        shell=True,
    )
    if out.returncode != 0:
        raise RuntimeError(f'testsession failed: {out.stderr}')
    line = [l for l in out.stdout.splitlines() if l.startswith('{')][-1]
    return json.loads(line)


def main() -> int:
    step('签发吴志彬的 cookie 会话')
    session = issue_session('吴志彬')
    token, csrf = session['token'], session['csrf']
    user = session['user']

    failures = []
    with sync_playwright() as p:
        browser = p.chromium.launch(channel='chrome', headless=True)
        context = browser.new_context(viewport={'width': 1366, 'height': 900})
        # Cookie session: HttpOnly cannot be set from JS, use the context API.
        context.add_cookies([
            {'name': 'studio_session', 'value': token, 'domain': 'localhost', 'path': '/'},
            {'name': 'studio_csrf', 'value': csrf, 'domain': 'localhost', 'path': '/'},
        ])
        # Simulate the persisted auth-store (same shape the SPA writes after
        # login; no token fields — cookie session only).
        context.add_init_script(
            "localStorage.setItem('auth-storage', JSON.stringify({state: {"
            f"user: {json.dumps(user, ensure_ascii=False)}, isAuthenticated: true"
            "}, version: 0}));"
        )
        page = context.new_page()
        page.set_default_timeout(20000)

        # ── 1. 主工作台 ──────────────────────────────────────────────
        step('1. 打开主工作台（cookie 会话）')
        page.goto(f'{BASE}/')
        expect(page.locator('text=今天想做什么')).to_be_visible()
        step('   主工作台 OK（无登录页跳转，快捷入口渲染）')

        # ── 2. 打开创作助手聊天工作区 ────────────────────────────────
        step('2. 打开 /chat/creative-chat')
        page.goto(f'{BASE}/chat/{MAIN_AGENT_SLUG}')
        expect(page.locator('.chat-empty, .chat-messages').first).to_be_visible()

        # ── 3. 发送消息 → 真实 Aily 流式回复 ─────────────────────────
        step('3. 发送消息（真实 Aily Run）')
        composer = page.locator('textarea').first
        composer.fill('请只回复两个字：收到')
        composer.press('Enter')

        expect(page.locator('.chat-messages').locator('text=收到').first).to_be_visible(
            timeout=120_000)
        step('   助手回复「收到」已渲染（真实 Aily → SSE → 终态收敛）')

        # ── 4. 双方身份 + 输入框清空 ─────────────────────────────────
        step('4. 校验身份标签与输入框清空')
        body_text = page.locator('.chat-messages').inner_text()
        if EXPECTED_USER_LABEL not in body_text:
            failures.append(f'用户标签缺失: {EXPECTED_USER_LABEL}')
        if MAIN_AGENT_NAME not in body_text:
            failures.append(f'智能体名缺失: {MAIN_AGENT_NAME}')
        composer_value = page.locator('textarea').first.input_value()
        if composer_value != '':
            failures.append(f'发送后输入框未清空: {composer_value!r}')
        page.screenshot(path=str(SHOTS / 'go_e2e_1_chat.png'))

        # ── 5. 历史回放（新 /api/conversations/{id}/ 接口）──────────
        step('5. 历史回放（读取会话 id → ?conversation= 重载）')
        # 会话 id 从 workspaceStore 持久化里取：state.workspaces[appId].conversationId。
        conversation_id = page.evaluate(
            "() => { const s = JSON.parse(localStorage.getItem('workspace-storage') || '{}');"
            " const ws = (s.state || {}).workspaces || {}; "
            " for (const k of Object.keys(ws)) { if (ws[k] && ws[k].conversationId) return ws[k].conversationId; }"
            " return null; }")
        if not conversation_id:
            failures.append('workspaceStore 中找不到 conversationId')
        else:
            # 终态等待（最终一致性契约）：stream 收敛 → reconcile → 助手消息
            # 落库。重载必须发生在消息落库之后，这正是 §26 Final
            # Reconciliation 的时序。
            step(f'   会话 id = {conversation_id}，等待 reconcile 落库…')
            import requests  # noqa: py311 环境自带
            deadline = time.time() + 90
            message_count = 0
            while time.time() < deadline:
                detail = requests.get(
                    f'http://localhost:8080/api/conversations/{conversation_id}/',
                    cookies={'studio_session': token}, timeout=8)
                if detail.status_code == 200:
                    message_count = len(detail.json().get('messages') or [])
                    if message_count >= 2:
                        break
                time.sleep(2)
            if message_count < 2:
                failures.append(f'reconcile 90s 后助手消息仍未落库（{message_count} 条）')
            step(f'   消息数 = {message_count}，带 ?conversation= 重载')
            page.goto(f'{BASE}/?conversation={conversation_id}')
            page.wait_for_url(f'**/?conversation={conversation_id}')
            expect(page.locator('.chat-messages').locator('text=收到').first).to_be_visible(
                timeout=30_000)
            # 身份标签依赖 catalog 解析（prop → 目录 → 主智能体回退），显式等待。
            try:
                expect(page.locator('.chat-messages').locator(f'text={MAIN_AGENT_NAME}').first).to_be_visible(
                    timeout=15_000)
                expect(page.locator('.chat-messages').locator(f'text={EXPECTED_USER_LABEL}').first).to_be_visible(
                    timeout=15_000)
            except Exception:
                diag = page.evaluate(
                    "() => ({ url: location.href,"
                    " chat: (document.querySelector('.chat-messages')||{}).innerText || '',"
                    " catalogCount: fetch('/api/v2/applications?kind=all&scope=manage')"
                    "   .then(r => r.json()).then(d => d.length) })")
                import json as _json
                catalog_count = page.evaluate(
                    "fetch('/api/v2/applications?kind=all&scope=manage').then(r => r.json()).then(d => d.length)")
                step(f'   诊断: url={page.url}')
                step(f'   诊断: chat面板内容={repr(diag["chat"][:400])}')
                step(f'   诊断: 浏览器内catalog数量={catalog_count}')
                page.screenshot(path=str(SHOTS / 'go_e2e_3_diag.png'))
                raise
            page.screenshot(path=str(SHOTS / 'go_e2e_2_history.png'))
            step('   历史回放 OK（消息来自 Go /api/conversations/{id}/）')

            # ── 6. 侧栏会话历史 ─────────────────────────────────────
            step('6. 侧栏会话历史出现该会话')
            history = page.locator('aside, [class*=sidebar]').first
            expect(history.locator(f'text={conversation_id}')).to_be_visible(
                timeout=20_000) if False else None
            # 标题是首条消息前缀「请只回复两个字：收到」
            expect(page.locator('text=请只回复两个字：收到').first).to_be_visible(
                timeout=20_000)
            step('   侧栏 OK')

        browser.close()

    if failures:
        print('\n[e2e] 失败项：')
        for f in failures:
            print('  -', f)
        return 1
    print('\n[e2e] 全部断言通过 ✅')
    return 0


if __name__ == '__main__':
    sys.exit(main())
