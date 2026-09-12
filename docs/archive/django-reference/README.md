# Django Reference Archive

This directory preserves the behaviorally relevant Django implementation that preceded the Go backend. It is historical reference material only and is not part of the runtime, build, test, or deployment path.

## Contents

- `source/apps/`: Django domain models, migrations, API behavior, management commands, and tests.
- `source/integrations/aily/`: the previously validated Aily client, mapper, adapter, dispatcher, and tests.
- `source/core/`: legacy agent-engine and language-model integrations.
- `source/backend/`: Django settings, URL routing, ASGI/WSGI configuration.
- `source/requirements/`, `source/Dockerfile`, `source/docker-compose.yml`: historical Python deployment manifests.
- `source/.env.example`: the historical environment template, with every
  KEY/SECRET/PASSWORD/TOKEN/DSN value redacted.
- `e2e_agent_market.py` and `e2e_chat_identity.py`: the Django-era browser
  acceptance flows; see [LEGACY_E2E.md](LEGACY_E2E.md).
- `source/examples/` and `source/static/`: historical examples and admin mock-up assets.
- `design/`: the former Django-era architecture and application/workflow documents recovered from Git history.

The authoritative current architecture and execution plan remain:

- `docs/Creation Agent Studio Go 后端目标架构.md`
- `docs/Creation Agent Studio Go 重构执行计划.md`
- `docs/Go重构交接提示词.md`
- `docs/开发进度清单.md`

## Deliberate exclusions

The archive excludes all secrets and generated/runtime data:

- the real `.env` / `.env.local`
- Python virtual environments
- `media/` and database/runtime data
- pytest caches, `__pycache__`, `.pyc`, and `.pyo`
- logs, local SQLite/database files, certificates, keys, and dumps

Do not copy credentials into this directory. The live Go configuration remains in the gitignored `backend-go/.env.local`.

## Usage rule

Use this source only to recover already-validated product behavior or historical context. Do not restore Django as a fallback backend and do not port its framework structure into Go. Provider behavior must still follow official documentation first, then recorded real integration results.
