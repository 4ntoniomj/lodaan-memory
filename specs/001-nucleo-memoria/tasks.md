# Tareas 001 — Núcleo de memoria

> Leyenda: `[P]` = paralelizable con las otras `[P]` de su fase. Los ejecutores escriben el código; el orquestador compila (`go build ./...`), pasa los tests (`go test ./...`) y devuelve los errores al ejecutor.

## Fase 0 — Preparación

- [x] 1. `go.mod` (módulo `lodan`, `go 1.25.0`, `toolchain go1.27.1`), dependencias del plan, `.gitignore` (binario `lodan`, `/dist`, `*.test`, `coverage.out`) y esqueleto de `cmd/lodan/main.go` con los subcomandos `serve`, `db`, `migrate`, `status` y `bench` (sin lógica todavía).
- [x] 2. [P] Paquete `internal/config`: rutas por SO, `config.json`, variables de entorno `LODAN_*` y valores por defecto del plan, con tests.
- [x] 3. [P] `AGENTS.md`, `CLAUDE.md` raíz (importa `@AGENTS.md`), `CLAUDE.md` de cada carpeta de funcionalidad y `CODEBASE.md` inicial.

## Fase 1 — Base

- [x] 4. Paquete `internal/database`:
  - clúster local (`initdb` con scram-sha-256, `postgresql.conf`, `pg_hba.conf`, start, stop, status);
  - lockfile;
  - pool pgx con registro de tipos pgvector;
  - migraciones embebidas;
  - `NewTestCluster(t)`;
  - tests.
- [x] 5. [P] Paquete `internal/embedding`:
  - interfaz `Embedder`;
  - cliente Ollama `/api/embed` en lote, con `keep_alive` y `dimensions`;
  - prefijos por modelo;
  - `FakeEmbedder` determinista para tests;
  - tests con `httptest`.
- [x] 6. Migración `0001` con el esquema del plan, y test de que migra en un clúster temporal.

## Fase 2 — Dominio

- [x] 7. [P] Paquete `internal/topic`: normalización de slugs, caché de temas y resolución por equivalencia, con tests (criterio 6).
- [x] 8. [P] Paquete `internal/session`: creación perezosa, actividad, cierre por inactividad, `end` con resumen, y `last`/`list` con filtros, con tests (criterios 14–16).
- [x] 9. Paquete `internal/memory`: `Remember` (lote, hash, clave, parecidos + relación sugerida, pendientes), `Get`, `Revise` (todas las acciones) y worker de pendientes, con tests (criterios 1–5 y 11–13).
- [x] 10. Paquete `internal/recall`: candidatos semánticos + texto, RRF, detección de tema, ficha, formato con tope de bytes e historial, con tests (criterios 7, 8 y 10).

## Fase 3 — Interfaz

- [x] 11. Paquete `internal/mcptools`:
  - las 6 herramientas con sus esquemas de entrada mínimos;
  - instrucciones (≤ 1.500 caracteres);
  - vinculación de sesión por conexión;
  - transportes stdio y HTTP en 127.0.0.1;
  - tests e2e con cliente MCP en memoria (criterios 17 y 18).
- [x] 12. Conectar la CLI:
  - `lodan serve [--http]`, con arranque perezoso del clúster y migración automática;
  - `lodan db init|start|stop|status`;
  - `lodan migrate`;
  - `lodan status`.
  - Test manual con un cliente real (Claude Code).

## Fase 4 — Rendimiento

- [x] 13. [P] Paquete `internal/benchmark` + `lodan bench --rows ...`: generador sintético, carga con COPY, construcción del índice, mediciones e informe markdown. Se puede empezar en cuanto esté la tarea 6.
- [x] 14. Ejecutar el benchmark a 10 mil, 100 mil y 1 millón:
  > Hecho el 2026-10-03: las cuatro escalas (10 mil, 100 mil, 1 millón y 10 millones) regeneradas en benchmark.md en un clúster aparte. A 10 millones no se cumple O1 en la máquina de referencia: ver verificacion.md.
  - escribir `specs/001-nucleo-memoria/benchmark.md`;
  - calibrar `dup_similarity` y `topic_similarity` con el modelo real;
  - ajustar los parámetros del índice si hace falta;
  - documentar los 10 M como hechos o pendientes.

## Fase 5 — Cierre

- [x] 15. Test de integración de paráfrasis con Ollama real (criterio 9, se salta si no hay Ollama) y comprobación de seguridad del clúster (criterio 19).
- [x] 16. Verificación de sdd contra la spec, auditoría de screaming-architecture, `detectar_stack.py --comparar CODEBASE.md`, `go vet` y commits Conventional Commits en `feat/nucleo-memoria`.
  > Verificación en verificacion.md (2026-09-29, actualizada el 2026-10-03 con el benchmark de 10 millones).
