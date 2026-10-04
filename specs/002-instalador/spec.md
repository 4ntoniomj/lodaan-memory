# Spec 002 — Instalador

> Nivel: **complejo** · Rama: `feat/instalador` (sobre `feat/nucleo-memoria`) · Hereda de: [specs/PRD.md](../PRD.md) (R13, O4, O5)

## Intent

Que el usuario tenga lodan funcionando con todas sus IAs con un solo comando, en Windows x64, Linux x64/ARM64 y macOS Intel/Apple Silicon, ejecutándose como **servicio de sistema** gestionable con las herramientas nativas (`systemctl`, `services.msc`, `launchctl`) o con `lodan service`. Administrador solo una vez, para registrar el servicio. Hoy hace falta tener ya PostgreSQL con pgvector y Ollama, compilar el binario, registrar el servidor a mano en cada IA y copiar la skill.

## Alcance

### Dentro

- Subcomandos `lodan install`, `lodan uninstall`, `lodan doctor` y `lodan service start|stop|restart|status|enable|disable` en el mismo binario.
- Servicio de sistema `lodan`: un proceso supervisor (`lodan service run`) que mantiene PostgreSQL en primer plano y las tareas de fondo (embeddings pendientes, cierre de sesiones). En Linux es una unidad de systemd del sistema con `User=<usuario>`; en macOS, un LaunchDaemon con `UserName`; en Windows, un servicio del Service Control Manager.
- PostgreSQL + pgvector desde conda-forge con micromamba, en el directorio de datos del usuario. En Windows se usa PostgreSQL 16, porque es la versión con la que conda-forge compila pgvector allí.
- Ollama:
  - si ya hay uno respondiendo en localhost, se reutiliza;
  - si no, se descarga el archivo oficial en el directorio del usuario y se arranca como proceso de usuario escuchando en 127.0.0.1.
- Descarga del modelo de embeddings por defecto (`embeddinggemma:300m-qat-q4_0`, el calibrado).
- Arranque automático con el sistema a través del propio servicio (habilitado por defecto).
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
   THEN se instala en el directorio del usuario, escucha solo en 127.0.0.1 y queda registrado como servicio de sistema `lodan-ollama`.
4. GIVEN clientes MCP instalados
   WHEN se instala
   THEN el instalador lista los que ha detectado, pide una sola confirmación (salvo con `--yes`) y añade la entrada `lodan` con la ruta absoluta del binario. Se conserva el resto del archivo y se guarda una copia `.bak-lodan`. Ejecutarlo dos veces no duplica nada.
5. WHEN se instala
   THEN la skill `lodan-memoria` queda en `~/.claude/skills/` (y en los clientes con carpeta de skills), las instrucciones globales llevan el bloque `<!-- lodan:inicio --> … <!-- lodan:fin -->` y la skill antigua `lodan-memory` se retira, con copia de seguridad.
6. WHEN se reinicia el equipo
   THEN el servicio `lodan` (y `lodan-ollama`, si lo instaló lodan) arranca solo. `systemctl status lodan` / `services.msc` / `launchctl print system/com.lodan` lo muestran, y `lodan service stop|start|disable|enable|status` lo gestionan de forma equivalente en los tres SO.
7. WHEN se ejecuta `lodan doctor`
   THEN se comprueban el binario, PostgreSQL, pgvector, Ollama, el modelo, el arranque automático, cada cliente configurado y la skill, con una línea por comprobación (`ok`, `aviso` o `error`) y la solución propuesta.
8. WHEN se ejecuta `lodan uninstall`
   THEN se quitan las entradas de los clientes, la skill, los bloques de instrucciones y el arranque automático, y se paran los servicios. Los datos solo se borran con `--purge` y tras una confirmación explícita.
9. Solo el registro, el arranque y la parada de los servicios requieren administrador (se pide con `sudo` o UAC en ese momento). Los datos y la configuración siguen en el perfil del usuario (en Windows, ubicación a decidir tras la prueba real, porque PostgreSQL no se ejecuta con cuentas de administrador). Ningún servicio escucha fuera de 127.0.0.1.
10. `go test ./...` pasa. Las piezas por SO se prueban con tests unitarios sobre directorios temporales (formatos de configuración, unidades, plist, registro simulado). Linux se prueba de extremo a extremo en la máquina de referencia; Windows, en el host Windows de la máquina de referencia si el usuario lo autoriza. macOS queda compilado y probado solo en unidad, y así se documenta.
