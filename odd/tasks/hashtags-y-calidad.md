# lodan: hashtags, alias dictionary and memory quality

Objective: many normalized tags per record, a server-side alias dictionary, relations weighted by tag rarity, a save preview, suggested keys, stored conflict verdicts and merging of repeated records, installed on this Windows machine for the three AIs and compatible with git sync.
Branch: `feat/hashtags-calidad` (from `feat/sincronizacion-git` 0795041, as the prompt requires). Delivery: `single-pr` (local branches, merges are the user's decision). Test runner: `go test ./...` (DB tests need `LODAN_PG_BIN_DIR=C:/Users/4ntoniomj/AppData/Local/lodan/runtime/pg/Library/bin`); tests written first by the `examinador` subagent (skill `tdd-examinador`), sealed, never edited by the builder.

## Specs

The user's request and prompt are quoted verbatim in L1; the proposal, its additions and the approval are in L2-L8. Quotes in «» are the user's words. "Tag" is the new name of what the code calls a topic (`topics`, `memory_topics`): tags reuse those tables.

### Tags

- **S1 — Several tags per record.** `remember` items accept `tags` (list of strings) and still accept `topics` (same meaning; both lists are joined). A leading `#` is ignored. Each tag is normalized with the existing `topic.Normalize`. After normalization and removing repeats, at most 8 tags per item; more is an error `ítem N: como máximo 8 etiquetas (llegaron M)` and nothing of the call is saved. No usable tag → `general` (unchanged). `revise update` with `topics` (or the new `tags`) follows the same rules. «cada registro lleva varias etiquetas, generales (`#agridemur`) y concretas (`#coche-1234abc`, `#matricula`), escritas en una forma normalizada». Tags extend the current topics; existing topics become tags as they are, without data changes («Decide si sustituyen a los temas actuales o los amplían, y cómo migrar los temas existentes sin perder nada»).
- **S2 — Same tag written with or without hyphens.** A requested tag whose letters and digits equal an existing tag's once every `-` is removed resolves to that existing tag (`coche-4860-ngl` → `coche-4860ngl`).
- **S3 — No automatic merge by meaning.** A requested tag that is not an existing tag, not a confirmed alias (S4) and not hyphen-equal (S2) is created as a new tag, however similar its embedding is to an existing one. This replaces today's merge at similarity ≥ `topic_similarity` (0.72) in `remember` and `revise update` (approved design, L4: an automatic merge would join two different vehicles such as 0762MWY and 0763MWY).
- **S4 — Output shows tags.** The saved line of `remember` says `etiquetas:` instead of `temas:`, each tag as `#slug`; a tag rewritten by S2 or S5 is shown `#requested→#canonical`; a tag created by this call is followed by ` (nueva)`. Example: `#902 guardado · etiquetas: #vdm, #coche-4860ngl, #itv (nueva)`. `get` shows `etiquetas: #a, #b` instead of `temas: a, b`. Every recall line (also the `revisa:` lines) shows the record's tags after the title: `#id [tipo fecha] título · #a #b — fragmento`.

### Alias dictionary

- **S5 — Confirmed aliases are applied by the server.** «el servidor sabe que `#agm` es `#agridemur` y lo corrige solo, venga de la IA que venga». A confirmed alias `A → C` rewrites `A` to `C` in `remember` (also in preview), in `revise update` tags and in the `topics` of `recall` (a priority, not a filter: L10). Lookups are hyphen-insensitive (S2). There are no chains: confirming `A → C` when `C` is itself an alias of `D` stores `A → D`; confirming `A → C` when other aliases point to `A` re-points them to `C`.
- **S6 — Managing aliases with `revise`.** New actions, no `id` needed:
  - `alias` with `tag` and `with_tag`: confirms `tag → with_tag`, moves every record tagged `tag` to `with_tag` (a record already tagged with both keeps one), and answers `alias #tag → #with_tag (N registros reetiquetados)`. Same tag on both sides → `una etiqueta no puede ser alias de sí misma`.
  - `reject_alias` with `tag` and `with_tag`: stores that they are different and answers `#tag ≠ #with_tag: no se volverá a sugerir`.
  - Both normalize their inputs; missing `tag` or `with_tag` → `hay que indicar tag y with_tag`.
- **S7 — Alias suggestions for new tags.** When `remember` (or its preview) creates a new tag, the server looks for existing tags that may be the same, excluding pairs already confirmed or rejected: (a) singular/plural: one equals the other plus `s` or `es`; (b) Damerau-Levenshtein distance 1 when both are at least 5 characters long (`coche-4860gnl` vs `coche-4860ngl`); (c) embedding similarity ≥ `topic_similarity`. At most 3 per new tag, those with more active records first, each as a line `  ¿alias?: #new ≈ #existing (N registros)` under the saved item. A real save stores them as pending suggestions; a preview stores nothing (approved design, L4: the new tag is saved as written and the suggestion is reported).

### Rarity

- **S8 — Strength of a link.** For two active records, strength = Σ over their shared tags of ln(N / n), N = active records, n = active records carrying that tag. Records are connected when strength ≥ `tag_link_strength` (new `config.json` field, default 3.0; env `LODAN_TAG_LINK_STRENGTH`; must be > 0). Not stored: computed when needed, so counts stay current. «compartir una etiqueta con 4 registros pesa mucho; una con 300, poco».
- **S9 — Where connections show.**
  - `remember` (real and preview): a line `  conectado por etiquetas: #a (#tag, 5.3), #b (#tag, 4.1)` with up to 5 connected active records, strongest first (strength with one decimal, the rarest shared tag named); in the preview the wording is `  conectaría:`. Omitted when there are none.
  - `recall`: after the results of each query, a block `Conectado por etiquetas:` with up to 5 active records connected to the first 3 results and not already shown, each as a recall line followed by ` (por #tag)`; it respects `recall_max_bytes`. «`recall` sigue las etiquetas concretas y trae lo conectado en una sola llamada».
- **S10 — Review candidates by rarity.** In the `revisa:` candidates, a record that shares with the new item a tag carried by at most 4 active records ranks before every candidate that only shares tags carried by more than half of the active records or only matches by text or meaning. (Replaces today's flat +1.0 bonus for any shared topic.)

### Preview, keys, one record per thing

- **S11 — Preview.** `remember` accepts top-level `preview: true`: it runs every check and saves nothing (no row in `memories`, `topics`, `memory_topics`, `memory_relations` or the alias table changes). L5: «y ates de guardar revisa las etiquetas y lo que tenga similitus». Output starts with `VISTA PREVIA — no se ha guardado nada`, then per item `ítem N «título»` and the lines: `  etiquetas: …` (S4 format), `  ya existe: #id (mismo contenido)` when an active record has the same content hash, `  clave K: sustituiría a #id` when the item's key is active on another record, the S7 `¿alias?` lines, the S12 `clave sugerida` line, the S9 `conectaría` line and the usual `  revisa:` lines.
- **S12 — Suggested key.** For an item without `key`, the server looks for an active record R with key K such that R is connected to the item (S8) and the part of K after its last `/`, normalized, is one of the item's tags or a whole `-`-delimited sequence of `Normalize(title)`. The strongest such R gives `  clave sugerida: K (sustituiría a #R)`, in the preview and after a real save. «el servidor propone la `key` a partir de las etiquetas, para actualizar en vez de duplicar».
- **S13 — One record per thing.** L6: «falta el tema de que solo se guarde un registro de cada cosa». Covered by: exact duplicate rejected (existing), same key supersedes (existing), S11 + S12 warn before saving, S15 merges existing repeats.

### Verdicts and merge

- **S14 — Verdict on a possible conflict.** `revise` action `verdict` with `id` (A), `with_id` (B), `verdict` ∈ {`contradice`, `sustituye`, `compatible`, `no_relacionado`} and `confidence` (0..1, required). Read as "A <verdict> B". With confidence ≥ 0.7:
  - `contradice`: relation `contradicts` confirmed, both stay active; answer `#A contradice #B: pregunta al usuario cuál es el bueno`.
  - `sustituye`: B is superseded by A exactly as `supersede`; answer `#A sustituye a #B`.
  - `compatible`: relation `related` confirmed and any `contradicts` between them rejected; answer `#A y #B son compatibles`.
  - `no_relacionado`: every `related`/`contradicts` between them rejected (a rejected `related` is stored if none existed); answer `#A y #B no tienen que ver`.
  - With confidence < 0.7 nothing is applied; the verdict is stored as a `suggested` relation (`contradicts`, `supersedes` or `related`; nothing for `no_relacionado`) and the answer is `veredicto «V» con confianza 0.55: queda pendiente, pregunta al usuario`.
  - The relation row keeps the verdict and the confidence; `get` shows them on the relation line (`veredicto compatible, confianza 0.90`).
  - Errors: `veredicto "x" no válido (válidos: contradice, sustituye, compatible, no_relacionado)`; `la confianza debe estar entre 0 y 1` (also when missing).
  - «Las 8 contradicciones pendientes (#815) deben poder resolverse así» (measured: 38 open, L2).
- **S15 — Merge repeated records.** L7: «juntar repetidos eso era». `revise` action `merge` with `ids` (≥ 2 active records), `title` and `content` (the merged text, written by the AI), optional `tags`.
  - Without `confirm`: preview, nothing written. Lines: `VISTA PREVIA — juntar #a, #b en un registro nuevo (no se ha guardado nada)`, `  título: …`, `  etiquetas: …` (given tags, else the union of the originals'), `  clave: K` (first non-empty key of the originals, if any), `  relaciones que pasan al nuevo: N`, `  #a, #b quedarán sustituidos por el nuevo`, `  huella: H` (8 hex chars that change if any original changes), `  para confirmar: repite con confirm: true y fingerprint: H`.
  - With `confirm: true` and the right `fingerprint`: creates the new record (kind and occurred_at of the first id), moves the key, copies the originals' relations to it (except those among the originals), supersedes every original by it (history kept), answers `#new junta #a, #b (quedan sustituidos)`.
  - Errors: `hay que indicar al menos 2 registros en ids`; `#N no está vigente`; `falta la huella de la vista previa (fingerprint)`; `los registros cambiaron desde la vista previa: vuelve a pedirla`.

### Status, sync, install

- **S16 — `status`.** The DB line says `etiquetas: T` instead of `temas: T` and adds ` · alias: A · sugerencias de alias: S · veredictos pendientes: V · contradicciones abiertas: C` (C = non-rejected `contradicts` with both records active). Then up to 10 lines `  ¿alias?: #a ≈ #b (N registros)` with the pending suggestions.
- **S17 — Migration 0004.** File `internal/database/migrations/0004_tags.sql`. Adds table `tag_aliases` (`alias text`, `canonical text`, `state text` in confirmed|suggested|rejected, `created_at`, `updated_at`, `sync_xid xid8`; primary key (alias, canonical); at most one `confirmed` row per alias) and nullable `verdict text` (contradice|sustituye|compatible|no_relacionado) and `confidence real` (0..1) on `memory_relations`. No existing data changes. Tested on a temporary database and on a restored copy of the real one, after a full `pg_dump` in `AppData\Local\lodan\backups-manual`.
- **S18 — Sync.** «Las equivalencias se pueden añadir y revisar, y viajan con la sincronización por git». Confirmed and rejected aliases travel as a new line type; last change wins by `updated_at`; importing a confirmed alias also retags the local records; suggestions stay local. Relation lines carry `verdict` and `confidence`. A file is written as `lodan-sync/2` only when it contains alias lines, otherwise `lodan-sync/1`; the reader accepts both. Imported changes are not exported again.
- **S19 — Skill and docs.** Skill `lodan-memoria` (repo `skills/lodan-memoria`, installed copies) explains tags, tag families (`coche-<matrícula>`, `cliente-<nombre>`, `finca-<nombre>`, `proyecto-<nombre>`), preview before saving, alias decisions, verdicts, keys `<etiqueta concreta>/<atributo>` and merge; tool descriptions, README and CODEBASE updated.
- **S20 — Install.** Installed on this Windows machine for Claude Code, opencode and Antigravity, working with git sync (`lodan sync run` OK after the change).
- **S21 — Put into use (with the user).** Seed the dictionary from #45, #349, #350 and plural variants, handling the double meaning of AGM (`#agm-repo` for the monorepo); propose plate twins and old repeats in batches for the user to confirm; resolve open contradictions with verdicts. Constraints: «Pregúntame una sola cosa cada vez»; never store workers' personal data (#546) or secrets.

## Tasks

| ID | Specs | Route | Status | Evidence |
| :--- | :--- | :--- | :--- | :--- |
| T1 | S1-S4, S17 (schema part for later tasks) | inline; tests by examinador | done | RED: build failures for missing `Tags`/`Created` + behaviour; GREEN: sealed tests OK; commit below |
| T2 | S5-S7, S16 | inline; tests by examinador | done | RED 24 subtests; GREEN sealed tests; commit below |
| T3 | S8-S10 | inline; tests by examinador | pending | |
| T4 | S11-S13 | inline; tests by examinador | pending | |
| T5 | S14 | inline; tests by examinador | pending | |
| T6 | S15 | inline; tests by examinador | pending | |
| T7 | S18 | inline; tests by examinador | pending | |
| T8 | S19 | inline (docs) | pending | |
| T9 | S17 (real DB), S20 | inline | pending | |
| T10 | S21 | inline, with the user | pending | |

Route note: no writer trigger fired at creation (sequential units sharing `remember.go`/`write.go`, so no parallelism); the context backstop may delegate later units, recorded here if it happens.

## Log

- **L1 (2026-10-17, user):** «Hay un prompt de implementaciones nuevas de lodan que unos de los temas de los que se habla son etiquetas o hastags, para que lo identifiques, haz lo que te dice el prompt». The prompt is `odd/prompts/hashtags-y-calidad.md` (commit 0795041), verbatim:

````text
[ROL]
Actúa como un ingeniero experto en sistemas de memoria para IA, recuperación de información y grafos de conocimiento, con pensamiento crítico. Trabajas sobre una aplicación ya creada.

[CONTEXTO]
lodan es mi memoria persistente, compartida por todas mis IAs (Claude Code, opencode y Antigravity) y sincronizada entre máquinas por git. Es un binario Go (repositorio `C:\Users\4ntoniomj\lodaan-memory`) con PostgreSQL + pgvector y Ollama para los embeddings, instalado como servicio en `C:\Users\4ntoniomj\AppData\Local\lodan`. Lo instalado sale de la rama `feat/sincronizacion-git` (sin fusionar en `dev`).

Hoy cada registro lleva de 1 a 3 «temas» (`topics`, `memory_topics`), que suelen ser generales («lodan», «agridemur»). Cada IA los escribe a su manera, y que dos registros compartan tema no los relaciona por sí solo. Los alias viven en registros de texto sueltos (por ejemplo #45 «AGM = Agridemur», #349 y #350 glosarios de Agridemur), así que una IA que pregunta por «Agridemur» puede no encontrar lo guardado como «AGM». La `key` la inventa cada IA, y por eso dos registros sobre lo mismo rara vez comparten clave. Las contradicciones solo se sospechan (`revisa:`) y la IA decide; hay 8 sin resolver (#815).

Engram (repo Gentleman-Programming/engram, resumen en lodan #889) resuelve parte de esto: sugiere la `topic_key` con familias fijas, guarda un veredicto por cada posible conflicto (`related`, `compatible`, `scoped`, `conflicts_with`, `supersedes`, `not_conflict`, con confianza; por debajo de 0.7 pregunta al usuario) y consolida registros repetidos con vista previa y confirmación. No tiene alias ni contradicción exacta por entidad.

Decisión mía (lodan #890): quiero etiquetas «como los hashtags de las redes sociales» para entrelazarlo todo, más lo útil de Engram.

[OBJETIVOS]
1. Hashtags: cada registro lleva varias etiquetas, generales (`#agridemur`) y concretas (`#coche-1234abc`, `#matricula`), escritas en una forma normalizada. Decide si sustituyen a los temas actuales o los amplían, y cómo migrar los temas existentes sin perder nada.
2. Diccionario de equivalencias: el servidor sabe que `#agm` es `#agridemur` y lo corrige solo, venga de la IA que venga. Las equivalencias se pueden añadir y revisar, y viajan con la sincronización por git.
3. Relaciones automáticas por etiquetas: dos registros que comparten una etiqueta quedan unidos con más fuerza cuanto más rara sea (compartir una etiqueta con 4 registros pesa mucho; una con 300, poco), para que no acabe todo relacionado con todo. `recall` sigue las etiquetas concretas y trae lo conectado en una sola llamada.
4. Clave sugerida por el servidor: para datos que cambian (por ejemplo la matrícula o el seguro de un coche), el servidor propone la `key` a partir de las etiquetas, para actualizar en vez de duplicar.
5. Veredicto ante conflictos: cuando un registro nuevo choca con otro, la IA guarda un veredicto (se contradicen, sustituye, compatibles, no tiene que ver), con su confianza, y me pregunta cuando no está segura. Las 8 contradicciones pendientes (#815) deben poder resolverse así.
6. Juntar registros repetidos en uno, con vista previa y confirmación antes de escribir, sin perder el historial.
7. Instalarlo en este Windows para las tres IAs y que funcione con la sincronización entre máquinas.

[RESTRICCIONES]
- No te inventes datos: lo que no sepas, lo investigas en el código o en la documentación; si sigues sin saberlo, lo dices.
- No trabajes en `main` ni directamente en `dev`: crea una rama nueva desde `feat/sincronizacion-git` (es de donde sale lo instalado).
- Cualquier cambio de esquema es una migración sobre mi base real: backup completo antes (`pg_dump` en `AppData\Local\lodan\backups-manual`) y prueba en una copia restaurada.
- Los tests los escribe el subagente `examinador` antes del código (skill `tdd-examinador`); quien programa no los toca.
- Como máximo 6 herramientas MCP: amplía las que hay (`remember`, `recall`, `revise`…) en vez de crear nuevas.
- Respeta lo que lodan ya tiene decidido que no se guarda (por ejemplo, datos personales de trabajadores) y no guardes secretos.
- Pregúntame una sola cosa cada vez, y solo cuando sea una decisión mía.

[FORMATO]
Primero analiza el código actual y mis registros reales y devuélveme una propuesta corta y en lenguaje sencillo: cómo serán las etiquetas, el diccionario de equivalencias, el cálculo de la fuerza de una relación, la migración de los temas actuales, qué cambia en cada herramienta y los riesgos. Incluye una medición previa sobre unos 50 registros reales (cuántas contradicciones, duplicados y alias se detectan hoy y cuántos se detectarían con la propuesta). Implementa solo cuando te dé el sí. Tono claro, directo y sin tecnicismos innecesarios.
````
- **L2 (2026-10-17, evidence + proposal):** read-only measurement on the real DB (784 active records) and a 52-record sample (36 with plates + the 16 of the #815 pairs). 64 active records tagged `agm` without `agridemur`; 39 say AGM and never Agridemur; `cosecha` 142 / `cosechas` 20 / `agm-cosechas` 25; AGM also names the monorepo. 38 open `contradicts` pairs (both active), not 8. Sample: 1326 pairs, 32 related; 87 share a plate (no plate in more than 6 records), 69 of them unrelated. 8 plate twins (e.g. 4860GNL~4860NGL) plus 1707KNK/1707NKN. Proposal: tags extend topics, alias dictionary, strength ln(N/n) computed on the fly, `remember` tags + suggested key, `recall` follows aliases and shows connections, `revise` verdict/alias/merge, `status` counts; risks: AGM ambiguity, sync format bump, tag noise. Stored in lodan #897.
- **L3 (2026-10-17, user):** «Como va a saber la IA que etiqueta usar y no va a crear una que ya existe, o como va a funcionar.»
- **L4 (2026-10-17, design answer):** three layers: (1) recall shows the tags of every result and the skill fixes tag families; (2) the server normalizes the form and applies confirmed aliases silently; (3) a new tag similar to an existing one (spelling, plural, meaning) is saved as written and reported as `¿alias?`, never merged automatically, because similarity would join different plates such as 0762MWY and 0763MWY; the AI confirms or rejects the alias, or asks the user when unsure; `status` counts pending suggestions.
- **L5 (2026-10-17, user):** «y ates de guardar revisa las etiquetas y lo que tenga similitus, dame un ejemplo de trabajo completo». Answer: `remember` with `preview: true` checks everything and saves nothing; worked example with the real records #71, #215, #251 (4860GNL is a typo of 4860NGL; 4860NGL belongs to VDM, Recol BK).
- **L6 (2026-10-17, user):** «perfecto, añadelo pero también falta el tema de que solo se guarde un registro de cada cosa y algo más, pensé que también formaba parte del prompt». Stored in lodan #898.
- **L7 (2026-10-17, user):** «juntar repetidos eso era». Evidence (#899): 23 pairs with similarity ≥ 0.90, 90 ≥ 0.85, 409 ≥ 0.80, 0 identical titles; the most similar are not repeats (parts 1/3 and 2/3 of one list, different farms, trainings on different days), so merging always needs a preview and confirmation.
- **L8 (2026-10-17, user):** «si» — approval to implement the full proposal. Branch `feat/hashtags-calidad` created from `feat/sincronizacion-git` (0795041). Next: T1 tests by the examinador.
- **L9 (2026-10-17, T1):** examinador sealed `internal/database/{database_test,tags_migration_test}.go`, `internal/mcptools/{e2e_test,server_test,tags_e2e_test}.go`, `internal/topic/topic_test.go`, `internal/memory/tags_test.go` (seal in `.git/tdd-sello-T1.sha256`). Decision on its doubts: S3 applies to `topic.Resolver` itself (its only callers are remember and revise update); `topic.Resolution` gains `Created`. RED: packages failed to build for the missing `Tags`/`Created` fields, migration tests failed. Built: migration `0004_tags.sql` (made rerunnable: `CREATE OR REPLACE FUNCTION`, `CREATE INDEX IF NOT EXISTS`), resolver without similarity merge plus hyphen-insensitive lookup, `tags` fields with the 8-tag limit, `etiquetas:` output, tags on recall lines. GREEN: seal OK, sealed tests pass; full suite `go test ./...` (database and gitsync rerun with `-timeout 60m`; the default 10 min timeout cut them): all OK except `TestCopyToStaging`, `TestEnsureMicromambaExecutable` and six `TestRunSupervisor*`, which also fail on the base commit 0795041 (known environmental failures). T2 takes the whole S16 (status line) since its counts need no later task.
- **L10 (2026-10-17, T2):** examinador wrote `internal/mcptools/aliases_e2e_test.go` and, with S16 in scope, changed the status assertion of sealed `e2e_test.go:336` (`temas: 0` → `etiquetas: 0`); T1 resealed with that reason, T2 sealed. RED: 24 subtests. Built: alias lookup in the resolver, `revise alias`/`reject_alias`, suggestions after a real save (count excludes the record just saved, matching the preview), recall topic lookup through aliases and without hyphens, status counters; `tag_aliases` added to `TestCluster.Reset`. Two tests looked wrong and the user approved fixing them («si»): recall `topics` is a priority («Temas conocidos a priorizar»), not a filter, so the test checks the alias is applied instead of exclusion; the status test's words collided in the 64-dim fake embedder (14 suggestions instead of 12), so it uses non-colliding words.
- **L11 (2026-10-17, T2 GREEN):** suite run found three more failures: `TestE2E` (sealed) showed a suggestion between two tags created in the same call → fixed in code: suggestions only compare with tags that existed before the call; `TestProbeMigracion0003SobreDatosExistentes` seeds with the current service on a 0001+0002 schema → alias lookup now treats a missing `tag_aliases` (SQLSTATE 42P01) as "no aliases", which also covers a hook writing with the new binary before the service migrates; `TestRunPequeno` (benchmark timing) also fails on the base commit → known environmental. After the fixes: mcptools, memory, topic and the gitsync probe pass; seals T1/T2 OK. The full suite is rerun with T3.
