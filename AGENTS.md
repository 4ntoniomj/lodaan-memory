# AGENTS.md — lodan

**Tipo:** Servicio local de memoria persistente para IAs compatibles con MCP.
**Stack:** Go (binario único) + PostgreSQL (+ pgvector opcional) + Ollama opcional para embeddings.
**Producto y arquitectura:** [PRD.md](PRD.md). **Módulos, versiones y dependencias:** [CODEBASE.md](CODEBASE.md).

## Principios no opcionales (a partir de `feat/sin-ollama`)

1. **Sin Ollama por defecto.** El binario funciona sin el servicio de embeddings activo. Para activar: `lodan serve --with-embeddings`.
2. **Tags concretas obligatorias.** Todo record guardado lleva al menos una tag concreta de las 13 familias definidas (ver § Taxonomía).
3. **6 campos estructurados.** Cuando el dato los tiene, se llenan los campos opcionales `qué`, `cómo`, `cuándo`, `dónde`, `por qué`, `nota`. No en texto libre dentro de `content`.
4. **Pre-save obligatorio.** Antes de `remember`, la IA verifica que el record no existe (hash + recall con la query). Si hay candidatos en `revisa:`, los lee todos y decide.
5. **Aliases proactivos.** La IA busca alias candidatos en el contenido antes de guardar; aplica `revise alias` cuando detecta.
6. **Multi-interacción siempre.** `recall` con múltiples queries en una sola llamada cuando el usuario pide varias cosas.
7. **IA correlaciona.** Cuando recibe N resultados de una sola `recall`, los lee juntos y los correlaciona en lenguaje natural. El server no lo hace.
8. **Embeddings opcionales.** Default off. Se conservan en schema y código para `--with-embeddings`. No se ejecuta el worker automático de embeddings.

## Taxonomía

### 13 familias concretas (mínimo 1 obligatoria por record)

| Familia | Formato | Uso |
|---|---|---|
| `proyecto-<slug>` | `#proyecto-erp-agm` | Proyectos con repo/worktree. |
| `persona-<slug>` | `#persona-juli` | Personas (vos y contactos personales). |
| `contacto-<empresa>-<persona>` | `#contacto-florette-berta` | Contactos profesionales. |
| `proveedor-<slug>` | `#proveedor-florette` | Empresas que te venden. |
| `cliente-<slug>` | `#cliente-vegamayor` | Empresas a las que vendés. |
| `finca-<slug>` | `#finca-royalveg` | Fincas Agridemur. |
| `vehiculo-<matrícula>` | `#vehiculo-4860ngl` | Coches de empresa (sin guiones en la matrícula). |
| `coche-<slug>` | `#coche-volvo-s60` | Coches personales con atributos. |
| `skill-<slug>` | `#skill-lodan-memoria` | Skills instaladas. |
| `herramienta-<slug>` | `#herramienta-opencode` | Software/herramientas. |
| `recurso-<slug>` | `#recurso-cosechas-2026-2027` | Rutas, archivos clave. |
| `evento-<slug>` | `#evento-sprint-2026-10-18` | Hitos importantes. |
| `apuntes-<slug>` | `#apuntes-pedido-agricultor` | Documentación agregada (procedures, guías, manuales). |

### Reglas
- Regex: `^[a-z][a-z0-9]*(-[a-z0-9]+)*(/[a-z0-9]+)*$`
- Jerarquía con atributo: `coche-volvo-s60/matricula`, `finca-royalveg/partidas`, `proveedor-florette/contacto-pedidos`.
- Denylist: `#misc`, `#general`, `#varios`, `#info`, mayúsculas, espacios.
- Aliases: singular canónico. Plurales y variantes se confirman con `revise alias`.

## Skills obligatorias

| Skill | Cuándo |
|---|---|
| `sdd` | Cualquier tarea de desarrollo: clasifica simple/compleja antes de tocar código. |
| `usar-git` | Commits, ramas, PRs. `main` solo recibe fusiones aprobadas. |
| `screaming-architecture` | Crear funcionalidad/carpeta nueva, mantener `internal/<area>/CLAUDE.md`. |
| `crear-skills` | Si falta una skill para una tarea. |
| `lodan-memoria` | **Siempre, en toda conversación.** Recupera antes de responder, guarda al instante. |
| `lodan-taxonomy` (NUEVO) | Al crear o re-taggear records. Referencia rápida de las 13 familias. |
| `lodan-quality` (NUEVO) | Pre-save checks obligatorios. |
| `lodan-alias` (NUEVO) | Detección y confirmación de aliases. |

**IMPORTANTE:** toda información con sustancia (decisiones, datos, preferencias, eventos, notas) va a lodan con `lodan-memoria`. No en ficheros de memoria.

**IMPORTANTE:** ninguna tarea se implementa sin skill apropiada. Si falta, créala primero.

## Estructura del repo

- `cmd/lodan/`: CLI (`serve`, `db`, `migrate`, `status`, `bench`, `sync`, `install`, `service`, `hook`).
- `internal/<funcionalidad>/`: una por feature de negocio, cada una con su `CLAUDE.md`.
- `internal/database/`, `internal/config/`: compartidas (3+ features).
- `internal/embedding/`: opcional. Compilado pero no ejecutado por default.
- `docs/specs/`: documentos de especificación (Phase 0).
- `odd/tasks/`: documentos de features.
- `PRD.md`, `CODEBASE.md`, `AGENTS.md`: documentos raíz.

## Comandos

- Compilar: `go build ./cmd/lodan`
- Tests: `go test ./...` (con `LODAN_PG_BIN_DIR` si hay tests con BD).
- Análisis: `go vet ./...` y `gofmt -l .` (vacío).
- Toolchain: `go.mod` fija `toolchain go1.27.1`.
- Instalar binario: `lodan install`.

## Reglas del proyecto

- Identificadores y código en inglés; documentación, mensajes al usuario y errores en español.
- Nada escucha fuera de `127.0.0.1`. PostgreSQL sin autenticación `trust` por TCP.
- Máximo 7 herramientas MCP (las 6 + `tags_index`).
- Respuestas en texto compacto, no JSON, con tope de bytes.
- La IA nunca escribe SQL ni calcula embeddings: eso lo hace el servidor.
- **Antes de cada `remember`**: la IA ejecuta `recall` con la query candidata para verificar duplicados. Si hay `revisa:`, decide por cada uno.
- **Antes de crear tag nueva**: la IA verifica con `tags_index()` si existe equivalente.
- **Cuando guarda un procedimiento/guía**: usa `#apuntes-<slug>` (no solo `#forma-de-trabajo`).
- No inventes comandos, flags ni APIs: verifícalos en la documentación o en el código de la dependencia.