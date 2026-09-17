ALTER TABLE applications
  DROP KEY idx_app_catalog_public,
  DROP KEY idx_app_catalog_category;

ALTER TABLE runs
  DROP KEY idx_runs_user_application_created;
