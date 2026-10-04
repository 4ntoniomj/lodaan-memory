# Qué guardar en lodan y qué no

Referencia de los pasos 1 y 3. El criterio es el del usuario (PRD de lodan, R14): se guarda todo dato con sustancia y nada temporal.

## Pregunta de control

Antes de guardar: «¿Esto le servirá al usuario o a otra IA en una conversación futura, dentro de días, meses o años?». Si sí, se guarda. Si solo sirve para la conversación actual, no.

## Sí se guarda

| Tipo (`kind`) | Qué es | Ejemplos |
| :--- | :--- | :--- |
| `decision` | Algo elegido entre alternativas, con su porqué | «Quiero que la base de datos use embeddings.» «lodan será nativo en Windows, Linux y macOS.» «Vamos con Postgres y no con SQLite porque…» |
| `preference` | Cómo le gusta hacer algo al usuario | «Entreno por la tarde, de lunes a viernes.» «Prefiero respuestas directas, sin relleno.» |
| `fact` | Dato estable del usuario o de su mundo | «Mi coche es un Seat León de 2016.» «El servidor de casa tiene 8 GB de RAM.» |
| `event` | Algo que pasó en un momento concreto | «Hoy he hecho pierna: sentadilla 5x5 con 100 kg.» «El martes cambié el aceite del coche.» |
| `note` | Nota, idea o pendiente con valor futuro | «Mañana tengo que llamar al taller.» «Idea: vídeo sobre pgvector.» |

También se guarda cuando el usuario confirma o rechaza una propuesta («vale, hazlo así», «no, mejor X»): eso es una decisión.

## No se guarda

| Caso | Ejemplo | Por qué |
| :--- | :--- | :--- |
| Órdenes de rol o de sesión | «Eres el agente orquestador.» «Responde en inglés en esta conversación.» | Configuran la sesión actual, no son hechos del usuario |
| Charla y cortesía | «Gracias», «vale», «a ver qué tal» | Sin valor futuro |
| Pasos intermedios del trabajo | «Ahora ejecuta los tests.» | Temporal |
| Lo que ya está guardado | Repetir un dato que `recall` acaba de devolver | Duplicado (el servidor también los detecta) |
| Lo que se deduce del código o de un documento | La estructura de carpetas de un repositorio | Ya está en su fuente |
| Secretos | Contraseñas, claves de API, DNI, números de cuenta | Riesgo sin beneficio |

## Temas (`topics`)

- De 1 a 3 temas por registro, cortos y en minúsculas: `entrenamiento`, `coche`, `lodan`, `trabajo`, `notas-diarias`.
- No hay catálogo fijo: crea el que haga falta, pero reutiliza el existente si lo conoces (el servidor fusiona equivalentes como `gimnasio` → `entrenamiento` y avisa en la respuesta).
- Un proyecto es un tema más (`lodan`); no hace falta un tema genérico `proyectos`.

## Claves (`key`)

Para datos que cambian con el tiempo y de los que solo interesa el valor vigente: `entrenamiento/horario`, `coche/kilometraje`, `lodan/estado`. Guardar con la misma `key` sustituye al anterior y conserva el antiguo como historial. No uses `key` para eventos: cada evento es único.

## Redacción

- Título: de 3 a 8 palabras, buscable, sin fecha si es un evento (la fecha va en `occurred_at`).
- Contenido: una o dos frases que se entiendan solas, con el porqué si lo hay. Mejor «Elegimos PostgreSQL + pgvector porque el usuario lo pidió y escala a millones de registros» que «Elegimos eso».
- En el idioma del usuario.
