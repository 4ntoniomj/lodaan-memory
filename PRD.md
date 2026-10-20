# PRD — lodan

Última actualización: 2026-10-20 (inicio de `feat/sin-ollama`).

## 1. Producto

**lodan** es un servicio local de memoria persistente para IAs compatibles con MCP. Binario Go único + PostgreSQL. Diseñado para correr en localhost con un único usuario (la IA) y un único dueño (Antonio).

**Cambio de rumbo (2026-10-20):** lodan deja de requerir Ollama por defecto. Embeddings se vuelven opcionales detrás de `--with-embeddings`. La búsqueda primaria pasa a ser **full-text + tags concretas obligatorias + trabajo semántico de la IA**. Inspirado en (Engram): binario autosuficiente, taxonomía-first, IA como motor relacional.

## 2. Capacidades (estado actual + propuesto `sin-ollama`)

### 2.1 Almacenar hechos estructurados
- Record con `title`, `content`, `kind` (`fact`/`preference`/`decision`/`event`/`note`), `occurred_at`, status (`active`/`superseded`/`invalidated`).
- **NUEVO (sin-ollama):** 6 campos estructurados opcionales para datos que los tengan: `qué`, `cómo`, `cuándo`, `dónde`, `por qué`, `nota`.
- Tags: mezcla de **generales** (categoría amplia) y **concretas** (identificador de objeto). **NUEVO:** tags concretas obligatorias en cada record, de las familias `proyecto-`, `persona-`, `contacto-`, `proveedor-`, `cliente-`, `finca-`, `vehiculo-`, `coche-`, `skill-`, `herramienta-`, `recurso-`, `evento-`, `apuntes-`.
- Key opcional (`<tag>/<atributo>`) para datos que evolucionan. Mismo `key` en el mismo topic sustituye (`supersede`).

### 2.2 Buscar con tres modos
- **rank** (default): combina full-text + topic + (opcional) embeddings semánticos. Ordena por relevancia.
- **list** (NUEVO): ordena por fecha desc, límite más alto. Para "muéstrame todo lo de Y en fecha X".
- Búsqueda híbrida (text + tags + opcional embeddings). Embeddings off → sigue funcionando vía full-text + tags.

### 2.3 Filtrar con tags y fechas (NUEVO)
`recall` acepta filtros de primera clase:
- `tags_all[]`: AND. El record debe tener todas estas tags.
- `tags_any[]`: OR. Al menos una.
- `date_from`, `date_to`: rango de fechas (RFC3339 o AAAA-MM-DD).
- `mode`: `rank` o `list`.
- Combinables: `(tags_all AND tags_any) AND date range`.

### 2.4 Multi-interacción
- `recall` con `queries[]` ejecuta varias preguntas en una sola llamada, devuelve bloques independientes. La IA correlaciona los resultados en su respuesta.
- `revise` con `ops[]` ejecuta varias operaciones en lote, independientes.

### 2.5 Relaciones y contradicciones
- La IA declara relaciones: `related`, `part_of`, `contradicts`, `supersedes`.
- Servidor detecta duplicados exactos (hash normalizado) y sugiere candidatos en `revisa:`.
- **NUEVO:** auto-relación al guardar: si el record comparte tags concretas raras (`n < 5`), el server crea `related` automáticamente con threshold 0.85.

### 2.6 Aliases
- La IA confirma aliases con `revise alias`.
- Servidor detecta candidatos (4 ya identificados: `wg-active-2026-10-19 ≈ wg-active-2026-10-18`, `pendiente ≈ pendientes`, `privacidad ≈ seguridad`, `correo ≈ correos`) y los sugiere en `status`.
- **NUEVO:** la IA es proactiva: al guardar, busca si las palabras del contenido son aliases naturales de tags existentes.

### 2.7 Resolver el mapa de tags (NUEVO)
- `tags_index(scope?)` devuelve el árbol de tags con jerarquía y conteos. La IA lo carga al inicio de sesión (cached en conversación) y antes de crear tags nuevas.

### 2.8 Sesiones
- `session end` con `summary` cierra la sesión y guarda resumen para la próxima.

### 2.9 Backup + sync entre máquinas
- Backup completo + diferencial + incremental (zstd). Ver spec 003.
- Sync por git cifrado con age. Ver spec 002.

## 3. Modelo de datos

```
memories (id, title, content, kind, status, occurred_at,
          tsv GIN-indexed, embedding NULL default,
          que?, como?, cuando?, donde?, por_que?, nota?)
memory_topics (memory_id, topic_id, active, kind, ts)
topics (id, slug UNIQUE)
memory_keys (topic_id, key, UNIQUE por topic activo)
memory_relations (source, target, relation, state, confidence, verdict)
memory_verdicts (dedicada a veredictos pendientes)
sync_* (estado de sync entre máquinas)
```

Constraints:
- Tags concretas: regex `^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$`.
- ≥1 tag concreta por record (validación en servicio).
- Key único por topic activo.

## 4. API MCP (6 tools)

| Tool | Función |
|---|---|
| `remember` | Guarda N items. Devuelve IDs y `revisa:` con candidatos. |
| `recall` | Búsqueda híbrida. Acepta filtros `tags_all`, `tags_any`, `date_from`, `date_to`, `mode`. |
| `get` | Recupera N records por ID. |
| `revise` | Operaciones de relación/contradicción/actualización/etc. en lote. |
| `session` | Gestión de sesiones (start, end, summary). |
| `status` | Estado del sistema: BD, embeddings, veredictos, aliases sugeridos. |

**NUEVO:** `tags_index` (séptima tool, en `sin-ollama`).

## 5. Operación

- Binario único `lodan` con subcomandos: `serve` (MCP stdio), `db`, `migrate`, `status`, `bench`, `sync`, `install`, `service`, `hook`.
- Servicio Windows / systemd para auto-arranque.
- Backup automático diario.
- Sin Ollama por defecto (sin-ollama). Si se quiere, `--with-embeddings`.

## 6. Calidad (métricas objetivo)

| Métrica | Target |
|---|---|
| % records con ≥1 tag concreta | >80% |
| % tags concretas reutilizadas (>1 uso) | >60% |
| % records huérfanos (sin relations) | <20% |
| Search success rate (queries que encuentran lo que existe) | >95% |
| `tags_index`/`expand_relations` usage | >40% |
| Duplicate rate (records no guardados por duplicar) | 10-20% |

Aceptación: 30 días consecutivos con métricas cumplidas.

## 7. Lo que NO hace

- No escucha fuera de `127.0.0.1`.
- No se sincroniza en la nube (sync solo entre máquinas del usuario, cifrado).
- No requiere autenticación de usuario (servicio local, single-user).
- No tiene UI web (es solo API MCP).
- No es multi-tenant.

## 8. Roadmap

- **v1.x (actual)**: dependencias (queues, multi-interaction).
- **v1.y (feat/sin-ollama, en desarrollo)**: Ollama opcional, taxonomía explícita, filtros de primera clase, `tags_index`, IA como motor semántico.
- **v2 (futuro)**: cloud sync opcional (Engram-style), si hay demanda real.