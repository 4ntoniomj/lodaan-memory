---
name: lodan-memoria
description: >-
  Usa la memoria persistente lodan (servidor MCP con las herramientas remember, recall,
  get, revise, session y status) para guardar sin preguntar lo que tiene sustancia de
  cada conversación (decisiones, datos estables, preferencias, eventos, notas) y para
  recuperar el contexto de un tema en una sola llamada, gastando pocos tokens. Activar
  cuando el usuario comparta un dato, una decisión o una
  preferencia ("entreno por la tarde", "hemos decidido usar Postgres"), cuando pregunte
  por algo pasado ("¿qué hice en el último entreno?", "recuérdame qué decidimos",
  "¿de qué hablamos la última vez?") o cuando salga un tema del que pueda haber
  contexto guardado. No usar para desarrollar, depurar o instalar el propio lodan
  (usar sdd) ni para guardar charla, órdenes de rol o datos temporales.
metadata:
  version: "1.0.0"
---

# lodan-memoria

## Contexto y objetivo

lodan es la memoria del usuario, única y compartida por todas sus IAs, que funciona solo en su máquina. Sin disciplina falla de dos formas: se llena de ruido (charla, órdenes de rol, cosas temporales) o se queda vacía (las decisiones se pierden porque nadie las guarda). Esta skill fija el ciclo: **recuperar antes de responder, guardar en el momento en que algo se decide, cerrar la sesión con un resumen.** El servidor hace el trabajo pesado (embeddings, duplicados, temas equivalentes, búsqueda); la IA solo habla en lenguaje natural y nunca escribe SQL ni calcula vectores.

Criterio de éxito: lo que tiene sustancia queda en lodan con título, tipo y temas correctos; lo trivial no; el contexto de un tema llega con una llamada a `recall`; el usuario ve una sola línea por cada guardado.

## Tarea o flujo de trabajo

### Paso 1. Decidir si hay algo que recuperar o guardar

Guardar y recuperar no requiere preguntar: el usuario lo ha delegado. Solo se pregunta en estos casos, con una pregunta y una respuesta por defecto:

1. El dato nuevo contradice uno guardado: «Tenía guardado X; ¿lo sustituyo por Y?» (por defecto: sí, sustituir con `key` o `revise supersede`).
2. El usuario pide borrar: «¿Lo borro definitivamente o lo marco como no válido?» (por defecto: marcarlo como no válido, `invalidate`).
3. No está claro si algo es duradero o solo para hoy, **y** además es sensible: «¿Quieres que lo recuerde?» (por defecto: no guardarlo).

Criterio de qué guardar y qué no, con ejemplos: [que-guardar.md](references/que-guardar.md).

Verificación: sabes si el mensaje trae algo que recuperar (un tema, una pregunta por el pasado) y/o algo que guardar.

### Paso 2. Recuperar antes de responder

En cuanto la conversación toca un tema (entrenamiento, el coche, un proyecto, un recuerdo…), llama **una vez** a `recall` con la pregunta en lenguaje natural, antes de contestar. Devuelve la ficha del tema (datos estables y últimos eventos) y los resultados.

- No repitas `recall` con variaciones de la misma pregunta: una llamada basta.
- Usa `get` solo si necesitas el contenido completo de un registro concreto que aparece en la respuesta.
- Para «la última conversación» usa `session` con `action: last` (filtros opcionales: `topic`, `client`, `date`).
- Si lodan no devuelve nada, dilo. Nunca rellenes el hueco con suposiciones.

Verificación: una sola llamada a `recall` (o a `session last`) por tema, y la respuesta al usuario se basa en lo que ha devuelto.

### Paso 3. Guardar en el momento en que algo se decide

En cuanto se asiente una decisión, una preferencia, un dato estable o un evento, llama a `remember` sin preguntar y sin esperar al final de la conversación. Cada registro:

- `title`: corto y buscable («Horario de entreno», «lodan usa pgvector»).
- `content`: autosuficiente, que se entienda sin la conversación; incluye el **porqué** si lo hay.
- `kind`: `decision`, `preference`, `fact`, `event` o `note`.
- `topics`: de 1 a 3 temas libres y cortos; reutiliza los existentes (el servidor fusiona los equivalentes).
- `key`: para datos que cambian con el tiempo (`entrenamiento/horario`); el nuevo sustituye al anterior.
- `occurred_at`: fecha real de un evento, si no es hoy.

Agrupa en una sola llamada los registros que salen a la vez. Plantilla: [templates/registro.md](templates/registro.md). Comprobación previa opcional: `python3 scripts/validar_registro.py --json '<items>'`.

Tras guardar, dile al usuario **una sola línea**: «Guardado en lodan: horario de entreno (preferencia).» Si la respuesta de `remember` avisa de parecidos o de un duplicado, decide: sustituir (`revise supersede`), relacionar (`revise relate`) o dejarlo.

Verificación: cada `remember` devuelve `#id guardado` (o duplicado explicado) y el usuario ha visto su línea.

### Paso 4. Corregir lo que deja de valer

- Cambió: guarda el nuevo con la misma `key`, o usa `revise` con `action: supersede`.
- Ya no es cierto: `revise` con `action: invalidate`.
- Borrado físico: solo si el usuario lo pide expresamente; `revise` con `action: delete` y `confirm: true`.

Verificación: la línea devuelta por `revise` confirma el cambio.

### Paso 5. Cerrar la sesión

Cuando la conversación termina y se guardó algo en ella, llama a `session` con `action: end` y un resumen de 1 a 3 frases: objetivo, qué se decidió y qué queda pendiente. Tras una compactación del contexto, vuelve a llamar a `recall` sobre el tema en curso antes de seguir.

Verificación: `session end` devuelve «Sesión cerrada con resumen».

## Formato de salida

1. La respuesta al usuario, basada en lo recuperado. La memoria es gestión interna, nunca la respuesta en sí.
2. Una línea por cada guardado o corrección: «Guardado en lodan: …» / «Actualizado en lodan: …».
3. Si lodan no está disponible (`status` falla o las herramientas no responden): una línea que lo diga, sin sustituirlo en silencio por otro almacén.

## Restricciones y reglas

- No inventes herramientas ni parámetros: solo las 6 de lodan, descritas en [herramientas.md](references/herramientas.md). Si una llamada falla por parámetros, lee el error y corrígelos.
- No guardes charla, órdenes de rol («eres el agente orquestador»), instrucciones de la sesión, datos temporales ni lo ya guardado.
- No guardes secretos: contraseñas, claves de API, números de documentos de identidad o de cuentas bancarias.
- No escribas SQL ni calcules embeddings; habla en lenguaje natural.
- No hagas varias llamadas a `recall` para la misma pregunta ni pidas `get` de todo por sistema: cada llamada cuesta tokens.
- Borrar definitivamente exige petición expresa del usuario.
- Origen y licencias de las ideas adaptadas: [procedencia.md](references/procedencia.md).
- Mejoras pendientes de esta skill: [backlog.md](references/backlog.md).

## Ejemplos

Conversación completa con recuperación, guardado, corrección y cierre, y un caso que no debe activarla: [EXAMPLE.md](EXAMPLE.md).
