# Herramientas de lodan

Referencia de los pasos 2–6. Son 6 y no hay más. Parámetros verificados en el código del servidor (`internal/mcptools`, lodan v2). Todas las respuestas son texto plano compacto. Todas las que reciben operaciones aceptan lotes con el mismo tope de seguridad: **200 por llamada**.

| Herramienta | Parámetros | Devuelve |
| :--- | :--- | :--- |
| `remember` | `items`: lista (1–200) de `{title, content, kind, topics?, key?, occurred_at?, relate?}`. `kind` ∈ `fact`, `preference`, `decision`, `event`, `note`. `occurred_at`: `AAAA-MM-DD` o RFC3339. `relate`: lista de `{id, relation?}` con `relation` ∈ `related` (por defecto), `contradicts`, `part_of` | Por registro, una línea `#id guardado · temas: …` con avisos (`sustituye #n`, duplicado, embedding pendiente, `relación con #n no creada: no existe`) y debajo hasta 6 líneas `  revisa: #id [tipo fecha] título — fragmento` |
| `recall` | `query` y/o `queries` (lista; en total 1–200), `topics?`, `limit?` (8 por defecto, máx. 20, por pregunta), `include_history?` | Con una pregunta: `Tema: …`, `Datos estables`, `Últimos eventos`, `Resultados`. Con varias: un bloque `## <pregunta>` por cada una, separados por una línea en blanco, y la pista `Usa get…` una sola vez al final. Una pregunta inválida muestra `error: …` en su bloque |
| `get` | `ids`: hasta 200 | Contenido completo, temas, clave y relaciones de cada registro |
| `revise` | Una operación en los campos de arriba y/o varias en `ops` (1–200 en total). Cada una: `id`, `action` ∈ `update`, `supersede`, `invalidate`, `delete`, `relate`, `confirm_relation`, `reject_relation`; `with_id?`, `relation?` (`related`, `contradicts`, `part_of`, `supersedes`), `title?`, `content?`, `topics?`, `confirm?` | Con una operación, una línea. Con varias, una por operación: `N. …` o `N. error: …`; se aplican en orden y una que falle no frena las demás |
| `session` | Igual que `revise`: una operación arriba y/o varias en `ops`. Cada una: `action` ∈ `last`, `list`, `end`; `topic?`, `client?`, `date?` (`AAAA-MM-DD`), `summary?` (obligatorio en `end`), `limit?` | Sesiones en una línea: fecha y hora, cliente, temas, estado y resumen. Con varias operaciones, numeradas como en `revise` |
| `status` | — | Estado de la base de datos y del servicio de embeddings, número de registros y de pendientes |

`recall`, `get` y `status` solo leen: el cliente puede lanzarlas en paralelo.

## Semántica que conviene saber

- Las líneas `revisa:` salen de tres fuentes: registros de los **mismos temas** (aunque no se parezcan en nada), registros que comparten **alguna palabra** y registros cercanos en **significado**. Nunca incluyen el propio registro, los guardados en la misma llamada, los que acaba de sustituir ni los que ya relaciona con `relate`.
- El servidor ya no crea relaciones por parecido: las relaciones las decide la IA (con `relate` al guardar o con `revise`).
- `revise supersede`: `id` es el registro **antiguo** y `with_id` el **nuevo** que lo sustituye.
- Los registros sustituidos o invalidados no salen en `recall` salvo con `include_history: true`.
- La sesión se crea sola en el primer `remember` de la conexión; `session end` la cierra con el resumen. `session last` excluye la sesión actual.
- Si el servicio de embeddings no responde, `remember` guarda igual (embedding pendiente) y `recall` busca solo por texto; se indica en la respuesta.

## Ahorro de tokens

- Una llamada a `recall` por turno, con todas las preguntas en `queries`; no la repitas reformulando.
- `get` solo para registros concretos que necesites completos, todos en una llamada.
- Agrupa los registros en un solo `remember` y las decisiones sobre `revisa:` en un solo `revise` con `ops`.
- No pidas `limit` alto salvo que el usuario quiera un listado.
