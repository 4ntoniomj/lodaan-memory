# CLAUDE.md — gitsync

## Resumen

**Problema**: llevar la memoria de una máquina a las demás sin perder ni duplicar nada y sin que en el repositorio se lea nada.
**Objetivo**: `lodan sync init|push|pull|run|schedule|unschedule` (la tarea periódica es del usuario, no del servicio): ficheros por lotes cifrados con age en `changes/<máquina>/` de un repositorio git (especificación: `odd/tasks/sincronizacion-git-y-hooks.md`, S1-S9).
**Alcance**: dentro: exportar, importar, reglas de fusión, git y la clave. Fuera: el esquema (migración `0003_sync.sql` en `database`) y la CLI (`cmd/lodan/sync.go`).

## Piezas

- `export.go`: exporta cada cambio local exactamente una vez. Una fila cambió si su `sync_xid` (transacción del último cambio local) es visible en la instantánea actual y no en la de la exportación anterior (`sync_state.export_snapshot`).
- `import.go` y `merge.go`: aplican los ficheros de las otras máquinas, del más antiguo al más nuevo por nombre, cada uno en una transacción con `SET LOCAL lodan.sync_import = 'on'` para que los triggers no los marquen como cambios locales.
- Ediciones simultáneas: `sync_rev` (versión), `sync_base` (última versión intercambiada), `sync_dirty` (edición local sin enviar) y `sync_revs` (versiones que ha tenido cada registro aquí).
- `git.go`: usa el `git` del sistema con la configuración del usuario; nunca pide contraseña (`GIT_TERMINAL_PROMPT=0`).

## Pruebas

`go test ./internal/gitsync/` (necesita PostgreSQL con pgvector y git; cada test arranca dos o tres clústeres y la suite tarda unos 12 minutos).
