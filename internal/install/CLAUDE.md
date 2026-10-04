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
- Windows: el proceso elevado con UAC corre en su propia ventana, que se cierra al terminar y se lleva su salida. Por eso solo allí `lodan service install` y `lodan service uninstall` reciben `--log <DataDir>/logs/service-elevated.log` (escriben ahí además de en stdout y stderr), el instalador lo borra antes de elevar y, si el paso falla, el servicio no aparece o queda sin arrancar, añade al mensaje las últimas 20 líneas (`elevated_log.go`). En Unix no se pasa: con `sudo` la salida llega a la terminal y un archivo creado por root en el directorio de datos del usuario sería un estorbo. En Windows `lodan-ollama` se registra con el envoltorio `lodan service ollama` (ver `internal/service/CLAUDE.md`).

## Pruebas

`go test ./internal/install/...`
