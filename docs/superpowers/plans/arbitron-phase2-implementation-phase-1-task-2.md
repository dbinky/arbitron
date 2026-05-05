# Phase 1, Task 2: Database Migration + sqlc Regeneration + Postgres Adapter for QueueRepository

## Objective
Create the database migration that adds `waiting` to the state enum, adds `queued_at` column, and adds the queue ordering index, then implement the Postgres adapter for `QueueRepository`.

## Dependencies
- Phase 1, Task 1 (domain model changes must exist for the adapter to implement)

## Files to Create/Modify
- `adapter/postgres/migrations/005_add_queue_support.up.sql` — migration up
- `adapter/postgres/migrations/005_add_queue_support.down.sql` — migration down
- `adapter/postgres/sqlc/queries/queue.sql` — sqlc query definitions
- `adapter/postgres/generated/` — regenerated sqlc code (run `sqlc generate`)
- `adapter/postgres/queue_repo.go` — QueueRepository implementation
- `adapter/postgres/instance_repo.go` — update Create/Get to handle `queued_at` field

## Implementation Steps

1. **Create migration up** (`005_add_queue_support.up.sql`):
   ```sql
   -- Add 'waiting' to instance_state enum
   ALTER TYPE instance_state ADD VALUE IF NOT EXISTS 'waiting';

   -- Add queued_at column
   ALTER TABLE instances ADD COLUMN queued_at TIMESTAMPTZ;

   -- Index for efficient queue ordering (state + priority + queued_at)
   CREATE INDEX idx_instances_queue_order ON instances (state, priority, queued_at)
       WHERE state = 'queued';
   ```

2. **Create migration down** (`005_add_queue_support.down.sql`):
   ```sql
   DROP INDEX IF EXISTS idx_instances_queue_order;
   ALTER TABLE instances DROP COLUMN IF EXISTS queued_at;
   -- Note: cannot remove enum value in PostgreSQL, leave 'waiting' in place
   ```

3. **Add sqlc queries** (`adapter/postgres/sqlc/queries/queue.sql`):
   ```sql
   -- name: CountRunning :one
   SELECT COUNT(*) FROM instances WHERE state = 'running';

   -- name: CountRunningByOwner :one
   SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = $1;

   -- name: NextEligible :one
   SELECT i.* FROM instances i
   WHERE i.state = 'queued'
     AND ($1::int = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running') < $1)
     AND ($2::int = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = i.owner_id) < $2)
   ORDER BY
     CASE i.priority
       WHEN 'high' THEN 1
       WHEN 'normal' THEN 2
       WHEN 'low' THEN 3
     END,
     i.queued_at ASC
   LIMIT 1;

   -- name: SetQueued :exec
   UPDATE instances SET state = 'queued', queued_at = $2, updated_at = now() WHERE id = $1;
   ```

4. **Run sqlc generate** to regenerate the `adapter/postgres/generated/` package.

5. **Implement QueueRepo** (`adapter/postgres/queue_repo.go`):
   ```go
   package postgres

   type QueueRepo struct {
       pool    *pgxpool.Pool
       queries *generated.Queries
   }

   func NewQueueRepo(pool *pgxpool.Pool) *QueueRepo {
       return &QueueRepo{pool: pool, queries: generated.New(pool)}
   }

   func (r *QueueRepo) CountRunning(ctx context.Context) (int, error)
   func (r *QueueRepo) CountRunningByOwner(ctx context.Context, ownerID uuid.UUID) (int, error)
   func (r *QueueRepo) NextEligible(ctx context.Context, globalCap int, perUserCap int) (*instance.Instance, error)
   func (r *QueueRepo) SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error
   ```
   Each method delegates to the generated queries and maps results to domain types.

6. **Update InstanceRepo** — modify `Create` to include `queued_at` parameter, modify `Get` to read `queued_at` from the row and populate `Instance.QueuedAt`, modify `List` to populate `InstanceSummary.QueuedAt`.

7. **Update sqlc instance queries** — add `queued_at` to the `CreateInstance` params and to the SELECT column lists in `GetInstance` and `ListInstances`.

## Tests
- `adapter/postgres/queue_repo_test.go`:
  - `TestCountRunning_Empty` — returns 0 for no instances
  - `TestCountRunning_WithRunning` — returns correct count
  - `TestCountRunningByOwner` — only counts for specified owner
  - `TestNextEligible_PriorityOrdering` — high before normal before low
  - `TestNextEligible_FIFOWithinPriority` — oldest queued_at wins
  - `TestNextEligible_SkipsUserAtCap` — skips user at per-user cap, returns next eligible
  - `TestNextEligible_GlobalCapFull` — returns nil when global cap full
  - `TestNextEligible_UnlimitedCaps` — cap of 0 disables check
  - `TestSetQueued` — updates state and queued_at

## Acceptance Criteria
- Migration applies cleanly on existing schema
- `waiting` exists in the `instance_state` enum
- `queued_at` column exists on `instances` table
- Partial index on `(state, priority, queued_at)` exists for queued state
- sqlc generates without errors
- `QueueRepo` implements `instance.QueueRepository` interface
- `NextEligible` respects priority ordering, FIFO within priority, and per-user cap skipping
- All repository tests pass with real Postgres (testcontainers or similar)
