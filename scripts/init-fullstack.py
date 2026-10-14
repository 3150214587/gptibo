#!/usr/bin/env python3
"""Create private local stack secrets; never overwrite or print a credential."""
import os
import secrets
from pathlib import Path
root = Path(__file__).resolve().parents[1]
secret_dir = root / '.secrets' / 'fullstack'
secret_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
def write_once(path, content):
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        return
    with os.fdopen(fd, 'w') as output:
        output.write(content)
for name in ('storefront-owner', 'storefront-app', 'storefront-auth', 'supply-owner', 'supply-app', 'supply-master', 'supply-admin'):
    write_once(secret_dir / name, secrets.token_hex(32))
for role in ('owner', 'app'):
    password = (secret_dir / ('supply-' + role)).read_text().strip()
    if not password or any(c not in '0123456789abcdef' for c in password):
        raise SystemExit('Existing database secret is not a generated hex value; inspect it privately.')
    filename = 'supply-owner-dsn' if role == 'owner' else 'supply-runtime-dsn'
    write_once(secret_dir / filename, f'postgres://platform_{role}:{password}@supply-db:5432/platform?sslmode=disable')
notifications = root / 'services/platform/.env.notifications'
write_once(notifications, notifications.with_suffix('.notifications.example').read_text())
config = root / 'services/storefront/.env.integrations'
write_once(config, config.with_suffix('.integrations.example').read_text())
(root / 'services/storefront/.secrets/integrations').mkdir(parents=True, exist_ok=True, mode=0o700)
print('Local stack secrets and disabled integration configuration are ready. Values are hidden; existing files were preserved.')
