# Tareas 002 — Instalador

> Los ejecutores escriben el código; el orquestador compila, prueba y hace los commits.

## Fase 0 — Preparación

- [x] 1. Mover la skill a `skills/lodan-memoria/` del repo (copia de `~/.claude/skills/lodan-memoria`), embebida con `embed.FS`; cambiar el modelo por defecto a `embeddinggemma:300m-qat-q4_0`; corregir `NewTestCluster` para que respete `LODAN_PG_BIN_DIR`.
- [x] 2. `lodan service run`: supervisor en primer plano (postgres en primer plano, reenvío de señales, mantenimiento) y `lodan service start|stop|restart|status|enable|disable`.

## Fase 1 — Dependencias

- [x] 3. [P] `micromamba.go`: descarga + sha256, `create` del entorno (PG 16 en Windows) y localización de los binarios por SO, con tests.
- [x] 4. [P] `ollama.go`: detección, descarga y extracción del archivo oficial por SO (zstd, tgz y zip), arranque de usuario y `pull` con progreso, con tests (`httptest`).
- [x] 5. [P] `service_*.go`: unidad systemd de sistema, LaunchDaemon y servicio SCM de Windows (instalar, quitar, controlar), con tests que generan los archivos en un directorio temporal; elevación (`sudo` / UAC) solo en ese paso.

## Fase 2 — Integración con clientes

- [x] 6. [P] `clients*.go`: registro de clientes, detección y editores JSON, TOML y YAML idempotentes con copia de seguridad; uso de CLI oficial cuando exista; tests con fixtures de cada formato.
- [x] 7. [P] `skill.go`: instalar y retirar la skill, bloques marcados en las instrucciones globales y retirada de `lodan-memory`, con tests.

## Fase 3 — Orquestación

- [x] 8. `install.go`, `doctor.go`, `uninstall` y subcomandos de la CLI (`--yes`, `--dry-run`, `--skip-*`, `--only`, `--purge`).
- [x] 9. Prueba de extremo a extremo en Linux con un `LODAN_DATA_DIR` y un HOME temporales (sin tocar la instalación real), y luego instalación real en la máquina del usuario.
  > Hecha el 2026-09-29: instalación real en Linux (WSL2) como servicio systemd.
- [x] 10. Compilación cruzada para las 5 plataformas; prueba en el host Windows si el usuario lo autoriza.
  > Compilación cruzada correcta para windows/amd64, linux/amd64, linux/arm64, darwin/amd64 y darwin/arm64 (2026-10-03). Prueba en el Windows 11 real del usuario el 2026-10-03 con `--skip-service`; el registro del servicio (UAC) queda pendiente: ver verificacion.md.
- [x] 11. Verificación de sdd, auditoría, `CODEBASE.md` y commits.
  > Ver verificacion.md.
