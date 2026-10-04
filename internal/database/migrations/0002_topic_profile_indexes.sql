-- Índices de la ficha del tema (recall.ProfileSQL y recall.EventsSQL).
--
-- El índice de 0001 sobre (topic_id, kind, ts DESC) WHERE active no permitía leer los datos
-- estables (varios tipos) ya ordenados por fecha: el planificador tenía que recoger todos los
-- de cada tipo y ordenarlos. Se sustituye por dos índices parciales cuyos predicados son
-- idénticos a los de las consultas, de modo que el orden del índice sirve tal cual el
-- ORDER BY ts DESC ... LIMIT n.

-- Nombre autogenerado por PostgreSQL en 0001: tabla + columnas + "_idx".
DROP INDEX IF EXISTS memory_topics_topic_id_kind_ts_idx;

CREATE INDEX memory_topics_profile_idx ON memory_topics (topic_id, ts DESC)
  WHERE active AND kind IN ('fact','preference','decision');

CREATE INDEX memory_topics_events_idx ON memory_topics (topic_id, ts DESC)
  WHERE active AND kind = 'event';
