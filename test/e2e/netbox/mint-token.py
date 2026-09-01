# Mints the API token the operator authenticates with, idempotently.
#
# Fed to `manage.py shell` by the harness rather than done through SUPERUSER_API_TOKEN,
# because the image only ever mints a **v2** token and a v2 token is presented as
# `Bearer nbt_<key>.<secret>`. internal/netbox sends `Authorization: Token <token>`, which
# NetBox 4.6 routes to its v1 path (netbox/api/authentication.py, V1_KEYWORD) -- so a v2
# token would authenticate nothing the operator sends.
#
# The value is a fixed string. It is a throwaway credential in a throwaway database that is
# published on localhost for the length of one test run; making it random would only mean
# the harness had to plumb it back out of here.
from users.models import Token, User

TOKEN = 'e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0e2e0'

user = User.objects.get(username='admin')
existing = Token.objects.filter(user=user, version=1)
if existing.filter(plaintext=TOKEN).exists():
    print('token-exists')
else:
    existing.delete()
    Token.objects.create(user=user, token=TOKEN, version=1, write_enabled=True)
    print('token-created')
