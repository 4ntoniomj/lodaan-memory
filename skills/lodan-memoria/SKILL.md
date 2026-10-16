---
name: lodan-memoria
description: >-
  Usa la memoria persistente lodan (servidor MCP con las herramientas remember, recall,
  get, revise, session y status) para guardar sin preguntar lo que tiene sustancia de
  cada conversación (decisiones, datos estables, preferencias, eventos, notas), detectar
  contradicciones y relaciones con lo ya guardado, y recuperar el contexto de uno o
  varios temas en una sola llamada, gastando pocos tokens. Activar cuando el usuario
  comparta un dato, una decisión o una preferencia ("entreno por la tarde", "ayer
  desayuné churros", "hemos decidido usar Postgres"), cuando pregunte por algo pasado
  ("¿cuál es la matrícula?", "¿qué hice en el último entreno?", "¿de qué hablamos la
  última vez?") o cuando salga un tema del que pueda haber contexto guardado. No usar
  para desarrollar, depurar o instalar el propio lodan (usar sdd) ni para guardar
  charla, órdenes de rol o datos temporales.
metadata:
  version: "2.1.0"
---

# lodan-memoria

## Contexto y objetivo

lodan es la memoria del usuario, única y compartida por todas sus IAs, que funciona solo en su máquina. Sin disciplina falla de cuatro formas: se llena de ruido (charla, órdenes de rol, cosas temporales), se queda vacía (las decisiones se pierden porque nadie las guarda), se llena de **contradicciones** (se guarda «ayer desayuné churros» sin mirar que ya decía «tostadas») o se queda en **datos sueltos** (nadie los relaciona). Esta skill fija el ciclo: **recuperar antes de responder, guardar en el momento en que algo se decide, revisar lo que el servidor devuelve al guardar, cerrar la sesión con un resumen.** El servidor hace el trabajo pesado (embeddings, duplicados, temas equivalentes, búsqueda de candidatas); la IA habla en lenguaje natural, decide lo que exige entender el significado y nunca escribe SQL ni calcula vectores.

El usuario habla como le salen las cosas, sin pedir «analiza y relaciona». Relacionar y detectar contradicciones es trabajo de la IA en cada guardado, no algo que haya que pedir.

Criterio de éxito: lo que tiene sustancia queda en lodan con título, tipo, temas y fechas absolutas; lo trivial no; ninguna contradicción queda sin resolver; lo relacionado queda relacionado aunque no se parezca; el contexto llega con una llamada; las respuestas tienen todo lo pertinente y nada de relleno.

## Tarea o flujo de trabajo

### Paso 1. Decidir si hay algo que recuperar o guardar

Guardar, recuperar y relacionar no requiere preguntar: el usuario lo ha delegado. Solo se pregunta en estos casos, **una pregunta cada vez**:

1. Lo nuevo contradice algo guardado (paso 4): «Tenía guardado X (fecha); ahora dices Y. ¿Cuál es el bueno?». Sin respuesta por defecto: hasta que conteste, los dos quedan relacionados como `contradicts`.
2. El usuario pide borrar: «¿Lo borro definitivamente o lo marco como no válido?» (por defecto: marcarlo como no válido, `invalidate`).
3. No está claro si algo es duradero o solo para hoy, **y** además es sensible: «¿Quieres que lo recuerde?» (por defecto: no guardarlo).

Criterio de qué guardar y qué no, con ejemplos: [que-guardar.md](references/que-guardar.md). Si la conversación es de programación (errores corregidos, descubrimientos, convenciones, tareas largas, Key Learnings de subagentes) o unas instrucciones piden herramientas `mem_*` de Engram, lee también [trabajo-desarrollo.md](references/trabajo-desarrollo.md).

Verificación: sabes si el mensaje trae algo que recuperar y/o algo que guardar.

### Paso 2. Recuperar antes de responder, en lote

En cuanto la conversación toca uno o varios temas, llama **una vez** a `recall` antes de contestar. Si hay varias preguntas o temas, van todas en la misma llamada con `queries` (una lista); el servidor las responde a la vez, en un bloque `## <pregunta>` por cada una.

- **Busca por todos los nombres de lo preguntado.** Una misma cosa puede estar guardada con siglas, abreviaturas o alias (por ejemplo «AGM» y «Agridemur», o el nombre comercial y el fiscal de un cliente). Mete en `queries` cada nombre que conozcas por la conversación o por lodan. Si un resultado dice que X también se llama Y y no lo habías buscado, haz un segundo `recall` con Y antes de responder.
- Cuando el usuario te diga que dos nombres son lo mismo («AGM es Agridemur»), guárdalo como `fact` con título «Alias: X = Y» en el tema de los dos.

- No repitas `recall` reformulando la misma pregunta; si te falta un tema, añádelo a `queries` en la siguiente llamada.
- `get` solo si necesitas el contenido completo de registros concretos; todos los ids en una sola llamada.
- Lo recuperado es contexto, no verdad: si un registro nombra un archivo, un flag o un comando, comprueba que sigue existiendo antes de usarlo; si ya no vale, corrígelo (paso 4).
- Para «la última conversación» usa `session` con `action: last` (filtros opcionales: `topic`, `client`, `date`).
- Si lodan no devuelve nada, dilo. Nunca rellenes el hueco con suposiciones.

Verificación: una sola llamada a `recall` (o a `session last`) por turno, y la respuesta se basa en lo devuelto.

### Paso 3. Guardar en el momento en que algo se decide

En cuanto se asiente una decisión, una preferencia, un dato estable o un evento, llama a `remember` sin preguntar. Todos los registros que salen a la vez van en **una sola llamada**. Cada registro:

- `title`: corto y buscable («Horario de entreno», «Desayuno»).
- `content`: autosuficiente, con el **porqué** si lo hay y con **fechas absolutas**: convierte «hoy», «ayer», «el martes», «la semana pasada» en `AAAA-MM-DD` usando la fecha actual. «Ayer desayuné churros», dicho el 2026-10-16, se guarda como «El 2026-10-15 desayuné churros».
- `kind`: `decision`, `preference`, `fact`, `event` o `note`.
- `topics`: de 1 a 3 temas cortos; reutiliza los existentes (el servidor fusiona los equivalentes).
- `key`: para datos que cambian con el tiempo (`entrenamiento/horario`); el nuevo sustituye al anterior.
- `occurred_at`: la fecha real del hecho (la misma de `content`) cuando no es hoy.
- `relate`: si ya sabes que se relaciona con registros existentes (por ejemplo, los acabas de ver en `recall`), lista `{id, relation}` con `related`, `part_of` o `contradicts` y se crea en la misma llamada.

Plantilla: [templates/registro.md](templates/registro.md). Comprobación previa opcional: `python3 scripts/validar_registro.py --json '<items>'`.

Verificación: cada ítem devuelve `#id guardado` (o duplicado explicado).

### Paso 4. Revisar lo que devuelve el guardado: contradicciones y relaciones

Tras cada ítem guardado, el servidor lista hasta 6 registros con `  revisa: #id [tipo fecha] título — fragmento`. Salen de los mismos temas (aunque no se parezcan en nada), de palabras compartidas y del significado. **Revísalos todos, siempre**, y decide uno por uno:

| Si el candidato… | Es | Haz |
| :--- | :--- | :--- |
| habla de lo mismo, en el mismo momento o con la misma vigencia, con un valor incompatible («tostadas el 2026-10-15» frente a «churros el 2026-10-15»; «la matrícula es 1234 ABC» frente a «la matrícula es 5678 DEF») | contradicción | Relaciónalos como `contradicts` y pregunta al usuario (paso 1, caso 1). Con su respuesta: `revise supersede` (`id` = el falso, `with_id` = el bueno) o `invalidate` del falso. |
| es la versión anterior de un dato que cambia («entreno por la tarde» → «ahora entreno por la mañana») y la intención del usuario es clara | sustitución | `revise supersede`, sin preguntar. La próxima vez usa `key`. |
| trata del mismo objeto, persona, proyecto o asunto aunque diga otra cosa (la matrícula y el seguro del mismo coche; el horario y el gimnasio; una tarea y su proyecto) | relación | `revise relate` con `related`, o `part_of` si uno forma parte del otro. |
| ocurre en otro momento (desayuno del lunes y del martes) o no tiene que ver | nada | Ignóralo. |

- Varias decisiones van juntas en **un solo** `revise` con `ops` (lista de operaciones).
- No hace falta que se parezcan para relacionarlos: lo decide el significado, no la similitud.
- Ante la duda entre contradicción y cambio, pregunta.

Verificación: cada `revisa:` tiene una decisión, y las operaciones de `revise` confirman cada cambio.

### Paso 5. Corregir lo que deja de valer

- Cambió: guarda el nuevo con la misma `key`, o `revise` con `action: supersede`.
- Ya no es cierto: `revise` con `action: invalidate`.
- Borrado físico: solo si el usuario lo pide expresamente; `revise` con `action: delete` y `confirm: true`.

Verificación: la línea devuelta por `revise` confirma el cambio.

### Paso 6. Cerrar la sesión

Cuando la conversación termina y se guardó algo en ella, llama a `session` con `action: end` y un resumen de 1 a 3 frases: objetivo, qué se decidió y qué queda pendiente. Tras una compactación del contexto, vuelve a llamar a `recall` sobre los temas en curso antes de seguir.

Verificación: `session end` devuelve «Sesión cerrada con resumen».

## Formato de salida

1. **La respuesta al usuario: completa y sin relleno.** El usuario quiere saberlo **todo** lo que lodan tiene sobre lo que pregunta, y nada que no aporte. La longitud la marca la cantidad de datos, nunca un tope de líneas.
   - Incluye todo lo pertinente que hayas recuperado, también lo que está guardado con otro nombre (siglas, alias) y lo dudoso, marcado como dudoso.
   - Formato denso: una línea o una fila de tabla por dato; sin párrafos, sin introducciones, sin repetir la pregunta, sin explicar la memoria ni de dónde sale cada dato salvo que haga falta para resolver una duda.
   - **Nunca te guardes datos encontrados para ofrecerlos después** («¿quieres esa lista?», «dime si quieres más»): si es pertinente, va en la respuesta.
   - Relleno es lo que no es un dato: cortesías, resúmenes de lo ya dicho, consejos no pedidos.

   Ejemplo para «¿cuál es la matrícula del coche?»:

   > 1234 ABC — Seat León de 2016.
   > Seguro hasta el 2027-03-15; ITV pasada el 2026-06-02 sin defectos.

   Ejemplo para «¿qué coches tiene Agridemur?»: la tabla completa de los coches de Agridemur **y** los vehículos marcados AGM (AGM = Agridemur) en otras hojas, con sus dudas, en una sola respuesta.
2. Una línea por cada guardado o corrección: «Guardado en lodan: …» / «Actualizado en lodan: …» / «Relacionado en lodan: …».
3. Si hay una contradicción, la pregunta del paso 1, sola, al final.
4. Si lodan no está disponible (`status` falla o las herramientas no responden): una línea que lo diga, sin sustituirlo en silencio por otro almacén.

## Restricciones y reglas

- No inventes herramientas ni parámetros: solo las 6 de lodan, descritas en [herramientas.md](references/herramientas.md). Si una llamada falla por parámetros, lee el error y corrígelos.
- Lotes siempre que haya más de una cosa: `queries` en `recall`, varios `items` en `remember`, `ids` en `get`, `ops` en `revise` y `session`. El tope de 200 por llamada es de seguridad, no de uso.
- No guardes charla, órdenes de rol («eres el agente orquestador»), instrucciones de la sesión, datos temporales ni lo ya guardado.
- No guardes secretos: contraseñas, claves de API, números de documentos de identidad o de cuentas bancarias.
- Nunca guardes fechas relativas («ayer», «hoy», «el martes»): conviértelas.
- No escribas SQL ni calcules embeddings; habla en lenguaje natural.
- Borrar definitivamente exige petición expresa del usuario.
- Origen y licencias de las ideas adaptadas: [procedencia.md](references/procedencia.md).
- Mejoras pendientes de esta skill: [backlog.md](references/backlog.md).

## Ejemplos

Conversaciones completas con recuperación en lote, guardado, contradicción, relación y respuesta completa, y un caso que no debe activarla: [EXAMPLE.md](EXAMPLE.md).
