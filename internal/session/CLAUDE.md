# CLAUDE.md — session

## Resumen

**Problema**: poder recuperar "la última conversación" (o la última sobre un tema, de un cliente o de una fecha) sin colisiones entre IAs abiertas a la vez.
**Objetivo**: sesiones perezosas con ULID, cierre por inactividad, resumen al cerrar, `last`/`list` con filtros (criterios 14–16).
**Alcance**: dentro: ciclo de vida y consulta de sesiones. Fuera: el transporte MCP (va en `mcptools`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Pruebas

`go test ./internal/session/...`
