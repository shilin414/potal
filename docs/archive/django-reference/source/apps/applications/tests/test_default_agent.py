"""The workspace's default main agent (§38) must be singular and runnable."""
import pytest
from django.core.exceptions import ValidationError

from apps.applications.models import Application, ApplicationCategory
from apps.catalog.models import ApplicationRuntimeBinding, Provider
from apps.users.models import User


@pytest.fixture
def setup(db):
    user = User.objects.create_user(username='main-agent', password='test')
    category = ApplicationCategory.objects.create(
        name='Main Agents', slug='main-agents')
    provider = Provider.objects.create(key='feishu_aily', name='Aily')

    def make(slug, agent_id, kind=Application.Kind.CHAT, bound=True):
        application = Application.objects.create(
            category=category, name=slug, slug=slug, description='',
            created_by=user, kind=kind, renderer_key='chat')
        if bound:
            ApplicationRuntimeBinding.objects.create(
                application=application, provider=provider,
                provider_key='feishu_aily', runtime_type='agent',
                external_resource_id=agent_id, identity_mode='user',
                execution_mode='interactive')
        return application

    return make


@pytest.mark.django_db
def test_set_default_agent_keeps_exactly_one(setup):
    first = setup('sales', 'agent_sales')
    second = setup('purchase', 'agent_purchase')

    Application.set_default_agent(first)
    assert Application.default_agent() == first

    # Switching the main agent must clear the previous one: TiDB cannot
    # express this as a conditional unique constraint (models.W036).
    Application.set_default_agent(second)
    assert Application.default_agent() == second
    assert Application.objects.filter(is_default_agent=True).count() == 1
    assert not Application.objects.get(pk=first.pk).is_default_agent


@pytest.mark.django_db
def test_set_default_agent_is_idempotent(setup):
    application = setup('sales', 'agent_sales')

    Application.set_default_agent(application)
    Application.set_default_agent(application)

    assert Application.objects.filter(is_default_agent=True).count() == 1


@pytest.mark.django_db
def test_fixed_page_cannot_be_the_main_agent(setup):
    page = setup('change-oa-password', '', kind=Application.Kind.CUSTOM,
                 bound=False)

    with pytest.raises(ValidationError):
        Application.set_default_agent(page)

    assert Application.objects.filter(is_default_agent=True).count() == 0


@pytest.mark.django_db
def test_unbound_chat_application_cannot_be_the_main_agent(setup):
    # A chat app without a runtime binding cannot receive a message, so it
    # must not become the composer's default target.
    unbound = setup('draft-agent', '', bound=False)

    assert unbound.default_agent_eligible is False
    with pytest.raises(ValidationError):
        Application.set_default_agent(unbound)


@pytest.mark.django_db
def test_default_agent_is_none_when_unset(setup):
    setup('sales', 'agent_sales')

    assert Application.default_agent() is None
