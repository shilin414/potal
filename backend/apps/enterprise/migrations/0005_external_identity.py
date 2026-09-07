import django.db.models.deletion
from django.conf import settings
from django.db import migrations, models


class Migration(migrations.Migration):
    dependencies = [
        ('enterprise', '0004_seed_governance_policies'),
        migrations.swappable_dependency(settings.AUTH_USER_MODEL),
    ]
    operations = [
        migrations.CreateModel(
            name='ExternalIdentity',
            fields=[
                ('id', models.BigAutoField(auto_created=True, primary_key=True, serialize=False, verbose_name='ID')),
                ('created_at', models.DateTimeField(auto_now_add=True)),
                ('updated_at', models.DateTimeField(auto_now=True)),
                ('subject', models.CharField(max_length=500)),
                ('claims', models.JSONField(blank=True, default=dict)),
                ('last_login_at', models.DateTimeField(blank=True, null=True)),
                ('organization', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, related_name='external_identities', to='enterprise.organization')),
                ('provider', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, related_name='identities', to='enterprise.identityprovider')),
                ('user', models.ForeignKey(on_delete=django.db.models.deletion.CASCADE, related_name='external_identities', to=settings.AUTH_USER_MODEL)),
            ],
            options={'db_table': 'external_identities'},
        ),
        migrations.AddConstraint(
            model_name='externalidentity',
            constraint=models.UniqueConstraint(fields=('provider', 'subject'), name='unique_provider_subject'),
        ),
    ]
