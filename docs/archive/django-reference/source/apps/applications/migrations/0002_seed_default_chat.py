"""Seed the default chat application against mutable Agent/Application models."""
from django.db import migrations


def seed_default_chat(apps, schema_editor):
    Application = apps.get_model('applications', 'Application')
    ApplicationCategory = apps.get_model('applications', 'ApplicationCategory')
    ApplicationAgentBinding = apps.get_model(
        'applications', 'ApplicationAgentBinding')
    ChatApplicationProfile = apps.get_model(
        'applications', 'ChatApplicationProfile')
    GuidedPrompt = apps.get_model('applications', 'GuidedPrompt')
    GuidedQuestion = apps.get_model('applications', 'GuidedQuestion')
    GuidedOption = apps.get_model('applications', 'GuidedOption')
    Agent = apps.get_model('agents', 'Agent')

    general = Agent.objects.filter(slug='general').first()
    if general is None:
        return
    category, _ = ApplicationCategory.objects.get_or_create(
        slug='chat', defaults={
            'name': '聊天应用',
            'description': '由智能体和 Skill 驱动的聊天应用',
            'icon': 'chat',
            'order': 0,
        })
    application, _ = Application.objects.get_or_create(
        slug='creative-chat',
        defaults={
            'category': category,
            'name': '创作助手',
            'description': '使用智能体和引导问题完成日常内容创作',
            'icon': '✨',
            'color': '#6d5dfc',
            'tags': ['聊天', '智能体', '创作'],
            'developer': 'Creation Studio',
            'is_public': True,
            'created_by_id': general.created_by_id,
            'organization_id': general.organization_id,
            'kind': 'chat',
            'renderer_key': 'chat',
        },
    )
    ChatApplicationProfile.objects.get_or_create(
        application=application,
        defaults={
            'welcome_message': '告诉我你的创作目标，我会陪你把想法变成作品。',
            'input_placeholder': '描述你的创作需求…',
            'empty_state_title': '今天想创作什么？',
            'allow_agent_selection': False,
            'allow_skill_selection': True,
        })
    ApplicationAgentBinding.objects.get_or_create(
        application=application, agent=general,
        defaults={'label': general.name, 'is_default': True, 'order': 0})
    prompts = [
        ('video-script', '写一个短视频脚本', '🎬',
         '请帮我写一个短视频脚本，主题是：{topic}\n风格：{style}', [
             ('topic', '视频主题', 'text', True, []),
             ('style', '内容风格', 'single_choice', False,
              [('relaxed', '轻松有趣'), ('professional', '专业讲解'),
               ('story', '故事叙述')]),
         ]),
        ('polish-copy', '优化产品文案', '✍️',
         '请优化下面的产品文案，使表达更有吸引力：\n{content}', [
             ('content', '原始文案', 'text', True, []),
         ]),
        ('brainstorm', '创意头脑风暴', '💡',
         '请围绕“{topic}”提供 10 个不同方向的创意。', [
             ('topic', '创意主题', 'text', True, []),
         ]),
    ]
    for order, (key, title, icon, template, questions) in enumerate(prompts):
        prompt, _ = GuidedPrompt.objects.get_or_create(
            application=application, key=key,
            defaults={
                'title': title,
                'icon': icon,
                'prompt_template': template,
                'action': 'preview',
                'is_featured': True,
                'order': order,
            })
        for question_order, (question_key, label, question_type, required,
                             options) in enumerate(questions):
            question, _ = GuidedQuestion.objects.get_or_create(
                guided_prompt=prompt, key=question_key,
                defaults={
                    'label': label,
                    'type': question_type,
                    'required': required,
                    'order': question_order,
                })
            for option_order, (value, option_label) in enumerate(options):
                GuidedOption.objects.get_or_create(
                    question=question, value=value,
                    defaults={'label': option_label, 'order': option_order})


class Migration(migrations.Migration):
    dependencies = [
        ('applications', '0001_initial'),
        ('agents', '0003_seed_general_agent'),
    ]
    operations = [migrations.RunPython(
        seed_default_chat, migrations.RunPython.noop)]
