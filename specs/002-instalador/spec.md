# Spec 002 — Instalador

> Nivel: **complejo** · Rama: `feat/instalador` (sobre `feat/nucleo-memoria`) · Hereda de: [specs/PRD.md](../PRD.md) (R13, O4, O5)

## Intent

Que el usuario tenga lodan funcionando con todas sus IAs con un solo comando, en Windows x64, Linux x64/ARM64 y macOS Intel/Apple Silicon, sin permisos de administrador y sin tocar nada fuera de su perfil. Hoy hace falta tener ya PostgreSQL con pgvector y Ollama, compilar el binario, registrar el servidor a mano en cada IA y copiar la skill.

## Alcance

### Dentro

- Subcomandos `lodan install`, `lodan uninstall` y `lodan doctor` en el mismo binario.
- PostgreSQL + pgvector desde conda-forge con micromamba, en el directorio de datos del usuario. En Windows se usa PostgreSQL 16, porque es la versión con la que conda-forge compila pgvector allí.
- Ollama:
  - si ya hay uno respondiendo en localhost, se reutiliza;
  - si no, se descarga el archivo oficial en el directorio del usuario y se arranca como proceso de usuario escuchando en 127.0.0.1.
- Descarga del modelo de embeddings por defecto (`embeddinggemma:300m-qat-q4_0`, el calibrado).
- Arranque automático al iniciar sesión: systemd `--user` en Linux, LaunchAgent en macOS y clave `HKCU\…\Run` en Windows.
- Configuración de los clientes MCP detectados: Claude Code, Claude Desktop, Cursor, Codex, Gemini CLI, Windsurf, VS Code, Hermes, opencode, Zed y LM Studio. Idempotente y con copia de seguridad del archivo.
- Instalación de la skill `lodan-memoria` (embebida en el binario) en los clientes que admiten skills, y de un bloque de reglas marcado en sus archivos de instrucciones globales.
- Sustitución de la skill antigua `lodan-memory` del prototipo.
- Salida en español con progreso y resumen final.

### Fuera

- Publicar binarios (GoReleaser, releases): no hay repositorio remoto todavía.
- Instaladores gráficos y firma de código (Gatekeeper, SmartScreen).
- Windows ARM64.
- Desactivar la auto memory de Claude Code (lo decide el usuario).
- Copias de seguridad (siguiente versión).

## Criterios de aceptación

1. GIVEN una máquina sin PostgreSQL de lodan
   WHEN se ejecuta `lodan install --yes`
   THEN se descarga micromamba (con su SHA-256 verificado), se crea el entorno con PostgreSQL + pgvector, se inicializa el clúster, se migra y `lodan status` responde `BD: ok`.
2. GIVEN un Ollama respondiendo en 127.0.0.1:11434
   WHEN se instala
   THEN no se descarga otro Ollama; si falta el modelo, se descarga con `/api/pull` mostrando el progreso.
3. GIVEN que no hay ningún Ollama
   WHEN se instala (tras confirmar el tamaño de la descarga, ~1,5 GB)
   THEN se instala en el directorio del usuario, escucha solo en 127.0.0.1 y arranca al iniciar sesión.
4. GIVEN clientes MCP instalados
   WHEN se instala
   THEN el instalador lista los que ha detectado, pide una sola confirmación (salvo con `--yes`) y añade la entrada `lodan` con la ruta absoluta del binario. Se conserva el resto del archivo y se guarda una copia `.bak-lodan`. Ejecutarlo dos veces no duplica nada.
5. WHEN se instala
   THEN la skill `lodan-memoria` queda en `~/.claude/skills/` (y en los clientes con carpeta de skills), las instrucciones globales llevan el bloque `<!-- lodan:inicio --> … <!-- lodan:fin -->` y la skill antigua `lodan-memory` se retira, con copia de seguridad.
6. WHEN se reinicia la sesión del usuario
   THEN PostgreSQL de lodan (y Ollama, si lo instaló lodan) arrancan solos.
7. WHEN se ejecuta `lodan doctor`
   THEN se comprueban el binario, PostgreSQL, pgvector, Ollama, el modelo, el arranque automático, cada cliente configurado y la skill, con una línea por comprobación (`ok`, `aviso` o `error`) y la solución propuesta.
8. WHEN se ejecuta `lodan uninstall`
   THEN se quitan las entradas de los clientes, la skill, los bloques de instrucciones y el arranque automático, y se paran los servicios. Los datos solo se borran con `--purge` y tras una confirmación explícita.
9. Nada del proceso requiere admin ni escribe fuera del perfil del usuario, y ningún servicio escucha fuera de 127.0.0.1.
10. `go test ./...` pasa. Las piezas por SO se prueban con tests unitarios sobre directorios temporales (formatos de configuración, unidades, plist, registro simulado). Linux se prueba de extremo a extremo en la máquina de referencia; Windows, en el host Windows de la máquina de referencia si el usuario lo autoriza. macOS queda compilado y probado solo en unidad, y así se documenta.
