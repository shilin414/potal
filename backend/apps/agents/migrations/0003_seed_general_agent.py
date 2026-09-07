"""Seed the fallback agent used by conversations without an explicit agent."""
from django.db import migrations


GENERAL_SYSTEM_PROMPT = """你是一个专业的视频创作助手，可以帮助用户：
1. 规划视频内容和结构
2. 提供创意建议
3. 优化文案和脚本
4. 回答视频创作相关问题

请用友好、专业的语气与用户交流。"""


def seed_general_agent(apps, schema_editor):
    User = apps.get_model('users', 'User')
    AgentCategory = apps.get_model('agents', 'AgentCategory')
    Agent = apps.get_model('agents', 'Agent')
    owner = User.objects.filter(is_superuser=True).first()
    if owner is None:
        owner, _ = User.objects.get_or_create(username='system')
    category, _ = AgentCategory.objects.get_or_create(
        slug='general', defaults={'name': '通用', 'order': 0})
    Agent.objects.get_or_create(
        slug='general',
        defaults={
            'name': '通用助手',
            'description': '通用创作助手，适用于没有指定专用智能体的对话。',
            'system_prompt': GENERAL_SYSTEM_PROMPT,
            'category': category,
            'created_by': owner,
            'is_public': True,
        },
    )


class Migration(migrations.Migration):
    dependencies = [('agents', '0002_initial')]
    operations = [migrations.RunPython(
        seed_general_agent, migrations.RunPython.noop)]
