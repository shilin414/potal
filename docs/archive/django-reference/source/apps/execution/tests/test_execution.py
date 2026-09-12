import pytest
from django.utils import timezone

from apps.applications.models import Application, ApplicationCategory
from apps.catalog.models import ApplicationRuntimeBinding, Provider
from apps.execution.models import Run, RunLease
from apps.execution.services import RunManager
from apps.users.models import User


@pytest.fixture
def run_setup(db):
    user = User.objects.create_user(username='runner', password='test')
    category = ApplicationCategory.objects.create(
        name='Agents', slug='agents-test')
    application = Application.objects.create(
        category=category,
        name='Assistant', slug='assistant-test',
        description='test', created_by=user,
        renderer_key='chat')
    provider = Provider.objects.create(
        key='feishu_aily', name='Aily')
    binding = ApplicationRuntimeBinding.objects.create(
        application=application,
        provider=provider,
        provider_key='feishu_aily',
        runtime_type='agent',
        external_resource_id='agent_test')
    return user, application, binding


@pytest.mark.django_db
def test_create_and_cas_claim_run(run_setup):
    user, application, binding = run_setup
    run = RunManager.create_run(
        user=user,
        application=application,
        runtime_binding=binding,
        input={'content': [{'type': 'text', 'text': 'hello'}]},
    )

    claimed = RunManager.claim_next(
        'worker-1', 'feishu_aily', lease_seconds=60)
    second_claim = RunManager.claim_next(
        'worker-2', 'feishu_aily', lease_seconds=60)

    assert claimed.pk == run.pk
    assert claimed.status == Run.Status.RUNNING
    assert claimed.attempt == 1
    assert second_claim is None
    assert RunLease.objects.filter(
        run=run, worker_id='worker-1').exists()
    assert list(run.events.values_list('event_type', flat=True)) == [
        'run.started']


@pytest.mark.django_db
def test_run_event_sequences_are_monotonic(run_setup):
    user, application, binding = run_setup
    run = RunManager.create_run(
        user=user, application=application, runtime_binding=binding)

    first = RunManager.append_event(run, 'content.started')
    second = RunManager.append_event(run, 'content.delta', {'text': 'a'})

    assert (first.sequence, second.sequence) == (1, 2)
    assert second.payload == {'text': 'a'}


@pytest.mark.django_db
def test_expired_lease_requeues_interrupted_run(run_setup):
    user, application, binding = run_setup
    run = RunManager.create_run(
        user=user, application=application, runtime_binding=binding)
    claimed = RunManager.claim_next(
        'dead-worker', 'feishu_aily', lease_seconds=60)
    RunLease.objects.filter(run=claimed).update(
        expires_at=timezone.now() - timezone.timedelta(seconds=1))

    recovered = RunManager.recover_expired_leases()

    claimed.refresh_from_db()
    assert recovered == 1
    assert claimed.status == Run.Status.QUEUED
    assert not RunLease.objects.filter(run=claimed).exists()
    assert list(claimed.events.values_list('event_type', flat=True)) == [
        'run.started', 'run.interrupted']


@pytest.mark.django_db
def test_runtime_snapshot_never_persists_secret(run_setup):
    user, application, binding = run_setup
    binding.secret_ref = 'AILY_SECRET'
    binding.save(update_fields=['secret_ref'])

    run = RunManager.create_run(
        user=user, application=application, runtime_binding=binding)

    assert run.runtime_snapshot['external_resource_id'] == 'agent_test'
    assert 'secret_ref' not in run.runtime_snapshot


@pytest.mark.django_db
def test_finish_event_carries_final_output_and_error(run_setup):
    user, application, binding = run_setup
    run = RunManager.create_run(
        user=user, application=application, runtime_binding=binding)

    RunManager.finish(run, Run.Status.SUCCEEDED,
                      output={'text': '最终回复', 'status': 'Completed'},
                      provider_status='Completed', finish_reason='stop')
    completed = run.events.filter(event_type='run.completed').get()
    assert completed.payload['text'] == '最终回复'
    assert completed.payload['status'] == 'succeeded'

    # A failed finish surfaces the error fields for the UI.
    run2 = RunManager.create_run(
        user=user, application=application, runtime_binding=binding)
    RunManager.finish(run2, Run.Status.FAILED,
                      error_code='aily_auth_error', error_message='no uat')
    failed = run2.events.filter(event_type='run.failed').get()
    assert failed.payload['error_code'] == 'aily_auth_error'
    assert failed.payload['error_message'] == 'no uat'
