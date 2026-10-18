# CLAUDE.md — hook

## Resumen

**Problema**: recuperar el contexto tras una compactación y guardar lo útil de los subagentes no puede depender de que la IA se acuerde.
**Objetivo**: `lodan hook compact` y `lodan hook subagent-stop`, que ejecutan los clientes de IA en su ciclo de vida (especificación: `odd/tasks/sincronizacion-git-y-hooks.md`, S10-S12).
**Alcance**: dentro: leer el evento, instrucciones de recuperación, resumen de la compactación y bloque `## Key Learnings`. Fuera: registrar los hooks en cada cliente (`internal/install`).

## Reglas

- Un hook nunca bloquea al cliente: la CLI siempre sale con 0, no arranca PostgreSQL y tiene un tope de tiempo.
- No se guardan secretos ni DNI/NIE (`secretRes` en `hook.go`).

## Pruebas

`go test ./internal/hook/`
