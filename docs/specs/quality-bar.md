# D4 — Quality bar specification (lodan-sin-ollama)

**Estado:** propuesta de diseño. Pendiente de aprobación por Antonio.
**Owner del documento:** orquestador de `feat/sin-ollama`.
**Aprobación:** requerida para salir de Phase 0 y arrancar Phase 1.

---

## 1. Contexto

Sin embeddings obligatorios, la **métrica de éxito** ya no es "el servidor te entiende" sino "la IA etiqueta bien y vos no perdés nada en el recall". Este spec define cómo medimos eso y qué cuenta como feature cerrado.

Las métricas se miden en **tres momentos**:

1. **Baseline pre-migración (T1):** antes de T4. Snapshot del estado actual.
2. **Post-migración (T4):** después del script de re-tagging y aliases. Métricas deben cumplir target.
3. **Post-uso (30 días después del merge):** métricas se mantienen durante 30 días consecutivos.

---

## 2. Métricas de cobertura

### 2.1 `% records con ≥1 tag concreta`

- **Definición:** records activos (`status='active'`) que tienen al menos un `memory_topics` cuyo `topics.slug` matchea la regex de D2.
- **Target post-migración:** **≥80%**.
- **Target post-uso (30 días):** **≥95%**.
- **Por qué:** sin tag concreta, el record no es navegable. Es ruido. La IA no lo va a recuperar salvo full-text exacto, lo cual es frágil.

### 2.2 `% tags concretas reutilizadas (>1 uso)`

- **Definición:** `count(tags concretas con >1 record activo) / count(tags concretas totales)`.
- **Target post-migración:** **≥60%**.
- **Por qué:** tags de un solo uso sugieren que la IA está inventando tags sin consistencia. Una taxonomía bien aplicada tiene la mayoría de tags reutilizadas.

### 2.3 `% records huérfanos (sin relations, sin tags concretas reutilizadas)`

- **Definición:** records activos con 0 `memory_relations` Y 0 tags concretas que se repitan en otros records.
- **Target post-migración:** **<20%**.
- **Por qué:** un record huérfano está desconectado del grafo. La taxonomía y las relaciones lo mantienen navegable.

---

## 3. Métricas de búsqueda

### 3.1 `Search success rate`

- **Definición:** `count(recalls que devolvieron ≥1 resultado en `Resultados`) / count(recalls totales)`.
- **Target:** **≥95%** en queries reales (no pruebas sintéticas).
- **Medición:** log del server con `q=...&empty=true` y agregación semanal. No se automatiza el corpus de prueba — son queries reales del usuario durante el día.
- **Falla analizable:** cuando un recall devuelve `Sin resultados`, la IA registra el motivo (`falta tag`, `falta full-text`, `combinación de filtros vacía`).

### 3.2 `Recall "Connected" usage rate`

- **Definición:** `count(recalls donde el bloque `Conectado por etiquetas:` fue usado por la IA para responder) / count(recalls totales)`.
- **Target:** **≥40%**.
- **Por qué:** la IA que no usa `Connected` está desperdiciando el caso más importante — descubrir relaciones que no se piden.

### 3.3 `Search failure analysis`

- **Definición:** lista de queries que devolvieron "sin resultados" en las últimas 4 semanas.
- **Target:** la lista se mantiene **<10%** del total de queries.
- **Acción:** cada falla se categoriza (falta tag, falta full-text, error de filtros) y se priorizan remediaciones.

---

## 4. Métricas de duplicación

### 4.1 `Duplicate rate`

- **Definición:** `count(remember calls donde la IA decide no guardar porque hay duplicado por hash o topic+contenido) / count(remember calls totales)`.
- **Target medido pero no fijo:** **>0%** post-migración, idealmente **10-20%**.
- **Por qué:** tasa 0% significa que la IA no está mirando antes de guardar. Tasa >30% significa que está en modo "no guardo nada".

### 4.2 `Hash dedup hits`

- **Definición:** `count(items rechazados por hash exacto) / count(items totales en remember)`.
- **Target:** variable. Mide el costo del mecanismo.

### 4.3 `Topic+content dedup hits`

- **Definición:** `count(items rechazados por topic + contenido solapado) / count(items totales)`.
- **Target:** variable. Mide la calidad del nuevo mecanismo de dedup.

---

## 5. Aceptación del feature

El feature **no está cerrado** hasta que se cumplan todas estas condiciones:

- [ ] Las 5 métricas de cobertura y búsqueda (§2.1, §2.2, §2.3, §3.1, §3.3) cumplidas durante **30 días consecutivos** post-merge.
- [ ] `Search success rate ≥95%` medido sobre uso real (no sintético).
- [ ] Smoke test de 24 horas post-merge pasa sin regresiones.
- [ ] Cero regresiones en `go test ./...`.
- [ ] ADR publicado (D5 del runbook, no de Phase 0).
- [ ] Skill `lodan-memoria` actualizada (T2b) y carga correctamente.

---

## 6. Cómo se miden

### 6.1 Script `scripts/metricas.sh`

Lee el data dir activo (`$LODAN_DATA_DIR` o autodetectado vía `lodan status`). Corre SQL directo sobre Postgres. Emite un reporte en stdout + un JSON estructurado.

Salida ejemplo:

```json
{
  "total_records": 938,
  "records_with_concrete_tag": 814,
  "coverage_pct": 86.8,
  "concrete_tags_total": 87,
  "concrete_tags_reused": 61,
  "reuse_pct": 70.1,
  "orphan_records": 142,
  "orphan_pct": 15.1,
  "search_success_rate": null,
  "search_connected_usage_rate": null
}
```

Las métricas de búsqueda (§3) requieren log parsing, no SQL directo. El script las lee del log del server (`server.log` o equivalente) si está disponible; si no, devuelve `null` para esas métricas.

### 6.2 Comparación pre/post con snapshots

Cada métrica se guarda en `metrics/<YYYY-MM-DD>.json` después de correr el script. La comparación pre/post se hace con `scripts/metricas_diff.sh` que toma dos snapshots y emite las deltas.

### 6.3 Pipeline de medición

1. **Baseline (T1):** correr `scripts/metricas.sh` sobre el data dir activo ANTES de T4. Guardar en `metrics/baseline.json`.
2. **Post-migración (T4):** correr sobre el data dir de prueba post-migración. Guardar en `metrics/post-migration.json`. Verificar §2.1 ≥80%.
3. **Post-uso (semanal, durante 30 días):** correr una vez por semana. Guardar en `metrics/YYYY-MM-DD.json`. Verificar que las 5 métricas se mantienen.

### 6.4 Tests del script

- Test unitario de cada query SQL (mock DB).
- Test de integración contra una BD de prueba con datos conocidos.
- Test del formato JSON.

---

## 7. Riesgos

| Riesgo | Mitigación |
|---|---|
| Baseline pre-migración tiene % muy bajo (<50%) | El target ≥80% post-migración puede ser inalcanzable con datos legacy. Mitigación: en T4 se hace un esfuerzo extra de re-tagging manual para los huérfanos más usados. |
| El usuario genera pocas queries reales en el periodo | Las métricas §3 se relajan: se reportan como `n/a` si el tamaño de muestra es <30 queries/semana. |
| El servidor no loguea queries por default | Se activa el log en `lodan serve --log-queries` durante el periodo de medición. |
| Métricas se cumplen pero la calidad real es mala | Revisión manual semanal por Antonio: top 10 queries, top 10 records huérfanos, top 10 fallas de búsqueda. |

---

## 8. Aprobación

- [ ] Antonio confirma los targets §2.1 ≥80%, §2.2 ≥60%, §2.3 <20%, §3.1 ≥95%.
- [ ] Antonio confirma la ventana de 30 días consecutivos post-merge.
- [ ] Antonio confirma el script `scripts/metricas.sh` y la ubicación `metrics/`.
- [ ] Antonio confirma el smoke test de 24 horas como gate final.

Cuando esté aprobado, este spec se mueve a `Status: approved` y Phase 1 puede arrancar.