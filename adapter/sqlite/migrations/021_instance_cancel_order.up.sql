-- Wall-clock timestamps cannot order creation and cancellation in one second.
-- Keep this clock outside instances so no table rebuild or new domain field is
-- needed. AUTOINCREMENT prevents purged history from reusing an earlier order.
CREATE TABLE instance_creation_order (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id TEXT NOT NULL UNIQUE REFERENCES instances(id) ON DELETE CASCADE,
    cancel_through_sequence INTEGER CHECK (cancel_through_sequence >= 0)
);

-- Historical stamps only recorded seconds: preserve their existing inclusive
-- timestamp semantics. Their original within-second order is unrecoverable.
INSERT INTO instance_creation_order (instance_id)
    SELECT id FROM instances ORDER BY created_at, rowid;
UPDATE instance_creation_order AS boundary
   SET cancel_through_sequence = (
       SELECT COALESCE(MAX(child_order.sequence), 0)
         FROM instance_creation_order child_order
         JOIN instances child ON child.id = child_order.instance_id
        WHERE child.created_at <= (
            SELECT subtree_cancel_requested_at FROM instances
             WHERE id = boundary.instance_id))
 WHERE instance_id IN (
     SELECT id FROM instances WHERE subtree_cancel_requested_at IS NOT NULL);

-- Both order and intent are committed with their real producer's statement.
-- Trigger failure rolls back the entire instance insert / intent update.
CREATE TRIGGER instance_creation_order_insert
AFTER INSERT ON instances
BEGIN
    INSERT INTO instance_creation_order (instance_id) VALUES (NEW.id);
END;

CREATE TRIGGER instance_subtree_cancel_boundary_update
AFTER UPDATE OF subtree_cancel_requested_at ON instances
BEGIN
    UPDATE instance_creation_order
       SET cancel_through_sequence = CASE
           WHEN NEW.subtree_cancel_requested_at IS NULL THEN NULL
           ELSE (SELECT COALESCE(MAX(sequence), 0) FROM instance_creation_order)
       END
     WHERE instance_id = NEW.id;
END;
