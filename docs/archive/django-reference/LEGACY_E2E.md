# Legacy Django E2E scripts (archived)

These two scripts were the Django-era acceptance flows. They require the removed
Django backend on port 8000 and a `.venv` interpreter, and they reference the
former Django JWT/session machinery:

- `e2e_agent_market.py` — agent market authoring (create Aily custom agent,
  avatar upload, default agent), which is now covered against the Go API by the
  progress-list acceptance flows and the Go frontend.
- `e2e_chat_identity.py` — chat identity flow with Django JWT session
  injection, now replaced by `backend-go/tests/e2e_go_chat.py`.

They are kept as historical reference for the validated product behavior they
asserted. They are not runnable against the Go backend and are not part of any
test baseline. Do not "fix" them by re-pointing them at the Go API wholesale —
the Go E2E baseline already covers the current contract.
