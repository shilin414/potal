# -*- coding: utf-8 -*-
"""Enterprise directory/ACL browser acceptance against the local Go API."""
import json
import os
import pathlib
import subprocess
import sys
from playwright.sync_api import sync_playwright

sys.stdout.reconfigure(encoding='utf-8', errors='replace')

BASE=os.environ.get('STUDIO_E2E_BASE', 'http://localhost:3030')
ROOT=pathlib.Path(__file__).resolve().parent.parent
BACKEND=ROOT/'backend-go'
SHOTS=pathlib.Path(__file__).parent

def issue(user):
    result=subprocess.run(['go','run','./cmd/testsession','-user',user],cwd=BACKEND,capture_output=True,text=True,timeout=120,shell=True)
    if result.returncode: raise RuntimeError(result.stderr)
    return json.loads([line for line in result.stdout.splitlines() if line.startswith('{')][-1])

def context(browser,session):
    ctx=browser.new_context(viewport={'width':1440,'height':900})
    ctx.add_cookies([
      {'name':'studio_session','value':session['token'],'domain':'localhost','path':'/'},
      {'name':'studio_csrf','value':session['csrf'],'domain':'localhost','path':'/'},
    ])
    ctx.add_init_script("localStorage.setItem('auth-storage', JSON.stringify({state:{user:%s,isAuthenticated:true},version:0}));" % json.dumps(session['user'],ensure_ascii=False))
    return ctx

def main():
    failures=[]
    def check(ok,label,detail=''):
        print(('  OK   ' if ok else '  FAIL ')+label+((' — '+detail) if detail and not ok else ''))
        if not ok: failures.append(label+': '+detail)
    admin_s,user_s=issue('demo'),issue('system')
    with sync_playwright() as p:
      browser=p.chromium.launch(channel='chrome',headless=True)
      admin=context(browser,admin_s);page=admin.new_page();page.set_default_timeout(20000)
      page.goto(BASE+'/enterprise',wait_until='networkidle')
      check('/enterprise' in page.url,'管理员可进入企业控制台',page.url)
      check(page.get_by_text('企业控制台',exact=True).count()>0,'企业控制台布局已渲染')
      stats_response=page.request.get(BASE+'/api/v2/admin/directory/stats')
      check(stats_response.status==200,'目录统计接口可用',str(stats_response.status))
      runs_response=page.request.get(BASE+'/api/v2/admin/directory/sync-runs?limit=1')
      check(runs_response.status==200,'同步历史接口可用',str(runs_response.status))
      page.screenshot(path=str(SHOTS/'enterprise_1_overview.png'),full_page=True)
      for path,heading in [('/enterprise/resources/agents','智能体管理'),('/enterprise/resources/apps','应用管理'),('/enterprise/access/agents','智能体授权'),('/enterprise/directory','部门与人员'),('/enterprise/directory/sync','同步管理'),('/enterprise/audit','审计日志')]:
        page.goto(BASE+path,wait_until='networkidle');check(page.get_by_role('heading',name=heading).count()>0,path+' 渲染',page.locator('body').inner_text()[:300])
      page.goto(BASE+'/enterprise/directory/sync',wait_until='networkidle');page.screenshot(path=str(SHOTS/'enterprise_2_sync.png'),full_page=True)
      # Backend staff gate: authenticated normal user receives 403.
      user=context(browser,user_s);up=user.new_page();up.goto(BASE+'/enterprise',wait_until='networkidle')
      check(up.url.rstrip('/')==BASE,'普通用户直输 /enterprise 被重定向',up.url)
      up.goto(BASE+'/',wait_until='networkidle');nav=up.locator('.header-nav-item').all_inner_texts();check(not any('企业控制台' in x for x in nav),'普通用户导航不显示企业控制台',str(nav))
      response=up.request.get(BASE+'/api/v2/admin/directory/departments')
      check(response.status==403,'普通用户调用 admin API 返回 403',str(response.status))
      up.screenshot(path=str(SHOTS/'enterprise_3_user_guard.png'),full_page=True)

      # Persisted browser state is not authoritative. Reproduce a common
      # account-switch boundary: localStorage still says "admin", while the
      # HttpOnly cookie already belongs to a normal user.
      stale=context(browser,admin_s)
      stale.clear_cookies()
      stale.add_cookies([
        {'name':'studio_session','value':user_s['token'],'domain':'localhost','path':'/'},
        {'name':'studio_csrf','value':user_s['csrf'],'domain':'localhost','path':'/'},
      ])
      sp=stale.new_page()
      sp.goto(BASE+'/', wait_until='networkidle')
      checked_nav=sp.locator('.header-nav-item').all_inner_texts()
      check(not any('企业控制台' in x for x in checked_nav),
            '普通用户会话校验后不显示缓存的管理员入口')
      stored_user=sp.evaluate("JSON.parse(localStorage.getItem('auth-storage')).state.user")
      check(str(stored_user.get('id'))==str(user_s['user']['id']),
            '缓存管理员身份已替换为当前普通用户',
            str(stored_user))
      stale.close()
      user.close();admin.close();browser.close()
    if failures:
      print('\nFAILURES');[print(' - '+x) for x in failures];return 1
    print('\nAll enterprise browser checks passed.');return 0
if __name__=='__main__': raise SystemExit(main())
