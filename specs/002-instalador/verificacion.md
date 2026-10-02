# Verificación 002 — Instalador

Fecha: 2026-10-03 · Rama: `feat/instalador` · Máquina: i5-4590, 4 núcleos, 8 GB, sin GPU; Windows 11 con WSL2

## Resultado global

`gofmt -l .` vacío. `go vet ./...` limpio con `GOOS` linux, windows y darwin. `go test ./...` en verde. Compilación cruzada correcta para las 5 plataformas. Linux: instalación real como servicio systemd desde el 2026-09-29 (`lodan status`: `BD: ok`). Windows 11: prueba real el 2026-10-03, lanzada desde WSL sin elevar (`install --yes --skip-service`, `doctor`, MCP, supervisor, backup y restore, `uninstall --purge`).

## Criterios de aceptación

| # | Estado | Evidencia |
|---|---|---|
| 1 | ✅ | Linux y Windows: micromamba con SHA-256, PostgreSQL + pgvector (Windows: PostgreSQL 16.15 y pgvector 0.8.6), clúster, migraciones y `BD: ok` / `doctor` sin errores |
| 2 | ✅ | Windows: con un Ollama en 127.0.0.1:11434 se reutiliza y no se descarga otro (simulación y doctor); Linux: Ollama del sistema reutilizado |
| 3 | ⚠️ Sin prueba real | La descarga de Ollama solo ocurre si se registran servicios, y no se ha hecho en ninguna máquina real; cubierto por tests con `httptest` (zstd, tgz y zip) |
| 4 | ✅ | Windows: Gemini CLI detectado, entrada añadida con copia `.bak-lodan` y retirada al desinstalar sin tocar el resto; idempotencia en tests |
| 5 | ✅ | Windows y Linux: skill en `~/.claude/skills`, bloque en las instrucciones globales; retirada de `lodan-memory` en tests |
| 6 | ⚠️ Parcial | Linux: servicio systemd real gestionado con `systemctl` y `lodan service`. Windows: el registro en el SCM necesita aceptar el UAC y no se ha probado; `lodan service status` sin elevar sí funciona. macOS: solo en unidad |
| 7 | ✅ | Windows: `doctor` con 11–12 comprobaciones y solución propuesta en cada aviso |
| 8 | ✅ | Windows: `uninstall --purge` quita la entrada del cliente, los bloques, la skill y las carpetas vacías que creó lodan, para PostgreSQL y borra los datos tras escribir «borrar» |
| 9 | ✅ | Sin elevar en Windows se instala, se consulta el servicio y se ejecuta PostgreSQL; los datos van a `%LOCALAPPDATA%\lodan`. Todo escucha en 127.0.0.1 |
| 10 | ✅ | `go test ./...`; macOS compilado y probado solo en unidad, como prevé el criterio |

## Fallos encontrados en la prueba real de Windows (corregidos)

| Commit | Fallo |
|---|---|
| `acfdad9` | La suma de micromamba se pedía como `micromamba-win-64.exe.sha256`, que no existe (404) |
| `e122594` | `pg_ctl start` acababa con `exec.ErrWaitDelay` porque el postmaster hereda la tubería de salida |
| `0c097e5` | `doctor` y `lodan service status` exigían administrador para consultar el SCM |
| `0b4c51c` | El instalador creaba `~/.claude` al instalar la skill y `doctor` daba por instalado Claude Code |

## Huecos

| Severidad | Hueco | Acción |
|---|---|---|
| Media | Windows: registro del servicio `lodan` (y `lodan-ollama`) en el SCM, con UAC, sin probar; cuenta del servicio sin decidir | Repetir `lodan install --yes` en Windows con el usuario delante para aceptar el UAC |
| Media | Instalación de Ollama por lodan sin prueba real en ninguna plataforma | Se ejercitará en la misma prueba de Windows |
| Baja | En Windows cada `pg_ctl start` tarda 5 s más (vence `WaitDelay` antes de liberar la tubería) | Redirigir la salida de `pg_ctl start` a un archivo en lugar de una tubería |
| Info | macOS solo compilado y probado en unidad | Previsto por el criterio 10 |
