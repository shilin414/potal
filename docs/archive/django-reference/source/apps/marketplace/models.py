from django.db import models
from apps.users.models import User
from apps.templates.models import Template

class TemplateReview(models.Model):
    RATING_CHOICES = [(i, str(i)) for i in range(1, 6)]

    template = models.ForeignKey(Template, on_delete=models.CASCADE, related_name='reviews')
    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='template_reviews')
    rating = models.IntegerField(choices=RATING_CHOICES)
    comment = models.TextField(blank=True)
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        unique_together = [['template', 'user']]
        db_table = 'template_reviews'

    def __str__(self):
        return f'{self.template.title} - {self.rating}星'

class TemplateComment(models.Model):
    template = models.ForeignKey(Template, on_delete=models.CASCADE, related_name='comments')
    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='template_comments')
    content = models.TextField()
    parent = models.ForeignKey('self', on_delete=models.CASCADE, null=True, blank=True, related_name='replies')
    created_at = models.DateTimeField(auto_now_add=True)
    updated_at = models.DateTimeField(auto_now=True)

    class Meta:
        ordering = ['created_at']
        db_table = 'template_comments'

    def __str__(self):
        return f'{self.template.title} - {self.user.username}'

class UserFavorite(models.Model):
    user = models.ForeignKey(User, on_delete=models.CASCADE, related_name='favorites')
    template = models.ForeignKey(Template, on_delete=models.CASCADE, related_name='favorited_by')
    created_at = models.DateTimeField(auto_now_add=True)

    class Meta:
        unique_together = [['user', 'template']]
        db_table = 'user_favorites'

    def __str__(self):
        return f'{self.user.username} - {self.template.title}'