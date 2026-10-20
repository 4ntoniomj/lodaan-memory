# D5 — Validation tooling specification (lodan-sin-ollama)

**Estado:** propuesta de diseño. Pendiente de aprobación por Antonio.
**Owner del documento:** orquestador de `feat/sin-ollama`.
**Aprobación:** requerida para salir de Phase 0 y arrancar Phase 1.

---

## 1. Contexto

La red de seguridad automatizada. Antes de que un registro llegue al servidor, el validador Python lo inspecciona. Sin esa inspección, tags mal formadas, tags vagas, jerarquías rotas y secretos llegan a la BD y contaminan las métricas de D4.

Este spec define los **cambios a `scripts/validar_registro.py`** y los **scripts nuevos** (`sugerir_jerarquia.py`, `audit_taxonomia.py`).

---

## 2. Updates a `scripts/validar_registro.py`

Estado actual: valida `title`, `content`, `kind`, `topics`, `occurred_at`, `relate`, fechas relativas, secretos. **No valida forma de tags, ni jerarquía, ni denylist, ni concreta obligatoria.**

Cambios propuestos:

### 2.1 Nueva regex de forma — tag canónica

```python
TAG_REGEX = re.compile(r"^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$")
DENYLIST = {"misc", "general", "varios", "info", "vago", "na", "none"}
```

### 2.2 Check de forma por tag

Para cada `topic`/`tag` del item:

- Si matchea `TAG_REGEX` → OK.
- Si tiene mayúsculas → **normalizar a minúsculas** y emitir warning `tag «X» normalizada a «x»`.
- Si tiene espacios, `#`, `_` → **error** bloqueante: `tag «X» mal formada`.
- Si está en `DENYLIST` → **error** bloqueante: `tag «X» está en denylist (vaga)`.

### 2.3 Check de ≥1 tag concreta

Concatenar `topics + tags` (el script acepta ambos campos). Si **ninguna** matchea `TAG_REGEX` o todas son generales (definidas por `prefijo in {"trabajo", "personal", "lodan", "arquitectura", "odd", "feature", "runbook"}`), emitir **warning** (no error, para no romper guardados legacy): `sin tag concreta; el registro queda huérfano y rompe métricas D4`.

> **Nota:** el script no puede saber con certeza si una tag es "concreta" sin acceso a la BD; usa heurística de forma + prefijo de generales. La validación dura está en la BD (D1) y en la IA (D3).

### 2.4 Check de jerarquía con atributos

Si una tag matchea `/<atributo>` (contiene `/`), extraer la parte base (`tag.split("/")[0]`). Verificar que la base **matche `TAG_REGEX`**. Si no, **error** bloqueante: `tag «X» con atributo pero base «Y» no es tag concreta válida`.

No se valida que la base exista como topic en la BD — eso lo hace el servidor al guardar.

### 2.5 Check de `key`

Si `key` está presente:

- Debe matchear `TAG_REGEX + "/<atributo>"`.
- Si no, **error** bloqueante: `key «X» debe tener forma <tag-concreta>/<atributo>`.
- Si `kind == "event"` y `key` presente, **warning**: `eventos emitidos no deberían llevar key (one-shot)`.

### 2.6 Check de jerarquía base (≥3 atributos)

Si una tag tiene más de 3 atributos (más de 3 segmentos separados por `/`), **error** bloqueante: `tag «X» tiene más de 3 atributos jerárquicos (máx 3)`.

### 2.7 Output ampliado

```json
{
  "valido": bool,
  "errores": ["..."],
  "avisos": [
    "registro 1: tag «X» normalizada a «x»",
    "registro 1: sin tag concreta; el registro queda huérfano"
  ],
  "tags_normalizadas": [["X", "x"]]
}
```

El campo `tags_normalizadas` permite al IA aplicar las normalizaciones automáticamente antes de enviar al servidor.

### 2.8 Salida de error con códigos

Sin cambios: 0 válido, 1 con errores, 2 entrada ilegible. Añadir 3 = "denegado por secrets detectados".

---

## 3. Integración con el flujo de la IA

Documentar en `SKILL.md` (Paso 3) y `EXAMPLE.md`:

1. El IA construye la lista de items.
2. El IA escribe el JSON a un archivo temporal y corre `python3 scripts/validar_registro.py --file /tmp/registro.json`.
3. Si `errores`: el IA corrige y vuelve a correr.
4. Si `avisos`: el IA puede:
   - Aplicar `tags_normalizadas` automáticamente.
   - Decidir si el aviso es bloqueante para el caso (p.ej. "sin tag concreta" puede aceptarse si el usuario explícitamente pide un record general).
5. Si `valido: true`, el IA envía `remember` con los items (con tags normalizadas aplicadas).
6. Si hay secretos detectados, **bloquea** y reporta al usuario.

---

## 4. Tests del validador

Ubicación: `scripts/test_validar_registro.py` (ya existe, ampliar).

Cobertura obligatoria:

| Caso | Input | Esperado |
|---|---|---|
| Tag válida | `proyecto-erp-agm` | OK |
| Tag con atributo | `coche-volvo-s60/matricula` | OK |
| Tag con mayúsculas | `Proyecto-Erp` | Warning + normalización |
| Tag con espacios | `mi proyecto` | Error bloqueante |
| Tag con `_` | `proyecto_erp` | Error bloqueante |
| Tag vaga | `general`, `misc` | Error bloqueante |
| Sin tag concreta | Solo `#trabajo #arquitectura` | Warning |
| Tag con 4 atributos | `coche-volvo-s60/a/b/c/d` | Error bloqueante |
| Key bien formado | `coche-volvo-s60/matricula` | OK |
| Key mal formado | `matricula` (sin base) | Error bloqueante |
| Event con key | `{kind: "event", key: "..."}` | Warning |
| Relate con id inexistente | `{relate: [{id: 99999}]}` | OK (servidor valida) |
| Secreto | title contiene "API key" | Bloqueante, código 3 |
| Fecha relativa | content "ayer desayuné" | Warning (ya existe) |

---

## 5. Scripts nuevos (opcionales, fuera de alcance por ahora)

**Decisión:** mientras estamos en pruebas y consolidación de la v1 de Phase 0, dejamos los scripts auxiliares para una iteración posterior. Lo que hace falta ahora es solo el core del validador (§2) y su integración con la IA (§3). Las herramientas auxiliares se documentan aquí como referencia futura, pero no se programan en este round.

### 5.1 `scripts/sugerir_jerarquia.py` (futuro)

Dado un item (título + contenido), sugiere tags concretas apropiadas por heurística:

- "matrícula XXXX" → `vehiculo-xxxx` (sin guiones).
- "coche Volvo S60" → `coche-volvo-s60`.
- "cliente ACME" → `cliente-acme`.
- "proyecto X" → `proyecto-x`.

Y atributos si aplica: "matrícula" → `<etí>/matricula`, "seguro" → `<etí>/seguro`.

Output: JSON con sugerencias, no bloquea. La IA decide.

### 5.2 `scripts/audit_taxonomia.py` (futuro)

Corre SQL sobre la BD activa y emite un reporte:

- Tags concretas con 1 solo uso.
- Tags generales usadas como si fueran concretas.
- Records sin tag concreta (huérfanos).
- Aliases `suggested` con más de 30 días.
- Veredictos pendientes (confidence < 0.7 y > 7 días).
- Contradicciones abiertas.

Output: JSON + tabla Markdown.

Uso futuro: una vez por semana o bajo demanda, cuando el sistema esté estable y necesitemos auditar.

---

## 6. Migración: tolerar legacy

Hasta que el script de T4 migre los 938 legacy a tags concretas, el validador **debe tolerar** registros sin tag concreta. Por eso §2.3 es **warning**, no error.

Esto se documenta explícitamente en el output: `legacy: el registro no tiene tag concreta pero se permite (modo compatibilidad hasta <FECHA_T4>)`.

Una vez T4 completa, el validador se actualiza para que §2.3 sea **error bloqueante**.

---

## 7. Riesgos

| Riesgo | Mitigación |
|---|---|
| El validador es muy estricto y rompe guardados que antes funcionaban | §6: tolerar legacy hasta T4. |
| El validador es muy laxo y deja pasar tags que el servidor rechaza | El servidor tiene D1 CHECK como red final. El validador es primera línea, no única. |

---

## 8. Aprobación

- [ ] Antonio confirma el check de ≥1 tag concreta como **warning** (no error) hasta T4.
- [ ] Antonio confirma la denylist: `misc`, `general`, `varios`, `info`, `vago`, `na`, `none`.
- [ ] Antonio confirma que §5 (scripts auxiliares) queda fuera del alcance de este feature.
- [ ] Antonio confirma los tests §4 como cobertura mínima.

Cuando esté aprobado, este spec se mueve a `Status: approved` y los updates a `validar_registro.py` pueden arrancar (parte de T2b).