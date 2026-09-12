import pytest
from channels.layers import InMemoryChannelLayer

# In-memory channel layer config so consumer group tests don't need Redis.
# The "channels.InMemoryChannelLayer" path is the importable dotted name.
_INMEMORY_LAYERS = {
    'default': {
        'BACKEND': 'channels.layers.InMemoryChannelLayer',
        'CONFIG': {},
        'TEST_CONFIG': {'capacity': 1000, 'expiry': 60},
    },
}


@pytest.fixture(autouse=True)
def _inmemory_channel_layer():
    """Force an in-memory channel layer so consumer group tests don't need Redis."""
    from django.test.utils import override_settings

    # Seed a ready-made instance directly into the manager cache, then also
    # override CHANNEL_LAYERS so any cache-reset (setting_changed) rebuilds an
    # in-memory layer rather than the Redis one from settings.
    from channels.layers import channel_layers
    channel_layers.backends['default'] = InMemoryChannelLayer()

    with override_settings(CHANNEL_LAYERS=_INMEMORY_LAYERS):
        yield

    channel_layers.backends.pop('default', None)
