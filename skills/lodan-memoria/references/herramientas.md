# Herramientas de lodan

Referencia de los pasos 2–5. Son 6 y no hay más. Parámetros verificados en el código del servidor (`internal/mcptools`, lodan v1). Todas las respuestas son texto plano compacto.

| Herramienta | Parámetros | Devuelve |
| :--- | :--- | :--- |
| `remember` | `items`: lista (1–20) de `{title, content, kind, topics?, key?, occurred_at?}`. `kind` ∈ `fact`, `preference`, `decision`, `event`, `note`. `occurred_at`: `AAAA-MM-DD` o RFC3339 | Una línea por registro: `#id guardado · temas: …`, más avisos (`sustituye #n`, `parecidos: #n «título» (0.93)`, duplicado, embedding pendiente) |
| `recall` | `query` (lenguaje natural), `topics?`, `limit?` (8 por defecto, máx. 20), `include_history?` | `Tema: …` (si se detecta), `Datos estables`, `Últimos eventos`, `Resultados`, con tope de tamaño |
| `get` | `ids`: hasta 20 | Contenido completo, temas, clave y relaciones de cada registro |
| `revise` | `id`, `action` ∈ `update`, `supersede`, `invalidate`, `delete`, `relate`, `confirm_relation`, `reject_relation`; `with_id?`, `relation?` (`related`, `contradicts`, `part_of`, `supersedes`), `title?`, `content?`, `topics?`, `confirm?` | Una línea con el resultado |
| `session` | `action` ∈ `last`, `list`, `end`; `topic?`, `client?`, `date?` (`AAAA-MM-DD`), `summary?` (obligatorio en `end`), `limit?` | Sesiones en una línea: fecha y hora, cliente, temas, estado y resumen |
| `status` | — | Estado de la base de datos y del servicio de embeddings, número de registros y de pendientes |

## Semántica que conviene saber

- `revise supersede`: `id` es el registro **antiguo** y `with_id` el **nuevo** que lo sustituye.
- Los registros sustituidos o invalidados no salen en `recall` salvo con `include_history: true`.
- La sesión se crea sola en el primer `remember` de la conexión; `session end` la cierra con el resumen. `session last` excluye la sesión actual.
- Si el servicio de embeddings no responde, `remember` guarda igual (embedding pendiente) y `recall` busca solo por texto; se indica en la respuesta.

## Ahorro de tokens

- Una llamada a `recall` por tema; no la repitas reformulando.
- `get` solo para registros concretos que necesites completos.
- Agrupa varios registros en un solo `remember`.
- No pidas `limit` alto salvo que el usuario quiera un listado.
