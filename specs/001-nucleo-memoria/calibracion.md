# Calibración de umbrales de similitud — spec 001

- **Fecha:** 2026-09-29.
- **Modelo:** `embeddinggemma:300m-qat-q4_0` (768 dimensiones) en la máquina de referencia, con Ollama en `http://127.0.0.1:11434`.
- **Cómo se obtuvo:** `TestCalibracionSimilitudes` (`internal/recall/ollama_integration_test.go`). Embebe 36 documentos y 2 consultas en un solo lote (3,364 s).
- **Cómo repetirlo:** `LODAN_OLLAMA_IT=1 LODAN_EMBED_MODEL=embeddinggemma:300m-qat-q4_0 go test ./internal/recall/ -run TestCalibracionSimilitudes -v`.
- **Condiciones de la medida:**
  - los temas se embeben como los embebe `topic`: slug con espacios y documento sin título;
  - los registros se embeben como los embebe `memory`: título más contenido, como documento;
  - las consultas llevan el prefijo de consulta del modelo y se comparan con el embedding del tema (como hace `recall`);
  - la similitud es el coseno.

## Resultados

### Temas (slug con espacios, documento sin título)

| Grupo | Coseno | Par |
|---|---|---|
| equivalente | 0,7375 | `gym` ↔ `entrenamiento` |
| equivalente | 0,8339 | `gimnasio` ↔ `entrenamiento` |
| equivalente | 0,9050 | `coche` ↔ `vehiculo` |
| equivalente | 0,9014 | `trabajo` ↔ `empleo` |
| equivalente | 0,6820 | `salud` ↔ `medico` |
| distinto | 0,6086 | `coche` ↔ `entrenamiento` |
| distinto | 0,3862 | `lodan` ↔ `entrenamiento` |
| distinto | 0,6216 | `trabajo` ↔ `salud` |
| distinto | 0,5459 | `notas-diarias` ↔ `coche` |
| distinto | 0,6012 | `recetas` ↔ `finanzas` |

### Registros (título + contenido, documento)

| Grupo | Coseno | Par |
|---|---|---|
| casi duplicado | 0,9812 | [Entreno de piernas] ↔ [Entreno de piernas] |
| casi duplicado | 0,9551 | [Cambio de aceite] ↔ [Cambio de aceite] |
| casi duplicado | 0,9744 | [Reunión de equipo] ↔ [Reunión de equipo] |
| casi duplicado | 0,9918 | [Base de datos de lodan] ↔ [Base de datos de lodan] |
| paráfrasis | 0,8061 | [Horario de entreno] ↔ [Horario de gimnasio] |
| paráfrasis | 0,8711 | [Seguro del coche] ↔ [Renovación del seguro] |
| paráfrasis | 0,8069 | [Trabajo remoto] ↔ [Teletrabajo] |
| paráfrasis | 0,8934 | [Editor de código] ↔ [Herramienta favorita] |
| relacionado | 0,6588 | [Entreno de piernas] ↔ [Entreno de espalda] |
| relacionado | 0,7961 | [Cambio de aceite] ↔ [Cambio de neumáticos] |
| relacionado | 0,5340 | [Reunión de equipo] ↔ [Reunión con cliente] |
| no relacionado | 0,1739 | [Entreno de piernas] ↔ [Renovación del pasaporte] |
| no relacionado | 0,2455 | [Seguro del coche] ↔ [Receta de lentejas] |
| no relacionado | 0,3283 | [Trabajo remoto] ↔ [Cumpleaños de mi madre] |
| no relacionado | 0,2612 | [Editor de código] ↔ [Vacaciones en Asturias] |

### Consulta (prefijo de consulta) frente a tema (documento)

| Grupo | Coseno | Consulta | Tema |
|---|---|---|---|
| correcto | 0,5501 | «¿qué hice en el último entreno?» | `entrenamiento` |
| incorrecto | 0,2234 | «¿qué hice en el último entreno?» | `coche` |
| incorrecto | 0,2498 | «¿qué hice en el último entreno?» | `trabajo` |
| correcto | 0,1072 | «¿cuándo toca la ITV?» | `coche` |
| incorrecto | 0,0823 | «¿cuándo toca la ITV?» | `entrenamiento` |
| incorrecto | 0,0861 | «¿cuándo toca la ITV?» | `trabajo` |

## Resumen

Umbrales de partida: tema 0,85 y duplicado 0,92.

| Medida | Mínimo | Máximo | Media |
|---|---|---|---|
| Temas equivalentes | 0,6820 | 0,9050 | 0,8120 |
| Temas distintos | 0,3862 | 0,6216 | 0,5527 |
| Registros casi duplicados | 0,9551 | 0,9918 | 0,9756 |
| Registros paráfrasis | 0,8061 | 0,8934 | 0,8444 |
| Registros relacionados | 0,5340 | 0,7961 | 0,6630 |
| Registros no relacionados | 0,1739 | 0,3283 | 0,2523 |
| Consulta con su tema correcto | 0,1072 | 0,5501 | 0,3286 |
| Consulta con temas incorrectos | 0,0823 | 0,2498 | 0,1604 |

Con 0,85, de los cinco pares equivalentes solo dos (`coche`–`vehiculo` y `trabajo`–`empleo`) se habrían fusionado, y ninguna consulta habría detectado su tema.

## Decisiones

### 1. `topic_similarity`: de 0,85 a 0,72

- **Motivo:** los equivalentes van de 0,68 a 0,91 (`gym`–`entrenamiento` 0,74, `gimnasio`–`entrenamiento` 0,83) y los distintos no pasan de 0,62. Con 0,72 hay un margen de 0,10 sobre el peor distinto.
- **Límite conocido:** `salud`–`medico` (0,68) queda por debajo y no se fusionaría. Se acepta: fusionar dos temas distintos por error es peor que tener dos temas casi iguales.
- **Alcance:** solo lo usa `topic.Resolver` para decidir si un tema nuevo es equivalente a uno existente.

### 2. `topic_detect_similarity` (nuevo): 0,40

- **Motivo:** para detectar el tema de una consulta no sirve el umbral de equivalencia: se compara una pregunta con el nombre de un tema, no dos nombres entre sí. El coseno entre la consulta y su tema correcto va de 0,11 a 0,55, y con temas incorrectos llega hasta 0,25. Con 0,40 hay un margen de 0,15 sobre el peor incorrecto.
- **Límite conocido:** no basta por sí solo. «¿cuándo toca la ITV?» solo llega a 0,11 con `coche`, así que la similitud directa no lo detecta. De ahí la segunda vía (votación).
- **Configuración:** `topic_detect_similarity` en `config.json`, o `LODAN_TOPIC_DETECT_SIMILARITY`; debe estar en (0,1].

### 3. Detección del tema en `recall`, en dos vías

Si el usuario pasa `topics`, se usan tal cual (`TopicVia` = "pedido"). Si no, el tema se detecta así:

1. **Similitud directa:** si el tema más parecido a la consulta tiene un coseno ≥ `topic_detect_similarity`, se usa ese tema (`TopicVia` = "similitud"). Solo cuenta el coseno y este umbral, no el de equivalencia del resolver.
2. **Votación,** si la anterior falla o no hay embedding (`TextOnly`): tras la fusión RRF se toman los 5 primeros resultados y se consultan sus temas en una sola query. Si el primer resultado y al menos otro de esos 5 comparten un tema, se usa (`TopicVia` = "votación"). Si hay varios, gana el que aparece en más de los 5; en empate, el de menor id.

Con el tema elegido se lee la ficha. En la votación esto ocurre después de la fusión: se quitan de los resultados los registros que ya salen en la ficha y se vuelve a cortar al límite. En la votación no se aplica el bonus de tema a la fusión, porque el tema se conoce después de ella y aplicarlo reordenaría los resultados que lo han decidido.

**Motivo:** el tema debe detectarse aunque la consulta no se parezca a su nombre. Si los mejores resultados de la búsqueda híbrida ya comparten un tema, esa señal es más fiable que el parecido con su nombre.

### 4. `dup_similarity`: se mantiene en 0,92

Los casi duplicados llegan a 0,9551 como mínimo y las paráfrasis a 0,8934 como máximo, así que 0,92 queda entre ambos grupos. Los relacionados (≤ 0,80) y los no relacionados (≤ 0,33) quedan lejos.

## Limitaciones de la muestra

- Son 10 pares de temas, 15 de registros y 2 consultas: sirve para fijar el orden de magnitud y comprobar que hay margen, no para estimar tasas de acierto.
- Todo depende del modelo y de la cuantización. Si cambia `embed_model`, hay que repetir la calibración.
