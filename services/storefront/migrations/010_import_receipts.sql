-- Preserve source audit/metadata verbatim while mapping the operational schema.
-- These tables are owner-only: runtime cannot read the offline export archive.
CREATE TABLE legacy_imports (
 sha256 text PRIMARY KEY,
 report jsonb NOT NULL,
 imported_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE legacy_import_rows (
 source_table text NOT NULL,
 ordinal bigint NOT NULL,
 payload jsonb NOT NULL,
 PRIMARY KEY(source_table,ordinal)
);
