-- The capacity index is a pure access path for the admission query: dropping
-- it changes no stored data and no query RESULT — only the plan (admission
-- falls back to a scan of provider_submissions). Safe to roll back on its
-- own, but a 3.3 deployment should be rolled back as a whole (see the 3.3
-- deployment notes): the capacity semantics live in the code, not here.
ALTER TABLE provider_submissions
DROP INDEX idx_provider_submissions_capacity;
