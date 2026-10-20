# Runbook: ejecutar lodan-sin-ollama

**Propósito:** instrucciones operativas para tomar la rama `feat/sin-ollama` desde "documentos escritos" hasta "feature cerrado y mergeado a dev". Aplicable por el usuario o un agente IA que ejecute el feature.

**Documentos relacionados:**
- Feature doc: `odd/tasks/lodan-sin-ollama.md` (diseño, specs, tasks, log).
- PRD: `PRD.md`. AGENTS: `AGENTS.md`. CODEBASE: `CODEBASE.md`.

---

## 1. Qué es lodan-sin-ollama (resumen de contexto)

lodan sin dependencia obligatoria de Ollama. PostgreSQL sigue siendo obligatorio (con tsvector para full-text en español). Lo que cambia:

- **Ollama** pasa de obligatorio a opcional (flag `--with-embeddings`).
- **Búsqueda primaria:** full-text (tsvector + `websearch_to_tsquery`) + tags concretas + IA-managed relations. Embeddings son bonus, no requisito.
- **Taxonomía:** tags concretas obligatorias en cada record, de las 13 familias (`proyecto-`, `persona-`, `contacto-`, `proveedor-`, `cliente-`, `finca-`, `vehiculo-`, `coche-`, `skill-`, `herramienta-`, `recurso-`, `evento-`, `apuntes-`).
- **6 campos estructurados** opcionales (`qué`, `cómo`, `cuándo`, `dónde`, `por qué`, `nota`).
- **Filtros de primera clase** en `recall`: `tags_all`, `tags_any`, `date_from`, `date_to`, `mode`.
- **Tool nueva** `tags_index()`: mapa de tags al inicio de sesión.
- **IA correlaciona** resultados de multi-query; el server no.

---

## 2. Ramas y repositorios (reglas duras)

| Repo | Rama | Qué contiene | Notas |
|---|---|---|---|
| `loreanse/lodan-memory` | `feat/sin-ollama` (actual) | Cambios de **código** del feature | Ya creada, en `dev`. NO pushear a `main` directamente. |
| `loreanse/lodan-memory` | `feat/sin-ollama-migration` (opcional) | Scripts de migración de datos | Solo si los scripts no entran en el código de T2. |
| `loreanse/lodan.sync` | `feat/sin-ollama` | **Datos migrados** (sync entre máquinas) | Rama SEPARADA del código. Aquí van los exports de gitsync. |

**Reglas:**
- **NUNCA** mezclar cambios de código y datos en la misma rama.
- **NUNCA** pushear directo a `main`. Solo PR desde `dev`.
- **SIEMPRE** crear la rama de sync (`loreanse/lodan.sync:feat/sin-ollama`) desde `main` de ese repo, no desde otra rama.

---

## 3. Pre-requisitos absolutos (verificar antes de empezar)

- [ ] `feat/sin-ollama` activa en `loreanse/lodan-memory` (rama creada en `dev`).
- [ ] Documentos raíz commiteados: `PRD.md`, `AGENTS.md`, `CODEBASE.md`.
- [ ] Feature doc `odd/tasks/lodan-sin-ollama.md` con Phase 0 cerrado (D1-D5 docs escritos, revisados y aprobados por el usuario).
- [ ] **Backup completo** del estado actual: `lodan db backup --full` → guardar en `~/backups/lodan-pre-sin-ollama-<YYYYMMDD>/`. Verificar checksum. Sin backup verificado, NO avanzar.
- [ ] Data dir de prueba creado: `C:\Users\4ntoniomj\AppData\Local\lodan-sin-ollama-data\` o equivalente. Inicializado con `lodan install` apuntando a ese path.
- [ ] Binario actual corriendo y funcional. `lodan status` reporta OK.
- [ ] **Acceso a `loreanse/lodan.sync`** verificado.

---

## 4. Fase A: TDD (RED primero)

Orden de tests, capa por capa. Tests escritos ANTES de implementación. Cada test falla primero (RED), se implementa lo mínimo para verde (GREEN), luego refactor.

### A.1 — `tags_index` (S15)
- [ ] Test `TestTagsIndex_ReturnsTree`: con BD seed, devuelve árbol con jerarquía y conteos.
- [ ] Test `TestTagsIndex_ScopeFilter`: filtrar por `#trabajo` o `#personal`.
- [ ] Test `TestTagsIndex_EmptyDB`: BD vacía devuelve árbol vacío, sin panic.
- [ ] **Implementar** `tags_index()` en `internal/mcptools/`. Solo lo mínimo para verde.

### A.2 — Filtros en `recall` (S12)
- [ ] Test `TestRecall_TagsAll`: con 3 tags concretas, solo records que tengan las 3.
- [ ] Test `TestRecall_TagsAny`: con 2 tags, records con al menos una.
- [ ] Test `TestRecall_DateRange`: con `date_from`/`date_to`, solo records en rango.
- [ ] Test `TestRecall_TagsAllAndAny`: AND entre grupos + OR dentro de `tags_any`.
- [ ] Test `TestRecall_DateAndTags`: fecha + tags combinados.
- [ ] Test `TestRecall_EmptyQueryWithFilters`: query vacía + filtros válidos = OK; query vacía sin filtros = error.
- [ ] Test `TestRecall_InvalidTagsIgnored`: tag inexistente se ignora; todas inválidas en `tags_all` = error.
- [ ] Test `TestRecall_ModeList`: orden por fecha desc, formato con fecha prominent.
- [ ] Test `TestRecall_BackwardCompat`: llamadas sin los nuevos parámetros no rompen comportamiento previo.
- [ ] **Implementar** cambios en `internal/recall/recall.go`, `internal/recall/sql.go`, `internal/mcptools/read.go`.

### A.3 — Modo sin Ollama (S4, S5, S6)
- [ ] Test `TestServe_StartsWithoutOllama`: binario arranca con Ollama caído.
- [ ] Test `TestRecall_FullTextOnly`: con embedder mockeado como `ErrUnavailable`, `recall` retorna resultados vía full-text + tags.
- [ ] Test `TestRemember_WithoutOllama`: `remember` no falla sin Ollama; items se guardan con `embedding=NULL`.
- [ ] Test `TestStatus_ReportsUnavailableAsNormal`: "Ollama: no disponible" sin error.
- [ ] Test `TestMaintenanceLoop_SkipsFillPending`: con `--with-embeddings=false`, `RunOnce` no llama `FillPending`.
- [ ] **Implementar** cambios en `internal/embedding/`, `internal/mcptools/maintenance.go`, `cmd/lodan/serve.go`.

### A.4 — Verificación después de TDD
- [ ] `go test ./...` pasa 100%.
- [ ] Cobertura de `internal/recall`, `internal/mcptools`, `internal/embedding` ≥80%.
- [ ] Tests originales (sin-ollama) siguen pasando (no regresiones).
- [ ] `go vet ./...` y `gofmt -l .` limpios.

---

## 5. Fase B: Migración de datos (rama separada si scripts son grandes)

### B.1 — Pre-migración
- [ ] Backup fresco verificado (re-restaurar en una BD de prueba para confirmar integridad).
- [ ] Script de migración escrito pero NO ejecutado todavía.
- [ ] Script idempotente: si corre 2 veces, no rompe (verificar con 2 ejecuciones de prueba).
- [ ] Script incluye:
  - Re-tagging de records existentes con tags concretas de las 13 familias.
  - Confirmación de los 10 aliases conocidos (`wg-active-2026-10-19 ≈ wg-active-2026-10-18`, etc.).
  - Jerarquización de datos evolutivos (keys con `coche-volvo-s60/matricula`, etc.).
  - Validación post-migración: ≥80% de records con tag concreta, <20% huérfanos.

### B.2 — Migración en data dir de prueba
- [ ] Apuntar `lodan serve --data-dir=lodan-sin-ollama-data/`.
- [ ] Correr script de migración sobre ese data dir.
- [ ] Verificar conteo: 938 records siguen existiendo.
- [ ] Verificar muestreo: 10 records al azar tienen tags concretas.
- [ ] Verificar aliases: 10 pares confirmados.
- [ ] Verificar relaciones: <20% huérfanos.

### B.3 — Migración en data dir activo
- [ ] Backup fresco (no del paso B.1, uno nuevo).
- [ ] Apuntar `lodan serve` al data dir activo (el de siempre).
- [ ] Detener el servicio temporalmente.
- [ ] Correr script de migración.
- [ ] Re-arrancar servicio.
- [ ] Verificar conteo y muestreo igual que en B.2.

---

## 6. Fase C: Sync a repo privado (rama separada)

### C.1 — Setup del repo de sync
- [ ] Repo `loreanse/lodan.sync` clonado/actualizado.
- [ ] Rama `feat/sin-ollama` creada desde `main` de ese repo (no desde otra rama).
- [ ] No se pushea nada todavía.

### C.2 — Export de datos migrados
- [ ] `lodan sync` exporta los datos migrados al directorio local del sync repo.
- [ ] Verificar que los exports van a la rama `feat/sin-ollama` (no contaminan `main`).
- [ ] Push a `loreanse/lodan.sync:feat/sin-ollama` con mensaje `data(sin-ollama): exportación inicial de registros migrados`.

---

## 7. Fase D: Implementación final en feat/sin-ollama

### D.1 — Skills nuevas (en `~/.config/opencode/skills/`)
- [ ] `lodan-taxonomy` instalada: referencia rápida de las 13 familias + reglas + ejemplos válidos.
- [ ] `lodan-quality` instalada: pre-save checks obligatorios (dedup, contradicciones, aliases).
- [ ] `lodan-alias` instalada: gestión proactiva de aliases.

### D.2 — Skill `lodan-memoria` actualizada
- [ ] Énfasis en tags concretas obligatorias (las 13 familias).
- [ ] 6 campos estructurados documentados.
- [ ] Pre-save obligatorio (`recall` antes de `remember`).
- [ ] Auto-relación al guardar (rare tags, threshold 0.85).
- [ ] Revisión rigurosa de `revisa:` candidatos.
- [ ] Uso de `queries[]` para multi-scope (no scope de sesión).
- [ ] `tags_index()` al inicio de cada sesión.
- [ ] Actualización de `references/herramientas.md` con `tags_all`/`tags_any`/`date_from`/`date_to`/`mode`/`expand_relations`.
- [ ] `EXAMPLE.md` con 3 ejemplos de uso con filtros.

### D.3 — Documentación
- [ ] ADR nuevo en `docs/decisions/` explicando por qué embeddings pasaron de obligatorios a opcionales.
- [ ] `docs/ARCHITECTURE.md` (si existe) actualizado al modelo "tags-first, embeddings-optional".

---

## 8. Fase E: Pruebas manuales

### E.1 — Funcionales del programa
- [ ] Instalar binario nuevo en el sistema.
- [ ] Arrancar `lodan serve` (sin Ollama). `lodan status` correcto.
- [ ] Probar `recall` con cada filtro nuevo por separado:
  - [ ] `tags_all=["#trabajo", "#proyecto-erp-agm"]`
  - [ ] `tags_any=["#finca-royalveg", "#finca-rebelao"]`
  - [ ] `date_from="2026-10-01", date_to="2026-10-20"`
  - [ ] `mode="list"` con orden por fecha
- [ ] Probar `tags_index()`: devuelve árbol con jerarquía y conteos.
- [ ] Guardar un record de prueba con tag concreta de cada tipo. Verificar rechazo de tags mal formadas.
- [ ] Buscar el record recién guardado por tag concreta. Verificar match.
- [ ] Probar multi-scope: `queries=["actividad trabajo", "actividad personal"]`. Verificar 2 bloques.
- [ ] Probar aliases: `alias=` con un par conocido, candidato aparece. Confirmar.
- [ ] Probar `revisa:` al guardar: hash duplicado detectado, candidato sugerido.

### E.2 — Aceptación
- [ ] 24h de uso real sin Ollama (programa en uso, no de prueba).
- [ ] Métricas de D4 (calidad) verificadas donde sea posible medir en el momento:
  - [ ] % records con ≥1 tag concreta ≥80%
  - [ ] search success rate en queries reales ≥95% (medición subjetiva)
- [ ] Smoke test completo (programa + IAs usándolo normalmente).

---

## 9. Fase F: Verificación de objetivos (cierre)

**Todos los objetivos son NO OPCIONALES. Si alguno falla, el feature NO está terminado.**

- [ ] **O1:** Tags concretas obligatorias en ≥80% de los records (medido post-migración).
- [ ] **O2:** `recall` con filtros de primera clase funcional: `tags_all`, `tags_any`, `date_from`, `date_to`, `mode`.
- [ ] **O3:** `tags_index()` funcional y devuelve árbol con jerarquía y conteos.
- [ ] **O4:** `serve` arranca sin Ollama (probado y verificado).
- [ ] **O5:** `FillPending` desactivado por default; se activa con `--with-embeddings`.
- [ ] **O6:** Migración completa de los 938 records con tags concretas (sin pérdida de datos).
- [ ] **O7:** Backup completo verificado antes de migración (checksum OK, restauración probada).
- [ ] **O8:** Datos de migración en rama separada (`loreanse/lodan.sync:feat/sin-ollama`).
- [ ] **O9:** TDD aplicado: tests escritos antes de implementación, RED observado, GREEN verificado.
- [ ] **O10:** `go test ./...` pasa 100%. Cobertura ≥80% en paquetes modificados.
- [ ] **O11:** Pruebas manuales completas (Fase E).
- [ ] **O12:** Skill `lodan-memoria` actualizada con los nuevos parámetros y principios.
- [ ] **O13:** Skills nuevas (`lodan-taxonomy`, `lodan-quality`, `lodan-alias`) instaladas.
- [ ] **O14:** ADR creado explicando el cambio sin-ollama.
- [ ] **O15:** Smoke test de 24h pasado sin regresiones.

---

## 10. Cierre del feature

### Si todos los O1-O15 están cumplidos:
1. Marcar todas las tasks T1-T6 (y T0.1-T0.5 + T2b) como `done` en `odd/tasks/lodan-sin-ollama.md`.
2. Agregar entrada al log con resumen de cierre: commits, métricas, evidencia.
3. Cerrar sesión: `lodan session end --summary "..."`.
4. Si todo OK: PR de `feat/sin-ollama` a `dev`. Review por el usuario. Merge con `--no-ff`.

### Si alguno no se cumple:
1. **NO** marcar como completado.
2. Documentar el gap en el log del feature doc.
4. Decidir con el usuario: continuar, cerrar parcial, o revertir.

---

## 11. Anti-patrones (NO hacer)

- **NO** mergear `feat/sin-ollama` a `dev` sin pasar la Fase F completa.
- **NO** pushear datos a `main` de `loreanse/lodan.sync`. Solo a `feat/sin-ollama`.
- **NO** correr la migración sin backup verificado en `B.3`.
- **NO** marcar tests como `done` si no se observó RED antes.
- **NO** declarar "terminado" si algún objetivo O1-O15 falla.
- **NO** cambiar el scope (recordatorios): tags concretas obligatorias son NO OPCIONALES.