# Verificación 001 — Núcleo de memoria

Fecha: 2026-09-29 · Rama: `feat/nucleo-memoria` · Máquina: i5-4590, 4 núcleos, 8 GB, sin GPU (WSL2)

## Resultado global

`go vet ./...` limpio, `gofmt -l .` vacío, `go test -race ./...` en verde (tests contra PostgreSQL 18 + pgvector 0.8.1 reales). Test de integración con Ollama real (`LODAN_OLLAMA_IT=1`) en verde. Prueba de humo de `lodan serve` por stdio con Ollama y PostgreSQL reales: correcta.

## Criterios de aceptación

| # | Estado | Evidencia |
|---|---|---|
| 1–5 | ✅ | `internal/memory` (guardado, duplicado, clave, parecidos, pendientes y plazo) |
| 6 | ✅ | `internal/topic` (embedder controlado) + calibración real: `gimnasio→entrenamiento` en la prueba de humo |
| 7, 8, 10 | ✅ | `internal/recall` (ficha, token exacto, historial) |
| 9 | ✅ | `TestParafrasisReal`: 4/4 paráfrasis en posición 1–2 o en la ficha |
| 11–13 | ✅ | `internal/memory` (`Get`, `Revise`) |
| 14–16 | ✅ | `internal/session` y e2e de `mcptools` (dos clientes, sesiones distintas) |
| 17 | ✅ | e2e: 6 herramientas; instrucciones de 1.171 caracteres |
| 18 | ✅ | e2e HTTP: `Host` ajeno → 403; `0.0.0.0` rechazado |
| 19 | ✅ | `internal/database`: 127.0.0.1, `scram-sha-256`, sin `trust`, secreto 0600 |
| 20 | ⚠️ Parcial | 10 mil, 100 mil y 1 millón medidos. A 1 millón, BD p50/p95 = 60,5 / 87,8 ms con recall@10 0,89 (400 candidatos). **10 millones pendiente.** Embedding real: p50 0,75 s, p95 2,7 s (coste fijo, PRD O1-b) |
| 21 | ✅ | `go test -race ./...` y `go vet ./...` |

## Huecos y observaciones

| Severidad | Hueco | Qué hacer |
|---|---|---|
| Media | Benchmark a 10 millones sin ejecutar (índice HNSW estimado ~3,8 GB en 8 GB de RAM) | Ejecutarlo antes del release (criterio de release del PRD) |
| Media | `benchmark.md` actual solo contiene la escala de 1 millón (ejecución `--reuse`); las escalas 10 mil y 100 mil están en el commit `80276d6` con las consultas anteriores | Regenerar las tres escalas junto con la de 10 millones |
| Baja | La recuperación incluye candidatos semánticos poco parecidos (ruido, más tokens) | Filtrar por similitud mínima tras calibrar consulta frente a documento |
| Baja | `tools/list` ocupa ~4,5 KB (~1.100 tokens fijos por sesión) | Recortar descripciones de parámetros si hace falta |
| Baja | `salud`–`medico` (0,68) no se fusiona con el umbral 0,72 | Límite conocido, documentado en `calibracion.md` |
| Info | Auditoría de screaming-architecture marca «sin tests» todas las funcionalidades | Falso positivo: tests junto al código (`*_test.go`), desviación documentada en `plan.md` |
| Info | Windows y macOS sin probar | Corresponde a la spec 002 (instalador) |
