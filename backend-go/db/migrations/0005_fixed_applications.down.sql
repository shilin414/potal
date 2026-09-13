DELETE FROM applications
WHERE slug IN ('barcode-query', 'oa-unlock', 'oa-password', 'material-query');

DELETE FROM application_categories WHERE slug = 'apps';

ALTER TABLE applications
    DROP KEY idx_applications_kind_enabled,
    DROP COLUMN enabled;
