# D3 — AI skill specification (lodan-sin-ollama)

**Estado:** propuesta de diseño. Pendiente de aprobación por Antonio.
**Owner del documento:** orquestador de `feat/sin-ollama`.
**Aprobación:** requerida para salir de Phase 0 y arrancar Phase 1.

---

## 1. Contexto

La pieza que compensa la falta de embeddings obligatorios es la **disciplina de la IA**: la skill `lodan-memoria` fija cómo el modelo decide qué guardar, cómo etiqueta, cómo revisa candidatos y cómo recupera. Sin embeddings, los candidatos del servidor salen por full-text + topic, así que la IA debe ser **más cuidadosa**, no menos.

Este spec documenta los **cambios a la skill** y a sus sub-documentos. La skill actual (`~/.config/opencode/skills/lodan-memoria/`, versión `3.0.0`) tiene la base correcta; los cambios refinan las reglas y añaden los filtros de primera clase de S12.

---

## 2. Cambios por archivo

### 2.1 `SKILL.md` — cambios

#### 2.1.1 Tag concreta obligatoria — regla dura

**Estado actual** (Paso 3a, líneas 78-87):

> "Una mezcla de **generales** (que se repiten mucho: `#agridemur`, `#vdm`) y **concretas** (que identifican un objeto: `#coche-4860ngl`, `#matricula`). Las concretas son las que `recall` sigue y las que arman las relaciones por rareza. Familias para las concretas: `coche-<matrícula>`, `cliente-<nombre>`, `finca-<nombre>`, `proyecto-<nombre>`..."

**Cambio propuesto:**

> "**Regla dura:** todo registro DEBE llevar **al menos una tag concreta** de las 12 familias (ver §3.2). Sin tag concreta el registro queda huérfano y rompe las métricas de cobertura (D4). Si una no etiqueta concreta, decide no llamar `remember`. Las generales son complemento, no sustituto."

#### 2.2.2 Las 12 familias — referencia explícita

**Estado actual:** 4 familias mencionadas + "cualquier otra familia que el usuario declare".

**Cambio propuesto:** listar las 12 familias (definidas en D2 §3) y añadir el enlace a `references/taxonomia.md` (referencia rápida).

#### 2.2.3 Pre-save obligatorio (recall antes de remember)

**Estado actual** (Paso 3): "En cuanto se asiente una decisión… llama a `remember` sin preguntar."

**Cambio propuesto:** añadir antes de cada `remember` una llamada `recall` sobre los temas candidatos, para:

- Evitar duplicados que el full-text pueda haber escapado.
- Detectar contradicciones tempranas.
- Informar al usuario si hay conflicto antes de pedirle input.

Excepción: si el `remember` trae `key` y es una sustitución (`supersede` por la misma `key`), el recall puede ser opcional — el servidor ya lista los candidatos por esa key.

#### 2.2.4 Revisión rigurosa de `revisa:`

**Estado actual** (Paso 4, líneas 88-108): tabla con 4 casos.

**Cambio:** añadir una línea explícita al inicio:

> "**Sin embeddings obligatorios, los candidatos son por full-text + topic + tags compartidas. La IA debe revisar los 6 con más cuidado, no menos.** Si un candidato tiene la misma tag concreta y mismo año/época, es candidato a contradicción o duplicación — decidir antes de seguir."

#### 2.2.5 Auto-relación al guardar (rare tags)

**Estado actual:** no hay mención.

**Cambio (nuevo Paso 3b):**

> "Si el registro guarda una tag concreta **rara** (usada en <3 docettos activos, umbral configurable, default 0.85 en escala de rareza), el servidor propondrá relaciones por tag en la respuesta. La IA acepta las evidentes y deja las dudosas para el usuario."

Umbral concreto: `tag_link_strength >= 0.85` activa auto-relación. Por debajo, queda como sugerencia.

#### 2.2.6 Uso de `queries[]` para multi-scope

**Estado actual** (Paso 2, líneas 45-58): menciona `queries` como lista.

**Refuerzo:** clarificar que `queries[]` es para multi-**scope**, no multi-**session**. Cuando se quiere "actividad trabajo + actividad personal" en una sola llamada, van las dos queries con `tags_all` diferentes. El session-based se hace con `session last`, no con `recall`.

#### 2.2.7 `tags_index()` al inicio de cada sesión

**Estado actual:** no existe la tool.

**Cambio (nuevo Paso 1a):**

> "Al arrancar la primera llamada de la sesión, si el servidor expone `tags_index()`, llamarlo para tener el mapa completo de tags concretas y conteos. Sirve para que la IA sepa qué tags concretas existen antes de inventar una nueva."

`tags_index()` es nueva (S15) y devuelve el árbol de tags con jerarquía y conteos. Se documenta en §3.

### 2.2 `references/herramientas.md` — cambios

#### 2.2.1 `recall` con filtros de primera clase (S12)

**Cambio en la fila de la tabla `recall`:**

```
| `recall` | `query` y/o `queries`, `topics?`, `tags_all?`, `tags_any?`, `date_from?`, `date_to?`, `mode?` (`"list"` ordena por fecha desc; default es el modo actual), `limit?`, `include_history?` | Igual que antes. Modo `list` cambia el orden y el formato. |
```

Añadir tras la tabla:

> **Filtros de primera clase** (S12):
> - `tags_all []string`: el record debe tener TODAS estas tags. AND. Default: vacío.
> - `tags_any []string`: el record debe tener AL MENOS UNA. OR. Default: vacío.
> - `date_from`, `date_to`: RFC3339 o `AAAA-MM-DD`. Default: vacío.
> - `mode`: `"list"` para ordenar por `occurred_at` desc, formato con fecha prominent. Default: `"topic"` (modo actual).
> - **Combinación:** `tags_all` y `tags_any` se aplican simultáneamente (AND entre grupos). `date_from` y `date_to` se aplican en AND con los tags.
> - **Query vacía:** se permite cuando hay al menos un filtro (tags o fechas). Sin query ni filtros → error `la consulta no puede estar vacía`.
> - **Tags inválidas:** si una tag no existe en `topics`, se ignora silenciosamente. Si TODAS las tags de `tags_all` no existen, error `tags_all: ninguna tag existe`.

#### 2.2.2 `tags_index()` — nueva herramienta (S15)

```
| `tags_index` | `scope?` (`"trabajo"` \| `"personal"` \| vacío = todas), `limit?` | Árbol de tags concretas (12), de nivel, con jerarquía y conteos. Ejemplo: `proyecto-erp-agm (24), proyecto-erp-agm/cosechas (8), coche-volvo-s60 (12), coche-volvo-s60/matricula (3)`. |
```

### 2.3 `references/que-guardar.md` — cambios

#### 2.3.1 Énfasis en tags concretas

**Estado actual:** no menciona tags concretas obligatorias.

**Cambio:** añadir una sección al inicio:

> "Antes de guardar nada, decidir **al menos una tag concreta** de las 12 familias (D2). Si no podés calar, no es el caso de archivo — es charla, o no tenés suficiente contexto todavía."

### 2.4 `EXAMPLE.md` — 3 ejemplos nuevos (S14)

**Estado actual:** los ejemplos están bien pero no muestran filtros de primera clase.

**Cambio:** añadir 3 ejemplos con filtros al final:

#### Ejemplo 1: multi-scope con filtros

> "**Usuario:** ¿qué hice en el proyecto erp-agm en octubre?"
>
> IA: `recall` con `queries=["actividad proyecto"], tags_all=["proyecto-erp-agm"], date_from="2026-10-01", date_to="2026-10-20", mode="list"`.
>
> Respuesta: 3 records listados por fecha desc, formato con `Tx #id fecha título`.

#### Ejemplo 2: filtros OR

> "**Usuario:** ¿qué tengo sobre las fincas Royalveg y Rebelao?"
>
> IA: `recall` con `queries=["fincas"], tags_any=["finca-royalveg", "finca-rebelao"], mode="list"`.
>
> Respuesta: bloque con records de las dos fincas.

#### Ejemplo 3: pre-save con `recall`

> "**Usuario:** la matrícula del Volvo S60 es 4860 NGL."
>
> IA: primero `recall tags_all=["coche-volvo-s60"]` para ver si hay datos contradictorios. Encuentra `#36 viejo` que dice "Matrícula 1234 ABC". Decide: es el dato correcto, el nuevo sustituye. Llama `remember` con `tags=["coche-volvo-s60", "coche-volvo-s60/matricula"]`, `key="coche-volvo-s60/matricula"`. Revisa `revisa: #36 [fact 2026-08-12] "Matrícula vieja"`. Confirma `revise supersede id=36 id=NOW`.

### 2.5 `references/taxonomia.md` — crear (opcional, recomendado)

Referencia rápida con:

- Las 12 familias con ejemplo válido + anti-patrón (resumen de D2).
- Forma regex.
- Singular canónico + plurales alias.
- Edge cases (`vehiculo-` vs `coche-`, híbridas legítimas).
- Tags snapshot.

Esto lo lee la IA antes de un guardado para no equivocarse con la forma.

### 2.6 `scripts/sugerir_tags.py` — crear (opcional)

Asistente que dado un record (título + contenido) sugiere tags concretas apropiadas. Usa reglas heurísticas:

- Si menciona "Volvo", sugiere `coche-volvo-s60` o pide clarificación.
- Si menciona "matrícula XXXX", sugiere `vehiculo-xxxx` (sin guiones).
- Si menciona "cliente ACME", sugiere `cliente-acme`.
- Si menciona "proyecto X", sugiere `proyecto-x`.

No bloquea — es sugerencia. La IA decide.

---

## 3. Cambios al server (referencia, no scope D3)

### 3.1 `recall` extendido con filtros (S12)

Ver D2.2.1. Implementación en `internal/recall/recall.go`, `internal/recall/sql.go`, `internal/mcptools/read.go`.

### 3.2 `tags_index()` nueva (S15)

Implementación en `internal/mcptools/`. Devuelve árbol de tags con jerarquía y conteos. Scope filter opcional (`trabajo` / `personal`).

---

## 4. Disciplina sin embeddings — checklist de la IA

Esta sección va al final del `SKILL.md` como resumen de la disciplina:

```markdown
## Disciplina de lodan-sin-ollama

Sin embeddings obligatorios, la calidad del recall depende 100% de tu disciplina. Antes de cada guardado:

- [ ] ¿Este record tiene al menos una tag concreta de las 12 familias? Si no, no lo guardes.
- [ ] ¿Hiciste un `recall` reciente sobre el tema? Si no, hazlo.
- [ ] ¿Revisaste los `revisa:` que devolvió el servidor? Si no, no sigas.
- [ ] ¿La tag concreta que vas a usar está en `tags_index()`? Si no, créala siguiendo D2.
- [ ] ¿Hay aliases por confirmar (`¿alias?: …`)? Confirma o rechaza explícitamente.
- [ ] ¿El record necesita `key` (dato evolutivo)? Si sí, usa la convención `<familia>-<slug>/<atributo>`.
```

---

## 5. S14 — Documentación del contrato

Esta sección referencia el entregable de S14 (skill update + documentation). Los cambios de §2.1 a §2.6 son los entregables. Se aplican en **T2b** (inline, después de T2).

Criterio de T2b:

- Todos los cambios §2.1-§2.4 commiteados.
- `references/taxonomia.md` creado (recomendado).
- `EXAMPLE.md` con los 3 ejemplos de §2.4.
- Skill recargada en el runtime (verificación: `SKILL.md` se ha leído de nuevo).

---

## 6. Riesgos

| Riesgo | Mitigación |
|---|---|
| La IA olvida el pre-save y guarda sin recall | El paso 3 del SKILL.md lo nombra explícitamente y la disciplina del §4 lo refuerza. |
| Tags concretas inventadas que no están en `tags_index()` | D5 validador las detecta y el servidor las rechaza si la forma no matchea la regex de D1. |
| Aliases no confirmados proliferan | D4 métrica `% aliases confirmed / total` y revisión periódica del IA. |
| Filtros `tags_all`/`tags_any` rompen comportamiento | Test `TestRecall_BackwardCompat` (S13) verifica que llamadas sin los nuevos campos funcionan idéntico. |

---

## 7. Aprobación

- [ ] Antonio confirma el énfasis en tag concreta obligatoria como regla dura.
- [ ] Antonio confirma el pre-save obligatorio (recall antes de remember).
- [ ] Antonio confirma `tags_index()` como herramienta de sesión (no bloqueante si el server no la expone).
- [ ] Antonio confirma los 3 ejemplos de §2.4.
- [ ] Antonio confirma el umbral de auto-relación `tag_link_strength >= 0.85`.

Cuando esté aprobado, este spec se mueve a `Status: approved` y T2b puede arrancar.