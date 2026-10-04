CREATE EXTENSION IF NOT EXISTS vector;

CREATE TYPE memory_kind   AS ENUM ('fact','preference','decision','event','note');
CREATE TYPE memory_status AS ENUM ('active','superseded','invalidated');
CREATE TYPE relation_kind AS ENUM ('supersedes','contradicts','related','part_of');
CREATE TYPE relation_state AS ENUM ('suggested','confirmed','rejected');

-- Ajustes fijados al crear la base (modelo y dimensiones de los embeddings).
CREATE TABLE lodan_settings (key text PRIMARY KEY, value text NOT NULL);

CREATE TABLE embedding_models (id smallint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text UNIQUE NOT NULL, dims int NOT NULL);

CREATE TABLE sessions (
  id text PRIMARY KEY,                       -- ULID: único y ordenable por tiempo
  client text NOT NULL, started_at timestamptz NOT NULL DEFAULT now(),
  last_activity_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
  summary text);
CREATE INDEX ON sessions (started_at DESC);

CREATE TABLE memories (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,   -- ids cortos (#123): menos tokens
  kind memory_kind NOT NULL, status memory_status NOT NULL DEFAULT 'active',
  title text NOT NULL CHECK (length(title) <= 200),
  content text NOT NULL CHECK (length(content) <= 8000),
  key text,                                    -- opcional: upsert de datos estables (idea topic_key de Engram)
  content_hash bytea NOT NULL,                 -- sha256 del contenido normalizado
  session_id text REFERENCES sessions(id) ON DELETE SET NULL,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  embedding halfvec({{.Dims}}), embedding_model smallint REFERENCES embedding_models(id),
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('spanish', title || ' ' || content)) STORED);
CREATE UNIQUE INDEX ON memories (content_hash) WHERE status = 'active';
CREATE UNIQUE INDEX ON memories (key) WHERE status = 'active' AND key IS NOT NULL;
CREATE INDEX ON memories USING gin (tsv);
CREATE INDEX ON memories (session_id);
CREATE INDEX ON memories (id) WHERE embedding IS NULL;           -- cola de pendientes
CREATE INDEX memories_bq_hnsw ON memories
  USING hnsw ((binary_quantize(embedding)::bit({{.Dims}})) bit_hamming_ops) WHERE status = 'active';

CREATE TABLE topics (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, slug text UNIQUE NOT NULL,
  embedding halfvec({{.Dims}}), created_at timestamptz NOT NULL DEFAULT now(), last_used_at timestamptz NOT NULL DEFAULT now());

CREATE TABLE memory_topics (          -- desnormalizada para la ficha del tema sin joins caros
  topic_id int REFERENCES topics(id) ON DELETE CASCADE, memory_id bigint REFERENCES memories(id) ON DELETE CASCADE,
  kind memory_kind NOT NULL, active boolean NOT NULL, ts timestamptz NOT NULL,
  PRIMARY KEY (topic_id, memory_id));
CREATE INDEX ON memory_topics (memory_id);
CREATE INDEX ON memory_topics (topic_id, kind, ts DESC) WHERE active;

CREATE TABLE session_topics (session_id text REFERENCES sessions(id) ON DELETE CASCADE,
  topic_id int REFERENCES topics(id) ON DELETE CASCADE, PRIMARY KEY (session_id, topic_id));

CREATE TABLE memory_relations (
  source_id bigint REFERENCES memories(id) ON DELETE CASCADE, target_id bigint REFERENCES memories(id) ON DELETE CASCADE,
  kind relation_kind NOT NULL, state relation_state NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_id, target_id, kind));
CREATE INDEX ON memory_relations (target_id);
