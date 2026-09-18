# Enterprise deployment and operations

> Status date: 2026-09-18. The Django control plane is retired; the enterprise
> resource, directory, and Application ACL control plane is implemented in Go.

## Current scope

- Administrator identity remains `users.is_staff = 1`.
- `/enterprise/*` is staff-only in the React router and all corresponding APIs
  independently enforce staff access.
- Resource authoring continues to use `/api/v2/applications`; every mutation is
  staff-only. `/agents` and `/apps` are consumer-only discovery surfaces.
- Enterprise governance APIs live under `/api/v2/admin/*`:
  - directory departments, employees, sync configuration and sync runs;
  - Application access policies (all / assigned / admin_only);
  - audit-log inspection.
- Feishu organization data is fetched with application identity
  (`tenant_access_token`) through `directory/v1`, staged, validated, and only
  then published in one MySQL transaction.
- Department inheritance uses a closure table because MySQL 5.7 has no
  recursive CTE.
- Application ACL is shared by chat agents and fixed applications. Catalog
  pages, workspace bootstrap, resolve, mention, favorites, run/schedule
  admission, and the worker pre-submit gate use the same policy.

## Rollout

`ENTERPRISE_ACL_ENABLED=false` keeps the legacy `is_public` rule active while
Directory synchronization and grants are populated. Before enabling it:

1. grant the Feishu app the required Directory API and field permissions;
2. complete at least one successful full sync and compare department/employee
   counts plus OAuth `open_id` matching;
3. configure Application access policies;
4. verify representative users across catalog, resolve, mention, Run and
   Schedule surfaces;
5. set `ENTERPRISE_ACL_ENABLED=true` and restart API, scheduler and workers.

New resources are always created with `access_mode=admin_only`. Fixed
applications are additionally created disabled until an administrator verifies
that the shipped `renderer_key` exists.

## Required Feishu application permissions

Minimum API permissions:

- `directory:department:list`
- `directory:employee:list`

Department fields:

- `directory:department.base:read`
- `directory:department.parent_id:read`
- `directory:department.order_weight:read`

Employee fields:

- `directory:employee.base.name.name:read`
- `directory:employee.base.department:read`
- `directory:employee.base.active_status:read`
- `directory:employee.base.is_resigned:read`
- `directory:employee.base.avatar:read`
- `directory:employee.work.staff_status:read`

The app's Contacts data range must cover every department Potal should manage.
These are application-identity permissions and must not be added to the user
OAuth scope.

## Runtime topology

```text
React SPA
    │
    ├─ studio-api :8080
    ├─ studio-stream :8081
    ├─ studio-scheduler
    │    ├─ user schedule loop
    │    └─ directory sync loop
    ├─ studio-worker --provider=feishu_aily
    └─ studio-worker --provider=feishu_delivery

MySQL 5.7 stores business truth; Redis remains cache/queue/notification state.
```
