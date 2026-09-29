# Benchmark de lodan

Generado por `lodan bench` el 2026-09-29 16:41:11 CEST.

## Entorno

- CPU: Intel(R) Core(TM) i5-4590 CPU @ 3.30GHz
- Núcleos: 4
- RAM total: 7896 MB
- Sistema: linux/amd64
- PostgreSQL: 18.6 (Ubuntu 18.6-0ubuntu0.26.04.1)
- pgvector: 0.8.1

## Parámetros

- Dimensiones de los vectores: 768 (`halfvec`)
- Candidatos del índice binario (Q1): 100, 200, 400; Q2 devuelve hasta 40
- `hnsw.ef_search` = max(candidatos, 40), como `memory.Nearest`
- HNSW: `m` = 16 y `ef_construction` = 64 (valores por defecto de pgvector; la migración no los fija)
- `maintenance_work_mem`: no aplica, los datos y los índices se reutilizaron
- Escalas (filas): 1000000 (**datos reutilizados**: no se cargó nada ni se reconstruyeron los índices HNSW y GIN; solo se midió la última escala)
- Consultas medidas por escala: 500, tras 20 de calentamiento
- Consultas para el recall@10: 100
- Clústeres de vectores: 2000; temas: 1000; semilla: 42

## Resultados por escala

### Construcción, tamaños y RAM

| Filas | Carga (s) | Índice HNSW (s) | Índice GIN (s) | Tabla (MB) | HNSW (MB) | GIN (MB) | Total con índices (MB) | RAM postgres (MB) |
|---|---|---|---|---|---|---|---|---|
| 1000000 |  |  |  | 1121.5 | 379.5 | 70.8 | 3257.8 | 1220.5 |

### recall@10

| Filas | Candidatos | `hnsw.ef_search` | recall@10 |
|---|---|---|---|
| 1000000 | 100 | 100 | 0.502 |
| 1000000 | 200 | 200 | 0.736 |
| 1000000 | 400 | 400 | 0.893 |

### Latencia p50 / p95 (ms)

| Filas | Candidatos | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|---|
| 1000000 | 100 | 7.6 / 11.9 | 2.0 / 2.7 | 30.8 / 54.3 | 1.5 / 12.2 | 44.9 / 74.9 |
| 1000000 | 200 | 11.1 / 19.0 | 3.4 / 4.0 | 30.8 / 54.3 | 1.5 / 12.2 | 50.6 / 77.4 |
| 1000000 | 400 | 18.0 / 29.1 | 6.1 / 7.4 | 30.8 / 54.3 | 1.5 / 12.2 | 60.5 / 87.8 |

### Latencia p99 (ms)

| Filas | Candidatos | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|---|
| 1000000 | 100 | 118.0 | 3.3 | 68.4 | 27.0 | 158.7 |
| 1000000 | 200 | 21.8 | 4.5 | 68.4 | 27.0 | 99.8 |
| 1000000 | 400 | 37.2 | 9.4 | 68.4 | 27.0 | 110.3 |

### Índices de la ficha del tema

Índice parcial que usa el plan de PostgreSQL para el tema más frecuente.

| Filas | ProfileSQL (`memory_topics_profile_idx`) | EventsSQL (`memory_topics_events_idx`) |
|---|---|---|
| 1000000 | sí | sí |

## Embedding real de una consulta

| Modelo | Consultas | p50 (ms) | p95 (ms) |
|---|---|---|---|
| embeddinggemma:300m-qat-q4_0 | 30 | 39.3 | 84.7 |

## Notas

- **Datos sintéticos por clústeres.** Los vectores se generan alrededor de centroides aleatorios normalizados, con ruido gaussiano (σ = 0.035 por componente sobre el vector sin normalizar) y se normalizan después. Las consultas de prueba se generan igual, con centroides y ruido nuevos.
- **Estructura de los clústeres.** El centroide tiene norma 1 y la norma del ruido es ≈ σ·√D = 0.035·√768 ≈ 0.97, así que el coseno esperado entre una fila y su centroide es 1/√(1 + 0.97²) ≈ 0.72. Se eligió σ para acercarse a la estructura de embeddings reales; con un σ mayor el ruido domina y los datos apenas forman clústeres.
- **El recall sobre datos reales puede diferir.** Los embeddings reales tienen otra estructura que estos clústeres artificiales, así que el recall@10 medido aquí es una referencia y no una garantía. Conviene repetirlo con datos y un modelo reales.
- **Recall@10.** Media de la intersección entre el top 10 de Q1 + Q2 (índice binario y reordenación con el vector completo) y el top 10 de la búsqueda exacta, dividida entre 10.
- **Consultas de producción.** El benchmark ejecuta el SQL exportado por el código de producción (`memory.CandidatesSQL`, `memory.RerankSQL`, `recall.TextCandidatesSQL`, `recall.ProfileSQL` y `recall.EventsSQL`), no copias. Queda fuera lo que hace el código de producción alrededor del SQL (la transacción de solo lectura de `Nearest`, la fusión y el formato). Con menos de 40 candidatos, `Nearest` los sube a 40 en producción; aquí se mide el valor pedido tal cual.
- **Latencias.** Cada consulta se mide con el reloj del cliente, incluida la ida y vuelta local con PostgreSQL. Q1 y Q2 se miden para cada número de candidatos; Q3 y Q4 no dependen de él y se miden una sola vez por escala (por eso se repiten en las filas de una misma escala). «Total» es la suma por repetición de Q1 + Q2 + Q3 + Q4. Q4 son dos consultas: hasta 8 datos estables y hasta 5 eventos.
- **Texto.** Los textos salen de un vocabulario español de unas 295 palabras, así que cada palabra aparece en una fracción grande de las filas. Las consultas de vocabulario de Q3 son siempre de 2 palabras distintas, que `websearch_to_tsquery` combina con AND, de modo que cada consulta encuentra solo las filas que contienen ambas. El 10 % de las consultas de texto buscan en cambio un token exacto tipo matrícula (`1234-ABC`), presente en el 1 % de las filas. El ranking se calcula como máximo sobre 2000 coincidencias (`recall.TextRankCap`).
- **Carga por escalas.** Las escalas crecen de forma incremental: se añaden filas hasta llegar a cada tamaño. Antes de cada carga se eliminan el índice HNSW y el GIN de `tsv`, y se recrean después con la misma definición de la migración. Los índices de `memory_topics` no se tocan: se mantienen durante la carga y se hace `ANALYZE` al final.
- **Datos reutilizados.** No se cargó nada ni se reconstruyeron los índices HNSW y GIN, por eso los tiempos de carga y de construcción están vacíos. Se ejecutaron las migraciones (que crean los índices de `memory_topics` si faltaban) y `ANALYZE`.
- **RAM.** Suma del RSS de los procesos de PostgreSQL del clúster (solo en Linux). Como cuenta la memoria compartida una vez por cada proceso que la ha tocado, sobrestima el uso real.
- **Base de datos.** Todo se ejecuta en la base `lodan_bench`, separada de la de datos reales.
