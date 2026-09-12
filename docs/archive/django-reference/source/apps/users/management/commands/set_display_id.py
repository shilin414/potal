"""维护「聊天里显示的用户 user_id」（``姓名（user_id）`` 里的那串数字）。

背景：飞书 OAuth 的 `/authen/v1/user_info` **只在应用拥有「获取用户 user ID」
权限时**才返回 `user_id`（企业工号形态，如 19127920）。本项目登录用的 scope 不含
通讯录权限，因此拿不到——但产品上要显示它。于是：

* 登录时若接口返回了 `user_id` / `employee_no`，会自动写入（且**不覆盖**已有值）；
* 没返回时用本命令人工维护一次即可，后续重新登录不会被清掉；
* 谁都没有时回退本地主键（``display_id`` 永远不为空，界面不会出现「（）」）。

用法：

    # 看当前所有用户的展示身份（等于前端会渲染的字符串）
    python manage.py set_display_id --list

    # 设置（--user 传 username 或本地主键）
    python manage.py set_display_id --user 吴志彬 --user-id 19127920

    # 清空后回退（飞书 user_id → 本地主键）
    python manage.py set_display_id --user 吴志彬 --clear

Django Admin 同样可改：用户 → display_id。
"""
from django.contrib.auth import get_user_model
from django.core.management.base import BaseCommand, CommandError

from apps.users.display import display_identity

User = get_user_model()


class Command(BaseCommand):
    help = '查看 / 设置聊天里显示的用户 user_id（User.display_id）'

    def add_arguments(self, parser):
        parser.add_argument('--list', action='store_true',
                            help='列出所有用户的展示身份后退出')
        parser.add_argument('--user', default='',
                            help='本地 User.username，或 User 主键')
        parser.add_argument('--user-id', default='',
                            help='要展示的 user_id（工号 / 飞书 user_id）')
        parser.add_argument('--clear', action='store_true',
                            help='清空该用户的 display_id（回退飞书 user_id / 本地主键）')

    def handle(self, *args, **options):
        if options['list']:
            return self._list()

        target = (options['user'] or '').strip()
        if not target:
            raise CommandError('必须指定 --user，或使用 --list 查看现状')

        user = None
        if target.isdigit():
            user = User.objects.filter(pk=int(target)).first()
        user = user or User.objects.filter(username=target).first()
        if user is None:
            raise CommandError(f'找不到用户：{target}')

        if options['clear']:
            user.display_id = ''
            user.save(update_fields=['display_id', 'updated_at'])
            self.stdout.write(self.style.SUCCESS(
                f'已清空 {user.username} 的 display_id → {display_identity(user)}'))
            return

        new_id = (options['user_id'] or '').strip()
        if not new_id:
            raise CommandError('必须指定 --user-id（或 --clear）')
        user.display_id = new_id
        user.save(update_fields=['display_id', 'updated_at'])
        self.stdout.write(self.style.SUCCESS(
            f'已设置 {user.username} → {display_identity(user)}'))

    def _list(self):
        rows = []
        for user in User.objects.order_by('pk'):
            info = display_identity(user)
            rows.append((user.pk, user.username, user.auth_source,
                         user.display_id, info['display_name'], info['display_id']))
        if not rows:
            self.stdout.write('（没有用户）')
            return
        self.stdout.write(f'{"id":>7}  {"username":<16} {"auth_source":<12} '
                          f'{"display_name":<14} {"display_id":<10} 前端渲染')
        for pk, username, source, raw_id, name, display_id in rows:
            label = f'{name}（{display_id}）' if name else display_id
            origin = 'db' if raw_id else 'fallback'
            self.stdout.write(f'{pk:>7}  {username:<16} {source:<12} '
                              f'{name:<14} {display_id:<10} {label}  [{origin}]')
