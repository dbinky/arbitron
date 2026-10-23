-- Preserve damaged report bytes without acknowledging their publication intent.
-- The advancement journal remains independently replayable. Quarantine applies
-- only to these exact bytes; a repaired report is retried on the next cycle.
CREATE TABLE model_refresh_report_quarantine (
    report_id TEXT NOT NULL REFERENCES model_refresh_reports(id),
    report TEXT NOT NULL,
    first_seen_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    PRIMARY KEY (report_id, report)
);
