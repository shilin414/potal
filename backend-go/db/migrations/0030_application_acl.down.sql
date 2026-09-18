DROP TABLE IF EXISTS application_user_grants;
DROP TABLE IF EXISTS application_department_grants;
ALTER TABLE applications DROP KEY idx_applications_access, DROP COLUMN access_mode;
