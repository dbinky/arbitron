-- Latest Model Profiles: immutable run pins (D1).
--
-- model_pins: JSON object {profile alias -> specific model} captured once at
--   trigger time by the engine. NULL means a LEGACY run created before this
--   migration: it keeps live profile resolution until it finishes and is never
--   backfilled from today's profiles. A run created after this migration always
--   writes non-NULL JSON, including '{}' for a workflow with no model aliases.
-- model_pins_version: 0 only alongside a NULL model_pins (legacy), 1 alongside a
--   valid snapshot. Any other combination (NULL with 1, JSON with 0, an
--   unsupported version, invalid JSON) is corruption and fails the read in
--   InstanceRepo rather than silently falling back to live resolution.
--
-- Deliberately no CHECK constraint: the repository enforces the discriminator
-- on both write and read so a corrupt pair fails with an actionable error.
ALTER TABLE instances ADD COLUMN model_pins TEXT;
ALTER TABLE instances ADD COLUMN model_pins_version INTEGER NOT NULL DEFAULT 0;
