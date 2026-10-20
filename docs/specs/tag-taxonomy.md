# D2 — Tag taxonomy specification (lodan-sin-ollama)

**Estado:** propuesta de diseño. Pendiente de aprobación por Antonio.
**Owner del documento:** orquestador de `feat/sin-ollama`.
**Aprobación:** requerida para salir de Phase 0 y arrancar Phase 1.

---

## 1. Contexto

Sin embeddings obligatorios, la **calidad del recall** depende de la disciplina de tags. Cada record necesita **al menos una tag concreta** de las 12 familias documentadas aquí para que:

- `recall` con `tags_all` / `tags_any` funcione (sin tag concreta no hay forma de filtrar bien).
- El IA pueda inferir relaciones entre records (comparten `#coche-volvo-s60` → están relacionados).
- Las búsquedas full-text no dependan únicamente de paráfrasis en el contenido.
- Las métricas de D4 (`% records con tag concreta ≥80%`) sean evaluables.

Este spec define la taxonomía completa: las 12 familias, las reglas formales, los aliases, los anti-patrones, y los edge cases (jerarquía con atributos, híbridas legítimas, snapshots).

---

## 2. Reglas universales

Aplican a **toda tag concreta** que entra al sistema:

1. **Forma:** regex `^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$` (enforced por CHECK en `topics.slug`, ver D1).
2. **Mínimo una concreta por record.** Tags generales (`#trabajo`, `#personal`, `#lodan`) son complemento, no sustituto.
3. **Slug kebab-case.** Guiones para separar segmentos, slash `/` para jerarquía.
4. **Sin tildes.** `proyecto-erp` no `proyecto-érp`. Las búsquedas toleran apóstrofos (`websearch_to_tsquery` los maneja), pero la forma canónica es sin tildes.
5. **Singular canónico.** `#cosecha` no `#cosechas`, `#contacto` no `#contactos`, `#vehiculo` no `#vehiculos`. Plurales van a `tag_aliases` como `confirmed`.
6. **Hasta 3 atributos jerárquicos.** `#coche-volvo-s60/matricula` (1 atributo), `/itv` (otro atributo, máximo 3 total).
7. **Consistencia entre memoria y topics.** Si guardás un record con `#proyecto-erp-agm`, el topic `proyecto-erp-agm` debe existir o crearse en la misma operación.

---

## 3. Las 12 familias

| Familia | Forma | Para qué | Ejemplo válido | Anti-patrón |
|---|---|---|---|---|
| **`proyecto-<slug>`** | `proyecto-<slug>` | Proyectos con repo o worktree. | `#proyecto-erp-agm`, `#proyecto-lodan-memory`, `#proyecto-finanzas-hogar` | `#proyecto` (sin slug), `#Project-Erp` (mayúscula) |
| **`persona-<slug>`** | `persona-<slug>` | Vos mismo y contactos personales. | `#persona-antonio`, `#persona-maria-lopez` | `#persona` (sin slug), `#antonio` (sin prefijo de familia) |
| **`contacto-<empresa>-<persona>`** | `contacto-<empresa>-<persona>` | Contactos profesionales de una empresa. | `#contacto-florette-pedidos`, `#contacto-bbva-asesor` | `#contacto-pedidos-florette` (orden invertido), `#contacto-juan` (sin empresa) |
| **`proveedor-<slug>`** | `proveedor-<slug>` | Empresas que te venden. | `#proveedor-florette`, `#proveedor-endesa` | `#proveedor-luz-endesa` (mezcla familia: provide service, ver §3.1) |
| **`cliente-<slug>`** | `cliente-<slug>` | Empresas a las que vendés. | `#cliente-mercadona`, `#cliente-agridemur-francia` | `#cliente-mio` (vago) |
| **`finca-<slug>`** | `finca-<slug>` | Fincas Agridemur. | `#finca-royalveg`, `#finca-rebelao` | `#finca-1` (slug no descriptivo) |
| **`vehiculo-<matrícula>`** | `vehiculo-<matrícula>` | Coches de la flota. La matrícula sin guiones. | `#vehiculo-4860ngl`, `#vehiculo-1234abc` | `#vehiculo-4860-ngl` (con guiones, debe ir sin), `#vehiculo-volvo` (esto es `coche`, no `vehiculo`) |
| **`coche-<slug>`** | `coche-<slug>` | Coches personales con atributos evolutivos. | `#coche-volvo-s60`, `#coche-seat-ateca` | `#coche` (sin slug) |
| **`skill-<slug>`** | `skill-<slug>` | Skills instaladas o que aprendés. | `#skill-lodan-memoria`, `#skill-react`, `#skill-cocina` | `#skill-programacion` (demasiado amplio, preferí `#skill-react` o similar) |
| **`herramienta-<slug>`** | `herramienta-<slug>` | Software o herramientas que usás. | `#herramienta-vscode`, `#herramienta-postgres`, `#herramienta-opencode` | `#herramienta-ia` (vago) |
| **`recurso-<slug>`** | `recurso-<slug>` | Rutas de red, archivos clave, URLs. | `#recurso-backup-synology`, `#recurso-doc-prd` | `#recurso-carpeta` (vago) |
| **`evento-<slug>`** | `evento-<slug>` | Hitos importantes (releases, bodas, viajes). | `#evento-lanzamiento-v1`, `#evento-boda-maria-juan` | `#evento-ayer` (usá fecha en el contenido, no en la tag) |

### 3.1 Edge case — `vehiculo-` vs `coche-`

Reglas formales:

- **`vehiculo-<matrícula>`**: coche identificado por matrícula legal. La matrícula es inmutable. Atributos jerárquicos: `/seguro`, `/itv`, `/revision`.
- **`coche-<slug>`**: coche identificado por nombre/modelo. Atributos jerárquicos: `/matricula`, `/color`, `/km`.

Si tenés un Volvo S60 matrícula 4860 NGL:

- `#vehiculo-4860ngl` para datos legales (seguro, ITV).
- `#coche-volvo-s60` para datos del modelo (color, km, mantenimientos).

Si la matrícula es estable y conocida, podés usar **solo** `#vehiculo-<matrícula>` y poner el modelo en el `content`. Pero si necesitás navegar por modelo (p.ej. "todos los Volvos"), necesitás `#coche-<slug>` también.

### 3.2 Edge case — `proveedor-<slug>` vs `skill-recurso-`

- `#proveedor-florette` es la **empresa**.
- `#herramienta-florette-web` (si existiera) sería el **software** de Florette.
- `#recurso-florette-catalogo` sería el **archivo/URL** del catálogo.

Mezclar familia y producto en una sola tag es anti-patrón.

---

## 4. Tags generales vs concretas

**Concretas:** las 12 familias. Identifican **una entidad específica** (un proyecto, una persona, una finca, un evento).

**Generales:** no atan a una entidad específica. Son contexto o taxonomía cruzada.

| Generales válidas | Notas |
|---|---|
| `#trabajo` | Cobertura: cosas profesionales. Complemento, no sustituto. |
| `#personal` | Cobertura: vida personal. |
| `#lodan` | Cobertura: sistema lodan (meta). |
| `#arquitectura` | Cobertura: decisiones arquitectónicas. |
| `#odd` | Cobertura: flujo Organic Driven Development. |
| `#feature` | Cobertura: trabajo de feature. |
| `#runbook` | Cobertura: procedimientos operativos. |
| `#recurso` | ⚠️ También existe `recurso-<slug>` como familia. El general `#recurso` se mantiene como sinónimo de "es un recurso" — no confundir. |

Reglas:

1. **Todo record debe tener ≥1 tag concreta.** Esto es un gate duro (D5 validador + D1 CHECK no la imponen automáticamente porque depende del contenido del record, no de la BD).
2. **Las generales se recomiendan pero no son obligatorias.**
3. **Las generales no sustituyen a las concretas.** Un record `#trabajo #arquitectura` sin concreta es huérfano.

---

## 5. Jerarquía con atributos

Forma: `<familia>-<slug>/<atributo>` con hasta 3 atributos.

Reglas:

1. **Atributo en minúscula, sin tildes, kebab-case.** `#coche-volvo-s60/matricula`, no `#coche-volvo-s60/Matrícula`.
3. **El atributo es evolutivo por convención.** Datos que cambian con el tiempo del objeto: `matricula`, `km`, `seguro`, `itv`, `revision`.
4. **La parte base (`<familia>-<slug>`) debe existir como topic.** Si `#coche-volvo-s60/matricula` se guarda, debe existir `#coche-volvo-s60` también. D5 validador lo verifica.
5. **Máximo 3 atributos.** `#coche-volvo-s60/matricula/itv/seguro` es válido (3). `#coche-volvo-s60/a/b/c/d` no.
6. **Búsqueda por atributo.** `recall tags_all=["coche-volvo-s60/matricula"]` filtra solo records con ese atributo exacto.

Ejemplo trabajado:

```
Record: "El Volvo S60 tiene la matrícula 4860 NGL"
Tags: #coche-volvo-s60, #coche-volvo-s60/matricula

Record: "ITV del Volvo pasada el 2026-09-15"
Tags: #coche-volvo-s60, #coche-volvo-s60/itv

Record: "Seguro del Volvo con Mutua Madrileña hasta 2027-03"
Tags: #coche-volvo-s60, #coche-volvo-s60/seguro
```

---

## 6. Aliases

### 6.1 Regla del singular canónico

| Canónico | Plurales detectados | Estado propuesto |
|---|---|---|
| `#cosecha` | `#cosechas` | confirmed |
| `#contacto` | `#contactos` | confirmed |
| `#vehiculo` | `#vehiculos` | confirmed |
| `#finca` | `#fincas` | confirmed |
| `#cliente` | `#clientes` | confirmed |
| `#proveedor` | `#proveedores` | confirmed |
| `#persona` | `#personas` | confirmed |
| `#proyecto` | `#proyectos` | confirmed |
| `#skill` | `#skills` | confirmed |
| `#herramienta` | `#herramientas` | confirmed |
| `#evento` | `#eventos` | confirmed |
| `#recurso` | `#recursos` | confirmed |

**Total: 12 pares.** El feature doc hablaba de 10 (4 del servidor + 6 del muestreo) — esta tabla cubre las 12 con criterio uniforme (todas las familias que tienen plural irregular o usado). Los 10 del runbook original son un subset a confirmar.

### 6.2 Cómo se aplican

- `tag_aliases.alias = 'cosechas'`, `tag_aliases.canonical = 'cosecha'`, `state = 'confirmed'`.
- El IA, al guardar, usa siempre el canónico. Si por error usa el alias, el validador (D5) lo corrige antes de guardar.

---

## 7. Tags snapshot/evento

Caso: `#snapshot-<YYYYMMDD>` se canonicaliza como `#snapshot` con la fecha en el **contenido** del record, no en la tag.

**Por qué:** una tag por día genera explosión combinatorial y dificulta `tags_all`/`tags_any`. Mejor un solo `#snapshot` con el contenido que tenga la fecha ISO 8601.

Ejemplo:

```
❌ #snapshot-2026-10-20: "Estado del proyecto al 2026-10-20: X, Y, Z"
✅ #snapshot #proyecto-erp-agm: "Estado del proyecto al 2026-10-20: X, Y, Z"
```

`date_from=2026-10-01, date_to=2026-10-20, tags_all=["snapshot"]` filtra snapshots del rango.

---

## 8. Tags híbridas legítimas

Estas tags están en uso y NO se pueden romper. Reglas documentadas:

| Tag actual | Equivalencia | Tratamiento |
|---|---|---|
| `#erp-agm` | ≡ `#proyecto-erp-agm` | Alias `confirmed`. El IA usa el canónico `#proyecto-erp-agm`. Si aparece `#erp-agm` solo, el validador lo convierte antes de guardar. |
| `#agm-cosechas` | sub-proyecto de `#proyecto-erp-agm` | Tag concreta propia `#proyecto-agm-cosechas` (forma completa) más alias `agm-cosechas → proyecto-agm-cosechas`. |
| `#agridemur-marketing` | sub-proyecto de `#proyecto-erp-agm` | `#proyecto-agridemur-marketing` canónico, alias `#agridemur-marketing → #proyecto-agridemur-marketing`. |

Decisión: **canonicalizar al form completo** en este feature. Los aliases `confirmed` se crean en T4 (migración de datos).

---

## 9. Anti-patrones

Tags que el IA debe **rechazar o convertir** antes de guardar:

| Anti-patrón | Ejemplo | Acción |
|---|---|---|
| Tag vaga | `#misc`, `#general`, `#varios`, `#info` | Denylist: el validador (D5) bloquea con error. |
| Mayúsculas | `#Coche-Volvo` | Normalizar a minúsculas. |
| Espacios | `#mi proyecto` | Denylist. |
| Mayúsculas en jerarquía | `#coche-volvo-s60/Matricula` | Normalizar a minúsculas. |
| Mezcla español/inglés inconsistente | `#proyecto` + `#project-erp` | Decidir por record; recomendar español. |
| Tag que duplica contenido | título "Coche Volvo S60" + tag `#coche-volvo-s60` (bien), pero `#coche-volvo` cuando ya hay `#coche-volvo-s60` (redundante) | Warning, no error. El IA decide. |
| Tag sin slug | `#proyecto`, `#coche`, `#persona` (sin la segunda parte) | Denylist. |
| Más de 3 atributos | `#coche-volvo-s60/a/b/c/d` | Denylist. |
| Guiones bajos | `#proyecto_erp` | Denylist. |

---

## 10. Validación automatizada (resumen, no implementación)

| Capa | Mecanismo | Efecto |
|---|---|---|
| BD (D1) | CHECK en `topics.slug` | Tag `tag=malo` no se puede insertar como topic. |
| Validador Python (D5) | Regex + denylist + check de jerarquía base | Tag mal formado detectado antes de `remember`. Error bloquea guardado. |
| IA (D3) | Disciplina de la skill | El IA no intenta guardar tags mal formadas. |

---

## 11. Métricas (referencia D4)

Estas métricas se miden **post-migración** (T4) y **post-uso** (semanas después del merge):

- `% records con ≥1 tag concreta` — target **≥80%** post-migración, **≥95%** tras 30 días.
- `% tags concretas reutilizadas (>1 uso)` — target **≥60%**.
- `% records huérfanos (sin relations, sin tags concretas reutilizadas)` — target **<20%**.

---

## 12. Aprobación

- [ ] Antonio confirma las 12 familias. ¿Falta alguna? ¿Sobra alguna?
- [ ] Antonio confirma el subset de 12 aliases de §6.1.
- [ ] Antonio confirma los anti-patrones de §9. ¿Agregar/quitar?
- [ ] Antonio confirma la regla "snapshot = topic + fecha en contenido".
- [ ] Antonio confirma la canonicalización de las híbridas de §8.

Cuando esté aprobado, este spec se mueve a `Status: approved` y Phase 1 puede arrancar.