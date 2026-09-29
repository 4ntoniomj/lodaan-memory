# CLAUDE.md — recall

## Resumen

**Problema**: que la IA tenga el contexto de un tema en una sola llamada, rápido y con pocos tokens.
**Objetivo**: búsqueda híbrida (semántica con índice binario + reordenación, texto completo en español, fusión RRF), ficha del tema y formato compacto con tope de bytes (criterios 7, 8 y 10).
**Alcance**: dentro: lectura y formato. Fuera: escritura de registros (va en `memory`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Convenciones específicas

Cualquier cambio en las consultas de búsqueda se valida con el benchmark (`internal/benchmark`) antes de darse por bueno.

## Pruebas

`go test ./internal/recall/...`
