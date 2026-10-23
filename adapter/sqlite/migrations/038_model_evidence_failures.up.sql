-- Consecutive non-confirming checks of one (model, path, configuration). It is
-- probe scheduling state only (retry backoff and retirement), never proof.
-- Existing rows start at 0 and are therefore retried on the next cycle.
ALTER TABLE model_usability_evidence ADD COLUMN failures INTEGER NOT NULL DEFAULT 0;
