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

## Pruebas

`go test ./internal/mcptools/...` (incluye e2e con cliente MCP en memoria)
