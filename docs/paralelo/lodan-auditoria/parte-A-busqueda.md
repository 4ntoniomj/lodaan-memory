[4antoniomj:parte] Eres una PARTE de un trabajo en paralelo: construye solo esta parte, no la dividas ni escribas prompts para otras partes.

# Parte A: busqueda (recall de lodan lite)

## Rol

Trabajas SOLO en esta parte, dentro de tu worktree. Otro chat construye a la vez la parte B (escritura: remember y `lodan db fix-encoding`) en su worktree. No leas ni edites código de la parte B: el contrato es suficiente. Si sientes que necesitas esperar a la otra parte, es un error del corte: repórtalo y sigue con lo que puedas.

## Contexto

- Proyecto: lodan lite, memoria persistente MCP para IAs · Stack: Go 1.25 (toolchain go1.27.1), PostgreSQL ≥ 16 con búsqueda full-text, SDK MCP de Go v1.8.0.
- Directorio de trabajo: `C:\Users\4ntoniomj\repos\lodaan-memory.worktrees\busqueda` (ejecuta todo aquí).
- Rama: `feat/lodan-auditoria-busqueda` desde `feat/lodan-auditoria` (`1991800`).
- Comandos: tests `go test ./...` (focalizados: `go test ./internal/recall/... ./internal/mcptools/...`) · lint `go vet ./...` · build `go build ./...` · rendimiento `go run ./cmd/lodan bench` si cambias el SQL (las consultas de `internal/recall/sql.go` son las que mide).
- Fallos previos conocidos en Windows, ajenos a esta parte: `TestCopyToStaging` (internal/database) y `TestEnsureMicromambaExecutable` (internal/install). No los arregles; compáralos con la base.
- Rango reservado: migración `0005` (`internal/database/migrations/0005_search.sql`).
- Las extensiones `unaccent` y `pg_trgm` están disponibles en el PostgreSQL que instala lodan (verificado en `AppData/Local/lodan/runtime/pg/Library/share/extension/`).
- Fuera de alcance: todo lo de remember, `lodan db`, gitsync, hook, install, service; `ContentHash`/`NormalizeContent`; desplegar el binario o tocar el directorio de datos activo.

## Skills a cargar antes de trabajar

- `tdd-qa`: `C:\Users\4ntoniomj\.claude\skills\tdd-qa\SKILL.md` (obligatoria; si no está instalada en este entorno, sigue la sección «Puerta de calidad» de este prompt, que es suficiente)
- `go-testing`: `C:\Users\4ntoniomj\.claude\skills\go-testing\SKILL.md`
- `work-unit-commits`: `C:\Users\4ntoniomj\.claude\skills\work-unit-commits\SKILL.md`

## Contrato congelado (NO modificable)

El contrato completo está en `docs/paralelo/lodan-auditoria/contrato.md` dentro de tu worktree: léelo entero antes de empezar; forma parte de este prompt. Lo que te afecta:

**Campo nuevo de `recall`:**

```go
Mode string `json:"mode,omitempty" jsonschema:"rank (por defecto): por relevancia; list: por fecha, lo más reciente primero"`
```

- `""`/`"rank"`: comportamiento actual. `"list"`: orden `occurred_at DESC, id DESC`; `limit` por defecto 20, máximo 50; sin ficha del tema ni «Últimos eventos»; con `query` vacía exige al menos un filtro.
- Errores exactos: `mode no válido: usa rank o list` · `mode list necesita query o algún filtro (tags_all, tags_any, date_from, date_to)`.

**Avisos exactos (primera línea del bloque afectado):**

- `(sin coincidencia con todas las palabras: resultados con alguna de ellas)`
- `(sin coincidencia exacta: resultados parecidos)`
- La línea existente `(solo búsqueda por texto: el servicio de embeddings no está disponible)` aparece una sola vez por respuesta, al principio.

**Migración `0005_search.sql`:** `CREATE EXTENSION IF NOT EXISTS unaccent;`, `CREATE EXTENSION IF NOT EXISTS pg_trgm;`, configuración `lodan_es` (copia de `spanish` con `unaccent` antes de `spanish_stem` para `word`, `hword`, `hword_part`, `asciiword`, `asciihword`, `hword_asciipart`), columna `memories.tsv` regenerada como `to_tsvector('lodan_es', title || ' ' || content)` con su índice GIN. Toda consulta de texto (`TextCandidatesSQL`, `CandidateTextSQL` y cualquier otra) usa `'lodan_es'`.

**Fixtures:**

| id lógico | título | contenido | etiquetas | occurred_at |
|---|---|---|---|---|
| F1 | `Albarán de gasoil` | `Se apunta siempre: fecha, matrícula, empresa y litros.` | `recurso-gasoil` | 2026-10-01 |
| F2 | `Matrícula 3871LNY` | `Matrícula 3871LNY: empresa VDM, concepto RECOL BK.` | `vehiculo-3871lny`, `recurso-gasoil` | 2026-10-05 |
| F3 | `Lectura del gasoil` | `Medición matinal del depósito de gasoil.` | `recurso-gasoil` | 2026-10-03 |

## Allowed edit surfaces

```text
internal/recall/**
internal/mcptools/read.go
internal/mcptools/recall_test.go
internal/mcptools/recall_mode_e2e_test.go
internal/mcptools/server.go
internal/benchmark/**
internal/database/migrations/0005_search.sql
README.md (solo la sección de recall)
```

Además, los archivos de la sección «Costuras», solo como se especifican.

## Costuras

- `README.md`: edita solo la sección de `recall` (documenta `mode`, los avisos y la búsqueda sin tildes). La parte B edita las secciones de `lodan db` y `remember`.
- Dependencias Go nuevas: ninguna. No toques `go.mod` ni `go.sum`.
- `pg_trgm`: créala con `CREATE EXTENSION IF NOT EXISTS` (la parte B hace lo mismo en `0006`; es idempotente).

## Invariantes y código existente a reutilizar

- `internal/recall/sql.go` es la única fuente de verdad de las consultas (lo mide `lodan bench`): pon ahí el SQL nuevo.
- Los filtros de etiquetas y fechas (S4) deben aplicarse **dentro** de la subconsulta, antes del `LIMIT TextRankCap`; hoy se aplican después (`sql.go`, función `TextCandidatesSQL`).
- La búsqueda alternativa (S2, S3) solo se ejecuta cuando la búsqueda normal devuelve 0 resultados; el orden es: todas las palabras → alguna palabra (`or` en `websearch_to_tsquery`) → parecido por trigramas (`word_similarity` sobre `unaccent(lower(title))`; elige y documenta el umbral, por defecto 0.4).
- Reutiliza `Result.Format` y `lines()` de `internal/recall/format.go` para los avisos; respeta el tope de bytes (`RecallMaxBytes`).
- El tema pedido por `topics` (`TopicViaRequested`) conserva su ficha completa; solo el tema detectado se filtra (S6).
- Mensajes al usuario en español, como el resto del proyecto; identificadores en inglés; comentarios con la densidad e idioma del código que rodean.

## Criterios de aceptación

- S1 Tildes: con F1, `recall query="albaran"`, `query="albarán"` y `query="ALBARAN"` devuelven F1 en los tres casos.
- S2 Palabras de más: con F1–F3, `recall query="albaran gasoil inexistentepalabra"` devuelve F1 y su bloque empieza por `(sin coincidencia con todas las palabras: resultados con alguna de ellas)`. Si la búsqueda con todas las palabras encuentra algo, no hay aviso y el resultado es el de hoy.
- S3 Erratas: con F2, `recall query="matricla"` devuelve F2 con el aviso `(sin coincidencia exacta: resultados parecidos)`. Solo se usa cuando S2 tampoco encuentra nada.
- S4 Filtros antes del límite: con 2100 registros vigentes, de los que 5 (los más antiguos) llevan `proyecto-filtro`, `recall query="" tags_all=["proyecto-filtro"]` devuelve los 5; con `date_from`/`date_to` igual.
- S5 Modo lista: con F1–F3, `recall mode="list" tags_any=["recurso-gasoil"]` devuelve F2, F3, F1 en ese orden; `mode="otro"` devuelve el error exacto; `mode="list"` sin query ni filtros devuelve su error exacto.
- S6 Menos ruido: una llamada con `queries` de 3 preguntas y sin embeddings muestra la línea de embeddings una sola vez. Con un tema detectado (no pedido en `topics`), «Datos estables» y «Últimos eventos» solo muestran los registros del tema que coinciden con la consulta; si ninguno coincide, esas secciones no aparecen. Con el tema pedido en `topics`, la ficha se muestra como hoy.
- S7 Sin regresiones: la suite de recall existente sigue en verde; `tags_all`, `tags_any`, fechas, `include_history` y «Conectado por etiquetas» se comportan como en la base. Añade un test que fije el comportamiento previo de cada una de esas opciones y haz una comparación diferencial contra una copia de la base en `mktemp -d`.

## Puerta de calidad (obligatoria)

Esta sección no se elimina ni se resume al rellenar la plantilla. Ejecuta las cinco fases en orden dentro de este chat, tú o tus subagentes:

1. **RED**: antes de escribir código de producción, un test por criterio `S#`, por la interfaz pública (salida, código de salida, dato persistido, respuesta HTTP, pantalla), nunca por funciones internas. Ejecuta y pega la salida RED real: debe fallar por la razón correcta, no por un error de sintaxis.
2. **Sello**: `sha256sum internal/recall/audit_search_test.go internal/mcptools/recall_mode_e2e_test.go` (en PowerShell, `Get-FileHash -Algorithm SHA256 <archivos>`) y anota los hashes. Desde aquí los tests no se tocan; si uno parece incorrecto, se reporta, no se edita.
3. **GREEN**: implementa lo mínimo hasta que pasen, refactoriza con la suite en verde, corre lint y build, y vuelve a calcular los hashes: deben coincidir. Pega la salida GREEN real.
4. **QA**: usa la parte como una persona real (persona: Antonio, que pregunta a su IA «¿qué matrícula tiene el coche de Recol BK?», «albaranes de gasoil», «lo último del proyecto agm-cosechas» escribiendo deprisa, sin tildes y con erratas), cada opción en alcance, con datos realistas y adversariales (vacíos, límites, caracteres especiales, repeticiones). Usa un directorio de datos de prueba en `mktemp -d`, nunca el activo. Cada hallazgo con pasos, esperado, obtenido y evidencia. Lo no probado se declara NO PROBADO.
5. **Bug de QA**: primero un test nuevo que lo reproduce (RED), después la corrección (GREEN), y QA otra vez. Máximo dos rondas de corrección; si sigue abierto, repórtalo.

Una parte sin RED real, sin GREEN real, con el sello roto o con bugs de QA abiertos está NO terminada, aunque los tests pasen.

## Cómo trabajar

- Commitea los tests (`test(recall): …`) antes del código de producción y no los modifiques después; commitea la implementación aparte (`feat(recall): …`).
- Puedes usar subagentes según tus reglas normales: explorador para mapear el código existente, examinador (`4antoniomj-examiner`) para escribir y sellar los tests de la fase 1, QA (`4antoniomj-qa`) para la fase 4 y verificador: esta parte nombra riesgo alto (item 1, la migración regenera una columna persistida; item 3, cambia una herramienta MCP que consumen otras IAs), así que lanza el verificador independiente después de tus checks. Si tu entorno no tiene subagentes, haz tú cada fase.
- No lances escritores para otras partes ni leas sus worktrees.

## Reglas

- No cambies el contrato ni salgas de tu rango; si es imposible cumplirlo, PARA y explica por qué.
- Instala solo las dependencias que declara la sección «Costuras», con su versión exacta. Si falta otra, repórtalo.
- Conventional Commits, sin atribución de IA ni `Co-Authored-By`. No hagas push, no mergees, no toques otras ramas.

## Entrega

1. Criterios S1..S7 → pass/fail
2. Salida real RED y GREEN
3. Sello de tests: hashes y si sigue intacto (sí/no)
4. Hallazgos de QA (o «0 hallazgos»), con lo NO PROBADO explícito
5. Archivos tocados + `git diff --stat feat/lodan-auditoria...HEAD`
6. Commits
7. Riesgos (línea `Risk:`) y lo que quedó fuera
8. `## Key Learnings` (1–5 frases, o se omite)
