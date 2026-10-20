# ADR-005: lodan-sin-ollama

## Contexto

`lodan` se diseñó con **dos motores de búsqueda** para el recall:

1. **Full-text** sobre `tsvector` en español (Postgres) — siempre disponible.
2. **Embeddings semánticos** vía Ollama (modelo `embeddinggemma:300m-qat-q4_0`, 768 dims) — disponible solo si Ollama está corriendo.

Históricamente Ollama corría como dependencia obligatoria: el binario `lodan serve` requería Ollama vivo para arrancar; el worker `FillPending` corría por default para calcular embeddings de records nuevos; el recall combinaba ambos caminos y degradaba a `textOnly=true` solo cuando Ollama respondía `ErrUnavailable` en runtime.

Problemas reales:

- **Portabilidad**: instalar lodan en una máquina nueva requiere instalar también Ollama (~700 MB) y descargar el modelo (~300 MB). En máquinas con recursos limitados (CI, portatiles viejos, entornos de pruebas) el binario falla al arrancar.
- **Acoplamiento operacional**: si Ollama se cae a mitad de uso, `recall` y `remember` degradan, pero el worker de mantenimiento (`FillPending`) sigue intentando y ensucia el log. La complejidad operacional es alta para un beneficio ambiguo.
- **Costo del embedding**: el modelo corre en CPU por default en máquinas sin GPU; cada embedding tarda 0.5-2 s; los records recién guardados quedan 30+ minutos sin embedding mientras `FillPending` no pasa; durante ese tiempo el recall semántico no los encuentra.
- **Cobertura real**: de los 967 records vigentes, 782 (81%) tienen `embedding IS NULL` tras meses de uso. La cobertura semántica es baja en la práctica.

Motivaciones adicionales en este proyecto:

- Agridemur (el usuario) tiene 938 registros personales/profesionales donde la taxonomía de tags concretas es **más discriminante** que la similitud coseno. El recall debe priorizar full-text + filtros de tags, no embeddings.
- El nuevo flag `--with-embeddings` ofrece el camino semántico sin obligar a tenerlo activo por default.
- Hay 766 records pendientes de embedding en producción que ahora son explícitamente opcionales.

## Decisión

`lodan` se refactoriza a `lodan-sin-ollama`:

1. **Ollama pasa de obligatorio a opcional.** El binario arranca con Ollama caído. `lodan status` reporta "Ollama: no disponible" como estado normal (no error).
2. **Flag `--with-embeddings`** (default `false`). Activa el worker `FillPending`. Si el flag está en `false`, `RunOnce` salta el path de embeddings y loggea "modo sin embeddings".
3. **Taxonomía de tags concreta como sustituto del embedding.** Las 12 familias de D2 (`proyecto-`, `persona-`, `contacto-`, `proveedor-`, `cliente-`, `finca-`, `vehiculo-`, `coche-`, `skill-`, `herramienta-`, `recurso-`, `evento-`) son el mecanismo primario de búsqueda estructurada. Filtros de primera clase (`tags_all`, `tags_any`, `date_from`, `date_to`) en `recall`.
4. **Reversibilidad.** La rama `feat/sin-ollama` no toca los índices HNSW ni la cola de pendientes en BD. El server evita esos paths por runtime. Si rollback a `main`, la BD queda idéntica.
5. **Sin pérdida de capacidades**: jerarquía de tags, veredictos con confianza, key/supersede, aliases confirmados, gitsync, Postgres se conservan.
6. **Migración de datos**: script `scripts/migrate-taxonomy/` con whitelist hardcoded de ~60 tags candidatas a concretas (recurso-*, herramienta-*, proveedor-*, cliente-*, evento-*, proyecto-*). Aplicado al data dir activo (967 records).

## Alternativas consideradas

### A. Cambiar de motor: pasar de Postgres a SQLite

Rechazado. La inversión en 938 registros, infra de gitsync y tooling operacional sobre Postgres hace que la migración no se justifique. La simplificación operativa viene de quitar Ollama, no de cambiar de motor.

### B. Mantener Ollama obligatorio pero más fácil de instalar

Rechazado. El problema es **operacional** (qué pasa cuando Ollama no está), no de instalación. Simplificar la instalación no resuelve el caso "este portatil no tiene GPU ni CPU para correr embeddings".

### C. Embeddings opcionales pero basados en API externa (OpenAI, Cohere)

Rechazado. Introduce dependencia de red, costo por consulta y problemas de privacidad (los registros se envían a un tercero). La solución on-device con Ollama sigue siendo válida cuando se activa.

### D. Query language explícito en `recall` (estilo Lucene)

Rechazado como overkill. La taxonomía de tags + filtros `tags_all`/`tags_any` cubre los casos de uso reales (búsqueda por proyecto, por fecha, por familia). Un DSL agregaría complejidad sin ganancia proporcional.

## Consecuencias

### Positivas

- **Portabilidad**: `lodan install --skip-ollama` configura lodan sin Ollama; el binario arranca donde antes fallaba.
- **Disciplina de tags**: el IA ahora clasifica con taxonomía explícita (12 familias) en lugar de depender de embeddings implícitos. La métrica `% records con tag concreta ≥80%` se vuelve un objetivo verificable.
- **Velocidad de arranque**: sin esperar ping a Ollama, el binario arranca en <1 s.
- **Reversibilidad**: la rama `feat/sin-ollama` no toca la BD físicamente; rollback a `main` deja la BD idéntica. Confirmado en commit `388f081`.
- **Búsqueda estructurada más potente**: `recall tags_all=["proyecto-erp-agm"] date_from="2026-10-01"` no requería query semántica; es SQL directo.

### Negativas

- **Cobertura de tags concreta es 76.0%** (medido en data dir activo tras migración). Por debajo del target 80% del runbook. Aceptado por Antonio (2026-10-20): los huérfanos llevan tags generales genuinas (`agridemur`, `lodan`, `proyectos`, `forma-de-trabajo`, `clientes`, `odd`, `arquitectura`, `ia`, `pruebas`, `despliegue`, `feature`, `programa`, `modelo`, `calidad`) que cubren dominios amplios. Forzar 80% cambiaría la semántica de cobertura a recurso-*. Documentado en `quality-bar.md §2.1`.
- **El camino semántico sigue ahí pero hay que activarlo explícitamente.** Quien quiera embeddings vecinos paga el costo de arrancar Ollama y descargar el modelo.
- **El worker `FillPending`** ya no corre por default. Los 766 records sin embedding se quedan así hasta que alguien corra `lodan serve --with-embeddings=true` con Ollama vivo.
- **La ficha del tema detectado ya no aparece cuando hay filtros de tags/fechas** en `recall`. Cambio de comportamiento explícito del feature (commit `eb2c506`).

### Cómo migrar

1. Backup completo (`lodan db backup --full`).
2. Apuntar a data dir nuevo o usar el activo.
3. `lodan install` con `--skip-ollama`.
4. Correr `go build -o /tmp/migrate-taxonomy.exe ./scripts/migrate-taxonomy/ && /tmp/migrate-taxonomy.exe`.
5. `lodan serve --with-embeddings=false` (default).
6. Si quieres embeddings, arranca Ollama y `lodan serve --with-embeddings=true`.

### Cómo revertir

`git checkout main` en `loreanse/lodan-memory` y rebuild. La BD queda idéntica. La rama `feat/sin-ollama` del sync-repo `4ntoniomj/lodan.sync` contiene los datos migrados; `main` no se tocó (esta versión no vivirá ahí).

## Referencias

- Specs: `docs/specs/data-storage-sin-ollama.md`, `tag-taxonomy.md`, `ai-skill-sin-ollama.md`, `quality-bar.md`, `validation-tooling.md` (en `loreanse/lodan-memory:feat/sin-ollama`).
- Runbook operativo: `odd/tasks/lodan-sin-ollama-prompt.md` y feature doc `odd/tasks/lodan-sin-ollama.md`.
- Skills: `~/.config/opencode/skills/lodan-memoria`, `lodan-taxonomy`, `lodan-quality`, `lodan-alias`.
- Memoria del proyecto: lodan #1120–#1144.
- ADRs previos referenciados: ninguno formal; decisiones similares en `AGENTS.md` y `CODEBASE.md` no se documentaron como ADR.