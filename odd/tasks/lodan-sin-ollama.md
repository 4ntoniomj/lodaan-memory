# lodan-sin-ollama — Feature document

**Objective:** Refactorizar lodan para que Ollama deje de ser dependencia obligatoria, manteniendo todas las capacidades (jerarquía de tags, veredictos con confianza, key/supersede, aliases confirmados, gitsync) y mejorando el output de búsquedas vía taxonomía explícita de tags + trabajo semántico hecho por la IA.

**Inspiración arquitectónica:** lo mejor de Engram (single-binary-feel, recall sin servicios externos, foco en taxonomía) + lo mejor de lodan (jerarquía, veredictos, key/supersede, gitsync, Postgres).

**Branch:** `feat/sin-ollama` desde `dev` (NO desde `feat/ia-portable-tunnel`).

**Delivery:** `feature-branch-chain` si Phase 1 > 400 líneas; si no, `single-pr`.

**Test runner:** `go test ./...` con `LODAN_PG_BIN_DIR=C:/Users/4ntoniomj/AppData/Local/lodan/runtime/pg/Library/bin`.

**Risk line:** item 6 (delivery/environment: el binario activo deja de requerir Ollama como dependencia; cambia el contrato operacional de instalación y la disciplina de guardado de la IA).

**Estructura:** dos fases. **Phase 0 (diseño) es un gate obligatorio** — no se toca código hasta que todos los entregables de D1-D5 estén aprobados.

---

## Phase 0 — Design (gate, sin código)

Phase 0 produce cinco documentos de diseño. Cada uno se revisa y aprueba por separado. Ningún archivo del repo se modifica en Phase 0 salvo `odd/tasks/lodan-sin-ollama.md` mismo (este archivo) y los nuevos documentos de diseño que se especifiquen abajo.

### D1 — Data storage specification

Decisiones de almacenamiento y schema. **No es "qué tablas hay", es "cómo se almacenan los datos para soportar todo lo demás sin fallar"**.

Cubrir explícitamente:

1. **Schema Postgres final**: qué columnas se quedan, cuáles se marcan como "nullable permanente" (embedding, embedding_model).
2. **pgvector**: ¿se conserva o se remueve? Argumentos a favor de mantenerlo (costo cero si no se usa) vs remover (limpieza). Decisión recomendada: **mantener**, pero sin índice activo.
3. **Constraints**:
   - Formato de tags concretas: regex `^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$`. CHECK constraint o validación en la capa de servicio.
   - Unicidad de `key` por topic activo. Si dos records tienen la misma `key` en el mismo topic, el segundo invalida al primero (esto es `supersede`).
   - `topic_key` consistente con `key` cuando aplique.
4. **Índices**:
   - `GIN(tsv)` para full-text (ya existe).
   - `B-tree(memory_topics.topic_id)`.
   - `B-tree(memory_keys(topic_id, key)) WHERE active` para la unicidad.
   - Sin índice en `embedding` (no se usa).
5. **Migración**:
   - Nueva migración `NNNN_taxonomy_constraints.up.sql` y su `.down.sql`.
   - Idempotente: si se corre dos veces, no rompe.
7. **Validación**: el script de migración se prueba primero contra el data dir de prueba de S2/T1.

Entregable concreto: `docs/specs/data-storage-sin-ollama.md` (~100 líneas).

### D2 — Tag taxonomy specification

La taxonomía completa, con reglas formales. Esto es el **corazón del feature**. Sin esto bien hecho, ningún recall funciona bien.

Cubrir explícitamente:

1. **Doce familias concretas** con definición, ejemplos válidos y anti-patrones:
   - `proyecto-<slug>` — proyectos con repo o worktree.
   - `persona-<slug>` — personas (vos y contactos personales).
   - `contacto-<empresa>-<persona>` — contactos profesionales.
   - `proveedor-<slug>` — empresas que te venden.
   - `cliente-<slug>` — empresas a las que vendés.
   - `finca-<slug>` — fincasAgridemur.
   - `vehiculo-<matrícula>` — coches de la flota (sin guiones en la matrícula).
   - `coche-<slug>` — coches personales con atributos evolutivos.
   - `skill-<slug>` — skills instaladas.
   - `herramienta-<slug>` — software/herramientas.
   - `recurso-<slug>` — rutas de red, archivos clave.
   - `evento-<slug>` — hitos importantes.

2. **Tags generales vs concretas**: distinción formal con ejemplos. Regla: **toda record DEBE llevar al menos una tag concreta**. Las generales son opcionales pero recomendadas.

3. **Jerarquía con atributos**:
   - Cuándo usar `<familia>-<slug>/<atributo>` vs `<familia>-<slug>` solo.
   - Ejemplos trabajados: `coche-volvo-s60/matricula`, `coche-volvo-s60/itv`, `proveedor-florette/contacto-pedidos`.
   - Límite: máximo 3 atributos por tag.

4. **Aliases**:
   - Regla del singular canónico: `#cosecha` (no `#cosechas`), `#contacto` (no `#contactos`), etc.
   - Los plurales se crean como alias confirmado.
   - Aliases detectados a confirmar: 10 pares (los 4 que sugiere el servidor + 6 nuevos del muestreo).

5. **Anti-patrones**:
   - Tags vagas: `#misc`, `#general`, `#varios`, `#info`.
   - Tags con mayúsculas: `#Coche-Volvo`.
   - Tags con espacios: `#mi proyecto`.
   - Mezcla español/inglés inconsistente.
   - Tags que duplican contenido del record.

6. **Tags híbridas legítimas**: `#erp-agm ≡ #proyecto-erp-agm`, `#agm-cosechas` (sub-proyecto de `#proyecto-agm`), `#agridemur-marketing` (sub-proyecto de `#proyecto-erp-agm`). Reglas documentadas.

7. **Tags snapshot/evento**: `#snapshot-<YYYYMMDD>` se canonicaliza como `#snapshot` con fecha en el contenido.

Entregable concreto: `docs/specs/tag-taxonomy.md` (~200 líneas).

### D3 — AI skill specification

Cambios a la skill `lodan-memoria` y skills nuevas que se necesitan. **Esta es la pieza que compensa la falta de embeddings: la disciplina del sí mismo**.

Cubrir explícitamente:

1. **Regla obligatoria de tag concreta**: el IA no puede guardar un record sin al menos una tag concreta (de las 12 familias). Si la IA intenta hacerlo, decide no llamar `remember`.

2. **Decisión "tag nueva vs tag existente"**:
   - Buscar primero tags similares (usar `recall` con la etiqueta candidata).
   - Si existe tag equivalente, reutilizarla (relacionar).
   - Si no existe, crearla siguiendo las reglas de formato.

3. **Uso de `key`**: cuándo aplica (datos evolutivos del mismo objeto), cuándo no (eventos one-shot).

4. **Revisión de `revisa:` candidatos** (crítico sin embeddings):
   - El IA lee los 6 candidatos que devuelve `remember`.
   - Decide por cada uno: contradicción → preguntar al usuario; relación → `relate`/`part_of`; duplicado → `merge` o no guardar.
   - Sin embeddings, los candidatos son por full-text + topic, así que el IA debe ser **más cuidadoso**, no menos.

5. **Estrategia de recall sin embeddings**:
   - Preferir queries con keywords específicas (no paráfrasis vagas).
   - Usar navegación por topic cuando se conoce el topic.
   - Confiar en `Connected` (rare tags) para descubrir relaciones no explícitas.

6. **Trabajo de mantenimiento de la IA** (periódico, no por sesión):
   - Detección de duplicados: comparar records que comparten tag concreta + contenido similar.
   - Sugerencia de aliases: detectar singular/plural y otros pares.
   - Sugerencia de tags concretas faltantes: para records con solo tags generales.

7. **Skills a modificar/crear**:
   - **Modificar**: `~/.config/opencode/skills/lodan-memoria/SKILL.md` (reglas + ejemplos).
   - **Modificar**: `references/que-guardar.md` (énfasis en tags concretas).
   - **Modificar**: `references/herramientas.md` (nuevas reglas de tag).
   - **Crear (opcional)**: `references/taxonomia.md` (referencia rápida de las 12 familias).
   - **Crear (opcional)**: `scripts/sugerir_tags.py` (asistente que sugiere tags concretas para un record dado su contenido).

Entregable concreto: cambios al skill `lodan-memoria` (delta respecto a la versión actual) + `docs/specs/ai-skill-sin-ollama.md` con el detalle.

### D4 — Quality bar specification

**Cómo medimos que el feature logra "que no falle en ninguna búsqueda y no guarde lo mismo dos veces"**.

Cubrir explícitamente:

1. **Métricas de cobertura**:
   - `% records con ≥1 tag concreta`: target **>80%** después de la migración.
   - `% tags concretas reutilizadas (>1 uso)`: target **>60%** (sugiere disciplina).
   - `% records huérfanos (sin relations)`: target **<20%**.

2. **Métricas de búsqueda**:
   - `Search success rate`: queries sobre temas con records guardados que retornan al menos un resultado. Target **>95%**.
   - `Recall "Connected" usage rate`: % de recalls que usan la sección Connected. Target **>40%**.
   - `Search failure analysis`: lista de las queries que devolvieron "sin resultados" con el motivo (¿falta tag?, ¿falta full-text match?).

3. **Métricas de duplicación**:
   - `Duplicate rate`: % de `remember` calls que el IA decide no guardar porque coincide con un existente. Target medido pero no fijo (debe ser >0 después de la migración, idealmente 10-20%).
   - `Hash dedup hits`: % de items rechazados por hash exacto (existente).
   - `Topic+content dedup hits`: % de items rechazados por solapamiento de topic + contenido similar (nuevo).

4. **Aceptación del feature**:
   - Las 5 métricas de cobertura y búsqueda cumplidas durante 30 días consecutivos.
   - Smoke test de 24 horas post-merge pasa.
   - Cero regresiones en los tests existentes.

5. **Cómo se miden**:
   - Script `scripts/metricas.sh` que corre sobre el data dir y emite un reporte.
   - Comparación pre/post con snapshots.

Entregable concreto: `docs/specs/quality-bar.md` (~80 líneas).

### D5 — Validation tooling specification

Cómo prevenimos que entren datos malos a la BD. **Esta es la red de seguridad automatizada**.

Cubrir explícitamente:

1. **Updates a `validar_registro.py`**:
   - Check obligatorio: `≥1 tag concreta` con regex (warning, no error, para no romper guardados legacy).
   - Check recomendado: `key` presente si el record tiene datos evolutivos.
   - Check denylist: rechazar tags en `denylist.txt` (`#misc`, `#general`, etc.).
   - Check jerarquía: si tiene `/<atributo>`, validar que la parte base exista como tag.
   - Check relations: validar que los IDs en `relate` existan.

2. **Output del validador**:
   - Warnings (sugerencias): no bloquean el guardado, el IA decide.
   - Errors (reglas duras): bloquean el guardado (ej: tag mal formada).

3. **Integración con el flujo del IA**:
   - El IA corre el validador ANTES de llamar `remember`.
   - Si hay errors, ajusta y vuelve a intentar.
   - Si hay warnings, los reporta al usuario pero guarda igual.

4. **Tests del validador**:
   - Cobertura: casos válidos, warnings, errors, edge cases.
   - Integrado en el test suite de lodan (Go test del script Python via subprocess).

5. **Nuevas herramientas opcionales**:
   - `scripts/sugerir_jerarquia.py`: dado un record sin tag concreta, sugiere la jerarquía apropiada.
   - `scripts/audit_taxonomia.py`: barre la BD y reporta inconsistencias.

Entregable concreto: `docs/specs/validation-tooling.md` (~60 líneas) + updates a `scripts/validar_registro.py`.

---

## Phase 1 — Implementation specs

Phase 1 solo arranca cuando Phase 0 (D1-D5) está aprobada. Los specs de implementación quedan como referencia pero pueden ajustarse si Phase 0 los invalida.

### S1 — Backup completo antes de cualquier cambio
> Antes de tocar BD o código, ejecutar `lodan db backup --full` sobre el data dir activo. El backup se guarda en una ruta FUERA del árbol de trabajo (sugerido: `C:\Users\4ntoniomj\backups\lodan-pre-sin-ollama-<YYYYMMDD>\`). Se verifica el checksum. Sin backup verificado, no se avanza a S2/S3.

### S2 — Data directory aislado para pruebas
> Se crea un nuevo data dir (`C:\Users\4ntoniomj\AppData\Local\lodan-sin-ollama-data\` o equivalente) inicializado con `lodan install` apuntando a ese path. Se importa la copia de seguridad de S1 dentro. El binario puede arrancar con `--data-dir` apuntando a este directorio. Los cambios de S4+ se prueban PRIMERO sobre este directorio.

### S3 — Rama `feat/sin-ollama` creada y pusheada
> Rama desde `dev` (no desde `feat/ia-portable-tunnel`). Conventional commits en español. NO se mergea a dev hasta que T1-T8 estén GREEN.

### S4 — `lodan serve` arranca y sirve el MCP sin Ollama
> El binario debe poder arrancar sin Ollama. Cambio: el startup de `serve` no debe comprobar disponibilidad de Ollama como precondición. `lodan status` reporta "Ollama: no disponible" como estado normal.

### S5 — Worker `FillPending` desactivado por default
> El worker periódico queda detrás de un flag `--with-embeddings`. Por default, no corre. El código de embeddings se conserva compilado.

### S6 — `Recall` corre 100% con full-text + tags
> `Recall` retorna resultados sin invocar el embedder cuando está caído. El path de embeddings se conserva detrás del flag de S5.

### S7 — Taxonomía de tags aplicada
> Aplicar D2 al data dir de prueba vía script de migración idempotente.

### S8 — Decisión sobre los 753 registros pendientes
> Decisión a tomar en T0 (después de Phase 0). Default propuesto: aceptar que quedan no indexados semánticamente (full-text los cubre).

### S9 — Tests RED para comportamiento sin Ollama
> Tests: serve arranca sin Ollama; recall funciona solo con full-text; remember no falla sin Ollama; status reporta no disponible; maintenance loop no llama FillPending por default.

### S10 — Documentación actualizada
> README, ARCHITECTURE, skill lodan-memoria, ADR nuevo.

### S11 — Smoke test post-merge
> Binario nuevo corre 24h en setup real sin Ollama. Verifica no regresiones y adopción de taxonomía.

### S12 — `recall` extendido con filtros de primera clase
> `internal/mcptools/read.go` `recallInput` agrega campos opcionales:
> - `tags_all []string` (AND): el record debe tener TODAS estas tags. Default: vacío (sin filtro).
> - `tags_any []string` (OR): el record debe tener AL MENOS UNA de estas tags. Default: vacío (sin filtro).
> - `date_from string` (RFC3339 o AAAA-MM-DD): `occurred_at >= date_from`. Default: vacío (sin límite).
> - `date_to string` (RFC3339 o AAAA-MM-DD): `occurred_at <= date_to`. Default: vacío (sin límite).
>
> **Combinación:** `tags_all` y `tags_any` se aplican simultáneamente (AND entre grupos). `date_from` y `date_to` se aplican en AND con los tags.
>
> **Query vacía:** se permite cuando hay al menos un filtro (tags o fechas). Sin query ni filtros → error `la consulta no puede estar vacía` (mismo mensaje que hoy).
>
> **Tags inválidas:** si una tag no existe en `topics`, se ignora silenciosamente. Si TODAS las tags de `tags_all` no existen, error `tags_all: ninguna tag existe`.
>
> Cambios en código:
> - `internal/recall/recall.go`: agregar campos a `Request` (línea 23-28 ya tiene la struct).
> - `internal/recall/sql.go`: `TextCandidatesSQL` y `TextCandidatesBatchSQL` aceptan los nuevos parámetros.
> - `internal/mcptools/read.go`: agregar campos al `recallInput` con jsonschema.
>
> Costo estimado: ~150 líneas (SQL + tests + schema MCP).

### S13 — Tests RED para los nuevos filtros
> Tests que deben pasar GREEN al final:
> - `TestRecall_TagsAll`: con 3 tags concretas en `tags_all`, solo records que tengan las 3.
> - `TestRecall_TagsAny`: con 2 tags, records que tengan al menos una.
> - `TestRecall_DateRange`: con `date_from` y `date_to`, records dentro del rango. Sin filtros de fecha, devuelve todos.
> - `TestRecall_TagsAllAndAny`: combinación AND-OR (3 tags all + 2 tags any).
> - `TestRecall_DateAndTags`: combinación fecha + tags.
> - `TestRecall_EmptyQueryWithFilters`: query vacía + tags, devuelve records por filtro sin buscar texto.
> - `TestRecall_InvalidTagsIgnored`: tag inexistente se ignora; todas inválidas → error.
> - `TestRecall_BackwardCompat`: llamadas SIN los nuevos campos funcionan idéntico a antes.

### S14 — Documentación del nuevo contrato en skills y herramientas
> - `~/.config/opencode/skills/lodan-memoria/references/herramientas.md`: documentar los 4 nuevos parámetros con ejemplos.
> - `~/.config/opencode/skills/lodan-memoria/SKILL.md`: enseñar al IA a extraer filtros del lenguaje natural ("del proyecto X", "de la fecha Y", "de trabajo").
> - `~/.config/opencode/skills/lodan-memoria/EXAMPLE.md`: agregar 3 ejemplos de uso con filtros.
> - Tests del validador Python cubren que las tags de `tags_all`/`tags_any` cumplan el formato de la taxonomía (D2).

---

## Tasks

| ID | Specs | Route | Status | Evidence |
| --- | --- | --- | --- | --- |
| **T0.1** | D1 (data storage) | inline (diseño) | done | docs/specs/data-storage-sin-ollama.md; reversible refactor (`388f081`) aprobado |
| **T0.2** | D2 (tag taxonomy) | inline (diseño) | done | docs/specs/tag-taxonomy.md; 12 familias confirmadas |
| **T0.3** | D3 (AI skill) | inline (diseño) | done | docs/specs/ai-skill-sin-ollama.md; pre-save + tags_index + auto-relación |
| **T0.4** | D4 (quality bar) | inline (diseño) | done | docs/specs/quality-bar.md; 30 días post-merge |
| **T0.5** | D5 (validation tooling) | inline (diseño) | done | docs/specs/validation-tooling.md; core confirmado, scripts auxiliares fuera |
| **T1** | S1, S2, S3, S8 | inline (backup + setup) | pending | awaits T0.1-T0.5 |
| **T2** | S4, S5, S6, S12, S13 | delegated worker | pending | awaits T1; S12-S13 extienden T2 con filtros de primera clase |
| **T2b** | S14 | inline (skill update) | pending | awaits T3 |
| **T3** | S9 | inline (TDD con T2) | pending | awaits T2 |
| **T4** | S7 | delegated worker (migración batched) | pending | awaits T3 |
| **T5** | S10 | inline | pending | awaits T4 |
| **T6** | S11 | inline (smoke test usuario) | pending | awaits T5 merge |

---

## Log

### L1 (2026-10-20, user, original request verbatim)
"Es que quiero probar los cambios de los datos que me has propuesto en otro sitio para no no afectar a los datos actuales, pero puedes hacer una copia de seguridad y aplicar los cambios. Además creo que quiero enfocarme en desarrollar lodan en una nueva rama enfocado a no usar ollama, como un lodan-lite pero que no es lite, es sin ollama, para cualquier ordenador enfocado en juntar lo mejor de engram y lo mejor de lodan usando las etiquetas con un sistema de etiquetado bien hecho, relación léxica y tal, hacemos el plan?"

### L2 (2026-10-20, evidence, decisión de motor)
Análisis previo en esta conversación: SQLite sería más simple empezando de cero, pero la inversión en 938 registros, gitsync infra y ops tooling montados sobre Postgres hace que la migración no se justifique. **Decisión:** mantener Postgres. La simplificación operativa viene de quitar Ollama, no de cambiar de motor.

### L3 (2026-10-20, plan v1)
Documento v1 creado con foco en refactor de código (11 specs S1-S11, 6 tasks T1-T6). Decisión arquitectónica guardada en lodan #1120.

### L4 (2026-10-20, evidence, taxonomía base)
Análisis de los 938 registros (muestreo ~30 records) reveló:
- Predominan tags generales (#proyectos, #arquitectura, #lodan, #gasoil) sobre concretas (familia-slug).
- Inconsistencias de sinonimia: 10 pares detectados (singular/plural + variantes).
- Familias faltantes: 8 (persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, skill-, herramienta-).
- Tags híbridas (#erp-agm, #agm-cosechas, #agridemur-marketing) sin regla de equivalencia explícita.

### L5 (2026-10-20, decision pendiente)
S8 (qué hacer con los 753 registros pendientes de embedding) queda como decisión abierta. Default propuesto: opción (b) — aceptar no indexación semántica.

### L6 (2026-10-20, user, scope correction)
> "Pero me refería al plan de como se va a guardar los datos, que sistemas de etiquetado vamos a usar, todo para que no falle en ninguna búsqueda y no guarde lo mismo dos veces, las skill que se van a definir/modificar para que la IA sepa usar lodan, todo antes de hacer cambios, porque es un cambio grande."

Antonio corrige el foco del plan: la v1 era demasiado code-centric (refactor del binario). Lo que se necesita es diseño aguas arriba: **cómo se guardan los datos, qué sistemas de etiquetado, cómo se garantiza calidad (sin fallos de búsqueda, sin duplicados), qué skills se modifican/crean** — todo antes de tocar código. Por eso este documento v2 añade Phase 0 con 5 entregables de diseño (D1-D5) como gate obligatorio. Phase 1 (código) solo arranca con D1-D5 aprobados.

### L7 (2026-10-20, plan v2)
Este documento. Estructura: Phase 0 (D1-D5 diseño) → Phase 1 (S1-S11 implementación). Phase 0 produce cinco docs de especificación que se revisan por separado antes de aprobar la entrada a Phase 1.

### L8 (2026-10-20, user, filtros de primera clase)
> "Lo de los filtros que te he dicho e posible? ... solo quiero hacer la feuture sin olla,a cudado con eso, vamos por la B entonces, como sería la búsqueda?"

Antonio confirma que `recall` debe extenderse con filtros de primera clase para soportar queries tipo "del trabajo del proyecto en esta fecha". Análisis previo: `recallInput` actual (`internal/mcptools/read.go` línea 14-21) acepta `Query`, `Queries`, `Topics`, `Limit`, `IncludeHistory`. **Falta:** multi-tag AND/OR, date range, combinación de filtros. Tres caminos evaluados: (A) IA orquesta con N llamadas (funciona pero paga tokens), (B) extender el server con `tags_all`/`tags_any`/`date_from`/`date_to` (~150 líneas), (C) query language explícito (sobreingeniería). **Decisión:** B. Specs añadidos: **S12** (extensión del `recallInput` con los 4 parámetros), **S13** (8 tests RED que cubren AND/OR/fecha/combinaciones/edge cases), **S14** (skill update + documentación de herramientas). Tasks afectadas: T2 absorbe S12-S13 (mismo delegated worker que el refactor sin-ollama); T2b nuevo para S14 (inline skill update).

### L9 (2026-10-20, evidence, Phase 0 entregado)
D1-D5 specs escritos y commiteados en commit `64c2bf4` (1045 líneas en `docs/specs/`). T0.1-T0.5 movidos de `pending` a `in_review`. Pendiente aprobación de Antonio para que Phase 1 (código) arranque.

Cada doc incluye al final una sección de Aprobación con items verificables. Cambios materiales del round:
- **D1**: pgvector se queda instalado; índice HNSW y cola de pendientes FillPending se eliminan; CHECK de regex en `topics.slug` y `tag_aliases.{alias,canonical}`.
- **D2**: 12 familias (no 13 — `apuntes-` no califica como concreta porque no identifica entidad); 12 pares de aliases singular/plural; regla snapshot = topic + fecha en contenido.
- **D3**: pre-save obligatorio (recall antes de remember), revisión rigurosa de `revisa:`, auto-relación por `tag_link_strength >= 0.85`, `tags_index()` al inicio de sesión.
- **D4**: target 30 días consecutivos post-merge; métricas §3 (search success rate, connected usage) requieren log parsing.
- **D5**: tag concreta obligatoria es warning hasta T4 (modo compatibilidad); denylist: `misc, general, varios, info, vago, na, none`.

### L10 (2026-10-20, user, ajustes por feedback directo)
Antonio revisa las decisiones materiales y modula:

- **D1 — reversible.** La rama `feat/sin-ollama` debe permitir rollback limpio a `main`/`dev` si algo sale mal. Cambios aplicados en commit `388f081`: la migración `0005_taxonomy_constraints` se reduce a solo `ADD CONSTRAINT` (no `DROP INDEX`). El índice `memories_bq_hnsw` y la cola `memories (id) WHERE embedding IS NULL` **se mantienen en la BD**: el server los evita por runtime cuando `--with-embeddings=false`. Si rollback, la BD queda idéntica.
- **D2 — confirmado en 12.** Sin cambios al spec. `apuntes-` queda fuera.
- **D3 — confirmado.** Sin cambios al spec. Pre-save obligatorio, `tags_index()`, auto-relación `>= 0.85`, ejemplos nuevos en `EXAMPLE.md`.
- **D4 — confirmado.** Sin cambios al spec.
- **D5 — core confirmado, scripts auxiliares fuera.** Cambios aplicados en commit `cb7ef28`: §5 (`sugerir_jerarquia.py`, `audit_taxonomia.py`) queda como referencia futura, no se programa en este round. El core del validador y la integración con la IA se mantienen.

Pendiente: confirmación de aprobación formal de Antonio en cada D (sección §8 de cada spec). Cuando eso llegue, T0.1-T0.5 pasan a `done` y Phase 1 arranca con T1.

### L11 (2026-10-20, user, Phase 0 aprobada)
Antonio aprueba D1-D5 en round único. T0.1-T0.5 pasan a `done`. Phase 0 cerrada. Phase 1 (código) queda autorizada.

**Próximo paso inmediato (T1):** verificación de pre-requisitos operacionales del runbook §3:

- [ ] Binario `lodan` accesible y funcional (`lodan status` reporta OK).
- [ ] Data dir activo identificable y backupable.
- [ ] Data dir de prueba `C:\Users\4ntoniomj\AppData\Local\lodan-sin-ollama-data\` inicializado.
- [ ] Backup `lodan db backup --full` con checksum verificado.
- [ ] Acceso a `loreanse/lodan.sync` verificado.

Una vez verificados, arranca Fase A del runbook (TDD: A.1 `tags_index`, A.2 filtros en `recall`, A.3 modo sin Ollama).