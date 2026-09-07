from django.core.cache import cache
from django.db import connection
from django.http import JsonResponse


def health(request):
    return JsonResponse({'status': 'ok'})


def readiness(request):
    checks = {'database': False, 'redis': False}
    try:
        with connection.cursor() as cursor:
            cursor.execute('SELECT 1')
            cursor.fetchone()
        checks['database'] = True
    except Exception:
        pass
    try:
        cache.set('readiness-check', 'ok', 5)
        checks['redis'] = cache.get('readiness-check') == 'ok'
    except Exception:
        pass
    status_code = 200 if all(checks.values()) else 503
    return JsonResponse({'status': 'ready' if status_code == 200 else 'not_ready',
                         'checks': checks}, status=status_code)
