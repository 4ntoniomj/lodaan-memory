# Benchmark de lodan

Generado por `lodan bench` el 2026-10-03 01:09:54 CEST.

## Entorno

- CPU: Intel(R) Core(TM) i5-4590 CPU @ 3.30GHz
- Núcleos: 4
- RAM total: 7896 MB
- Sistema: linux/amd64
- PostgreSQL: 18.6
- pgvector: 0.8.6

## Parámetros

- Dimensiones de los vectores: 768 (`halfvec`)
- Candidatos del índice binario (Q1): 100, 200, 400; Q2 devuelve hasta 40
- `hnsw.ef_search` = max(candidatos, 40), como `memory.Nearest`
- HNSW: `m` = 16 y `ef_construction` = 64 (valores por defecto de pgvector; la migración no los fija)
- `maintenance_work_mem` = 1GB al construir los índices
- Escalas (filas): 10000, 100000, 1000000, 10000000
- Consultas medidas por escala: 500, tras 20 de calentamiento
- Consultas para el recall@10: 100
- Clústeres de vectores: 2000; temas: 1000; semilla: 42

## Resultados por escala

### Construcción, tamaños y RAM

| Filas | Carga (s) | Índice HNSW (s) | Índice GIN (s) | Tabla (MB) | HNSW (MB) | GIN (MB) | Total con índices (MB) | RAM postgres (MB) |
|---|---|---|---|---|---|---|---|---|
| 10000 | 2.08 | 2.45 | 0.07 | 11.5 | 3.8 | 1.0 | 33.3 | 189.1 |
| 100000 | 23.88 | 24.85 | 0.52 | 112.5 | 38.0 | 9.1 | 328.5 | 976.4 |
| 1000000 | 197.95 | 210.37 | 5.61 | 1121.5 | 379.5 | 70.7 | 3257.7 | 1744.9 |
| 10000000 | 2393.28 | 37026.26 | 72.57 | 11208.0 | 3791.6 | 674.4 | 32536.7 | 1728.8 |

### recall@10

| Filas | Candidatos | `hnsw.ef_search` | recall@10 |
|---|---|---|---|
| 10000 | 100 | 100 | 0.682 |
| 10000 | 200 | 200 | 0.761 |
| 10000 | 400 | 400 | 0.842 |
| 100000 | 100 | 100 | 0.999 |
| 100000 | 200 | 200 | 0.999 |
| 100000 | 400 | 400 | 0.999 |
| 1000000 | 100 | 100 | 0.460 |
| 1000000 | 200 | 200 | 0.721 |
| 1000000 | 400 | 400 | 0.877 |
| 10000000 | 100 | 100 | 0.154 |
| 10000000 | 200 | 200 | 0.269 |
| 10000000 | 400 | 400 | 0.405 |

### Latencia p50 / p95 (ms)

| Filas | Candidatos | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|---|
| 10000 | 100 | 2.1 / 2.8 | 1.2 / 1.6 | 1.7 / 3.0 | 0.6 / 0.7 | 5.7 / 7.3 |
| 10000 | 200 | 3.5 / 4.3 | 1.8 / 2.4 | 1.7 / 3.0 | 0.6 / 0.7 | 7.8 / 9.4 |
| 10000 | 400 | 5.8 / 6.8 | 3.1 / 3.8 | 1.7 / 3.0 | 0.6 / 0.7 | 11.4 / 13.2 |
| 100000 | 100 | 3.1 / 4.0 | 1.3 / 1.8 | 14.9 / 16.7 | 0.7 / 0.8 | 20.2 / 22.3 |
| 100000 | 200 | 5.3 / 6.4 | 2.2 / 2.8 | 14.9 / 16.7 | 0.7 / 0.8 | 23.3 / 25.8 |
| 100000 | 400 | 9.5 / 11.0 | 3.9 / 4.7 | 14.9 / 16.7 | 0.7 / 0.8 | 29.3 / 32.2 |
| 1000000 | 100 | 7.2 / 12.3 | 1.9 / 2.3 | 27.2 / 33.9 | 1.2 / 7.1 | 38.3 / 69.1 |
| 1000000 | 200 | 10.2 / 16.8 | 3.1 / 3.6 | 27.2 / 33.9 | 1.2 / 7.1 | 43.4 / 56.6 |
| 1000000 | 400 | 15.7 / 25.9 | 5.5 / 6.0 | 27.2 / 33.9 | 1.2 / 7.1 | 51.2 / 66.9 |
| 10000000 | 100 | 272.5 / 751.9 | 20.4 / 30.5 | 203.1 / 282.0 | 1.6 / 33.5 | 511.6 / 1016.9 |
| 10000000 | 200 | 132.9 / 160.5 | 4.2 / 5.8 | 203.1 / 282.0 | 1.6 / 33.5 | 350.8 / 443.2 |
| 10000000 | 400 | 278.6 / 412.9 | 8.5 / 15.7 | 203.1 / 282.0 | 1.6 / 33.5 | 503.3 / 666.0 |

### Latencia p99 (ms)

| Filas | Candidatos | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|---|
| 10000 | 100 | 3.0 | 1.8 | 4.1 | 0.8 | 8.6 |
| 10000 | 200 | 4.5 | 2.6 | 4.1 | 0.8 | 10.2 |
| 10000 | 400 | 7.3 | 4.0 | 4.1 | 0.8 | 14.4 |
| 100000 | 100 | 4.3 | 2.1 | 20.2 | 1.0 | 25.1 |
| 100000 | 200 | 7.8 | 3.5 | 20.2 | 1.0 | 28.4 |
| 100000 | 400 | 14.3 | 5.1 | 20.2 | 1.0 | 35.6 |
| 1000000 | 100 | 162.7 | 2.6 | 43.3 | 32.9 | 203.9 |
| 1000000 | 200 | 18.2 | 3.8 | 43.3 | 32.9 | 77.8 |
| 1000000 | 400 | 29.3 | 6.2 | 43.3 | 32.9 | 86.2 |
| 10000000 | 100 | 1070.4 | 37.2 | 354.2 | 54.8 | 1418.3 |
| 10000000 | 200 | 186.8 | 8.1 | 354.2 | 54.8 | 500.1 |
| 10000000 | 400 | 512.3 | 19.3 | 354.2 | 54.8 | 762.5 |

### Índices de la ficha del tema

Índice parcial que usa el plan de PostgreSQL para el tema más frecuente.

| Filas | ProfileSQL (`memory_topics_profile_idx`) | EventsSQL (`memory_topics_events_idx`) |
|---|---|---|
| 10000 | sí | sí |
| 100000 | sí | sí |
| 1000000 | sí | sí |
| 10000000 | sí | sí |

## Embedding real de una consulta

| Modelo | Consultas | p50 (ms) | p95 (ms) |
|---|---|---|---|
| embeddinggemma:300m-qat-q4_0 | 30 | 102.3 | 218.6 |

## Notas

- **Datos sintéticos por clústeres.** Los vectores se generan alrededor de centroides aleatorios normalizados, con ruido gaussiano (σ = 0.035 por componente sobre el vector sin normalizar) y se normalizan después. Las consultas de prueba se generan igual, con centroides y ruido nuevos.
- **Estructura de los clústeres.** El centroide tiene norma 1 y la norma del ruido es ≈ σ·√D = 0.035·√768 ≈ 0.97, así que el coseno esperado entre una fila y su centroide es 1/√(1 + 0.97²) ≈ 0.72. Se eligió σ para acercarse a la estructura de embeddings reales; con un σ mayor el ruido domina y los datos apenas forman clústeres.
- **El recall sobre datos reales puede diferir.** Los embeddings reales tienen otra estructura que estos clústeres artificiales, así que el recall@10 medido aquí es una referencia y no una garantía. Conviene repetirlo con datos y un modelo reales.
- **Recall@10.** Media de la intersección entre el top 10 de Q1 + Q2 (índice binario y reordenación con el vector completo) y el top 10 de la búsqueda exacta, dividida entre 10.
- **Consultas de producción.** El benchmark ejecuta el SQL exportado por el código de producción (`memory.CandidatesSQL`, `memory.RerankSQL`, `recall.TextCandidatesSQL`, `recall.ProfileSQL` y `recall.EventsSQL`), no copias. Queda fuera lo que hace el código de producción alrededor del SQL (la transacción de solo lectura de `Nearest`, la fusión y el formato). Con menos de 40 candidatos, `Nearest` los sube a 40 en producción; aquí se mide el valor pedido tal cual.
- **Latencias.** Cada consulta se mide con el reloj del cliente, incluida la ida y vuelta local con PostgreSQL. Q1 y Q2 se miden para cada número de candidatos; Q3 y Q4 no dependen de él y se miden una sola vez por escala (por eso se repiten en las filas de una misma escala). «Total» es la suma por repetición de Q1 + Q2 + Q3 + Q4. Q4 son dos consultas: hasta 8 datos estables y hasta 5 eventos.
- **Texto.** Los textos salen de un vocabulario español de unas 295 palabras, así que cada palabra aparece en una fracción grande de las filas. Las consultas de vocabulario de Q3 son siempre de 2 palabras distintas, que `websearch_to_tsquery` combina con AND, de modo que cada consulta encuentra solo las filas que contienen ambas. El 10 % de las consultas de texto buscan en cambio un token exacto tipo matrícula (`1234-ABC`), presente en el 1 % de las filas. El ranking se calcula como máximo sobre 2000 coincidencias (`recall.TextRankCap`).
- **Carga por escalas.** Las escalas crecen de forma incremental: se añaden filas hasta llegar a cada tamaño. Antes de cada carga se eliminan el índice HNSW y el GIN de `tsv`, y se recrean después con la misma definición de la migración. Los índices de `memory_topics` no se tocan: se mantienen durante la carga y se hace `ANALYZE` al final.
- **RAM.** Suma del RSS de los procesos de PostgreSQL del clúster (solo en Linux). Como cuenta la memoria compartida una vez por cada proceso que la ha tocado, sobrestima el uso real.
- **Base de datos.** Todo se ejecuta en la base `lodan_bench`, separada de la de datos reales.
