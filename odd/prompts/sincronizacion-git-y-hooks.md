[ROL]
Actúa como un ingeniero experto en sistemas de memoria para IA, en sincronización de datos entre máquinas y en hooks de agentes de IA, con pensamiento crítico. Trabajas sobre una aplicación ya creada.

[CONTEXTO]
lodan es mi memoria persistente, compartida por todas mis IAs (Claude Code, opencode y Antigravity). Es un binario Go (repositorio `C:\Users\4ntoniomj\lodaan-memory`, rama por defecto `dev`) con PostgreSQL + pgvector y Ollama para los embeddings, instalado como servicio en `C:\Users\4ntoniomj\AppData\Local\lodan`. Las tablas son, entre otras, `memories`, `topics`, `memory_topics`, `memory_relations`, `sessions` y `session_topics`; los ids son números autoincrementales de cada base, así que el mismo id puede significar registros distintos en dos máquinas. Los embeddings se pueden recalcular: si un registro llega sin embedding, el mantenimiento del servicio lo calcula solo (`FillPending`).

Quiero dos cosas que hace Engram:
- Su sincronización por git: que la memoria de una máquina llegue a mis otras máquinas a través de un repositorio git, sin perder ni duplicar nada.
- Sus automatismos con hooks, que no dependen de la disciplina de la IA: tras una compactación del contexto, inyectar instrucciones de recuperación; al terminar un subagente, capturar lo útil de forma pasiva.

[OBJETIVOS]
1. Exportar la memoria a un repositorio git en ficheros por lotes (solo lo nuevo o cambiado desde la última exportación) e importarla en otra máquina, con dos subcomandos (por ejemplo `lodan sync push` y `lodan sync pull`) y la opción de hacerlo solo cada cierto tiempo desde el servicio.
2. Identidad global estable para cada registro, relación, tema y sesión (por ejemplo un ULID o UUID `sync_id`), para que importar sea idempotente: importar dos veces no duplica, y los ids locales nunca viajan como referencia.
3. Reglas de fusión explícitas: un registro sustituido o invalidado en una máquina lo queda también en la otra; si las dos máquinas cambian lo mismo, gana lo más reciente y la otra versión queda como historial; si dos registros vigentes chocan, se relacionan como `contradicts` y se le pregunta al usuario cuál vale.
4. No sincronizar los vectores de embeddings (se recalculan en cada máquina) ni secretos de configuración (`secret`, `config.json`).
5. Privacidad: lodan guarda datos personales. Decidido: el repositorio es `lodan-sync` en mi GitHub (`4ntoniomj/lodan-sync`, privado) y los ficheros van **cifrados**; en GitHub solo debe verse contenido ilegible. La clave vive solo en mis máquinas, nunca en el repositorio ni en lodan; explica cómo la guardo y cómo la paso a otra máquina, y avisa de que si se pierde el contenido del repositorio no se puede recuperar. Elige el cifrado con una librería estándar y mantenida, verificada en su documentación.
6. Instalar la sincronización en este Windows y dejarla lista para una segunda máquina.
7. Hook tras una compactación: justo después de compactar, la IA recibe automáticamente unas instrucciones de recuperación (llamar a `recall` en lote con los temas en curso antes de seguir) y el resumen de la última sesión de lodan de ese cliente. Si el cliente da el resumen de la compactación, guárdalo en lodan como resumen de la sesión.
8. Hook al terminar un subagente: lee su mensaje final, extrae el bloque `## Key Learnings` (de 1 a 5 puntos numerados) y guarda cada punto en lodan como `note`, en el tema del proyecto en el que se trabaja. Si no hay bloque, no guarda nada. Respeta lo que lodan ya tiene decidido que no se guarda (por ejemplo, datos personales de trabajadores) y no guardes secretos.
9. Los hooks son un subcomando del binario (por ejemplo `lodan hook compact` y `lodan hook subagent-stop`) que lee el JSON del evento por la entrada estándar; si lodan no está disponible, el hook no bloquea a la IA ni rompe la sesión.
10. Instálalos en cada IA donde el evento exista, sin borrar los hooks que ya tengo (en Antigravity hay uno de herdr en `~/.gemini/config/hooks.json`; en Claude Code puede haber otros en `~/.claude/settings.json`). Lo que ya está comprobado en su documentación el 2026-10-16:
   - Claude Code: `SessionStart` con matcher `compact` añade su salida al contexto tras compactar; `PostCompact` recibe `compact_summary`; `SubagentStop` recibe `last_assistant_message`, `agent_type` y `agent_transcript_path` (ojo: un subagente que entrega su informe con `SubagentHandback` lo deja en `tool_input.message` de ese tool, no en `last_assistant_message`).
   - opencode 2.0.30: los plugins tienen un hook de compactación (`experimental.session.compacting` en la documentación de plugins y `ctx.session.hook("compaction")` en la de v2); compruébalo en el código de la versión instalada. No hay un evento directo de fin de subagente: investiga si se puede detectar (por ejemplo, una sesión hija que pasa a inactiva) y, si no, dilo.
   - Antigravity: sus hooks solo tienen `SessionStart`, `PreToolUse`, `PostToolUse`, `PreInvocation`, `PostInvocation` y `Stop`; no hay evento de compactación ni de subagente. No lo simules: dime qué se puede hacer de verdad y qué no.

[RESTRICCIONES]
- No te inventes datos: lo que no sepas, lo investigas en el código o en la documentación; si sigues sin saberlo, lo dices.
- No trabajes en la rama `main` ni directamente en `dev`: crea una rama `feat/sincronizacion-git` desde `dev`.
- Añadir `sync_id` es una migración sobre mi base real: haz antes un backup completo (`pg_dump` en `AppData\Local\lodan\backups-manual`) y prueba la migración en una base temporal.
- Los tests los escribe el subagente `examinador` antes del código (skill `tdd-examinador`); quien programa no los toca.
- Como máximo 6 herramientas MCP (regla del proyecto): la sincronización y los hooks van como subcomandos del binario, no como herramientas nuevas.
- Prueba cada hook de verdad en cada IA (provocando una compactación y lanzando un subagente) antes de darlo por terminado.
- Ninguna credencial de git en el código ni en lodan; usa la configuración de git que ya tengo.
- Pregúntame una sola cosa cada vez, y solo cuando sea una decisión mía.

[FORMATO]
Primero analiza el código actual y devuélveme una propuesta corta: para la sincronización, el formato de los ficheros, la identidad global, las reglas de fusión, la migración y los riesgos; para los hooks, qué evento usas en cada IA, qué inyecta o guarda cada uno y qué no se puede hacer. Implementa solo cuando te dé el sí. Tono claro, directo y profesional.
