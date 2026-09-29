# Plan 001 — Núcleo de memoria

## Enfoque técnico

Un único binario Go, `lodan`, con subcomandos:
- `serve`: servidor MCP por stdio o HTTP en 127.0.0.1;
- `db`: gestiona un clúster PostgreSQL propio en el directorio de datos del usuario;
- `bench`: benchmark.

Todo el trabajo pesado lo hace el servidor. La IA solo envía y recibe texto:
- calcula los embeddings llamando a Ollama;
- deduplica;
- detecta temas;
- fusiona la búsqueda semántica y la de texto;
- formatea respuestas compactas en texto plano, con un tope de bytes.

## Estructura (screaming architecture, adaptada a Go)

```
lodan/
├── cmd/lodan/            # CLI: serve | db | migrate | status | bench
├── internal/
│   ├── memory/           # guardar, revisar, obtener: estados, dedup, clave, relaciones, worker de embeddings pendientes
│   ├── recall/           # búsqueda híbrida, detección de tema, ficha del tema, formato y tope de bytes
│   ├── topic/            # normalización de temas y equivalencias
│   ├── session/          # sesiones perezosas, cierre, "última conversación"
│   ├── embedding/        # cliente Ollama /api/embed, prefijos por modelo, embedder falso para tests
│   ├── mcptools/         # las 6 herramientas MCP, instrucciones, transportes stdio/HTTP
│   ├── benchmark/        # generador sintético, carga con COPY, mediciones e informe
│   ├── database/         # compartido: ciclo de vida del clúster local, pool pgx, migraciones embebidas
│   └── config/           # compartido: rutas por SO, fichero de configuración, valores por defecto
├── specs/  AGENTS.md  CLAUDE.md  CODEBASE.md  README.md
```

Cómo se aplica la skill:
- Cada funcionalidad lleva su `CLAUDE.md`.
- `database/` y `config/` son compartidos porque los usan 3 o más funcionalidades (Regla de Tres).
- Paquetes en inglés, en minúsculas y sin guiones (convención de Go).
- **Desviación documentada de la skill:** los tests van junto al código como `*_test.go`, que es la convención de Go, y no en una subcarpeta `tests/`. Una subcarpeta impediría probar el código no exportado. Los tests end-to-end MCP viven en `internal/mcptools/e2e_test.go`.

## Stack y dependencias

- **Go:** `go 1.25.0` en `go.mod` (mínimo que exige el SDK de MCP). Toolchain `go1.27.1`; con `GOTOOLCHAIN=auto` se descarga sola.
- **Módulos:**
  - `github.com/modelcontextprotocol/go-sdk` v1.8.0 (SDK oficial);
  - `github.com/jackc/pgx/v5` v5.11.0;
  - `github.com/pgvector/pgvector-go` v0.4.1 (tipo `HalfVector`);
  - `github.com/oklog/ulid/v2` v2.1.2.
- **Ollama:** cliente HTTP propio con `net/http`, sin SDK, contra `POST /api/embed`.
- **Configuración:** JSON con la stdlib, más variables de entorno `LODAN_*` que tienen prioridad.
- **PostgreSQL ≥ 16** (en Windows conda-forge solo tiene pgvector con PG 16), más pgvector ≥ 0.7.0 (`halfvec`, `binary_quantize(halfvec)`). En desarrollo se usa el PostgreSQL 18 del sistema (`/usr/lib/postgresql/18/bin`, con `vector`, `pg_trgm` y `unaccent`).
- **Modelo de embeddings por defecto:** `embeddinggemma` (768 dimensiones, multilingüe). Prefijos de su ficha: consulta `task: search result | query: `; documento `title: {título} | text: `. Configurable.

## Rutas y configuración (paquete `config`)

- **Directorio de datos:**
  - Linux: `$XDG_DATA_HOME/lodan`, o `~/.local/share/lodan` si no está definida;
  - macOS: `~/Library/Application Support/lodan`;
  - Windows: `%LOCALAPPDATA%\lodan`.
- **Contenido del directorio de datos:**
  - `pg/`: el clúster;
  - `config.json`;
  - `secret`: contraseña de PostgreSQL, con permisos 0600;
  - `logs/`.
- **Valores por defecto:**

| Opción | Valor |
|---|---|
| `pg_port` | 54329 |
| `pg_bin_dir` | autodetectado: `pg_config --bindir` o PATH |
| `ollama_url` | `http://127.0.0.1:11434` |
| `embed_model` | `embeddinggemma` |
| `embed_dims` | 768 |
| `embed_keep_alive` | `-1` (modelo siempre cargado, R12) |
| `embed_timeout_ms` | 2000 (tiempo máximo de espera del embedding al guardar; si vence, el registro queda pendiente) |
| `http_addr` | `127.0.0.1:7438` |
| `recall_max_bytes` | 6000 |
| `session_idle_minutes` | 30 |
| `dup_similarity` | 0,92 |
| `topic_similarity` | 0,85 |

- Los umbrales de similitud son valores iniciales. Se calibran con el modelo real y se documentan en `benchmark.md`.

## Clúster PostgreSQL local (paquete `database`)

- **`initdb`:**
  - `-D <datos>/pg -U lodan --auth-host=scram-sha-256 --auth-local=scram-sha-256 --pwfile=<tmp> -E UTF8 --locale=C`;
  - la contraseña es aleatoria de 32 bytes y se guarda en `secret`.
- **`postgresql.conf` añadido:**
  - `listen_addresses='127.0.0.1'`;
  - `port`;
  - `unix_socket_directories` en el directorio de datos en Unix, vacío en Windows;
  - `shared_buffers=256MB`, `work_mem=16MB`, `maintenance_work_mem=512MB`;
  - `max_connections=20`.
- **`pg_hba.conf` reescrito:** solo `host all lodan 127.0.0.1/32 scram-sha-256`, más `local` en Unix.
- **Arranque y parada:** `pg_ctl start -w -l <logs>/postgres.log`, `pg_ctl stop -m fast`, `pg_ctl status`.
- **Arranque perezoso:** `serve` arranca el clúster si no está en marcha. Usa un lockfile en el directorio de datos para que dos procesos stdio no lo inicien a la vez.
- **Migraciones:** ficheros SQL embebidos con `embed.FS`, tabla `schema_migrations`, cada una en su transacción y con un advisory lock.
- **Helper de test:** `database.NewTestCluster(t)` hace `initdb` en `t.TempDir()` con un puerto libre. Si no hay binarios, `t.Skip` con motivo.

## Esquema (migración 0001)

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TYPE memory_kind   AS ENUM ('fact','preference','decision','event','note');
CREATE TYPE memory_status AS ENUM ('active','superseded','invalidated');
CREATE TYPE relation_kind AS ENUM ('supersedes','contradicts','related','part_of');
CREATE TYPE relation_state AS ENUM ('suggested','confirmed','rejected');

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
  embedding halfvec(768), embedding_model smallint REFERENCES embedding_models(id),
  tsv tsvector GENERATED ALWAYS AS (to_tsvector('spanish', title || ' ' || content)) STORED);
CREATE UNIQUE INDEX ON memories (content_hash) WHERE status = 'active';
CREATE UNIQUE INDEX ON memories (key) WHERE status = 'active' AND key IS NOT NULL;
CREATE INDEX ON memories USING gin (tsv);
CREATE INDEX ON memories (session_id);
CREATE INDEX ON memories (id) WHERE embedding IS NULL;           -- cola de pendientes
CREATE INDEX memories_bq_hnsw ON memories
  USING hnsw ((binary_quantize(embedding)::bit(768)) bit_hamming_ops) WHERE status = 'active';

CREATE TABLE topics (id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY, slug text UNIQUE NOT NULL,
  embedding halfvec(768), created_at timestamptz NOT NULL DEFAULT now(), last_used_at timestamptz NOT NULL DEFAULT now());

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
```

Consideraciones del esquema:
- La dimensión 768 se fija en la migración a partir de la configuración. Cambiar de modelo o de dimensión requiere una migración y recalcular los embeddings (riesgo reconocido en el PRD).
- `memories` y `memory_topics` se mantienen coherentes dentro de la misma transacción desde el código, no con triggers, para que sea explícito y testeable.

## Herramientas MCP (6)

Todas las respuestas son texto plano compacto, no JSON: cuestan menos tokens. Formato de línea: `#id [tipo fecha] título — fragmento`.

| Herramienta | Entrada | Salida |
|---|---|---|
| `remember` | `items[]` con `title`, `content`, `kind`, `topics[]`, `key?`, `occurred_at?` | Una línea por ítem: id, temas finales (y equivalencias aplicadas), duplicado o pendiente, y "posibles duplicados/contradicciones" |
| `recall` | `query`, `topics?`, `limit?` (por defecto 8, máx. 20), `include_history?` | Ficha de tema + últimos eventos + resultados, con tope de `recall_max_bytes` |
| `get` | `ids[]` (máx. 20) | Contenido completo + relaciones |
| `revise` | `id`, `action` (`update`, `supersede`, `invalidate`, `delete`, `relate`, `confirm_relation`, `reject_relation`), `with_id?`, `relation?`, `title?`, `content?`, `topics?`, `confirm?` | Una línea con el resultado |
| `session` | `action` (`last`, `list`, `end`), `topic?`, `client?`, `date?`, `summary?`, `limit?` | Sesión o sesiones en formato compacto |
| `status` | — | Salud de la base de datos y de Ollama, embeddings pendientes, número de registros, modelo |

Detalles de funcionamiento:
- **Instrucciones MCP**, entregadas en `initialize` (≤ 1.500 caracteres): qué guardar y qué no; guardar sin interrumpir y avisar en una línea; usar `recall` en cuanto aparezca un tema; volver a consultar tras una compactación; cerrar con `session end` y un resumen.
- **Sesiones:** se crean de forma perezosa en el primer `remember` de cada conexión MCP (stdio: el proceso; HTTP: la sesión MCP del SDK). El cliente se toma de `InitializeParams().ClientInfo.Name`. `last_activity_at` se actualiza en cada llamada. Si el tiempo desde la última actividad supera `session_idle_minutes`, la sesión se cierra y el siguiente `remember` abre otra.
- **HTTP:** `mcp.NewStreamableHTTPHandler` con la protección de localhost del SDK activada (no se desactiva nunca) y escucha solo en `127.0.0.1`.

## Algoritmo de guardado (`memory`)

1. Normalizar el contenido (minúsculas, espacios colapsados, trim) y calcular su sha256. Si ya hay un registro vigente con ese hash, se devuelve como duplicado sin insertar.
2. Resolver los temas (paquete `topic`):
   - el slug se normaliza a kebab-case, en minúsculas y sin tildes;
   - si existe, se usa;
   - si no, se compara el embedding del slug contra la caché de temas en memoria y, si la similitud es ≥ `topic_similarity`, se reutiliza el tema existente;
   - si no hay ninguno equivalente, se crea;
   - el embedding de los temas nuevos comparte un único plazo de `embed_timeout_ms` para toda la llamada; si vence, el tema se crea con embedding NULL y el worker (`topic.FillPending`) lo completa después.
3. Calcular los embeddings en lote con una sola llamada a `/api/embed` para todos los ítems, con un plazo máximo de `embed_timeout_ms` (contexto con plazo propio). Si Ollama no está disponible o el plazo vence, `embedding` queda NULL (pendiente) y la respuesta lo indica. Si vence el contexto del llamador, es un error, no un pendiente.
4. En una sola transacción:
   - insertar en `memories` y `memory_topics`;
   - si hay `key` y un registro vigente con la misma clave, pasar el antiguo a `superseded` y crear la relación `supersedes` `confirmed`;
   - actualizar `session_topics`.
5. Si hay embedding: buscar como mucho 3 registros vigentes con similitud ≥ `dup_similarity`. Se usa el índice binario y se reordena con el vector completo, excluyendo el propio registro. Por cada uno se crea una relación `related` `suggested` (`ON CONFLICT DO NOTHING`) y se devuelve en la respuesta. Los registros pendientes no pasan por este paso al guardar: lo hace el worker (paso 6).
6. **Worker de pendientes:** una goroutine por proceso recorre cada 30 s los registros con `embedding IS NULL` usando `FOR UPDATE SKIP LOCKED`, en lotes de 32. Es seguro con varios procesos a la vez. Tras confirmar cada lote, para cada registro que siga vigente ejecuta la misma detección de parecidos del paso 5 (función común con `Remember`) y crea las relaciones `related` `suggested`; los que ya no estén vigentes solo reciben el embedding. Este paso es best effort y no afecta al resultado del lote. La respuesta de `remember` avisa de que embedding y parecidos quedan pendientes y se calculan en segundo plano.

## Algoritmo de recuperación (`recall`)

1. Embedding de la consulta con prefijo de consulta. Si Ollama falla, se busca solo por texto y se indica en la respuesta.
2. **Tema:** si la IA pasa `topics`, se usan. Si no, se toma el tema cuya similitud entre su embedding y el de la consulta sea mayor, si pasa de `topic_similarity`. Los temas se comparan en memoria contra la caché.
3. **Candidatos semánticos:**

   ```sql
   SELECT id FROM memories WHERE status = 'active'
   ORDER BY binary_quantize(embedding)::bit(768) <~> binary_quantize($q)::bit(768) LIMIT 100
   ```

   Se ajusta `hnsw.ef_search` al límite. Después se reordenan esos 100 con `embedding <=> $q` y se quedan los 40 mejores.
4. **Candidatos de texto:** `tsv @@ websearch_to_tsquery('spanish', $query)` ordenados por `ts_rank_cd`, con límite 40.
5. **Fusión RRF**, con k = 60: puntuación = Σ 1/(k + rango). Los resultados del tema detectado reciben un bonus de +0,01 (ajustable).
6. **Ficha del tema:** hasta 8 datos estables vigentes (`fact`, `preference`, `decision`) más los 5 últimos `event` por `ts DESC`, sacados del índice parcial de `memory_topics`.
7. **Formato:** ficha, luego eventos, luego resultados sin repetir los ya mostrados. El fragmento es de unos 160 caracteres. Se corta al llegar a `recall_max_bytes`, con la nota "usa get #id para el detalle".
8. Con `include_history`, los candidatos de texto incluyen también los no vigentes, y los resultados muestran su cadena `superseded`.

## Benchmark (`benchmark`)

- **Datos sintéticos:**
  - vectores en 2.000 clústeres (centroides aleatorios más ruido gaussiano, normalizados) en `halfvec(768)`;
  - títulos y contenidos generados con vocabulario español para el índice de texto;
  - temas repartidos siguiendo una distribución de Zipf.
- **Carga:** `COPY` en binario, y el índice HNSW se construye después de cargar. Se mide el tiempo de construcción.
- **Medidas:**
  - p50/p95 de la consulta de candidatos más la reordenación (sin embedding), con 500 consultas;
  - p50/p95 del embedding real de una consulta contra Ollama (100 consultas);
  - recall@10 del índice binario más la reordenación frente a la búsqueda exacta, en una muestra de 100 consultas;
  - `pg_relation_size` de la tabla y de cada índice;
  - RSS de los procesos PostgreSQL.
- **Configuración:** escalas por `--rows`. Se ejecuta en una base de datos separada (`lodan_bench`) para no tocar los datos reales. La salida es markdown.

## Alternativas descartadas

| Alternativa | Por qué se descarta |
|---|---|
| SQLite + FTS5 (como Engram) | El usuario fijó PostgreSQL + pgvector; además, sin búsqueda vectorial nativa. |
| Índice HNSW sobre `halfvec` completo | A 10 M son ~15,4 GB solo de vectores; no cabe en 8 GB. |
| IVFFlat | Peor rendimiento de consulta según la documentación de pgvector, y necesita reentrenar al crecer. |
| Reducir dimensiones (Matryoshka) de entrada | Se deja como palanca si el benchmark lo pide: `embed_dims` es configurable. |
| Respuestas en JSON | Más tokens que texto compacto. |
| Resúmenes con LLM | Descartado en el PRD por coste. |
| Triggers para `memory_topics` | Lógica oculta; se prefiere hacerlo en código dentro de la transacción. |
| Una herramienta MCP por operación (tipo Engram, 23) | Coste fijo de tokens; se agrupa en 6. |
| `embedded-postgres` de Go | No trae pgvector. |

## Riesgos

| Riesgo | Mitigación |
|---|---|
| El índice HNSW binario a 10 M no cabe bien en RAM o el recall@10 es bajo | El benchmark (tarea temprana) decide los parámetros (`m`, `ef_search`, dimensiones). Si hace falta, se documenta el límite y decide el usuario (criterio de release del PRD). |
| Los umbrales de similitud no son adecuados para el modelo real | Son configurables; se calibran con datos reales y se documentan. |
| Los ejecutores no tienen shell en esta sesión | Escriben el código; el orquestador compila, pasa los tests y les devuelve los errores. |
| `websearch_to_tsquery('spanish')` no quita tildes (sin `unaccent`) | Se acepta en la v1. Si `unaccent` está disponible, una migración posterior puede añadir una configuración `lodan_es`. |
