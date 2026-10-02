# CLAUDE.md — install

## Resumen

**Problema**: que lodan quede funcionando con todas las IAs del usuario con un solo comando, en Windows, Linux y macOS.
**Objetivo**: `lodan install`, `lodan doctor` y `lodan uninstall` (spec 002): PostgreSQL + pgvector con micromamba desde conda-forge, Ollama (reutilizado o instalado), modelo, servicio de sistema (vía `internal/service`, con `sudo`/UAC solo en ese paso), clientes MCP, skill `lodan-memoria` e instrucciones globales.
**Alcance**: dentro: orquestación idempotente y reversible. Fuera: el gestor de servicios de cada SO (vive en `internal/service`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial; tocar la instalación en los tres SO es siempre complejo. |
| `usar-git` | Cualquier commit o rama que la toque. |
| `crear-skills` | Si cambia la skill embebida (`skills/lodan-memoria`). |

## Convenciones específicas

- Todo efecto externo pasa por un campo inyectable de `Installer`: los tests nunca descargan, elevan ni tocan el HOME real.
- Ningún archivo de un cliente se crea si no existe la carpeta de su app; siempre copia `.bak-lodan` y escritura atómica.
- Claude Code se considera instalado solo si existe `~/.claude.json` o el CLI `claude` está en el PATH, no por la carpeta `~/.claude` (la crea el propio instalador al copiar la skill). El bloque de `~/.claude/CLAUDE.md` solo se escribe y comprueba con Claude Code detectado; al desinstalar se retira siempre, y `~/.claude` (y `skills/`) se borran solo si quedan totalmente vacías.
- `--dry-run` no escribe, no descarga y no eleva.

## Pruebas

`go test ./internal/install/...`
