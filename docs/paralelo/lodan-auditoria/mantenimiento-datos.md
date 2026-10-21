# Mantenimiento de datos de lodan — taxonomía, alias y veredictos

Prompt independiente del trabajo en paralelo: no toca código ni worktrees. Se puede pegar en cualquier chat que tenga la herramienta MCP `lodan` conectada, antes o después de la integración.

---

Necesito que pongas orden en las etiquetas de mi memoria lodan lite. No escribas código ni toques repositorios: trabaja solo con las herramientas MCP de lodan (`status`, `tags_index`, `recall`, `get`, `revise`, `remember`).

## Skills a cargar antes de trabajar

- `lodan-memoria`: `C:\Users\4ntoniomj\.claude\skills\lodan-memoria\SKILL.md` (si la ruta no existe, búscala en `~/.4antoniomj/skill-index.md`).

## Problemas detectados en la auditoría del 2026-10-21 (lodan #1279)

1. La misma cosa con dos familias: matrículas como `vehiculo-<matricula>` (p. ej. `vehiculo-3871lny`) y como `coche-<matricula>` (p. ej. `coche-3467mcc`).
2. Etiqueta general duplicando una concreta: `#gasoil` junto a `#recurso-gasoil`.
3. Proyectos con dos nombres: `proyecto-agm-cosechas` y `proyecto-agm-cosechas-go` (y la general `agm-cosechas`).
4. 5 sugerencias de alias sin resolver en `status` (`snapshot-2026-10-19 ≈ snapshot-2026-10-18`, `pendiente ≈ pendientes`, `privacidad ≈ seguridad`, `correo ≈ correos`, `decision ≈ decisiones`).
5. 24 veredictos pendientes en `status`.

## Cómo hacerlo

1. Llama a `status` y a `tags_index` y localiza todos los casos como los anteriores (no solo los ejemplos): familias que compiten por el mismo objeto, generales que repiten una concreta, nombres de proyecto duplicados, singulares y plurales.
2. Preséntame una sola tabla con: etiqueta actual → etiqueta canónica propuesta, nº de registros afectados y motivo. Para las matrículas, recomienda una sola familia y explica por qué. Para `privacidad ≈ seguridad`, recuerda que no son sinónimos: propón rechazar el alias si no hay evidencia.
3. Para los 24 veredictos pendientes, consúltalos con `get` y preséntame cada par con tu propuesta (`contradice`, `sustituye`, `compatible` o `no_relacionado`) y una línea de motivo.
4. Hazme una sola pregunta: si apruebo la tabla y los veredictos tal cual o qué cambio. Espera mi respuesta.
5. Con mi aprobación, aplícalo en lotes con `revise` (`ops`): `alias` / `reject_alias` para etiquetas, `verdict` para los veredictos. No borres registros ni etiquetas, no uses `delete` ni `merge` sin preguntarme.
6. Comprueba el resultado con `status` y `tags_index` (0 sugerencias de alias, 0 veredictos pendientes o los que yo haya dejado aparte) y con un `recall` de prueba por cada etiqueta canónica.
7. Guarda en lodan la decisión de la taxonomía (familia canónica de matrículas y alias aprobados) con `remember`, etiqueta `proyecto-lodan-memory` y clave `proyecto-lodan-memory/taxonomia`.

## Entrega

- Tabla final aplicada (antes → después, registros afectados).
- Veredictos aplicados.
- `status` antes y después.
- Lo que quedó pendiente y por qué.
