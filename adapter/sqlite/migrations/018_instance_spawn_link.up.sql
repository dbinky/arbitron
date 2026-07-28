-- Workflow spawn node Phase 1: link a child run back to the parent NODE that
-- spawned it.
--
-- parent_instance_id: the spawning parent run. NULL => this is a ROOT run,
--   which is the normal state for every instance created before this feature
--   and every manually triggered run after it.
-- parent_node_id: the specific spawn node WITHIN that parent. This is what
--   makes the link ADDRESSABLE. Without it, a fire-and-forget child launched
--   from node A reaching a terminal state while the parent waits at node B
--   would wake the parent and route node B by an event unrelated to it.
-- spawn_mode: how the link was created ('fire_and_forget' | 'wait_for_child').
--   Stored on the LINK rather than re-derived from the parent's workflow
--   snapshot so the resumption short-circuit and the wake match can key on it
--   without parsing every parent's snapshot.
--
-- REFERENTIAL INTEGRITY: there is deliberately NO FOREIGN KEY on
-- parent_instance_id. This follows the existing, documented precedent for
-- schedule_id (migration 014): a referenced row may be removed while
-- historical runs remain, and a purged parent must NOT cascade-delete a
-- child's execution history. A dangling parent id reads back as a link whose
-- parent no longer exists; read paths already treat that as a root-ish orphan
-- rather than a bug.
--
-- The spawn_mode CHECK is deliberately permissive of NULL so the columns stay
-- additive for every existing row.
ALTER TABLE instances ADD COLUMN parent_instance_id TEXT;  -- NULL => root run
ALTER TABLE instances ADD COLUMN parent_node_id     TEXT;  -- NULL => root run
ALTER TABLE instances ADD COLUMN spawn_mode         TEXT
    CHECK (spawn_mode IS NULL OR spawn_mode IN ('fire_and_forget','wait_for_child'));

-- One composite index serves every parent-keyed read this feature adds:
--   * children of a parent (hierarchy expand, cancel cascade)
--   * the awaited child of (parent, node, mode) — the resumption short-circuit
--     and the reconciler join
-- The leading column also makes the roots-only predicate
-- (parent_instance_id IS NULL) index-eligible. Portable to Postgres.
CREATE INDEX IF NOT EXISTS idx_instances_parent
    ON instances (parent_instance_id, parent_node_id, spawn_mode);

-- spawn_depth: how many spawn hops separate this run from its ROOT. A root run
-- is 0; its child is 1, and so on. It is the RUNTIME BOUND on spawn recursion.
--
-- WHY A PERSISTED COUNTER. Depth cannot be derived at author time: the spawn
-- graph is a graph of workflow NAMES resolved at run time, and a cycle can be
-- indirect (A spawns B spawns A) or dynamic. Without a bound, each generation
-- costs a row, a workspace, and a billed provider call — and for a shared or
-- published workflow the bill lands on the TRIGGERING tenant, not the author.
-- The alternative, walking the ancestry chain on every spawn, is N queries per
-- spawn to recompute a value that never changes once written.
--
-- NOT NULL DEFAULT 0 keeps the column additive: every pre-existing row reads
-- back as a root, which is what it is.
ALTER TABLE instances ADD COLUMN spawn_depth INTEGER NOT NULL DEFAULT 0;
