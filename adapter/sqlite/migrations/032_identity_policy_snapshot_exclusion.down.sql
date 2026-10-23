-- Deliberately retain the corrected policy and monotonic revision on rollback.
-- Reinstating the unsafe seed could advance profiles to dated snapshots, and a
-- policy replacement belongs to the revision-checked admin editing surface.
SELECT 1;
