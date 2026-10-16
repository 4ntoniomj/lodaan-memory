[ROL]
Actúa como un ingeniero experto en sistemas de memoria para IA y en sincronización de datos entre máquinas, con pensamiento crítico. Trabajas sobre una aplicación ya creada.

[CONTEXTO]
lodan es mi memoria persistente, compartida por todas mis IAs (Claude Code, opencode y Antigravity). Es un binario Go (repositorio `C:\Users\4ntoniomj\lodaan-memory`, rama por defecto `dev`) con PostgreSQL + pgvector y Ollama para los embeddings, instalado como servicio en `C:\Users\4ntoniomj\AppData\Local\lodan`. Las tablas son, entre otras, `memories`, `topics`, `memory_topics`, `memory_relations`, `sessions` y `session_topics`; los ids son números autoincrementales de cada base, así que el mismo id puede significar registros distintos en dos máquinas. Los embeddings se pueden recalcular: si un registro llega sin embedding, el mantenimiento del servicio lo calcula solo (`FillPending`).

Quiero lo que hace Engram con su sincronización por git: que la memoria de una máquina llegue a mis otras máquinas a través de un repositorio git, sin perder ni duplicar nada.

[OBJETIVOS]
1. Exportar la memoria a un repositorio git en ficheros por lotes (solo lo nuevo o cambiado desde la última exportación) e importarla en otra máquina, con dos subcomandos (por ejemplo `lodan sync push` y `lodan sync pull`) y la opción de hacerlo solo cada cierto tiempo desde el servicio.
2. Identidad global estable para cada registro, relación, tema y sesión (por ejemplo un ULID o UUID `sync_id`), para que importar sea idempotente: importar dos veces no duplica, y los ids locales nunca viajan como referencia.
3. Reglas de fusión explícitas: un registro sustituido o invalidado en una máquina lo queda también en la otra; si las dos máquinas cambian lo mismo, gana lo más reciente y la otra versión queda como historial; si dos registros vigentes chocan, se relacionan como `contradicts` y se le pregunta al usuario cuál vale.
4. No sincronizar los vectores de embeddings (se recalculan en cada máquina) ni secretos de configuración (`secret`, `config.json`).
5. Privacidad: lodan guarda datos personales. Antes de diseñar, pregúntame si quiero los ficheros del repositorio **cifrados** (en GitHub solo se ve contenido ilegible; la clave vive solo en mis máquinas y, si la pierdo, el contenido del repositorio no se puede recuperar) o **en claro** en un repositorio privado (legible por cualquiera que entre en la cuenta), y qué repositorio usar.
6. Instalarlo en este Windows y dejarlo listo para una segunda máquina.

[RESTRICCIONES]
- No te inventes datos: lo que no sepas, lo investigas en el código o en la documentación; si sigues sin saberlo, lo dices.
- No trabajes en la rama `main` ni directamente en `dev`: crea una rama `feat/sincronizacion-git` desde `dev`.
- Añadir `sync_id` es una migración sobre mi base real: haz antes un backup completo (`pg_dump` en `AppData\Local\lodan\backups-manual`) y prueba la migración en una base temporal.
- Los tests los escribe el subagente `examinador` antes del código (skill `tdd-examinador`); quien programa no los toca.
- Como máximo 6 herramientas MCP (regla del proyecto): la sincronización va como subcomando del binario, no como herramienta nueva.
- Ninguna credencial de git en el código ni en lodan; usa la configuración de git que ya tengo.
- Pregúntame una sola cosa cada vez, y solo cuando sea una decisión mía.

[FORMATO]
Primero analiza el código actual y devuélveme una propuesta corta (diseño del formato de los ficheros, identidad global, reglas de fusión, migración y riesgos). Implementa solo cuando te dé el sí. Tono claro, directo y profesional.
