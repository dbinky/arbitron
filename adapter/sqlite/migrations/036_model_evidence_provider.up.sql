-- The catalog provider whose offering a path confirmed. Announcements name it
-- instead of the first historical offering. Existing proof has no provenance
-- and stays empty; it is never inferred from the current catalog.
ALTER TABLE model_usability_evidence ADD COLUMN catalog_provider TEXT NOT NULL DEFAULT '';
