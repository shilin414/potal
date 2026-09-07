from django.db import migrations


def backfill_project_application(apps, schema_editor):
    Project = apps.get_model('projects', 'Project')
    Conversation = apps.get_model('conversations', 'Conversation')

    for project in Project.objects.filter(application__isnull=True).iterator():
        application_id = Conversation.objects.filter(
            project_id=project.id,
            application_id__isnull=False,
        ).order_by('-updated_at').values_list('application_id', flat=True).first()
        if application_id:
            Project.objects.filter(id=project.id).update(
                application_id=application_id)


class Migration(migrations.Migration):

    dependencies = [
        ('conversations', '0003_conversation_working_directory'),
        ('projects', '0005_project_application_project_working_directory'),
    ]

    operations = [
        migrations.RunPython(
            backfill_project_application,
            reverse_code=migrations.RunPython.noop,
        ),
    ]
