from django.db import migrations


def seed_governance(apps, schema_editor):
    Organization = apps.get_model('enterprise', 'Organization')
    GovernancePolicy = apps.get_model('enterprise', 'GovernancePolicy')
    for organization in Organization.objects.all().iterator():
        GovernancePolicy.objects.get_or_create(organization=organization)


class Migration(migrations.Migration):
    dependencies = [('enterprise', '0003_governancepolicy_identityprovider_and_more')]
    operations = [migrations.RunPython(seed_governance, migrations.RunPython.noop)]
