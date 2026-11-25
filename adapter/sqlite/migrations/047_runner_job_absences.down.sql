-- One-way for the widened supersession CHECK (see migrations/README.md):
-- receipts of kind 'reconnect_absent' are history and are kept.
DROP TABLE IF EXISTS runner_job_absences;
