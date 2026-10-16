# lodan: batches, contradiction review and relations

Objective: make every lodan tool accept batches, let the AI detect contradictions and relations when saving, and keep answers short.
Branch: `feat/lodan-lotes-relaciones` (from `dev`). Delivery: `single-pr`. Test runner: `go test ./...` (DB tests need `LODAN_PG_BIN_DIR=C:/Users/4ntoniomj/AppData/Local/lodan/runtime/pg/Library/bin`).

## Specs

User requirements quoted verbatim are in `## Log` (L1-L3). The agreed design:

- **S1 — Batches everywhere, one shared safety cap.** "trátalo a todo por igual". Every tool that takes operations accepts a list, all with the same safety cap `200` (not a usage limit):
  - `recall`: new input `queries` ([]string). `query` keeps working. The effective list is `query` (if non-empty) followed by `queries`, trimmed, empty strings dropped, exact duplicates answered once. No query at all → error `hay que enviar query o queries`. More than 200 → error `se enviaron N consultas y el máximo es 200`. `topics`, `limit` and `include_history` apply to every query.
  - `get`: `ids` cap goes from 20 to 200 (existing message `se pidieron N ids y el máximo es 200`).
  - `remember`: `items` cap goes from 20 to 200 (existing message `se enviaron N ítems y el máximo es 200`).
  - `revise`: new input `ops` ([]object with the same fields as today: `id`, `action`, `with_id`, `relation`, `title`, `content`, `topics`, `confirm`). The top-level fields keep working as one operation, placed before `ops`. No operation → error `hay que enviar una operación (id y action) u ops`. More than 200 → `se enviaron N operaciones y el máximo es 200`.
  - `session`: new input `ops` ([]object with `action`, `topic`, `client`, `date`, `summary`, `limit`). Top-level fields keep working as one operation before `ops`. Same messages as `revise` (`hay que enviar una operación (action) u ops`, `se enviaron N operaciones y el máximo es 200`).
  - Tool descriptions and JSON schema descriptions say the new caps (e.g. `máx. 200`).
- **S2 — Batch output format.**
  - With exactly one query/operation, the output is byte-identical to the current output.
  - `recall` with N>1 queries: one block per query, in order, separated by a blank line; each block starts with the line `## <query>`; the hint `Usa get con los ids para ver el detalle.` appears once, as the last line, not once per block. Each block is capped at `min(recall_max_bytes, max(4*recall_max_bytes/N, 600))` bytes with the existing truncation notice. A query that fails validation (e.g. too long) shows `error: <message>` in its block and the others are still answered.
  - `revise` and `session` with N>1 operations: operations run in order and independently (one failing does not stop the rest). Each operation's output is prefixed with `N. ` (1-based); a failed one is `N. error: <message>`. The tool result has `IsError` only when every operation failed.
- **S3 — Real concurrency and read-only hints.** `recall`, `get` and `status` declare `readOnlyHint: true`; `remember`, `revise` and `session` do not. A `recall` batch computes the query embeddings in batched calls to the embedder (not one call per query) and runs the searches concurrently with a bounded number of workers.
- **S4 — Review candidates on save.** "la IA buscase si hay algo y si es contradictorio me pregunte cual es la información real". For every newly saved (non-duplicate) item, `remember` searches with the hybrid recall engine (topic sheet of the item's topics + full text + semantic) using the item as the query, and lists up to 6 active candidates right after the item's line, one per line: `  revisa: <recall item line>` (two spaces, `revisa: `, then the same `#id [tipo fecha] título — fragmento` line recall uses). Candidates never include the item itself, records it superseded, records it related with `relate` in the same call, nor other items saved in the same call. Active records sharing a topic with the item must appear as candidates even when their semantic similarity is low: "puede estar algo relacionado y no tener similitud" (example only: «matrícula del coche» vs «seguro del coche» = 0.38). When a topic has at most 3 other active records, all of them appear. Duplicates get no candidates.
- **S5 — No similarity-only relations; relations in the same call.** `remember` and the pending-embedding worker no longer create `related`/`suggested` relations by similarity, and the `parecidos:` part of the output disappears. Each `remember` item accepts `relate`: a list of `{id, relation}` with `relation` in `related`, `contradicts`, `part_of` (default `related`); each creates a confirmed relation from the new record to `id`. Another relation value → validation error naming the allowed ones. A missing target does not fail the save: the item's line ends with ` · relación con #X no creada: no existe`.
- **S6 — Server instructions.** The MCP `Instructions` (≤1500 characters) tell the AI to: batch requests in one call; read every `revisa:` candidate after saving and decide contradiction (ask the user once which value is true, then `supersede`/`invalidate`), relation (`revise` ops or `relate`) or nothing; store absolute dates (convert «ayer», «hoy») in `content` and `occurred_at`; answer the user briefly: one line with the fact and at most two lines with the most useful related information.
- **S7 — Skill `lodan-memoria`.** Same rules as S6 with examples beyond the car (breakfast, schedules, people, projects), batching guidance, contradiction protocol (same subject + same time/validity + incompatible value = contradiction; different days = not), relation types, answer format "1 línea + hasta 2". `references/herramientas.md`, `EXAMPLE.md`, `que-guardar.md` and `eval_cases.json` updated. README and CODEBASE docs updated for the new inputs.
- **S8 — Relation backfill.** After installing, with a full backup first, the AI reviews the active records without relations topic by topic and creates the relations it judges real (batched `revise` ops), without similarity thresholds.
- **S9 — Install on this Windows machine** for Claude Code, opencode and Antigravity: new binary in `C:\Users\4ntoniomj\AppData\Local\lodan\bin`, updated skill where each client reads it, each client connects and lists the new schemas.

## Tasks

| ID | Specs | Route | Status | Evidence |
| --- | --- | --- | --- | --- |
| T0 | — | inline | done | sync installed skill into repo |
| T1 | S1-S5 | examinador writes tests; parent implements inline | pending | |
| T2 | S6-S7 | inline (passive docs + Instructions test) | pending | |
| T3 | S9 | inline | pending | |
| T4 | S8 | inline (AI judgement) | pending | |

## Log

- L1 (2026-10-16, user, original request): "Ahora mismo la IA solo puede hacer una petición simultanea a lodan y es algo contraproductivo porque a lo mejor la IA tiene que hacer varias peticiones y tiene que ir una por una en vez de mandar un lote y que responda con todo [...] cuando le digo que ayer desayune churros en vez de buscar si hay un registro anterior que diga lo contrario directamente lo guarda creando muchas contradicciones, yo creo que lo suyo sería que la IA buscase si hay algo y si es contradictorio me pregunte cual es la información real. Tampoco la IA crea relaciones [...] Si le hago preguntas me responde con lo justo pero muy extenso, por ejemplo le pregunto por una matrícula y me responde con 3 parrafos cuando se puede resumir en una linea [...] si yo le pregunto por la matrícula me responda con 3 líneas pero con toda la información que hay al respecto". Objectives: efficiency with simultaneous requests; analyse saving, relations and answers; proposal; implement everything; install on this OS for claude, opencode, antigravity. Restrictions: no invented data; not on `main`.
- L2 (2026-10-16, user): "cual es el problema con limitar las preguntas, porque no lo dejas ilimitado, las operaciones igual [...] lo de las relaciones puede estar algo relacionado y no tener similitud, creo yo, no está bien propuesto".
- L3 (2026-10-16, user, approval): "Vale, pero el reto de peticiones que puedes hacer con lodan está limitado, osea lo de escribir, actualizar y tal, tratalo a todo por igual, lo demás bien mientras que lo de la matrícula te lo hayas tomado como un ejemplo y no como lo único que hay que arreglar".
- L4 (evidence, 2026-10-16): server already handles calls concurrently (go-sdk `jsonrpc2.Async`; 5 pipelined recalls answered out of order in 0.09 s). Contradiction pair tostadas/churros cosine 0.81 < dup threshold 0.92. 24 automatic relations in the whole DB; 417/716 active records without relations. Installed skill was newer than the repo copy (T0).
