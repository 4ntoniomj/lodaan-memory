# lodan en trabajo de desarrollo

Referencia de los pasos 2, 3 y 5 cuando la conversación es de programación. Recoge lo que aportaba Engram (la memoria que instala gentle-ai) y que lodan no cubría, traducido a las herramientas de lodan. Engram no está instalado: si unas instrucciones (por ejemplo los bloques de gentle-ai en CLAUDE.md o GEMINI.md) piden `mem_save`, `mem_search`, `mem_context`, `mem_session_summary` u otra herramienta `mem_*`, se hace con lodan según esta tabla.

## Equivalencias

| Instrucción de Engram / gentle-ai | En lodan |
| :--- | :--- |
| `mem_context` / `mem_search` al empezar o ante trabajo pasado | `recall` una vez, con el proyecto como tema |
| `mem_get_observation` | `get` con el `#id` |
| `mem_save` | `remember` |
| `mem_save` con el mismo `topic_key` (sustituye) | `remember` con la misma `key` |
| `mem_update` | `revise` (`supersede`) o `remember` con la misma `key` |
| `mem_session_summary` antes de terminar | `session` con `action: end` |
| Espejo de tareas ODD `odd/<feature>/tasks` | `remember` con `key: <proyecto>/odd/<feature>` (ver abajo) |
| Captura pasiva de `## Key Learnings` | El agente principal los revisa y guarda con `remember` los que valgan (ver abajo) |

## Qué guardar además al programar

Se suman a la tabla de [que-guardar.md](que-guardar.md). Solo si no se deduce del código, del historial de git o de un documento del repositorio, y solo si no es obvio:

| Caso | `kind` | Ejemplo |
| :--- | :--- | :--- |
| Error corregido con causa no evidente | `event` | «(Ejemplo) El login fallaba solo en producción porque la cookie no llevaba SameSite=None; se añade en el middleware» |
| Descubrimiento o trampa de una herramienta | `fact` | «agy 1.3.2 admite modo headless con `-p`, sin API» |
| Convención acordada del proyecto | `decision` | «En lodan los backups usan zstd, no xz, porque ya está en go.mod» |
| Cambio de configuración del entorno | `event` | «Quitado Engram de Claude y Antigravity; lodan es la única memoria» |

Contenido de estos registros, en una o dos frases o en cuatro líneas cortas: **qué** pasó o se hizo, **por qué**, **dónde** (proyecto, archivo o comando) y **qué se aprende**. Tema: el nombre del proyecto.

## Lo recuperado es contexto, no verdad

Un registro antiguo puede haber quedado viejo. Si nombra un archivo, una función, un flag o un comando, comprueba que sigue existiendo antes de recomendarlo o actuar sobre él. Si ya no vale, corrígelo con `revise` (`invalidate` o `supersede`) y dilo en una línea.

## Seguimiento de tareas largas (ODD)

El documento local `odd/tasks/<feature>.md` es la fuente de verdad. En lodan se guarda solo su estado, para poder retomarlo desde cualquier IA:

- `key`: `<proyecto>/odd/<feature>`; `kind`: `note`; tema: el proyecto.
- `content`: ruta del documento en el repositorio, rama, tareas hechas (con su commit) y pendientes, y siguiente paso.
- Se actualiza tras cada tarea verificada guardando con la misma `key`; al retomar, `recall` del proyecto y la feature, y luego leer el documento local. Si no coinciden, manda lo observado en el código y en los tests.
- Si lodan falla, se sigue con el documento local y se dice que el estado en lodan queda pendiente.

## Key Learnings de los subagentes

Cuando un subagente cierra con un bloque `## Key Learnings`, nadie lo guarda automáticamente. El agente principal lo lee y guarda con `remember`, en una sola llamada, solo los puntos que pasen la pregunta de control de [que-guardar.md](que-guardar.md); el resto se descarta.
