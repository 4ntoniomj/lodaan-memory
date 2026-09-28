-- Activar pgvector
CREATE EXTENSION IF NOT EXISTS vector;

-- Tabla principal de recuerdos
CREATE TABLE IF NOT EXISTS memory_items (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    content TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now(),
    occurred_at TIMESTAMPTZ,
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    source_type TEXT NOT NULL,
    source_ref TEXT,
    conversation_id TEXT,
    turn_index BIGINT,
    importance REAL,
    embedding vector(384), -- Gemma 300m embeddings dimension
    embedding_model TEXT,
    content_hash TEXT,
    metadata JSONB,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);

-- Indice HNSW para pgvector (distancia coseno)
CREATE INDEX IF NOT EXISTS memory_items_embedding_idx ON memory_items USING hnsw (embedding vector_cosine_ops);
-- Indice de texto completo
CREATE INDEX IF NOT EXISTS memory_items_search_idx ON memory_items USING GIN (search_vector);
-- Indices jsonb
CREATE INDEX IF NOT EXISTS memory_items_metadata_idx ON memory_items USING GIN (metadata);

-- Tabla de relaciones
CREATE TABLE IF NOT EXISTS memory_links (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v7(),
    source_id UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    target_id UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now(),
    metadata JSONB
);

CREATE INDEX IF NOT EXISTS memory_links_source_idx ON memory_links (source_id);
CREATE INDEX IF NOT EXISTS memory_links_target_idx ON memory_links (target_id);
