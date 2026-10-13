-- D1 audit IDs include deterministic composite keys as well as UUIDs.
-- Preserve the original identity instead of hashing or discarding those records.
ALTER TABLE audit_events ALTER COLUMN id TYPE text USING id::text;
