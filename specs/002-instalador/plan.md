# Plan 002 — Instalador

## Enfoque técnico

Nueva funcionalidad `internal/install` en el mismo binario, organizada en pasos idempotentes. Cada paso tiene `Check` (¿ya está?) y `Apply` (hazlo), y comparte el mismo código con `doctor` (que solo ejecuta los `Check`) y con `uninstall` (`Remove`).

Orden de los pasos:
1. micromamba;
2. entorno PostgreSQL + pgvector;
3. clúster y migraciones (reutiliza `database`);
4. Ollama (reutilizar o instalar);
5. modelo;
6. arranque automático;
7. clientes MCP;
8. skill e instrucciones.

## Estructura

```
internal/install/
├── install.go        # orquestación de pasos, confirmaciones, --yes, resumen
├── micromamba.go     # descarga + sha256, create -r/-p --no-rc --override-channels -c conda-forge
├── ollama.go         # detección (GET /api/version), descarga del archivo oficial, pull con progreso
├── autostart_linux.go / autostart_darwin.go / autostart_windows.go   # build tags
├── clients.go        # tabla de clientes: detección, ruta por SO y formato
├── clients_json.go / clients_toml.go / clients_yaml.go   # editores idempotentes con copia .bak-lodan
├── skill.go          # skill embebida (embed.FS de skills/lodan-memoria) e instrucciones globales con marcadores
├── doctor.go
└── *_test.go
skills/lodan-memoria/  # fuente de la skill en el repo, embebida en el binario
```

## Decisiones

- **micromamba:** binario suelto de GitHub (`releases/latest/download/micromamba-<plataforma>[.exe]`) con verificación de su `.sha256` (comprobado el 2026-09-29 en Linux: 2.9.0, sha coincidente). El entorno se crea en `<datos>/runtime/pg` con raíz `<datos>/runtime/mamba`. En Linux se probó que instala PostgreSQL 18.6 + pgvector 0.8.6 en 17 s (363 MB) y que lodan migra con esos binarios.
- **Windows:** se pide `postgresql=16` de forma explícita; los binarios se buscan en `<env>\Library\bin`. Hay que verificarlo en Windows real.
- **Ollama:**
  - Linux: `.tar.zst` extraído en `<datos>/runtime/ollama`, con `LD_LIBRARY_PATH` a `lib/ollama` (vía comunitaria, hay que verificarla). Hace falta descomprimir zstd en Go: `github.com/klauspost/compress/zstd`, dependencia nueva justificada.
  - macOS: `ollama-darwin.tgz`.
  - Windows: `ollama-windows-amd64.zip`.
  - Arranca con `OLLAMA_HOST=127.0.0.1:11434` y `OLLAMA_MODELS=<datos>/runtime/ollama/models`.
  - Antes de descargar ~1,5 GB se pide confirmación.
- **Modelo por defecto:** `embeddinggemma:300m-qat-q4_0` (239 MB), el mismo con el que se calibraron los umbrales. Cambia el valor por defecto de `config`.
- **Arranque automático:**
  - Linux: unidad `lodan.service` en `~/.config/systemd/user`, con `ExecStart=<bin> db start --foreground` y `Restart=on-failure`, más la de Ollama si lo instaló lodan. Se activa con `systemctl --user enable --now`. `loginctl enable-linger` no se ejecuta (puede pedir polkit); se ofrece como consejo.
  - macOS: LaunchAgents `com.lodan.postgres` y `com.lodan.ollama` con `RunAtLoad` y `KeepAlive`, cargados con `launchctl bootstrap gui/<uid>`.
  - Windows: valor `lodan` en `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` con `<bin> db start --foreground`, usando `golang.org/x/sys/windows/registry` (ya es dependencia indirecta).
  - Para launchd y systemd, PostgreSQL no debe demonizarse: `lodan db start --foreground` ejecuta `postgres -D` en primer plano y reenvía las señales.
- **Clientes:**
  - Registro declarativo con: nombre, detección (existencia del archivo o del directorio de la app), ruta por SO, formato (JSON `mcpServers`, JSON `servers` de VS Code, JSON `mcp` de opencode, JSON `context_servers` de Zed, TOML de Codex, YAML de Hermes) y CLI opcional.
  - Para Claude Code, Codex y Gemini se usa su CLI oficial si está en el PATH; si no, se edita el archivo.
  - La entrada lleva la ruta absoluta del binario, `args: ["serve"]` y, en `env`, solo lo que difiera de los valores por defecto (Hermes no hereda el entorno).
  - Los editores conservan claves desconocidas, escriben de forma atómica (archivo temporal + rename) y dejan una copia `.bak-lodan` del original.
  - Rutas no verificadas (LM Studio en macOS, Windsurf tras su migración) se detectan solo si el archivo existe; nunca se crean a ciegas.
- **Skill e instrucciones:**
  - Carpetas de skills: `~/.claude/skills/lodan-memoria`, y `~/.gemini/config/skills/lodan-memoria` si existe `~/.gemini/config/skills` (allí se retira `lodan-memory`).
  - Instrucciones globales con bloque marcado (versión corta de la regla ya puesta en `~/.claude/CLAUDE.md`): `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md` y `~/.gemini/GEMINI.md`, solo si existe su carpeta de configuración.
  - El bloque se sustituye completo en cada instalación y se elimina en `uninstall`.
- **Interfaz:** texto plano en español con prefijos `✓`, `…`, `!` y `✗`. Flags: `--yes`, `--skip-ollama`, `--skip-clients`, `--only <cliente>` y `--dry-run` (muestra lo que haría sin tocar nada).

## Alternativas descartadas

| Alternativa | Motivo |
|---|---|
| `embedded-postgres` de Go | No trae pgvector |
| Compilar pgvector | Exige toolchain de C; en Windows, Visual Studio |
| Docker | El usuario pidió instalación nativa |
| Instalador oficial de Ollama | En Linux requiere sudo |
| Programador de tareas en Windows | No está claro que un usuario estándar pueda crear tareas `ONLOGON` |

## Riesgos

| Riesgo | Mitigación |
|---|---|
| Windows y macOS sin probar de extremo a extremo | Tests unitarios por SO; prueba real en el host Windows con autorización; macOS se documenta como no probado |
| Rutas de clientes que cambian | Solo se edita si existe; `doctor` lo reporta |
| Ollama en `~/.local` sin documentación oficial | Se prueba en Linux; si falla, se ofrece el instalador oficial como alternativa |
| Descargas grandes en conexiones lentas | Progreso visible y reanudación simple (si el archivo existe con el sha correcto, no se vuelve a bajar) |
