-- Catalog keyset pagination (执行报告 §21, 2026-09-17).
--
-- idx_app_catalog_public serves the paged market query driven from
-- applications: equality on (kind, enabled, is_public) + ordered keyset walk
-- on (created_at, id). The legacy idx_applications_kind_public lacks both
-- `enabled` (the 应用中心 switch hides rows from non-staff callers) and the
-- id tiebreaker.
--
-- idx_app_catalog_category additionally supports category-scoped pages
-- (category_slug filter / 应用中心 category rail).
--
-- idx_runs_user_application_created serves the per-page personal usage
-- aggregation (WHERE user_id = ? AND application_id IN (...) GROUP BY
-- application_id): idx_runs_user_created scans every run the user ever made,
-- which is exactly the "user views 24 agents, backend aggregates 100k runs"
-- problem the report calls out (§20).
ALTER TABLE applications
  ADD KEY idx_app_catalog_public (kind, enabled, is_public, created_at, id),
  ADD KEY idx_app_catalog_category (kind, enabled, category_id, created_at, id);

ALTER TABLE runs
  ADD KEY idx_runs_user_application_created (user_id, application_id, created_at);
