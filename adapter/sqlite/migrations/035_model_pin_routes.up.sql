-- Existing snapshots stay strict; never infer exceptions from live profiles.
ALTER TABLE instances ADD COLUMN model_pin_routes TEXT;
