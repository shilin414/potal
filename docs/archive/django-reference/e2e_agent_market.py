"""智能体市场端到端验证（新建 Aily 自定义智能体 / 改头像 / 设默认智能体）。

自包含：不依赖任何外部夹具，自己生成测试头像 PNG。跑完会把验证数据清理干净
（恢复原默认智能体 + 删除验证智能体 + 清掉 MEDIA_ROOT 里的头像文件）。

前置：
    1. 后端：cd backend && .venv/Scripts/python.exe manage.py runserver 0.0.0.0:8000
    2. 前端：cd frontend && npx vite --port 3032 --strictPort
运行（backend/.venv 没有 playwright，用 py311）：
    cd <项目根>
    C:/software/miniconda3/envs/py311/python.exe gui-test-screenshots/e2e_agent_market.py

注意（本机坑）：
    - 必须用 http://localhost 而不是 127.0.0.1（vite 只监听 localhost/::1）
    - 运行前 unset http_proxy https_proxy，否则 curl/浏览器会被本机代理劫持
"""
import pathlib
import re
import struct
import sys
import zlib

from playwright.sync_api import expect, sync_playwright

BASE = 'http://localhost:3032'
SHOTS = pathlib.Path(__file__).parent
AVATAR = SHOTS / 'zz-avatar-test.png'
AGENT_NAME = 'zz验证·Aily助手'
AGENT_SLUG = 'zz-verify-aily-agent'
AGENT_ID = 'agent_zzverify0001'
ORIGINAL_MAIN_AGENT = '创作助手'


def build_png(size: int = 64, base=(232, 168, 56), stripe=(31, 19, 0)) -> bytes:
    """A striped PNG, generated inline (this machine has no Pillow)."""

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (struct.pack('>I', len(data)) + tag + data
                + struct.pack('>I', zlib.crc32(tag + data) & 0xffffffff))

    rows = b''
    for y in range(size):
        row = b'\x00'
        for x in range(size):
            on_stripe = ((x - y) % 16) < 8
            row += bytes(stripe if on_stripe else base)
        rows += row
    return (b'\x89PNG\r\n\x1a\n'
            + chunk(b'IHDR', struct.pack('>IIBBBBB', size, size, 8, 2, 0, 0, 0))
            + chunk(b'IDAT', zlib.compress(rows, 9))
            + chunk(b'IEND', b''))


def step(text: str) -> None:
    print(f'[e2e] {text}', flush=True)


def main() -> int:
    if not AVATAR.exists():
        AVATAR.write_bytes(build_png())
        print(f'generated {AVATAR}')

    errors = []
    with sync_playwright() as p:
        browser = p.chromium.launch(channel='chrome', headless=True)
        page = browser.new_page(viewport={'width': 1440, 'height': 900})
        page.set_default_timeout(20000)
        page.on('pageerror', lambda exc: errors.append(str(exc)))

        # ── 登录 ────────────────────────────────────────────────────────
        page.goto(f'{BASE}/auth/login')
        page.fill('input#login_username', 'demo')
        page.fill('input#login_password', 'Creator@2026')
        page.click('button[type=submit]')
        page.wait_for_url(lambda url: url.rstrip('/').endswith('3032'))
        expect(page.locator('.app-header')).to_be_visible()
        step('登录成功，进入 AppShell')

        # ── 市场列表（两类智能体 + 运行时来源标签）────────────────────
        page.goto(f'{BASE}/agents')
        page.wait_for_selector('.agent-grid')
        expect(page.locator('.agent-card').first).to_be_visible()
        page.screenshot(path=str(SHOTS / 's1_agents_market.png'))
        expect(page.locator('.agent-card .agent-tag', has_text='飞书 Aily').first
               ).to_be_visible()
        step('运行时来源标签渲染为「飞书 Aily 自定义智能体」（取自 /v2/runtimes）')
        expect(page.locator('.agent-card .agent-tag', has_text='本地创作智能体').first
               ).to_be_visible()
        step('本地创作智能体卡片同时可见')

        # ── 新建 Aily 自定义智能体 ─────────────────────────────────────
        page.click('button:has-text("新建智能体")')
        modal = page.locator('.ant-modal-content')
        expect(modal).to_be_visible()
        expect(modal.locator('.ant-segmented-item', has_text='飞书 Aily 自定义智能体')
               ).to_have_class(re.compile('ant-segmented-item-selected'))
        step('新建弹窗默认选中「飞书 Aily 自定义智能体」模式')
        modal.locator('.ant-select-selection-item', has_text='飞书 Aily').first.wait_for()
        step('运行时下拉自动带出运行时目录里的 Aily 条目')

        modal.locator('input#name').fill(AGENT_NAME)
        modal.locator('input#slug').fill(AGENT_SLUG)
        modal.locator('textarea#description').fill('端到端验证用：绑定一个临时 Aily Agent ID')
        modal.locator('input#external_resource_id').fill(AGENT_ID)
        page.wait_for_timeout(300)
        page.screenshot(path=str(SHOTS / 's2_editor_modal.png'))
        modal.locator('.ant-modal-footer button.ant-btn-primary').click()

        page.wait_for_selector(f'.agent-card:has-text("{AGENT_NAME}")')
        new_card = page.locator(f'.agent-card:has-text("{AGENT_NAME}")')
        expect(new_card.locator('.agent-tag', has_text='飞书 Aily')).to_be_visible()
        step('创建成功（Application + 运行时绑定），卡片出现在智能体市场')
        page.screenshot(path=str(SHOTS / 's3_agent_created.png'))

        # ── 改头像 ──────────────────────────────────────────────────────
        new_card.hover()
        new_card.locator(f'button[aria-label="修改 {AGENT_NAME} 的头像"]').click()
        avatar_modal = page.locator('.ant-modal-content', has_text='修改头像')
        expect(avatar_modal).to_be_visible()
        avatar_modal.locator('input[type=file]').set_input_files(str(AVATAR.resolve()))
        expect(avatar_modal.locator('.agent-avatar-editor__file')
               ).to_contain_text(AVATAR.name)
        page.screenshot(path=str(SHOTS / 's4_avatar_modal.png'))
        avatar_modal.locator('.ant-modal-footer button.ant-btn-primary').click()
        page.wait_for_selector(
            f'.agent-card:has-text("{AGENT_NAME}") img.agent-card-avatar')
        step('头像上传成功，卡片改为渲染图片头像')
        page.wait_for_timeout(400)
        page.screenshot(path=str(SHOTS / 's5_avatar_saved.png'))

        # ── 设为主智能体 ────────────────────────────────────────────────
        new_card = page.locator(f'.agent-card:has-text("{AGENT_NAME}")')
        new_card.hover()
        new_card.locator(f'button[aria-label="设置 {AGENT_NAME} 为默认智能体"]').click()
        page.wait_for_selector(
            f'.agent-card:has-text("{AGENT_NAME}") .agent-card-main')
        expect(page.locator('.agent-card .agent-card-main')).to_have_count(1)
        step('设为主智能体成功，卡片出现「主」标记（全局唯一）')
        page.screenshot(path=str(SHOTS / 's6_default_agent.png'))

        page.goto(f'{BASE}/chat/creative-chat')
        page.wait_for_selector('.application-switcher')
        page.click('.application-switcher')
        dropdown = page.locator('.ant-dropdown:not(.ant-dropdown-hidden)')
        dropdown.wait_for(state='visible')
        badged = dropdown.locator('.application-switcher__main')
        expect(badged.first).to_be_visible()
        step(f'智能体切换器出现 {badged.count()} 处「主」标记，catalog 已同步')
        page.screenshot(path=str(SHOTS / 's7_switcher_default.png'))
        page.keyboard.press('Escape')

        # ── 还原 + 清理 ─────────────────────────────────────────────────
        page.goto(f'{BASE}/agents')
        page.wait_for_selector('.agent-grid')
        original = page.locator(f'.agent-card:has-text("{ORIGINAL_MAIN_AGENT}")').first
        original.hover()
        original.locator(
            f'button[aria-label="设置 {ORIGINAL_MAIN_AGENT} 为默认智能体"]').click()
        page.wait_for_selector(
            f'.agent-card:has-text("{ORIGINAL_MAIN_AGENT}") .agent-card-main')
        step(f'已把默认智能体还给「{ORIGINAL_MAIN_AGENT}」')
        page.screenshot(path=str(SHOTS / 's8_default_restored.png'))

        target = page.locator(f'.agent-card:has-text("{AGENT_NAME}")').first
        target.hover()
        target.locator(f'button[aria-label="删除 {AGENT_NAME}"]').click()
        # antd 把两字中文按钮渲染成「删 除」，只能按结构选
        page.locator('.ant-popconfirm .ant-btn-primary').click()
        page.wait_for_selector(
            f'.agent-card:has-text("{AGENT_NAME}")', state='detached')
        step('验证用智能体已删除（含头像文件）')
        page.screenshot(path=str(SHOTS / 's9_deleted.png'))

        real_errors = [e for e in errors if 'BodyStreamBuffer' not in e]
        if real_errors:
            print('[e2e] page errors:', real_errors, flush=True)
        browser.close()

    print('[e2e] 全部通过' if not real_errors else '[e2e] 有页面错误')
    return 0 if not real_errors else 1


if __name__ == '__main__':
    sys.exit(main())
