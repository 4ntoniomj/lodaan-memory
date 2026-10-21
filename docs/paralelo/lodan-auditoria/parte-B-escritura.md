[4antoniomj:parte] Eres una PARTE de un trabajo en paralelo: construye solo esta parte, no la dividas ni escribas prompts para otras partes.

# Parte B: escritura (remember y `lodan db fix-encoding` de lodan lite)

## Rol

Trabajas SOLO en esta parte, dentro de tu worktree. Otro chat construye a la vez la parte A (búsqueda: recall) en su worktree. No leas ni edites código de la parte A: el contrato es suficiente. Si sientes que necesitas esperar a la otra parte, es un error del corte: repórtalo y sigue con lo que puedas.

## Contexto

- Proyecto: lodan lite, memoria persistente MCP para IAs · Stack: Go 1.25 (toolchain go1.27.1), PostgreSQL ≥ 16, SDK MCP de Go v1.8.0.
- Directorio de trabajo: `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\escritura` (ejecuta todo aquí).
- Rama: `feat/lodan-auditoria-escritura` desde `feat/lodan-auditoria` (`1991800`).
- Comandos: tests `go test ./...` (focalizados: `go test ./internal/memory/... ./internal/mcptools/... ./cmd/lodan/...`) · lint `go vet ./...` · build `go build ./...`.
- Fallos previos conocidos en Windows, ajenos a esta parte: `TestCopyToStaging` (internal/database) y `TestEnsureMicromambaExecutable` (internal/install). No los arregles; compáralos con la base.
- Rango reservado: migración `0006` (`internal/database/migrations/0006_near_duplicates.sql`).
- La extensión `pg_trgm` está disponible en el PostgreSQL que instala lodan.
- Fuera de alcance: recall y todo `internal/recall/**`; gitsync, hook, install, service; `ContentHash`/`NormalizeContent`; desplegar el binario, ejecutar `fix-encoding --apply` contra la base real o tocar el directorio de datos activo.

## Skills a cargar antes de trabajar

- `tdd-qa`: `C:\Users\4ntoniomj\.claude\skills\tdd-qa\SKILL.md` (obligatoria; si no está instalada en este entorno, sigue la sección «Puerta de calidad» de este prompt, que es suficiente)
- `go-testing`: `C:\Users\4ntoniomj\.claude\skills\go-testing\SKILL.md`
- `work-unit-commits`: `C:\Users\4ntoniomj\.claude\skills\work-unit-commits\SKILL.md`

## Contrato congelado (NO modificable)

El contrato completo está en `docs/paralelo/lodan-auditoria/contrato.md` dentro de tu worktree: léelo entero antes de empezar; forma parte de este prompt. Lo que te afecta:

**Campo nuevo de cada item de `remember`:**

```go
Force bool `json:"force,omitempty" jsonschema:"true: guarda aunque sea casi idéntico a otro registro"`
```

**Líneas de salida exactas** (`N` = posición del item desde 1; parecido con dos decimales):

- `item N no guardado: falta una etiqueta concreta (proyecto-, persona-, contacto-, proveedor-, cliente-, finca-, vehiculo-, coche-, skill-, herramienta-, recurso-, evento-)`
- `item N no guardado: casi idéntico a #id (parecido 0.93); si es un cambio, usa revise update o key, o force: true`
- `  ¿duplicado?: #id (parecido 0.82)` (bajo la línea `#id guardado`, antes de las `revisa:`)
- `texto reparado (codificación)` como parte de la línea `#id guardado · …`
- `embedding y parecidos pendientes (se calculan en segundo plano)` solo con `WithEmbeddings` true.

**Comando:**

```text
lodan db fix-encoding            # simulación
lodan db fix-encoding --apply    # repara
```

Simulación: `#id título` por registro afectado y al final `N registros con texto mal codificado (simulación: no se ha cambiado nada; usa --apply)`; con 0: `0 registros con texto mal codificado`. `--apply`: `N registros reparados`; colisión de hash: `#id omitido: quedaría duplicado de #otro`. Salida 0; 1 si la base de datos no responde.

**Reglas de dominio:**

- Etiqueta concreta: casa con `^(proyecto|persona|contacto|proveedor|cliente|finca|vehiculo|coche|skill|herramienta|recurso|evento)-[a-z0-9]` tras la normalización actual (minúsculas, alias confirmados resueltos).
- Mojibake reparable: contiene `Ã` o `Â` y, al convertir el texto completo a bytes Windows-1252 (los bytes no definidos 0x81, 0x8D, 0x8F, 0x90 y 0x9D se aceptan tal cual), el resultado es UTF-8 válido y distinto del original. `AlbarÃ¡n` → `Albarán`, `mediciÃ³n` → `medición`, `RaÃºl` → `Raúl`, `Ã­` (con guion blando) → `í`. `PÃO` escrito a propósito no es reparable y se deja intacto.
- Casi duplicado: `similarity(lower(nuevo), lower(existente))` de `pg_trgm` sobre el contenido de registros vigentes. `≥ 0.90` → no se guarda (salvo `force`). Entre `0.75` y `0.90` con al menos una etiqueta en común → se guarda con `¿duplicado?`. Se excluye el registro que el propio item sustituye por `key`.

**Migración `0006_near_duplicates.sql`:** `CREATE EXTENSION IF NOT EXISTS pg_trgm;` y `CREATE INDEX IF NOT EXISTS memories_content_trgm_idx ON memories USING gin (lower(content) gin_trgm_ops) WHERE status = 'active';`

**Fixtures:**

| id lógico | título | contenido | etiquetas | occurred_at |
|---|---|---|---|---|
| F1 | `Albarán de gasoil` | `Se apunta siempre: fecha, matrícula, empresa y litros.` | `recurso-gasoil` | 2026-10-01 |
| F2 | `Matrícula 3871LNY` | `Matrícula 3871LNY: empresa VDM, concepto RECOL BK.` | `vehiculo-3871lny`, `recurso-gasoil` | 2026-10-05 |
| F3 | `Lectura del gasoil` | `Medición matinal del depósito de gasoil.` | `recurso-gasoil` | 2026-10-03 |
| F4 | `AlbarÃ¡n antiguo` | `VersiÃ³n definitiva del albarÃ¡n.` | `recurso-gasoil` | 2026-09-30 |

## Allowed edit surfaces

```text
internal/memory/**
internal/mcptools/write.go
internal/mcptools/preview.go
internal/mcptools/write_audit_e2e_test.go
cmd/lodan/db.go
cmd/lodan/fix_encoding_test.go
internal/database/migrations/0006_near_duplicates.sql
README.md (solo las secciones de remember y lodan db)
go.mod, go.sum (solo si usas golang.org/x/text directamente, ver Costuras)
```

Además, los archivos de la sección «Costuras», solo como se especifican.

## Costuras

- `README.md`: edita solo las secciones de `remember` y `lodan db` (documenta `force`, las nuevas líneas y `fix-encoding`). La parte A edita la sección de `recall`.
- Dependencias Go: si usas `golang.org/x/text/encoding/charmap` para Windows-1252, ejecuta `go get golang.org/x/text@v0.41.0` (la versión que ya fija `go.sum`). Ninguna otra.
- `pg_trgm`: créala con `CREATE EXTENSION IF NOT EXISTS` (la parte A hace lo mismo en `0005`; es idempotente).

## Invariantes y código existente a reutilizar

- La validación de etiqueta concreta, la detección de casi duplicados y la reparación al guardar van en el camino de la herramienta MCP (`internal/mcptools/write.go`, `handleRemember`, y `preview.go`), no dentro de `memory.Service.Remember`: `lodan hook` (`internal/hook/hook.go:182`) llama a `Remember` directamente y debe seguir guardando como hoy. Pon la lógica reutilizable (detector de mojibake, consulta de parecidos) en archivos nuevos de `internal/memory/` y llámala desde `write.go`.
- Un lote no se aborta por un item rechazado: los demás se guardan (igual que `revise ops`).
- `fix-encoding --apply` reutiliza el camino de actualización que ya usa `revise update` en `internal/memory/revise.go`, para que `content_hash`, `tsv` y `updated_at` se recalculen y `lodan sync` exporte el cambio. No escribas un `UPDATE` suelto que se salte eso.
- Respeta el índice único parcial sobre `content_hash` de los registros vigentes: si la reparación colisiona, se omite y se informa.
- Sigue el patrón de subcomandos de `cmd/lodan/db.go` (`switch` por nombre) y actualiza su texto de ayuda.
- Mensajes al usuario en español, como el resto del proyecto; identificadores en inglés; comentarios con la densidad e idioma del código que rodean.

## Criterios de aceptación

- S1 Etiqueta concreta: `remember` con un item etiquetado solo `#agridemur` no lo guarda y devuelve la línea exacta `item 1 no guardado: falta una etiqueta concreta (…)`; los demás items del lote sí se guardan. `preview: true` informa de lo mismo. `lodan hook` sigue guardando sin etiqueta concreta.
- S2 Casi duplicado: con F2 guardado, `remember` del contenido `Matrícula 3871LNY: empresa VDM y concepto RECOL BK.` no lo guarda y devuelve `item 1 no guardado: casi idéntico a #<F2> (parecido 0.xx); …`. Con `force: true` se guarda. Un item con la misma `key` que F2 lo sustituye como hoy, sin bloqueo. Con parecido entre 0.75 y 0.90 y etiqueta común, se guarda con la línea `¿duplicado?`. (Si el parecido real de este ejemplo no supera 0.90, el examinador elige otro par que lo supere y lo anota; los umbrales no cambian.)
- S3 Reparación al guardar: `remember` con título `AlbarÃ¡n` y contenido `VersiÃ³n definitiva` guarda `Albarán` y `Versión definitiva` y añade `texto reparado (codificación)`. Un texto sin mojibake se guarda idéntico.
- S4 `lodan db fix-encoding`: con F1–F4, la simulación lista solo F4 y termina en `1 registros con texto mal codificado (simulación: …)` sin cambiar nada; `--apply` deja F4 como `Albarán antiguo` / `Versión definitiva del albarán.`, conserva id, etiquetas, relaciones, `occurred_at` y estado, actualiza `updated_at` e imprime `1 registros reparados`; una segunda ejecución imprime `0 registros con texto mal codificado`.
- S5 Mensaje de embeddings: sin `WithEmbeddings`, la línea de guardado no contiene `embedding y parecidos pendientes`; con `WithEmbeddings` sí.
- S6 Sin regresiones: duplicado exacto sigue devolviendo `#id ya existía (duplicado): «…»`; `key` sigue sustituyendo; la suite existente de memory, mcptools, hook y gitsync sigue en verde. Añade un test que fije cada uno de esos comportamientos previos y haz una comparación diferencial contra una copia de la base en `mktemp -d`.

## Puerta de calidad (obligatoria)

Esta sección no se elimina ni se resume al rellenar la plantilla. Ejecuta las cinco fases en orden dentro de este chat, tú o tus subagentes:

1. **RED**: antes de escribir código de producción, un test por criterio `S#`, por la interfaz pública (salida, código de salida, dato persistido, respuesta HTTP, pantalla), nunca por funciones internas. Ejecuta y pega la salida RED real: debe fallar por la razón correcta, no por un error de sintaxis.
2. **Sello**: `sha256sum internal/memory/audit_write_test.go internal/mcptools/write_audit_e2e_test.go cmd/lodan/fix_encoding_test.go` (en PowerShell, `Get-FileHash -Algorithm SHA256 <archivos>`) y anota los hashes. Desde aquí los tests no se tocan; si uno parece incorrecto, se reporta, no se edita.
3. **GREEN**: implementa lo mínimo hasta que pasen, refactoriza con la suite en verde, corre lint y build, y vuelve a calcular los hashes: deben coincidir. Pega la salida GREEN real.
4. **QA**: usa la parte como una persona real (persona: una IA de Antonio que guarda deprisa datos de Agridemur, a veces sin etiqueta concreta, a veces repitiendo casi lo mismo con otra redacción, y a veces pegando texto copiado de un Excel antiguo con tildes rotas; y Antonio ejecutando `lodan db fix-encoding` primero en simulación), cada opción en alcance, con datos realistas y adversariales (vacíos, límites, caracteres especiales, repeticiones, lotes mixtos). Usa un directorio de datos de prueba en `mktemp -d`, nunca el activo. Cada hallazgo con pasos, esperado, obtenido y evidencia. Lo no probado se declara NO PROBADO.
5. **Bug de QA**: primero un test nuevo que lo reproduce (RED), después la corrección (GREEN), y QA otra vez. Máximo dos rondas de corrección; si sigue abierto, repórtalo.

Una parte sin RED real, sin GREEN real, con el sello roto o con bugs de QA abiertos está NO terminada, aunque los tests pasen.

## Cómo trabajar

- Commitea los tests (`test(memory): …`) antes del código de producción y no los modifiques después; commitea la implementación aparte (`feat(memory): …`, `feat(db): …`).
- Puedes usar subagentes según tus reglas normales: explorador para mapear el código existente, examinador (`4antoniomj-examiner`) para escribir y sellar los tests de la fase 1, QA (`4antoniomj-qa`) para la fase 4 y verificador: esta parte nombra riesgo alto (item 1, `fix-encoding --apply` modifica datos guardados; item 3, cambia el contrato de la herramienta MCP `remember`), así que lanza el verificador independiente después de tus checks. Si tu entorno no tiene subagentes, haz tú cada fase.
- No lances escritores para otras partes ni leas sus worktrees.

## Reglas

- No cambies el contrato ni salgas de tu rango; si es imposible cumplirlo, PARA y explica por qué.
- Instala solo las dependencias que declara la sección «Costuras», con su versión exacta. Si falta otra, repórtalo.
- Conventional Commits, sin atribución de IA ni `Co-Authored-By`. No hagas push, no mergees, no toques otras ramas.

## Entrega

1. Criterios S1..S6 → pass/fail
2. Salida real RED y GREEN
3. Sello de tests: hashes y si sigue intacto (sí/no)
4. Hallazgos de QA (o «0 hallazgos»), con lo NO PROBADO explícito
5. Archivos tocados + `git diff --stat feat/lodan-auditoria...HEAD`
6. Commits
7. Riesgos (línea `Risk:`) y lo que quedó fuera
8. `## Key Learnings` (1–5 frases, o se omite)
