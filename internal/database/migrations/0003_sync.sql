-- Sincronización entre máquinas (lodan sync): identidad global, marcas de cambio y estado.
--
-- sync_xid guarda la transacción local que hizo el último cambio que debe exportarse. Las
-- importaciones activan lodan.sync_import (SET LOCAL) para que lo que llega de otra máquina
-- no se marque como cambio local y no se vuelva a exportar.

-- Identidad global de cada registro y su versión de contenido.
ALTER TABLE memories ADD COLUMN sync_id uuid;
ALTER TABLE memories ADD COLUMN sync_rev uuid;
UPDATE memories SET sync_id = gen_random_uuid(), sync_rev = gen_random_uuid();
ALTER TABLE memories
  ALTER COLUMN sync_id SET DEFAULT gen_random_uuid(), ALTER COLUMN sync_id SET NOT NULL,
  ALTER COLUMN sync_rev SET DEFAULT gen_random_uuid(), ALTER COLUMN sync_rev SET NOT NULL;
CREATE UNIQUE INDEX memories_sync_id ON memories (sync_id);
-- sync_base: última versión intercambiada con otras máquinas; sync_dirty: hay una edición
-- local de contenido posterior a ese intercambio.
ALTER TABLE memories ADD COLUMN sync_base uuid;
ALTER TABLE memories ADD COLUMN sync_dirty boolean NOT NULL DEFAULT false;
ALTER TABLE memories ADD COLUMN sync_xid xid8;
UPDATE memories SET sync_xid = pg_current_xact_id();

ALTER TABLE memory_relations ADD COLUMN updated_at timestamptz;
UPDATE memory_relations SET updated_at = created_at;
ALTER TABLE memory_relations ALTER COLUMN updated_at SET DEFAULT now(), ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE memory_relations ADD COLUMN sync_xid xid8;
UPDATE memory_relations SET sync_xid = pg_current_xact_id();

ALTER TABLE sessions ADD COLUMN sync_xid xid8;
UPDATE sessions SET sync_xid = pg_current_xact_id();

-- Versiones de contenido que cada registro ha tenido en esta máquina.
CREATE TABLE sync_revs (sync_id uuid NOT NULL, rev uuid NOT NULL, PRIMARY KEY (sync_id, rev));
INSERT INTO sync_revs (sync_id, rev) SELECT sync_id, sync_rev FROM memories;

-- Registros borrados (para que el borrado llegue a las otras máquinas y no se recreen).
CREATE TABLE sync_tombstones (sync_id uuid PRIMARY KEY, deleted_at timestamptz NOT NULL DEFAULT now(), sync_xid xid8);

-- sync_id de otra máquina que corresponde a un registro local con el mismo contenido.
CREATE TABLE sync_aliases (alias uuid PRIMARY KEY, sync_id uuid NOT NULL);

-- Relaciones importadas cuyos registros aún no han llegado.
CREATE TABLE sync_pending_relations (
  source uuid NOT NULL, target uuid NOT NULL, kind relation_kind NOT NULL, state relation_state NOT NULL,
  created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL, PRIMARY KEY (source, target, kind));

-- Ficheros ya importados y estado de la exportación.
CREATE TABLE sync_imported_files (path text PRIMARY KEY, imported_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE sync_state (key text PRIMARY KEY, value text NOT NULL);

CREATE FUNCTION lodan_sync_importing() RETURNS boolean LANGUAGE sql STABLE AS
  $$ SELECT coalesce(current_setting('lodan.sync_import', true), '') = 'on' $$;

-- Marca los cambios locales de memories y cambia la versión cuando cambia el contenido.
CREATE FUNCTION lodan_sync_mark_memory() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF lodan_sync_importing() THEN
    IF TG_OP = 'INSERT' THEN NEW.sync_xid := NULL; ELSE NEW.sync_xid := OLD.sync_xid; END IF;
    RETURN NEW;
  END IF;
  NEW.sync_xid := pg_current_xact_id();
  IF TG_OP = 'UPDATE' AND (NEW.title, NEW.content, NEW.kind, NEW.key, NEW.occurred_at)
       IS DISTINCT FROM (OLD.title, OLD.content, OLD.kind, OLD.key, OLD.occurred_at) THEN
    NEW.sync_rev := gen_random_uuid();
    NEW.sync_dirty := true;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER memories_sync_insert BEFORE INSERT ON memories
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_memory();
CREATE TRIGGER memories_sync_update BEFORE UPDATE OF updated_at, status, title, content, key, kind, occurred_at ON memories
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_memory();

-- Recuerda cada versión de contenido que ha tenido un registro.
CREATE FUNCTION lodan_sync_remember_rev() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO sync_revs (sync_id, rev) VALUES (NEW.sync_id, NEW.sync_rev) ON CONFLICT DO NOTHING;
  RETURN NULL;
END $$;
CREATE TRIGGER memories_sync_revs AFTER INSERT OR UPDATE OF sync_rev ON memories
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_remember_rev();

-- Un borrado deja una lápida; si es local, se exporta.
CREATE FUNCTION lodan_sync_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO sync_tombstones (sync_id, sync_xid)
    VALUES (OLD.sync_id, CASE WHEN lodan_sync_importing() THEN NULL ELSE pg_current_xact_id() END)
    ON CONFLICT (sync_id) DO NOTHING;
  RETURN NULL;
END $$;
CREATE TRIGGER memories_sync_delete AFTER DELETE ON memories
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_tombstone();

-- Relaciones: fecha de cambio y marca local.
CREATE FUNCTION lodan_sync_mark_relation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF lodan_sync_importing() THEN
    IF TG_OP = 'INSERT' THEN NEW.sync_xid := NULL; ELSE NEW.sync_xid := OLD.sync_xid; END IF;
    RETURN NEW;
  END IF;
  NEW.sync_xid := pg_current_xact_id();
  NEW.updated_at := now();
  RETURN NEW;
END $$;
CREATE TRIGGER memory_relations_sync BEFORE INSERT OR UPDATE OF state ON memory_relations
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_relation();

-- Sesiones: marca local.
CREATE FUNCTION lodan_sync_mark_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF lodan_sync_importing() THEN
    IF TG_OP = 'INSERT' THEN NEW.sync_xid := NULL; ELSE NEW.sync_xid := OLD.sync_xid; END IF;
  ELSE
    NEW.sync_xid := pg_current_xact_id();
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER sessions_sync BEFORE INSERT OR UPDATE OF client, ended_at, summary, last_activity_at ON sessions
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_session();

-- Los temas de una sesión cambian sin tocar la fila de la sesión: se marca la sesión.
CREATE FUNCTION lodan_sync_mark_session_topics() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT lodan_sync_importing() THEN
    UPDATE sessions SET last_activity_at = last_activity_at WHERE id = NEW.session_id;
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER session_topics_sync AFTER INSERT ON session_topics
  FOR EACH ROW EXECUTE FUNCTION lodan_sync_mark_session_topics();
