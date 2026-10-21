CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS memories_content_trgm_idx ON memories USING gin (lower(content) gin_trgm_ops) WHERE status = 'active';
