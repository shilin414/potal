"""
Models for applications app (应用中心).
"""
import os
import uuid

from django.db import models, transaction
from django.utils import timezone

from apps.users.models import User

#: Avatar uploads (智能体市场「修改头像」). Kept deliberately permissive — the
#: stored file is served as an opaque image, so we only refuse things a
#: browser could not render. Size is capped to keep MEDIA_ROOT/DB rows sane.
AVATAR_ALLOWED_EXTENSIONS = frozenset({'png', 'jpg', 'jpeg', 'gif', 'webp'})
AVATAR_MAX_BYTES = 2 * 1024 * 1024


def application_avatar_upload_to(instance, filename: str) -> str:
    """Content-addressed-ish path so two uploads never collide.

    A random name also sidesteps non-ASCII / space filenames coming from
    browsers (Windows + MEDIA_ROOT on NTFS).
    """
    extension = os.path.splitext(filename or '')[1].lower()
    if len(extension) > 10 or not extension.isascii():
        extension = ''
    return (
        f'application-avatars/{timezone.now():%Y/%m}/'
        f'{uuid.uuid4().hex}{extension}'
    )


class ApplicationCategory(models.Model):
    """应用分类"""
    name = models.CharField(max_length=100, unique=True)
    slug = models.SlugField(unique=True, max_length=100)
    description = models.TextField(blank=True)
    icon = models.CharField(max_length=50, blank=True)
    order = models.IntegerField(default=0)

    class Meta:
        ordering = ['order']
        db_table = 'application_categories'

    def __str__(self):
        return self.name


class Application(models.Model):
    """应用及其当前运行配置。"""

    class Kind(models.TextChoices):
        CHAT = 'chat', '聊天应用'
        TASK = 'task', '任务应用'
        CUSTOM = 'custom', '自定义应用'
    category = models.ForeignKey(
        ApplicationCategory,
        on_delete=models.CASCADE,
        related_name='applications'
    )
    name = models.CharField(max_length=100)
    slug = models.SlugField(unique=True, max_length=100)
    description = models.TextField()
    icon = models.CharField(max_length=50, blank=True)
    # Uploaded avatar image (智能体市场「修改头像」). `icon` stays the emoji
    # fallback so existing rows keep rendering after the field is added.
    # FileField (not ImageField) on purpose: the project does not ship Pillow,
    # and the bytes are never transformed server-side.
    avatar = models.FileField(
        upload_to=application_avatar_upload_to, blank=True, default='')
    # Accent color (hex, e.g. '#2e1a1a') used by the frontend thumbnail gradient.
    color = models.CharField(max_length=20, blank=True, default='')
    tags = models.JSONField(default=list, blank=True)
    developer = models.CharField(max_length=100, blank=True, default='')
    screenshots = models.JSONField(default=list, blank=True)
    usage_count = models.IntegerField(default=0)
    is_public = models.BooleanField(default=True)
    created_by = models.ForeignKey(User, on_delete=models.CASCADE)
    organization = models.ForeignKey(
        'enterprise.Organization', on_delete=models.CASCADE, null=True, blank=True,
        related_name='applications'
    )
    kind = models.CharField(
        max_length=20, choices=Kind.choices, default=Kind.CUSTOM)
    renderer_key = models.SlugField(max_length=100, blank=True)
    executor_key = models.SlugField(max_length=100, blank=True)
    # The workspace's default main agent (§38): the fallback for the home
    # composer and for an unresolvable `@mention`. At most one application
    # holds it — enforced by Application.set_default_agent(), because TiDB
    # does not support conditional unique constraints (models.W036).
    is_default_agent = models.BooleanField(default=False)
    input_schema = models.JSONField(default=dict, blank=True)
    output_schema = models.JSONField(default=dict, blank=True)
    default_config = models.JSONField(default=dict, blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ['category__order', 'name']
        db_table = 'applications'

    def __str__(self):
        return self.name

    @property
    def default_agent_eligible(self) -> bool:
        """Only a runnable chat application can be the main agent."""
        if self.kind != self.Kind.CHAT:
            return False
        return self.runtime_bindings.filter(enabled=True).exists()

    @classmethod
    def set_default_agent(cls, application: 'Application') -> 'Application':
        """Make `application` the workspace's single default main agent.

        Uniqueness is enforced transactionally rather than by a database
        constraint (TiDB compatibility). Raises ValidationError when the
        application cannot receive a message.
        """
        from django.core.exceptions import ValidationError

        if not application.default_agent_eligible:
            raise ValidationError(
                '只有「有可用运行时绑定的聊天应用」才能设为主智能体')
        with transaction.atomic():
            cls.objects.filter(
                is_default_agent=True).exclude(pk=application.pk).update(
                is_default_agent=False)
            if not application.is_default_agent:
                application.is_default_agent = True
                application.save(update_fields=['is_default_agent', 'updated_at'])
        return application

    @classmethod
    def default_agent(cls):
        """The main agent row, if one was explicitly chosen."""
        return cls.objects.filter(is_default_agent=True).first()


class ApplicationFavorite(models.Model):
    """用户对 Application 的收藏（架构文档 §35 收藏快捷入口）。

    收藏是用户级业务事实（跨设备一致），因此落 TiDB 而非前端本地状态；
    workspaceStore 只保存 UI 状态。唯一约束用普通联合唯一（TiDB 不支持
    条件唯一约束），重复收藏由 get_or_create 幂等消化。
    """

    user = models.ForeignKey(
        User, on_delete=models.CASCADE, related_name='application_favorites')
    application = models.ForeignKey(
        Application, on_delete=models.CASCADE, related_name='favorites')
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        db_table = 'application_favorites'
        ordering = ['-created_at', '-id']
        constraints = [models.UniqueConstraint(
            fields=['user', 'application'],
            name='unique_application_favorite')]

    def __str__(self):
        return f'{self.user_id}:{self.application_id}'


class Skill(models.Model):
    """Skill 及其当前内容。"""

    class Visibility(models.TextChoices):
        PRIVATE = 'private', '私有'
        ORGANIZATION = 'organization', '组织'
        PUBLIC = 'public', '公开'

    class SourceType(models.TextChoices):
        BUNDLED = 'bundled', '内置'
        UPLOAD = 'upload', '上传'
        GIT = 'git', 'Git'
        REGISTRY = 'registry', '注册中心'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    organization = models.ForeignKey(
        'enterprise.Organization', on_delete=models.CASCADE, null=True, blank=True,
        related_name='skills',
    )
    slug = models.SlugField(max_length=120)
    name = models.CharField(max_length=120)
    description = models.TextField(blank=True)
    visibility = models.CharField(
        max_length=20, choices=Visibility.choices, default=Visibility.PRIVATE)
    owner = models.ForeignKey(
        User, on_delete=models.PROTECT, related_name='owned_skills')
    source_type = models.CharField(
        max_length=20, choices=SourceType.choices, default=SourceType.BUNDLED)
    source_uri = models.CharField(max_length=500, blank=True)
    artifact_key = models.CharField(max_length=500, blank=True)
    manifest = models.JSONField(default=dict, blank=True)
    content_hash = models.CharField(max_length=64, blank=True, db_index=True)
    is_active = models.BooleanField(default=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        db_table = 'skills'
        ordering = ['name']
        constraints = [models.UniqueConstraint(
            fields=['organization', 'slug'], name='unique_skill_slug_per_org')]

    def __str__(self):
        return self.name


class ChatApplicationProfile(models.Model):
    class ConversationPolicy(models.TextChoices):
        NEW_EACH_OPEN = 'new_each_open', '每次新建'
        RESUME_LAST = 'resume_last', '继续最近对话'
        CHOOSE_HISTORY = 'choose_history', '选择历史对话'

    class StarterLayout(models.TextChoices):
        CARDS = 'cards', '卡片'
        LIST = 'list', '列表'
        COMPACT = 'compact', '紧凑'

    application = models.OneToOneField(
        Application, on_delete=models.CASCADE, related_name='chat_profile')
    welcome_message = models.TextField(blank=True)
    input_placeholder = models.CharField(max_length=200, blank=True)
    empty_state_title = models.CharField(max_length=200, blank=True)
    allow_agent_selection = models.BooleanField(default=False)
    allow_skill_selection = models.BooleanField(default=True)
    allow_extra_skills = models.BooleanField(default=False)
    conversation_policy = models.CharField(
        max_length=30, choices=ConversationPolicy.choices,
        default=ConversationPolicy.CHOOSE_HISTORY)
    starter_layout = models.CharField(
        max_length=20, choices=StarterLayout.choices, default=StarterLayout.CARDS)

    class Meta:
        db_table = 'chat_application_profiles'


class ApplicationSkillBinding(models.Model):
    class Mode(models.TextChoices):
        REQUIRED = 'required', '必需'
        DEFAULT = 'default', '默认'
        OPTIONAL = 'optional', '可选'

    application = models.ForeignKey(
        Application, on_delete=models.CASCADE, related_name='skill_bindings')
    skill = models.ForeignKey(
        Skill, on_delete=models.PROTECT, related_name='application_bindings')
    mode = models.CharField(max_length=20, choices=Mode.choices, default=Mode.DEFAULT)
    config = models.JSONField(default=dict, blank=True)
    order = models.PositiveIntegerField(default=0)

    class Meta:
        db_table = 'application_skill_bindings'
        ordering = ['order', 'id']
        constraints = [models.UniqueConstraint(
            fields=['application', 'skill'],
            name='unique_application_skill_binding')]


class ApplicationAgentBinding(models.Model):
    application = models.ForeignKey(
        Application, on_delete=models.CASCADE, related_name='agent_bindings')
    agent = models.ForeignKey(
        'agents.Agent', on_delete=models.PROTECT, related_name='application_bindings')
    label = models.CharField(max_length=120, blank=True)
    is_default = models.BooleanField(default=False)
    config_overrides = models.JSONField(default=dict, blank=True)
    order = models.PositiveIntegerField(default=0)

    class Meta:
        db_table = 'application_agent_bindings'
        ordering = ['order', 'id']
        constraints = [models.UniqueConstraint(
            fields=['application', 'agent'],
            name='unique_application_agent_binding')]


class GuidedPrompt(models.Model):
    class Action(models.TextChoices):
        FILL = 'fill', '填入输入框'
        PREVIEW = 'preview', '预览后发送'
        SEND = 'send', '立即发送'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    application = models.ForeignKey(
        Application, on_delete=models.CASCADE, related_name='guided_prompts')
    key = models.SlugField(max_length=100)
    title = models.CharField(max_length=200)
    description = models.TextField(blank=True)
    icon = models.CharField(max_length=50, blank=True)
    prompt_template = models.TextField()
    action = models.CharField(
        max_length=20, choices=Action.choices, default=Action.PREVIEW)
    is_featured = models.BooleanField(default=False)
    order = models.PositiveIntegerField(default=0)

    class Meta:
        db_table = 'guided_prompts'
        ordering = ['order', 'title']
        constraints = [models.UniqueConstraint(
            fields=['application', 'key'], name='unique_guided_prompt_key')]


class GuidedQuestion(models.Model):
    class Type(models.TextChoices):
        TEXT = 'text', '文本'
        SINGLE_CHOICE = 'single_choice', '单选'
        MULTI_CHOICE = 'multi_choice', '多选'
        NUMBER = 'number', '数字'
        FILE = 'file', '文件'

    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    guided_prompt = models.ForeignKey(
        GuidedPrompt, on_delete=models.CASCADE, related_name='questions')
    key = models.SlugField(max_length=100)
    label = models.CharField(max_length=200)
    help_text = models.TextField(blank=True)
    type = models.CharField(max_length=30, choices=Type.choices)
    placeholder = models.CharField(max_length=300, blank=True)
    required = models.BooleanField(default=False)
    default_value = models.JSONField(null=True, blank=True)
    validation = models.JSONField(default=dict, blank=True)
    order = models.PositiveIntegerField(default=0)

    class Meta:
        db_table = 'guided_questions'
        ordering = ['order', 'id']
        constraints = [models.UniqueConstraint(
            fields=['guided_prompt', 'key'], name='unique_guided_question_key')]


class GuidedOption(models.Model):
    id = models.UUIDField(primary_key=True, default=uuid.uuid4, editable=False)
    question = models.ForeignKey(
        GuidedQuestion, on_delete=models.CASCADE, related_name='options')
    value = models.CharField(max_length=200)
    label = models.CharField(max_length=200)
    description = models.TextField(blank=True)
    icon = models.CharField(max_length=50, blank=True)
    order = models.PositiveIntegerField(default=0)

    class Meta:
        db_table = 'guided_options'
        ordering = ['order', 'id']
        constraints = [models.UniqueConstraint(
            fields=['question', 'value'], name='unique_guided_option_value')]
