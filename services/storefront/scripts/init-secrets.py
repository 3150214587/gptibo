#!/usr/bin/env python3
"""Initialize independent local development secrets without overwriting files."""
import os
import secrets
from pathlib import Path

root = Path(__file__).resolve().parents[1] / '.secrets'
root.mkdir(mode=0o700, exist_ok=True)
for name in ('owner-password', 'app-password', 'auth-secret'):
    try:
        fd = os.open(root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        print(f'{name}: already exists, unchanged')
        continue
    with os.fdopen(fd, 'w') as output:
        output.write(secrets.token_hex(32))
    print(f'{name}: created (value hidden)')
