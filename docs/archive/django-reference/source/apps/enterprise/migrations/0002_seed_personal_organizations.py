from django.db import migrations
from django.utils.text import slugify


def seed_personal_organizations(apps, schema_editor):
    User = apps.get_model('users', 'User')
    Organization = apps.get_model('enterprise', 'Organization')
    Membership = apps.get_model('enterprise', 'Membership')
    QuotaPolicy = apps.get_model('enterprise', 'QuotaPolicy')
    for user in User.objects.all().iterator():
        if Membership.objects.filter(user=user, is_active=True).exists():
            continue
        base = slugify(user.username)[:70] or f'user-{user.pk}'
        slug = f'{base}-{user.pk}'
        organization, _ = Organization.objects.get_or_create(
            slug=slug,
            defaults={'name': f'{user.username} Workspace', 'owner': user},
        )
        Membership.objects.get_or_create(
            organization=organization, user=user,
            defaults={'role': 'owner', 'is_active': True},
        )
        QuotaPolicy.objects.get_or_create(organization=organization)


class Migration(migrations.Migration):
    dependencies = [
        ('enterprise', '0001_initial'),
        ('users', '0002_alter_user_api_key_userapikey'),
    ]
    operations = [migrations.RunPython(seed_personal_organizations,
                                       migrations.RunPython.noop)]
