-- Etiquetas: diccionario de alias y veredictos sobre las relaciones.
--
-- Las etiquetas reutilizan las tablas de temas (topics, memory_topics); esta migración no
-- cambia ningún dato existente.

-- Diccionario de alias: alias → canonical. Una sugerencia (suggested) la propone el servidor,
-- un alias confirmado (confirmed) se aplica solo y uno rechazado (rejected) no se vuelve a
-- sugerir. Solo puede haber un canónico confirmado por alias.
CREATE TABLE tag_aliases (
  alias      text NOT NULL,
  canonical  text NOT NULL,
  state      text NOT NULL CHECK (state IN ('confirmed', 'suggested', 'rejected')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  sync_xid   xid8,
  PRIMARY KEY (alias, canonical),
  CHECK (alias <> canonical)
);
CREATE UNIQUE INDEX tag_aliases_confirmed_idx ON tag_aliases (alias) WHERE state = 'confirmed';
CREATE INDEX tag_aliases_canonical_idx ON tag_aliases (canonical);

-- Marca local para la sincronización, como en memory_relations. Las sugerencias no viajan.
CREATE OR REPLACE FUNCTION lodan_sync_mark_tag_alias() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF lodan_sync_importing() THEN
    IF TG_OP = 'INSERT' THEN NEW.sync_xid := NULL; ELSE NEW.sync_xid := OLD.sync_xid; END IF;
    RETURN NEW;
  END IF;
  NEW.updated_at := now();
  IF NEW.state = 'suggested' THEN
    NEW.sync_xid := NULL;
  ELSE
    NEW.sync_xid := pg_current_xact_id();
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER tag_aliases_sync BEFORE INSERT OR UPDATE OF state, canonical ON tag_aliases
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_tag_alias();

-- Búsqueda de una etiqueta sin tener en cuenta los guiones (coche-4860-ngl = coche-4860ngl).
CREATE INDEX IF NOT EXISTS topics_compact_slug_idx ON topics (replace(slug, '-', ''));

-- Veredicto de la IA sobre una posible relación o conflicto, con su confianza.
ALTER TABLE memory_relations
  ADD COLUMN verdict text CHECK (verdict IN ('contradice', 'sustituye', 'compatible', 'no_relacionado')),
  ADD COLUMN confidence real CHECK (confidence >= 0 AND confidence <= 1);
