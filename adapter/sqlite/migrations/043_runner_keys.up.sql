-- Runner keys (remote runners Phase 2). Only the SHA-256 hex hash of the
-- plaintext ark_ key is stored. machine_public_key is NULL until the first
-- machine proves possession; the claim is a single conditional UPDATE.
-- Names (and therefore runner ids) stay reserved after revocation for audit.
CREATE TABLE IF NOT EXISTS runner_keys (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    runner_id          TEXT NOT NULL UNIQUE,
    key_hash           TEXT NOT NULL UNIQUE,
    prefix             TEXT NOT NULL,
    machine_public_key BLOB CHECK (machine_public_key IS NULL OR length(machine_public_key) = 32),
    created_at         TEXT NOT NULL,
    claimed_at         TEXT,
    last_used_at       TEXT,
    revoked_at         TEXT
);

ALTER TABLE runner_sources ADD COLUMN capabilities TEXT NOT NULL DEFAULT '[]';
CREATE TABLE runner_inbox (
 job_id TEXT NOT NULL, generation INTEGER NOT NULL, runner_id TEXT NOT NULL,
 seq INTEGER NOT NULL, kind TEXT NOT NULL, payload BLOB NOT NULL,
 payload_hash TEXT NOT NULL, received_at TEXT NOT NULL,
 PRIMARY KEY(job_id,generation,kind,seq),
 CHECK ((kind = 'output' AND seq >= 1) OR (kind = 'result' AND seq = 0))
);
CREATE TRIGGER runner_inbox_replay BEFORE INSERT ON runner_inbox
WHEN EXISTS (SELECT 1 FROM runner_inbox WHERE job_id=NEW.job_id AND generation=NEW.generation AND kind=NEW.kind AND seq=NEW.seq)
BEGIN
 SELECT CASE WHEN EXISTS (SELECT 1 FROM runner_inbox WHERE job_id=NEW.job_id AND generation=NEW.generation AND kind=NEW.kind AND seq=NEW.seq AND (payload_hash != NEW.payload_hash OR runner_id != NEW.runner_id)) THEN RAISE(ABORT, 'conflicting runner inbox replay') END;
 SELECT RAISE(IGNORE);
END;
CREATE TABLE runner_inbox_cursors (
 job_id TEXT NOT NULL, generation INTEGER NOT NULL, runner_id TEXT NOT NULL,
 last_output_seq INTEGER NOT NULL DEFAULT 0 CHECK(last_output_seq >= 0),
 result_received INTEGER NOT NULL DEFAULT 0 CHECK(result_received IN (0,1)),
 PRIMARY KEY(job_id,generation)
);
