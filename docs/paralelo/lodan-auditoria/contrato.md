# Contrato congelado — lodan-auditoria

Fecha: 2026-10-21 · Commit base: `adf2f9d` · Rama de integración: `feat/lodan-auditoria`

Origen: auditoría de lodan lite del 2026-10-21 (lodan #1279). Este contrato cubre las propuestas 1 a 7. La propuesta 8 (limpieza de taxonomía) es trabajo de datos sin código y va en un prompt aparte (`mantenimiento-datos.md`).

## 1. Objetivo

Que recall encuentre lo que existe aunque la consulta lleve o no tildes, tenga una errata o una palabra de más, que permita listar por fecha, y que remember impida etiquetas sin familia concreta, casi duplicados y texto mal codificado. Medible: los criterios S# de las dos partes pasan sobre la suite `go test ./...` y en el QA de costuras.

## 2. Partes

| # | Parte | Rama | Worktree | Archivos que posee (exclusivo) | Rango reservado |
|---|-------|------|----------|--------------------------------|-----------------|
| A | busqueda | `feat/lodan-auditoria-busqueda` | `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\busqueda` | `internal/recall/**`, `internal/mcptools/read.go`, `internal/mcptools/recall_test.go`, `internal/mcptools/server.go`, `internal/benchmark/**` | migración `0005` |
| B | escritura | `feat/lodan-auditoria-escritura` | `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\escritura` | `internal/memory/**`, `internal/mcptools/write.go`, `internal/mcptools/preview.go`, `cmd/lodan/db.go` | migración `0006` |

Archivos nuevos permitidos: A crea `internal/database/migrations/0005_search.sql`; B crea `internal/database/migrations/0006_near_duplicates.sql`, `cmd/lodan/fix_encoding_test.go` y archivos nuevos dentro de `internal/memory/`.

## 3. Grafo de dependencias

Vacío. A solo cambia la lectura (recall); B solo cambia la escritura (remember y `lodan db`). Ninguna usa código nuevo de la otra. Las dos crean la extensión `pg_trgm` con `CREATE EXTENSION IF NOT EXISTS`, que es idempotente.

## 4. Contrato técnico

### Herramienta MCP `recall` (parte A)

Campo nuevo en la entrada, opcional:

```go
Mode string `json:"mode,omitempty" jsonschema:"rank (por defecto): por relevancia; list: por fecha, lo más reciente primero"`
```

- Valores: `""`/`"rank"` (comportamiento actual) y `"list"`.
- Otro valor → error de herramienta con el texto exacto: `mode no válido: usa rank o list`.
- `list`: orden `occurred_at DESC, id DESC`; `limit` por defecto 20, máximo 50; sin ficha del tema ni «Últimos eventos»; con `query` vacía exige al menos un filtro (tags o fechas); si no hay ninguno → error `mode list necesita query o algún filtro (tags_all, tags_any, date_from, date_to)`.

Líneas de aviso nuevas (texto exacto, cada una como primera línea del bloque afectado):

- `(sin coincidencia con todas las palabras: resultados con alguna de ellas)`
- `(sin coincidencia exacta: resultados parecidos)`

La línea existente `(solo búsqueda por texto: el servicio de embeddings no está disponible)` aparece **una sola vez por respuesta** de recall (al principio), no una vez por bloque.

### Herramienta MCP `remember` (parte B)

Campo nuevo en cada item, opcional:

```go
Force bool `json:"force,omitempty" jsonschema:"true: guarda aunque sea casi idéntico a otro registro"`
```

Líneas de salida nuevas (texto exacto; `N` es la posición del item empezando en 1, `#id` el registro existente, el parecido con dos decimales):

- `item N no guardado: falta una etiqueta concreta (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-)`
- `item N no guardado: casi idéntico a #id (parecido 0.93); si es un cambio, usa revise update o key, o force: true`
- `  ¿duplicado?: #id (parecido 0.82)` (línea bajo el `#id guardado`, antes de las `revisa:`)
- `texto reparado (codificación)` como parte de la línea `#id guardado · …`

La parte `embedding y parecidos pendientes (se calculan en segundo plano)` solo aparece cuando el servidor corre con embeddings (`WithEmbeddings` true).

### Comando `lodan db fix-encoding` (parte B)

```text
lodan db fix-encoding            # simulación: no cambia nada
lodan db fix-encoding --apply    # repara
```

- Simulación: una línea `#id título` por registro vigente con texto mal codificado y al final `N registros con texto mal codificado (simulación: no se ha cambiado nada; usa --apply)`. Con 0: `0 registros con texto mal codificado`.
- `--apply`: repara título y contenido y termina con `N registros reparados`. Si el contenido reparado coincide con el hash de otro registro vigente: `#id omitido: quedaría duplicado de #otro`, y no lo toca.
- Código de salida 0 en ambos casos; 1 si la base de datos no responde.

### Reglas de dominio

- **Etiqueta concreta**: una etiqueta lo es si casa con `^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]`, después de normalizarla igual que hoy (minúsculas, alias confirmados resueltos).
- **Texto mal codificado (mojibake)**: texto UTF-8 que fue leído como Windows-1252 y vuelto a guardar. Se considera reparable si contiene `Ã` o `Â` y, al convertir el texto completo a bytes Windows-1252 (los cinco bytes que Windows-1252 no define, 0x81, 0x8D, 0x8F, 0x90 y 0x9D, se aceptan tal cual), el resultado es UTF-8 válido y distinto del original. Ejemplos: `AlbarÃ¡n` → `Albarán`, `mediciÃ³n` → `medición`, `RaÃºl` → `Raúl`, `Ã­` (con guion blando) → `í`. Si no es reparable, se deja intacto (por ejemplo `PÃO` escrito a propósito).
- **Casi duplicado**: `similarity(lower(nuevo), lower(existente))` de `pg_trgm` sobre el contenido, contra registros vigentes. `≥ 0.90` → no se guarda (salvo `force`). Entre `0.75` y `0.90` y al menos una etiqueta en común → se guarda con la línea `¿duplicado?`. Se excluye el registro que el propio item sustituye por `key`.

### Esquema de base de datos

- `0005_search.sql` (A): `CREATE EXTENSION IF NOT EXISTS unaccent;`, `CREATE EXTENSION IF NOT EXISTS pg_trgm;`, configuración de búsqueda `lodan_es` (copia de `spanish` con `unaccent` antes de `spanish_stem` para `word`, `hword`, `hword_part`, `asciiword`, `asciihword`, `hword_asciipart`) y la columna `memories.tsv` regenerada como `to_tsvector('lodan_es', title || ' ' || content)` con su índice GIN. Toda consulta de texto usa `'lodan_es'`.
- `0006_near_duplicates.sql` (B): `CREATE EXTENSION IF NOT EXISTS pg_trgm;` y `CREATE INDEX IF NOT EXISTS memories_content_trgm_idx ON memories USING gin (lower(content) gin_trgm_ops) WHERE status = 'active';`

### Fixtures (las mismas para las dos partes)

| id lógico | título | contenido | etiquetas | occurred_at |
|---|---|---|---|---|
| F1 | `Albarán de gasoil` | `Se apunta siempre: fecha, matrícula, empresa y litros.` | `recurso-gasoil` | 2026-10-01 |
| F2 | `Matrícula 3871LNY` | `Matrícula 3871LNY: empresa VDM, concepto RECOL BK.` | `vehiculo-3871lny`, `recurso-gasoil` | 2026-10-05 |
| F3 | `Lectura del gasoil` | `Medición matinal del depósito de gasoil.` | `recurso-gasoil` | 2026-10-03 |
| F4 | `AlbarÃ¡n antiguo` | `VersiÃ³n definitiva del albarÃ¡n.` (mojibake) | `recurso-gasoil` | 2026-09-30 |

## 5. Costuras

- `README.md`: A solo edita la sección de `recall` (documentar `mode`, avisos y búsqueda sin tildes); B solo edita la sección de `lodan db` (documentar `fix-encoding`) y la de `remember`. No se crea ningún archivo compartido nuevo.
- Dependencias Go nuevas: ninguna. `golang.org/x/text` ya es indirecta; si B la usa directamente para Windows-1252, ejecuta `go get golang.org/x/text@v0.41.0` (la misma versión que ya fija `go.sum`); A no toca `go.mod`.

## 5b. Archivos de test por parte

- A: `internal/recall/audit_search_test.go`, `internal/mcptools/recall_mode_e2e_test.go`
- B: `internal/memory/audit_write_test.go`, `internal/mcptools/write_audit_e2e_test.go`, `cmd/lodan/fix_encoding_test.go`

## 6. Criterios de aceptación por parte

### Parte A — busqueda

- S1 Tildes: con F1, `recall query="albaran"`, `query="albarán"` y `query="ALBARAN"` devuelven F1 en los tres casos.
- S2 Palabras de más: con F1–F3, `recall query="albaran gasoil inexistentepalabra"` devuelve F1 y su bloque empieza por `(sin coincidencia con todas las palabras: resultados con alguna de ellas)`. Si la búsqueda con todas las palabras encuentra algo, no hay aviso y el resultado es el de hoy.
- S3 Erratas: con F2, `recall query="matricla"` devuelve F2 con el aviso `(sin coincidencia exacta: resultados parecidos)`. Solo se usa cuando S2 tampoco encuentra nada.
- S4 Filtros antes del límite: con 2100 registros vigentes, de los que 5 (los más antiguos) llevan `proyecto-filtro`, `recall query="" tags_all=["proyecto-filtro"]` devuelve los 5; con `date_from`/`date_to` igual.
- S5 Modo lista: con F1–F3, `recall mode="list" tags_any=["recurso-gasoil"]` devuelve F2, F3, F1 en ese orden; `mode="otro"` devuelve el error exacto del contrato; `mode="list"` sin query ni filtros devuelve su error exacto.
- S6 Menos ruido: una llamada con `queries` de 3 preguntas y sin embeddings muestra la línea de embeddings una sola vez. Con un tema **detectado** (no pedido en `topics`), «Datos estables» y «Últimos eventos» solo muestran los registros del tema que coinciden con la consulta; si ninguno coincide, esas secciones no aparecen. Con el tema pedido en `topics`, la ficha se muestra como hoy.
- S7 Sin regresiones: la suite de recall existente sigue en verde; `tags_all`, `tags_any`, fechas, `include_history` y «Conectado por etiquetas» se comportan como en `adf2f9d`.

### Parte B — escritura

- S1 Etiqueta concreta: `remember` con un item etiquetado solo `#agridemur` no lo guarda y devuelve la línea exacta `item 1 no guardado: falta una etiqueta concreta (…)`; los demás items del lote sí se guardan. `preview: true` informa de lo mismo. El hook (`lodan hook`, que llama a `memory.Remember` directamente) sigue guardando sin etiqueta concreta.
- S2 Casi duplicado: con F2 guardado, `remember` del contenido `Matrícula 3871LNY: empresa VDM y concepto RECOL BK.` no lo guarda y devuelve `item 1 no guardado: casi idéntico a #<F2> (parecido 0.xx); …`. Con `force: true` se guarda. Un item con la misma `key` que F2 lo sustituye como hoy, sin bloqueo. Con parecido entre 0.75 y 0.90 y etiqueta común, se guarda con la línea `¿duplicado?`.
- S3 Reparación al guardar: `remember` con título `AlbarÃ¡n` y contenido `VersiÃ³n definitiva` guarda `Albarán` y `Versión definitiva` y añade `texto reparado (codificación)`. Un texto sin mojibake se guarda idéntico.
- S4 `lodan db fix-encoding`: con F1–F4, la simulación lista solo F4 y termina en `1 registros con texto mal codificado (simulación: …)` sin cambiar nada; `--apply` deja F4 como `Albarán antiguo` / `Versión definitiva del albarán.`, conserva id, etiquetas, relaciones, `occurred_at` y estado, actualiza `updated_at` (para que `lodan sync` lo exporte) e imprime `1 registros reparados`; una segunda ejecución imprime `0 registros con texto mal codificado`.
- S5 Mensaje de embeddings: sin `WithEmbeddings`, la línea de guardado no contiene `embedding y parecidos pendientes`; con `WithEmbeddings` sí.
- S6 Sin regresiones: duplicado exacto sigue devolviendo `#id ya existía (duplicado): «…»`; `key` sigue sustituyendo; la suite de memory y mcptools existente sigue en verde.

## 7. Conflictos esperados

- `README.md`: si las dos partes editan secciones contiguas, git puede marcar conflicto. Se resuelve conservando los dos textos.
- `internal/database/migrations/`: sin conflicto (números distintos); se aplican en orden 0005 → 0006.

## 8. Fuera de alcance

- No cambiar `ContentHash` ni `NormalizeContent` (cambiaría la detección de duplicados exactos y la sincronización).
- No tocar `internal/gitsync/**`, `internal/hook/**`, `internal/database/*.go`, `internal/install/**`, `internal/service/**`.
- No arreglar los dos tests que ya fallan en Windows en la base (`TestCopyToStaging`, `TestEnsureMicromambaExecutable`): son previos y se ignoran al comparar.
- No desplegar el binario (`~/bin/lodan.lite.exe`), no ejecutar `fix-encoding --apply` contra la base real y no tocar el directorio de datos activo: eso lo decide el usuario tras la integración.
- La limpieza de etiquetas, alias y veredictos (propuesta 8) va en `mantenimiento-datos.md`.
