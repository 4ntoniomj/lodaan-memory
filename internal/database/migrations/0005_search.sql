CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_ts_config WHERE cfgname = 'lodan_es') THEN
        CREATE TEXT SEARCH CONFIGURATION lodan_es (COPY = spanish);
        ALTER TEXT SEARCH CONFIGURATION lodan_es
            ALTER MAPPING FOR word, hword, hword_part, asciiword, asciihword, hword_asciipart
            WITH unaccent, spanish_stem;
    END IF;
END $$;

ALTER TABLE memories DROP COLUMN tsv;
ALTER TABLE memories ADD COLUMN tsv tsvector GENERATED ALWAYS AS (to_tsvector('lodan_es', title || ' ' || content)) STORED;
CREATE INDEX ON memories USING gin (tsv);
