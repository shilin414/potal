-- Dropping the table loses the record of which submissions already reached
-- the provider (including the `unknown` ones), so a subsequent crash would
-- again be indistinguishable from "never submitted".
DROP TABLE IF EXISTS provider_submissions;
