# CLAUDE.md — memory

## Resumen

**Problema**: guardar lo que tiene sustancia sin duplicados y sin perder la historia de cómo cambia.
**Objetivo**: `Remember`, `Get`, `Revise` y el worker de embeddings pendientes (criterios 1–5 y 11–13 de la spec 001).
**Alcance**: dentro: estados, hash, clave, relaciones pedidas al guardar (`Item.Relate`), borrado definitivo con confirmación. Fuera: búsqueda (va en `recall`) y temas (van en `topic`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Convenciones específicas

`memories` y `memory_topics` se actualizan siempre en la misma transacción, desde el código (sin triggers).

`CandidatesSQL` y `RerankSQL` (`nearest.go`) son la única fuente del SQL de la búsqueda semántica: `Nearest` y `lodan bench` usan esos mismos textos. Cualquier cambio se valida con `lodan bench`.

`Remember` espera el embedding como mucho `embed_timeout_ms` (el plazo se aplica a un contexto hijo; si vence el del llamador es un error, no un pendiente). Lo mismo para los temas nuevos, vía `topic.Resolver.ResolveWithin`. Si el plazo vence o Ollama no está, el registro queda `Pending` con embedding NULL. `FillPending` calcula el embedding en segundo plano. Ni `Remember` ni `FillPending` crean relaciones por parecido: las relaciones las decide la IA (con `Item.Relate` o con `Revise`), porque dos registros pueden estar relacionados sin parecerse. Las candidatas a revisar tras guardar las busca `recall.Service.Candidates`. `MaxBatch` (200) es el tope de seguridad común de todos los lotes.

## Pruebas

`go test ./internal/memory/...`
