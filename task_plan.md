# Task Plan: Production Release Hardening

## Goal
Implement the actionable fixes from the Execution Correctness Closure final audit and Production Release Hardening plan, guided by the future architecture document. Explicitly out of scope: git history cleanup, repository security remediation, and secret rotation.

## Phases
1. Status: complete — Audited current Go implementation and mapped each actionable P0/P1 finding.
2. Status: complete — Implemented strict active/finalize ownership, external ID fencing, lease/occurrence strong checks, Reaper convergence, and cancelled event mapping.
3. Status: complete — Fixed TiDB/Redis integration workflow and added the MySQL 5.7 compatibility gate.
4. Status: complete — Moved Delivery fan-out into the Finalize transaction with durable delivery.executions + delivery.dispatch outbox rows.
5. Status: complete — Added priority/aging admission ordering and the distributed provider inflight semaphore with lease TTL.
6. Status: complete — Added terminal-write fencing, durable Delivery, Reaper occurrence convergence, and Delivery idempotency tests; extended invariant checker H/L.
7. Status: complete — Ran gofmt, build, vet, unit tests, and real TiDB/Redis integration tests; MySQL 5.7 job remains CI-environment-only.

## Constraints
- Preserve TiDB/MySQL-compatible SQL only.
- Keep provider differences inside adapters.
- Canonical business state must be transactional and ownership-fenced.
- Do not revert unrelated user changes.
