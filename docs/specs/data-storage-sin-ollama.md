# D1 — Data storage specification (lodan-sin-ollama)

**Estado:** propuesta de diseño. Pendiente de aprobación por Antonio.
**Owner del documento:** orquestador de `feat/sin-ollama`.
**Aprobación:** requerida para salir de Phase 0 y arrancar Phase 1 (código).

---

## 1. Contexto

`lodan-sin-ollama` quita Ollama como dependencia obligatoria. La BD sigue siendo Postgres, full-text en español (`tsvector` sobre `title || content`) sigue siendo la fuente primaria de búsqueda, y los embeddings quedan como bonus detrás de `--with-embeddings`.

Este documento cierra las decisiones de **storage** que el resto de la Phase 0 (D2 taxonomía, D3 skill, D4 quality, D5 validación) consume:

1. ¿Qué columnas se quedan tal cual y cuáles quedan **nullable permanente**?
2. ¿Qué hacemos con `pgvector` (mantener extensión, ¿índice activo)?
4. ¿Cómo se enforce la **forma de las tags** y la **unicidad de `key`**?
5. ¿Qué índices se mantienen, cuáles se eliminan, cuáles se añaden?

---

## 2. Estado actual (baseline)

Migraciones presentes: `0001_init.sql`, `0002_topic_profile_indexes.sql`, `0003_sync.sql`, `0004_tags.sql`.

Tablas y columnas relevantes:

| Tabla | Columna | Tipo | Notas |
|---|---|---|---|
| `memories` | `embedding` | `halfvec(N)` | Nullable. Con Ollama caído queda NULL. |
| `memories` | `embedding_model` | `smallint REFERENCES embedding_models(id)` | Nullable. Mismo caso. |
| `memories` | `key` | `text` | Nullable. Ya tiene índice único parcial `WHERE status='active' AND key IS NOT NULL`. |
| `memories` | `content_hash` | `bytea` | `sha256` del contenido normalizado; índice único parcial activo. |
| `memories` | `tsv` | `tsvector GENERATED ... STORED` | Generado de `title \|\| ' ' \|\| content` con config `spanish`. |
| `topics` | `slug` | `text UNIQUE NOT NULL` | Es el `tag` canónico. Sin regex aún. |
| `memory_topics` | `topic_id`, `memory_id`, `kind`, `active`, `ts` | PK compuesta `(topic_id, memory_id)` | Desnormalizada para la ficha del tema sin joins. |
| `tag_aliases` | `alias`, `canonical`, `state` | `(alias, canonical)` PK, `alias <> canonical` CHECK | Ya soporta singular/plural y otros pares. |

Índices presentes que importan:

- `memories USING gin (tsv)` — full-text. Se mantiene.
- `memories (id) WHERE embedding IS NULL` — cola de pendientes para `FillPending`. **Se elimina**: si embeddings son opt-in, el índice no se debe mantener.
- `memories_bq_hnsw ... WHERE status='active'` — HNSW sobre `binary_quantize(embedding)`. **Se elimina**: ya no se hace búsqueda semántica por default; opcional detrás de flag.
- `memory_topics (memory_id)` y `memory_topics (topic_id, kind, ts DESC) WHERE active` — soportan `tags_all` / `tags_any` / `mode='list'`. Se mantienen.
- `topics_compact_slug_idx (replace(slug, '-', ''))` — búsqueda insensible a guiones. Se mantiene.

Extensión: `vector` (pgvector) está instalada vía `0001_init.sql`. **Se mantiene** instalada (costo cero si no se usa; permite volver a habilitar embeddings sin migrar).

---

## 3. Decisiones

### 3.1 Schema final — qué se queda, qué queda nullable

| Columna | Decisión | Razón |
|---|---|---|
| `memories.embedding` | **Nullable permanente.** Sin `NOT NULL`. | Embeddings son opt-in. El 99% de records no tendrán embedding. |
| `memories.embedding_model` | **Nullable permanente.** Sin `NOT NULL`. | Si no hay embedding, no hay modelo. |
| `memories.key` | Nullable. Sin cambio. | Ya es opcional y útil para datos evolutivos. |
| `memories.content_hash` | `NOT NULL`. Sin cambio. | Necesario para dedup por hash. |
| `memories.tsv` | Generada. Sin cambio. | Motor primario de búsqueda. |

**No se renombra nada. No se cambia el tipo de nada. No se borran columnas.**

### 3.2 pgvector — mantener extensión, quitar índice activo

- **Extensión `vector` se mantiene.** Costo cero si no se inserta ninguna fila con embedding. Permite volver a activar embeddings vía flag sin nueva migración mayor.
- **Índice `memories_bq_hnsw` se elimina.** No se hace búsqueda semántica por default. Mantenerlo paga espacio y costo de `UPDATE` en vano.
- **Índice `memories (id) WHERE embedding IS NULL` se elimina.** Es la cola de pendientes para `FillPending`. Si `FillPending` está desactivado por default, la cola es vacía y el índice es ruido.

### 3.3 Constraints

#### 3.3.1 Forma de tags concretas — CHECK en `topics.slug`

Regex: `^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$`

- Empieza con minúscula.
- Segmentos separados por `-` con chars `[a-z0-9]+`.
- Atributos jerárquicos separados por `/` con chars `[a-z0-9]+`.
- Sin mayúsculas, sin espacios, sin guiones bajos, sin `#`.

Aplica como `CHECK (slug ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$')` sobre `topics.slug`.

**Importante:** esta regex NO se valida sobre el contenido de `memories` ni sobre strings arbitrarios. Se valida en `topics.slug` (el canónico) y — por convención — debe respetarse al crear nuevos `topics`.

Excepción documentada: los aliases en `tag_aliases.alias` y `tag_aliases.canonical` deben **matchear** la misma regex para mantener la consistencia (los aliases solo redirigen, no introducen forma nueva).

#### 3.3.2 Unicidad de `key`

Decisión: **`key` es global**, no por topic. Ya hay índice único parcial `WHERE status='active' AND key IS NOT NULL` sobre `memories.key`. Se mantiene así.

Justificación: en el sistema actual, `key` identifica un dato evolutivo (p.ej. `coche-volvo-s60/matricula`). Si el mismo `key` se asigna a un memory con `topic` distinto, es ambiguo y debe ser `supersede` (el nuevo invalida al anterior). Esa es la disciplina correcta y la soporta el constraint actual.

`topic_key` (sugerido por Engram): **no se modela como columna separada.** Si en el futuro hace falta scoping por topic, se añade `memory_topics.top_key text` con índice único parcial `(topic_id, top_key) WHERE active` en una migración posterior. No se anticipa en esta fase.

#### 3.3.3 Consistencia `key` ↔ `topic_key`

No aplica (no hay `topic_key` como columna separada). Cuando se introduzca, la regla será: si `memory_topics.top_key` está presente, debe matchear la regex de tags concretas (D2). Documentar para migración futura.

### 3.4 Índices

| Índice | Acción | Razón |
|---|---|---|
| `memories USING gin (tsv)` | Mantener | Búsqueda primaria. |
| `memories UNIQUE (content_hash) WHERE status='active'` | Mantener | Dedup por hash. |
| `memories UNIQUE (key) WHERE status='active' AND key IS NOT NULL` | Mantener | Supersede por key. |
| `memories (session_id)` | Mantener | Navegación por sesión. |
| `memory_topics (memory_id)` | Mantener | Joins de ficha. |
| `memory_topics (topic_id, kind, ts DESC) WHERE active` | Mantener | Tags-first queries y `mode='list'`. |
| `memory_topics_profile_idx`, `memory_topics_events_idx` | Mantener | Perfil de tema. |
| `topics_compact_slug_idx` | Mantener | Búsqueda ignorando guiones. |
| `memories (id) WHERE embedding IS NULL` | **Eliminar** | Cola de pendientes sin uso. |
| `memories_bq_hnsw` | **Eliminar** | HNSW sin búsqueda semántica por default. |

No se añaden índices nuevos en esta migración. Si el perfil de consultas cambia (p.ej. `tags_all` necesita un índice más selectivo), se mide y se justifica en una migración aparte.

### 3.5 Backups y restore (S1)

Antes de cualquier migración D1 o de T4, ejecutar `lodan db backup --full` sobre el data dir activo. El backup se guarda en una ruta fuera del árbol de trabajo (sugerido: `C:\Users\4ntoniomj\backups\lodan-pre-sin-ollama-<YYYYMMDD>\`). Verificar checksum. Restaurar en una BD de prueba para confirmar integridad.

---

## 4. Migración propuesta

`internal/database/migrations/0005_taxonomy_constraints.sql` (up + down en archivos separados si el runner lo exige).

**Up:**

```sql
BEGIN;

-- 1) Forma de tags concretas en topics.slug.
ALTER TABLE topics
  ADD CONSTRAINT topics_slug_format_chk
  CHECK (slug ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$');

-- 2) Misma forma para aliases (consistencia: aliases no introducen forma nueva).
--    tag_aliases.alias y tag_aliases.canonical deben matchear la regex.
ALTER TABLE tag_aliases
  ADD CONSTRAINT tag_aliases_alias_format_chk
  CHECK (alias ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$'),
  ADD CONSTRAINT tag_aliases_canonical_format_chk
  CHECK (canonical ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$');

-- 3) Quitar índice HNSW (búsqueda semántica deshabilitada por default).
DROP INDEX IF EXISTS memories_bq_hnsw;

-- 4) Quitar índice de cola de pendientes (FillPending desactivado por default).
DROP INDEX IF EXISTS memories_embedding_pending_idx;

COMMIT;
```

**Down:**

```sql
BEGIN;

-- Re-crear índice HNSW asumiendo que la dimensión configurada sigue siendo la misma.
-- Nota: requiere leer `embedding_models.dims` y aplicar el template. La migración down
-- documenta el shape, la implementación concreta la hace el runner (golang-migrate).
CREATE INDEX memories_bq_hnsw ON memories
  USING hnsw ((binary_quantize(embedding)::bit(N)) bit_hamming_ops) WHERE status='active';

CREATE INDEX ON memories (id) WHERE embedding IS NULL;

ALTER TABLE tag_aliases
  DROP CONSTRAINT IF EXISTS tag_aliases_alias_format_chk,
  DROP CONSTRAINT IF EXISTS tag_aliases_canonical_format_chk;

ALTER TABLE topics
  DROP CONSTRAINT IF EXISTS topics_slug_format_chk;

COMMIT;
```

**Idempotencia:**

- `ADD CONSTRAINT IF NOT EXISTS` no existe en Postgres estándar para CHECK. La migración detecta constraint por `pg_constraint` y aborta con mensaje claro si ya existe. Convención: numerar correlativo y confiar en que el runner no la ejecuta dos veces. Si se ejecuta dos veces manualmente, falla con `constraint already exists` — eso es OK y se documenta.
- `DROP INDEX IF EXISTS` ya lo es.

---

## 5. Validación

Antes de promover a Phase 1:

1. **Migración sobre data dir de prueba:**
   - Apuntar `lodan serve --data-dir=<dir-prueba>` con la BD de prueba recién restaurada del backup.
   - Correr migración. Sin errores.
   - Re-arrancar servicio. Sin errores.

2. **Smoke test del CHECK:**
   - `INSERT INTO topics (slug) VALUES ('Tag-Con-Mayuscula');` → debe fallar con violación de `topics_slug_format_chk`.
   - `INSERT INTO topics (slug) VALUES ('proyecto-erp-agm');` → debe pasar.
   - `INSERT INTO topics (slug) VALUES ('coche-volvo-s60/matricula');` → debe pasar.

3. **Métricas baseline (D4 referencia):**
   - `% records con ≥1 tag concreta` antes de migración.
   - `% tags concretas reutilizadas`.
   - `% records huérfanos`.
   Estas métricas se comparan contra las post-migración de T4.

4. **Regresión de tests existentes:**
   - `go test ./...` debe pasar 100% con la nueva migración aplicada.
   - Si algún test dependía del HNSW, se ajusta antes de mergear (preferentemente eliminando el test si la feature ya no aplica).

---

## 6. Riesgos del spec

| Riesgo | Mitigación |
|---|---|
| CHECK rechaza slugs legacy que ya están en BD | La migración añade la constraint; si falla por datos existentes, se documenta en runbook y se hace cleanup previo (T4). |
| Quitar `memories_bq_hnsw` rompe alguna búsqueda que dependía de él | El binario nuevo no expone búsqueda semántica por default; se conserva detrás de flag. Si alguien la usa, el flag la reactiva y el índice se recrea on-demand. |
| Aliases con forma legacy (mayúsculas, espacios) ya confirmados | La migración los rechaza. Plan de remediación: marcar como `rejected` los aliases mal formados, mantener `canonical` válido, y volver a confirmar. Se hace en el script de migración de T4. |

---

## 7. Aprobación

- [ ] Antonio revisa la regex. ¿Cubre las 12 familias de D2? ¿Falta algún caso?
- [ ] Antonio confirma: ¿`key` global o por topic?
- [ ] Antonio confirma: ¿se quita `memories_bq_hnsw`?
- [ ] Antonio confirma: ¿pgvector se queda instalado?

Cuando esté aprobado, este spec se mueve a `Status: approved` y Phase 1 puede arrancar.