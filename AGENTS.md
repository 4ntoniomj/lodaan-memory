# AGENTS.md — lodan

lodan es un servicio local de memoria persistente para cualquier IA compatible con MCP. Consta de un servidor en Go, PostgreSQL + pgvector y Ollama para los embeddings; solo escucha en localhost y tiene un único usuario. Producto y alcance: [specs/PRD.md](specs/PRD.md). Especificaciones por funcionalidad: `specs/NNN-slug/`.

## Skills obligatorias

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Siempre, para cualquier tarea de desarrollo: clasifica simple/compleja antes de tocar código. |
| `usar-git` | Siempre: ningún commit, rama o PR fuera de su flujo. `main` solo recibe fusiones aprobadas. |
| `screaming-architecture` | Al crear una funcionalidad o carpeta nueva en `internal/`, y en cada auditoría (mantiene `CODEBASE.md`). |
| `crear-skills` | Para la skill de uso de lodan (spec 003) o si falta una skill para una tarea. |

**IMPORTANTE:** ninguna tarea de desarrollo se implementa sin apoyarse en al menos una skill. Si al terminar de planificar no hay ninguna instalada que encaje, créala con `crear-skills` antes de escribir código.

## Estructura

- `cmd/lodan/`: CLI (`serve`, `db`, `migrate`, `status`, `bench`).
- `internal/<funcionalidad>/`: una carpeta por funcionalidad de negocio (`memory`, `recall`, `topic`, `session`, `embedding`, `mcptools`, `benchmark`), cada una con su `CLAUDE.md`.
- `internal/database/` e `internal/config/`: compartidas, porque las usan tres o más funcionalidades.
- Tests: junto al código como `*_test.go` (convención de Go; desviación documentada de screaming-architecture en `specs/001-nucleo-memoria/plan.md`).

## Comandos

- Compilar: `go build ./cmd/lodan`
- Tests: `go test ./...`
- Análisis: `go vet ./...` y `gofmt -l .` (debe salir vacío)
- Toolchain: `go.mod` fija `toolchain go1.27.1`; con `GOTOOLCHAIN=auto` (valor por defecto) Go la descarga sola.
- Los tests que necesitan base de datos arrancan un PostgreSQL temporal. Requieren binarios de PostgreSQL ≥ 16 con pgvector ≥ 0.7.0 (`pg_config` en el PATH o `LODAN_PG_BIN_DIR`); si no los hay, se saltan con aviso.

## Reglas del proyecto

- Identificadores y código en inglés; documentación, mensajes al usuario y errores en español.
- Nada escucha fuera de `127.0.0.1`. PostgreSQL nunca usa autenticación `trust` por TCP.
- Como máximo 6 herramientas MCP. Respuestas en texto compacto, no JSON, con tope de bytes.
- La IA nunca escribe SQL ni calcula embeddings: eso lo hace el servidor.
- No inventes comandos, flags ni APIs: verifícalos en la documentación o en el código de la dependencia (`go env GOMODCACHE`).
