import re
from typing import Any

from rest_framework.exceptions import ValidationError

from .models import Application, GuidedPrompt, GuidedQuestion


_PLACEHOLDER = re.compile(r'\{([a-zA-Z][a-zA-Z0-9_-]*)\}')


def validate_guided_prompt(prompt: GuidedPrompt) -> None:
    questions = list(prompt.questions.prefetch_related('options'))
    keys = {question.key for question in questions}
    placeholders = set(_PLACEHOLDER.findall(prompt.prompt_template))
    unknown = placeholders - keys
    if unknown:
        raise ValidationError({
            'prompt_template': f'引用了不存在的问题：{", ".join(sorted(unknown))}'})
    for question in questions:
        if (question.type in (GuidedQuestion.Type.SINGLE_CHOICE,
                              GuidedQuestion.Type.MULTI_CHOICE)
                and not question.options.exists()):
            raise ValidationError({question.key: '选择题至少需要一个选项。'})


def compose_guided_prompt(prompt: GuidedPrompt, answers: dict[str, Any]) -> dict:
    validate_guided_prompt(prompt)
    normalized: dict[str, Any] = {}
    for question in prompt.questions.prefetch_related('options'):
        value = answers.get(question.key, question.default_value)
        empty = value is None or value == '' or value == []
        if question.required and empty:
            raise ValidationError({question.key: f'{question.label}为必填项。'})
        if empty:
            normalized[question.key] = ''
            continue
        if question.type in (GuidedQuestion.Type.SINGLE_CHOICE,
                             GuidedQuestion.Type.MULTI_CHOICE):
            allowed = {option.value: option.label for option in question.options.all()}
            values = value if isinstance(value, list) else [value]
            invalid = [item for item in values if str(item) not in allowed]
            if invalid:
                raise ValidationError({question.key: '包含无效选项。'})
            labels = [allowed[str(item)] for item in values]
            normalized[question.key] = labels if question.type == GuidedQuestion.Type.MULTI_CHOICE else labels[0]
        else:
            normalized[question.key] = value

    def replace(match):
        value = normalized.get(match.group(1), '')
        return '、'.join(str(item) for item in value) if isinstance(value, list) else str(value)

    content = _PLACEHOLDER.sub(replace, prompt.prompt_template)
    content = '\n'.join(
        line.rstrip() for line in content.splitlines()
        if line.rstrip().rstrip('：:').strip()
    ).strip()
    return {
        'prompt': content,
        'normalized_answers': normalized,
        'guided_prompt_id': str(prompt.id),
        'application_id': prompt.application_id,
    }


def validate_application(application: Application) -> None:
    """Validate the current mutable application configuration before saving it."""
    if application.kind == Application.Kind.CHAT:
        if not hasattr(application, 'chat_profile'):
            raise ValidationError({'chat_profile': '聊天应用缺少聊天配置。'})
        bindings = application.agent_bindings.select_related('agent').all()
        if not bindings.exists():
            raise ValidationError({'agent_bindings': '聊天应用至少需要一个智能体。'})
        if bindings.filter(is_default=True).count() != 1:
            raise ValidationError({'agent_bindings': '聊天应用必须且只能有一个默认智能体。'})
        for prompt in application.guided_prompts.all():
            validate_guided_prompt(prompt)
        entry_prompt_key = application.default_config.get(
            'guided_entry_prompt_key')
        if (entry_prompt_key
                and not application.guided_prompts.filter(
                    key=entry_prompt_key).exists()):
            raise ValidationError({
                'default_config': '进入页问题组不存在，请重新选择。'})
