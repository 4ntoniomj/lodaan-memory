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
| 20 | ⚠️ Medido, O1 no se cumple a 10 M | Cuatro escalas en \`benchmark.md\` (2026-10-03). A 1 millón: BD p50/p95 = 51,2 / 66,9 ms, recall@10 0,877 (400 candidatos). **A 10 millones: BD p95 = 443 ms (200 candidatos) a 1.017 ms (100), recall@10 0,15–0,41**, por encima del objetivo de 300 ms; tabla e índices ocupan 32,5 GB con 8 GB de RAM. Índice HNSW: 10,3 h. Embedding real: p50 102 ms, p95 219 ms en el benchmark (modelo ya cargado); p50 0,75 s, p95 2,7 s en el uso real del 2026-09-29 (coste fijo, PRD O1-b) |
| 21 | ✅ | `go test -race ./...` y `go vet ./...` |

## Benchmark a 10 millones (2026-10-03)

Ejecutado en un clúster aparte (no el del usuario), con 10 mil, 100 mil, 1 millón y 10 millones de filas sintéticas y 100, 200 y 400 candidatos. Carga de 10 M: 40 min; índice HNSW: 10,3 h (`maintenance_work_mem` = 1 GB, el índice de 3,8 GB no cabe); búsqueda exacta para el recall: ~88 s por consulta. Las latencias a 10 M no son monótonas con los candidatos (100 es más lento que 200) porque la primera configuración se mide con la caché fría tras la búsqueda exacta: todo depende del disco. Los datos son sintéticos; con embeddings reales el recall puede diferir.

## Huecos y observaciones

| Severidad | Hueco | Qué hacer |
|---|---|---|
| Alta | A 10 millones no se cumple O1 (p95 de la BD 443–1.017 ms frente a < 300 ms) y el recall@10 cae a 0,15–0,41: con 32,5 GB de datos e índices en 8 GB de RAM todo va a disco, y la cuantización binaria de 768 dimensiones pierde precisión | Según el criterio de release del PRD, la versión no se publica hasta que el usuario decida: aceptar el límite documentado (p. ej., ~1 millón de registros en 8 GB) o cambiar el diseño |
| Baja | La recuperación incluye candidatos semánticos poco parecidos (ruido, más tokens) | Filtrar por similitud mínima tras calibrar consulta frente a documento |
| Baja | `tools/list` ocupa ~4,5 KB (~1.100 tokens fijos por sesión) | Recortar descripciones de parámetros si hace falta |
| Baja | `salud`–`medico` (0,68) no se fusiona con el umbral 0,72 | Límite conocido, documentado en `calibracion.md` |
| Info | Auditoría de screaming-architecture marca «sin tests» todas las funcionalidades | Falso positivo: tests junto al código (`*_test.go`), desviación documentada en `plan.md` |
| Info | Windows y macOS sin probar | Corresponde a la spec 002 (instalador) |
