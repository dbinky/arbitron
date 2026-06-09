-- Record each run's provenance (Phase 3 — schedule & run visibility).
-- trigger_source: how the run started — 'manual' (REST/MCP), 'schedule' (cron
--   tick), or 'run_now' (on-demand fire). Existing rows backfill to 'manual'
--   via the column DEFAULT.
-- schedule_id: the originating schedule's UUID for schedule/run_now runs; NULL
--   for manual runs. NO foreign key is enforced: a schedule may be deleted while
--   its historical runs remain (the dashboard renders "scheduled (deleted)").
ALTER TABLE instances ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'manual'
    CHECK (trigger_source IN ('manual','schedule','run_now'));
ALTER TABLE instances ADD COLUMN schedule_id TEXT;  -- NULL => manual run

-- Index the schedule linkage so per-schedule run lookups (and the dashboard's
-- schedule-origin annotation) are index-backed. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_schedule_id
    ON instances (schedule_id, created_at);
