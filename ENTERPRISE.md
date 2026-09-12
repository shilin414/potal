# Enterprise deployment and operations (planned — G13/G15)

> The Django control plane described by the previous version of this document
> has been retired (G10). Its historical implementation is archived, sanitized,
> under [docs/archive/django-reference/](docs/archive/django-reference/README.md).
> Enterprise governance is not yet implemented on the Go backend; this file
> now records the target scope and its current status only.

## Current status

- The Go backend (`backend-go/`) owns Identity, Catalog, Execution, SSE, and
  the Aily Agent provider. Enterprise console surfaces under `/enterprise/*`
  (organizations, RBAC, API keys, quota, audit, provider health) are planned
  for G13 and currently return 404 through the Vite proxy.
- Production deployment topology (Nginx REST/SSE split, container images,
  resource limits) is G15. The historical Docker Compose stack belonged to the
  Django deployment and was removed with it.
- Governance storage (quota_policies, audit_logs) already exists in the Go
  schema; the runtime enforcement and the React admin console are the missing
  parts (G13).

## Planned Go topology (G15)

```text
                    Nginx
                      │
      ┌───────────────┴────────────────┐
      ▼                                ▼
React SPA (static)              Go Backend
                    ┌──────────────┴─────────────┐
                    ▼                            ▼
             studio-api :8080             studio-stream :8081
             (REST control plane)         (SSE long connections)
                    │                            │
                    └──────────────┬─────────────┘
                                   │
                 ┌─────────────────┼─────────────────┐
                 ▼                 ▼                 ▼
               TiDB              Redis         Object Storage
                                   │
                                   ▼
                          Execution Plane
                    studio-worker --provider=feishu_aily
                                   │
                                   ▼
                             Feishu Aily
```

## Governance scope (G13)

- Quota: per-user token/cost/concurrent-run budgets backed by quota_policies.
- Audit: append-only audit_logs for admin/provider/binding changes.
- Provider health, circuit breaking, and usage accounting in the worker plane.
- React admin console replacing the retired Django Admin for Provider,
  RuntimeBinding, User, and runtime monitoring management.
- Organization/enterprise APIs and SSO/SCIM surfaces are future work under the
  same unified Application → RuntimeBinding → Run model; no Django-era chain
  will be restored.
