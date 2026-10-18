# CLAUDE.md — mcptools

## Resumen

**Problema**: cualquier IA debe poder usar la memoria hablando en lenguaje natural y gastando pocos tokens.
**Objetivo**: las 6 herramientas MCP (`remember`, `recall`, `get`, `revise`, `session`, `status`), las instrucciones del servidor y los transportes stdio y HTTP en 127.0.0.1 (criterios 17 y 18).
**Alcance**: dentro: adaptación MCP y vinculación de sesión por conexión. Fuera: lógica de dominio (vive en `memory`, `recall`, `session` y `topic`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |
| `crear-skills` | Si cambian las herramientas: la skill de uso (spec 003) debe actualizarse. |

## Convenciones específicas

Nunca más de 6 herramientas. Descripciones breves. La protección de localhost del SDK nunca se desactiva.
Los lotes son iguales en todas las herramientas: `queries` en `recall`, `items` en `remember`, `ids` en `get` y `ops` en `revise` y `session`, con el tope común `memory.MaxBatch` (200). Con una sola consulta u operación la salida es idéntica a la de antes; con varias, `recall` responde un bloque `## <pregunta>` por consulta (presupuesto por bloque: `blockBytes`) y `revise`/`session` numeran las líneas (`N. ` / `N. error: `). `recall`, `get` y `status` llevan `readOnlyHint`. Tras `remember`, `reviewCandidates` añade las líneas `  revisa: ` con `recall.Service.Candidates`.

## Pruebas

`go test ./internal/mcptools/...` (incluye e2e con cliente MCP en memoria)
