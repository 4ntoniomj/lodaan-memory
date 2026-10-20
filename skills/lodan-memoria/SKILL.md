---
name: lodan-memoria
description: >-
  Usa la memoria persistente lodan (servidor MCP con las herramientas remember, recall,
  get, revise, session y status) para guardar sin preguntar lo que tiene sustancia de
  cada conversación (decisiones, datos estables, preferencias, eventos, notas), detectar
  contradicciones y relaciones con lo ya guardado, y recuperar el contexto de uno o
  varios temas en una sola llamada, gastando pocos tokens. Etiquetas (generales como
  #agridemur y concretas como #coche-1234abc, con alias aplicados por el servidor),
  vista previa antes de guardar, veredictos de conflicto y merge de duplicados. Activar
  cuando el usuario comparta un dato, una decisión o una preferencia ("entreno por la
  tarde", "ayer desayuné churros", "hemos decidido usar Postgres"), cuando pregunte por
  algo pasado ("¿cuál es la matrícula?", "¿qué hice en el último entreno?", "¿de qué
  hablamos la última vez?") o cuando salga un tema del que pueda haber contexto
  guardado. No usar para desarrollar, depurar o instalar el propio lodan (usar sdd) ni
  para guardar charla, órdenes de rol o datos temporales.
metadata:
  version: "4.0.0"
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

En cuanto la conversación toca uno o varios temas, llama **una vez** a `tags_index` (si el servidor lo expone) para tener el mapa de tags concretas con jerarquía y conteos. Útil para saber qué nombres existen antes de inventar uno. Después llama **una vez** a `recall` antes de contestar. Si hay varias preguntas o temas, van todas en la misma llamada con `queries` (una lista); el servidor las responde a la vez, en un bloque `## <pregunta>` por cada una.

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

### Paso 3a. Etiquetas, alias y claves concretas

- **Etiquetas (`tags` o `topics`, se juntan):** máximo 8 por registro, sin `#`, en forma normalizada (`Normalize` la baja y le quita guiones para empatar `coche-4860-ngl` con `coche-4860ngl`). **Regla dura:** todo registro DEBE llevar al menos una etiqueta **concreta** de las 12 familias (ver `lodan-taxonomy`). Sin etiqueta concreta el registro queda huérfano y rompe las métricas de cobertura. Las generales (`#trabajo`, `#personal`, `#lodan`, `#odd`, `#arquitectura`, `#feature`, `#runbook`, `#forma-de-trabajo`) son complemento, no sustituto. Familias para las concretas (12):
  - `proyecto-<slug>`, `persona-<slug>`, `contacto-<empresa>-<persona>`, `proveedor-<slug>`, `cliente-<slug>`, `finca-<slug>`, `vehiculo-<matrícula>`, `coche-<slug>`, `skill-<slug>`, `herramienta-<slug>`, `recurso-<slug>`, `evento-<slug>`
  - Forma: `<familia>-<slug>/<atributo>` (jerarquía con `/`), hasta 3 atributos.
  - Si dudas de la familia, carga `lodan-taxonomy`.
- **Pre-save obligatorio:** antes de enviar `items[]`, pasa por `lodan-quality` (forma, denylist, jerarquía, secretos, fechas absolutas). Sin esto, el servidor devuelve errores que rompen el guardado.
- **Clave concreta (`key`):** una sola por registro, de la forma `<etiqueta concreta>/<atributo>` (`coche-4860ngl/matricula`, `coche-4860ngl/itv`, `coche-4860ngl/seguro`). Para datos que cambian con el tiempo: el nuevo sustituye al anterior con la misma clave.
- **Auto-relación por rareza:** si el registro lleva una etiqueta concreta rara (usada en <3 registros activos, umbral `tag_link_strength >= 0.85` por default), el servidor propondrá relaciones automáticas por tag. La IA acepta las evidentes y deja las dudosas para el usuario.
- **Alias:** dos formas de llegar al mismo tema (`#agm` y `#agridemur`). El servidor tiene un diccionario: el servidor lo aplica solo cuando está **confirmado** (`revise alias`). Si aún es una **sugerencia** (la lanza el propio servidor al detectar un parecido), la IA decide: confirmar con `revise alias`, rechazar con `revise reject_alias`, o preguntar al usuario si duda. Carga `lodan-alias` para decisiones sistemáticas.
- **Nada se junta por parecido:** una nueva etiqueta parecida a otra (`coche-4860gnl` vs `coche-4860ngl`) se guarda como está y aparece como `¿alias?: #nueva ≈ #existente` debajo del registro guardado; la IA decide. No se funden nunca solas — eso juntaría dos matrículas distintas (0762MWY y 0763MWY).

### Paso 4. Revisar lo que devuelve el guardado: contradicciones y relaciones

Tras cada ítem guardado, el servidor lista hasta 6 registros con `  revisa: #id [tipo fecha] título — fragmento`. Salen de los mismos temas (aunque no se parezcan en nada), de palabras compartidas y del significado. **Revísalos todos, siempre**, y decide uno por uno:

| Si el candidato… | Es | Haz |
| :--- | :--- | :--- |
| habla de lo mismo, en el mismo momento o con la misma vigencia, con un valor incompatible («tostadas el 2026-10-15» frente a «churros el 2026-10-15»; «la matrícula es 1234 ABC» frente a «la matrícula es 5678 DEF») | contradicción | Relaciónalos como `contradicts` y guarda un veredicto con `revise verdict id=A with_id=B verdict=contradice confidence=0.95`. Con confianza ≥ 0.7 el veredicto se aplica (los dos siguen activos pero quedan relacionados como `contradicts`); por debajo de 0.7 queda pendiente y la IA debe preguntar al usuario (paso 1, caso 1). Con su respuesta: `revise supersede` (`id` = el falso, `with_id` = el bueno) o `invalidate` del falso. |
| es la versión anterior de un dato que cambia («entreno por la tarde» → «ahora entreno por la mañana») y la intención del usuario es clara | sustitución | `revise supersede`, sin preguntar. La próxima vez usa `key` (`<etiqueta concreta>/<atributo>`). |
| trata del mismo objeto, persona, proyecto o asunto aunque diga otra cosa (la matrícula y el seguro del mismo coche; el horario y el gimnasio; una tarea y su proyecto) | relación | `revise relate` con `related`, o `part_of` si uno forma parte del otro. |
| ocurre en otro momento (desayuno del lunes y del martes) o no tiene que ver | nada | Ignóralo. |

Veredictos disponibles (`revise verdict` con `verdict` ∈ `contradice`, `sustituye`, `compatible`, `no_relacionado`):
- `sustituye`: B queda sustituido por A (como `revise supersede`).
- `compatible`: ambos quedan activos, relacionados como `related` y cualquier `contradicts` entre ellos se rechaza.
- `no_relacionado`: cualquier `related`/`contradicts` entre ellos se rechaza.

- Varias decisiones van juntas en **un solo** `revise` con `ops` (lista de operaciones).
- No hace falta que se parezcan para relacionarlos: lo decide el significado, no la similitud.
- Ante la duda entre contradicción y cambio, pregunta.

Verificación: cada `revisa:` tiene una decisión, y las operaciones de `revise` confirman cada cambio.

### Paso 5. Corregir lo que deja de valer

- Cambió: guarda el nuevo con la misma `key` (`<etiqueta concreta>/<atributo>`), o `revise` con `action: supersede`.
- Ya no es cierto: `revise` con `action: invalidate`.
- Borrado físico: solo si el usuario lo pide expresamente; `revise` con `action: delete` y `confirm: true`.
- Mismo dato guardado dos veces (uno más completo que el otro): `revise` con `action: merge`, `ids: [a, b]`, `title` y `content` del registro junto, `tags` (opcional, por defecto la unión de los originales). **Sin `confirm: true` es solo una vista previa**: el servidor devuelve un resumen con la huella de 8 hex. **Con `confirm: true` y `fingerprint` igual a esa huella**, el servidor crea el registro nuevo, mueve la primera clave no vacía, copia cada relación de los originales (salvo las que son entre ellos) al nuevo, y los marca como sustituidos. Tras la confirmación: `#N junta #a, #b (quedan sustituidos)`. El histórico (los registros sustituidos) se conserva.

Verificación: la línea devuelta por `revise` confirma el cambio.

### Paso 5a. Antes de guardar algo caro: `remember` con `preview: true`

Cuando los tags sean delicados (matrículas, IDs, comandos) o el contenido se parezca a algo ya guardado: lanza `remember` con `preview: true` antes de guardar de verdad. La respuesta lleva `VISTA PREVIA — no se ha guardado nada` y muestra, por cada ítem, las etiquetas normalizadas con `#slug`, los `#id` que ya dicen lo mismo (duplicado exacto), la `clave sugerida` que aplicaría el servidor si lo confirma, los `¿alias?` que detectaría y los `revisa:` que tendría que decidir la IA. Si todo encaja, repite sin `preview`; si no, ajusta antes de guardar.

### Paso 6. Cerrar la sesión

Cuando la conversación termina y se guardó algo en ella, llama a `session` con `action: end` y un resumen de 1 a 3 frases: objetivo, qué se decidió y qué queda pendiente. Tras una compactación del contexto, vuelve a llamar a `recall` sobre los temas en curso antes de seguir.

Verificación: `session end` devuelve «Sesión cerrada con resumen».

## Formato de salida

**Regla de oro: denso y completo, sin palabras de relleno.** Una línea de respuesta es «una línea» en el sentido de «sin párrafos ni conectores», NO en el sentido de «línea corta». Si el dato tiene 7 puntualizaciones (ruta, columnas, valores, excepciones, condiciones, ejemplos, significados ambiguos), la línea las enumera separadas por `;` o `,`. Comprimir datos cruciales para que la respuesta «quepa en una línea» es perder información, no ser denso.

1. **La respuesta al usuario: completa y sin relleno.** Da **todo** lo que lodan tiene sobre lo preguntado — incluye lo guardado con siglas, alias o nombres alternativos, y marca como duda lo dudoso. La longitud la marca la cantidad de datos, nunca un tope de líneas.
   - Formato denso: una línea o una fila de tabla por dato; sin párrafos, sin introducciones, sin repetir la pregunta, sin conectores vacíos («Pues bien», «Como dijiste», «Ahora bien», «He guardado…»), sin ofertas posteriores («¿quieres más?», «dime si quieres que…»), sin explicar la memoria ni de dónde sale cada dato salvo que haga falta para resolver una duda.
   - **Nunca te guardes datos encontrados para ofrecerlos después.** Si es pertinente, va en la respuesta.
   - **Lo que SÍ es dato y va siempre:** valores concretos (rutas de ficheros, nombres de columnas, números, fechas, nombres, IDs), puntualizaciones técnicas, condiciones, excepciones, significados ambiguos de acciones (tachar, borrar, marcar), ejemplos, quién dijo qué.
   - **Lo que NO es dato y se omite siempre:** saludos («¡Listo!», «¡Hecho!»), confirmaciones vacías sin contenido, resúmenes del prompt anterior, consejos no pedidos, párrafos de contexto que no aportan dato, ofertas de más ayuda, conexiones de cortesía.

   Ejemplo para «¿cuál es la matrícula del coche?»:

   > 1234 ABC — Seat León de 2016.
   > Seguro hasta el 2027-03-15; ITV pasada el 2026-06-02 sin defectos.

   Ejemplo para «¿qué coches tiene Agridemur?»: la tabla completa de los coches de Agridemur **y** los vehículos marcados AGM (AGM = Agridemur) en otras hojas, con sus dudas, en una sola respuesta.

     Ejemplo del `preview` antes de guardar:

     > VISTA PREVIA — no se ha guardado nada
     > ítem 1 «ITV pasada»
     >   etiquetas: #coche-4860ngl, #itv
     >   clave sugerida: coche-4860ngl/itv (sustituiría a #37)
     >   conectaría: #35 (por #coche-4860ngl), #12 (por #itv)
     >   revisa: #35 [fact 2026-09-12] «Coche de Ana: matrícula» — la ITV se asocia al mismo coche
     >   revisa: #12 [fact 2026-08-04] «ITV caducada 2026-08-01» — la ITV anterior, ahora superada
     >
     > Si todo encaja, repite `remember` sin `preview: true`.

2. **Una línea por cada guardado o corrección, con TODAS las puntualizaciones del registro.** Esa línea refleja el contenido del record, no solo el titular. Si el guardado incluye ruta, columnas, excepciones y significado de una acción, todo aparece en la línea — separado por `;` o `,`, no en frases con punto.

   Ejemplo real (registro #1158 del 2026-10-20, decisión):

   > Guardado en lodan: #1158 [decisión 2026-10-20] Cosecha vs Cargas Marketing — cruzar hoja de cosecha de MK con CARGAS MARKETING (hoja BASE del fichero `G:\Mi unidad\AGRIDEMUR\2. Cosechas\26-27 HOJA DE CARGAS MARKETING.xlsx`; campaña 26-27; copia cosecha_trabajo = 25-26 antigua); BASE con dos columnas FECHA (1ª SALIDA, 2ª CORTE/entrada) y columna CORTE = texto (AGM / VEGA MAYOR / MK / FRUCA), no fecha. Lo que está en cargas pero NO en cosecha = ese día no se corta → sin pedido agricultor y se TACHA en cosecha (siempre tras volver a comprobar cargas). Tachar = negación: en este contexto = NO se va a cargar.

   Variantes: «Guardado en lodan: #id [tipo fecha] …»; «Actualizado en lodan: #id [tipo fecha] …»; «Relacionado en lodan: #a con #b (related|part_of|contradicts)»; «Borrado como no válido: #id».
3. Si hay una contradicción, la pregunta del paso 1, sola, al final.
4. Si lodan no está disponible (`status` falla o las herramientas no responden): una línea que lo diga, sin sustituirlo en silencio por otro almacén.

## Restricciones y reglas

- No inventes herramientas ni parámetros: solo las 6 de lodan, descritas en [herramientas.md](references/herramientas.md). Si una llamada falla por parámetros, lee el error y corrígelos.
- Lotes siempre que haya más de una cosa: `queries` en `recall`, varios `items` en `remember`, `ids` en `get`, `ops` en `revise` y `session`. El tope de 200 por llamada es de seguridad, no de uso.
- No guardes charla, órdenes de rol («eres el agente orquestador»), instrucciones de la sesión, datos temporales ni lo ya guardado.
- No guardes secretos: contraseñas, claves de API, números de documentos de identidad o de cuentas bancarias.
- Nunca guardes fechas relativas («ayer», «hoy», «el martes»): conviértelas.
- **Etiquetas:** como máximo 8 por registro, mezcla de generales y concretas; las concretas son `coche-<matrícula>`, `cliente-<nombre>`, `finca-<nombre>`, `proyecto-<nombre>` u otra familia que el usuario declare, siempre en el formato `<familia>-<slug>`. Una sola `#` por delante se ignora. El servidor **no** junta nunca dos etiquetas por parecido (juntaría matrículas distintas); las sugerencias de alias las confirma o rechaza la IA (o el usuario).
- **Clave concreta (`key`):** una sola por registro, de la forma `<etiqueta concreta>/<atributo>`. El servidor sustituye al anterior con la misma clave.
- **Vista previa antes de guardar:** cuando los tags sean delicados o el contenido se parezca a algo ya guardado, lanza primero `remember` con `preview: true`. Corrige lo que la vista previa te diga (etiqueta sugerida, `¿alias?`, `revisa:`) antes de guardar de verdad.
- **Juntar duplicados (`merge`):** la primera vez, **solo vista previa** (sin `confirm`); con `confirm: true` y `fingerprint` igual al de la vista previa, el servidor crea el registro y marca los originales como sustituidos. Nunca `confirm` sin vista previa, y nunca cambies el contenido entre la vista previa y la confirmación (la huella cambia y el servidor rechaza).
- No escribas SQL ni calcules embeddings; habla en lenguaje natural.
- Borrar definitivamente exige petición expresa del usuario.
- Origen y licencias de las ideas adaptadas: [procedencia.md](references/procedencia.md).
- Mejoras pendientes de esta skill: [backlog.md](references/backlog.md).

## Ejemplos

Conversaciones completas con recuperación en lote, guardado, contradicción, relación y respuesta completa, y un caso que no debe activarla: [EXAMPLE.md](EXAMPLE.md).
