# Benchmark de lodan

Generado por `lodan bench` el 2026-09-29 12:56:53 CEST.

## Entorno

- CPU: Intel(R) Core(TM) i5-4590 CPU @ 3.30GHz
- Núcleos: 4
- RAM total: 7896 MB
- Sistema: linux/amd64
- PostgreSQL: 18.6 (Ubuntu 18.6-0ubuntu0.26.04.1)
- pgvector: 0.8.1

## Parámetros

- Dimensiones de los vectores: 768 (`halfvec`)
- `hnsw.ef_search` = 100
- HNSW: `m` = 16 y `ef_construction` = 64 (valores por defecto de pgvector; la migración no los fija)
- `maintenance_work_mem` = 1GB al construir los índices
- Escalas (filas): 10000, 100000, 1000000
- Consultas medidas por escala: 500, tras 20 de calentamiento
- Consultas para el recall@10: 100
- Clústeres de vectores: 2000; temas: 1000; semilla: 42

## Resultados por escala

### Construcción, recall, tamaños y RAM

| Filas | Carga (s) | Índice HNSW (s) | Índice GIN (s) | recall@10 | Tabla (MB) | HNSW (MB) | GIN (MB) | Total con índices (MB) | RAM postgres (MB) |
|---|---|---|---|---|---|---|---|---|---|
| 10000 | 3.16 | 4.87 | 0.11 | 0.686 | 11.5 | 3.8 | 1.0 | 33.3 | 191.9 |
| 100000 | 26.93 | 46.21 | 0.80 | 1.000 | 112.5 | 38.0 | 9.1 | 328.5 | 1111.2 |
| 1000000 | 286.07 | 524.15 | 10.13 | 0.502 | 1121.5 | 379.5 | 70.8 | 3257.8 | 1772.4 |

### Latencia p50 / p95 (ms)

| Filas | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|
| 10000 | 3.5 / 6.7 | 1.3 / 3.6 | 2.3 / 5.4 | 0.9 / 2.8 | 9.2 / 14.5 |
| 100000 | 4.7 / 8.4 | 1.5 / 3.9 | 20.2 / 40.5 | 1.9 / 6.4 | 30.2 / 50.8 |
| 1000000 | 14.8 / 26.0 | 2.2 / 5.7 | 287.1 / 536.2 | 40.9 / 108.1 | 356.4 / 622.0 |

### Latencia p99 (ms)

| Filas | Q1 candidatos | Q2 reordenación | Q3 texto | Q4 ficha de tema | Total |
|---|---|---|---|---|---|
| 10000 | 10.4 | 5.9 | 8.1 | 5.7 | 18.2 |
| 100000 | 11.8 | 5.5 | 58.5 | 7.9 | 67.0 |
| 1000000 | 34.1 | 8.3 | 765.6 | 153.9 | 825.7 |

## Embedding real de una consulta

| Modelo | Consultas | p50 (ms) | p95 (ms) |
|---|---|---|---|
| embeddinggemma:300m-qat-q4_0 | 30 | 748.5 | 2732.4 |

## Notas

- **Datos sintéticos por clústeres.** Los vectores se generan alrededor de centroides aleatorios normalizados, con ruido gaussiano (σ = 0.035 por componente sobre el vector sin normalizar) y se normalizan después. Las consultas de prueba se generan igual, con centroides y ruido nuevos.
- **Estructura de los clústeres.** El centroide tiene norma 1 y la norma del ruido es ≈ σ·√D = 0.035·√768 ≈ 0.97, así que el coseno esperado entre una fila y su centroide es 1/√(1 + 0.97²) ≈ 0.72. Se eligió σ para acercarse a la estructura de embeddings reales; con un σ mayor el ruido domina y los datos apenas forman clústeres.
- **El recall sobre datos reales puede diferir.** Los embeddings reales tienen otra estructura que estos clústeres artificiales, así que el recall@10 medido aquí es una referencia y no una garantía. Conviene repetirlo con datos y un modelo reales.
- **Recall@10.** Media de la intersección entre el top 10 de Q1 + Q2 (índice binario y reordenación con el vector completo) y el top 10 de la búsqueda exacta, dividida entre 10.
- **Latencias.** Cada consulta se mide con el reloj del cliente, incluida la ida y vuelta local con PostgreSQL. Q1 + Q2 + Q3 + Q4 se ejecutan en secuencia en cada repetición, y «Total» es el tiempo de la secuencia completa. Q4 son dos consultas: hasta 8 datos estables y hasta 5 eventos.
- **Texto.** Los textos salen de un vocabulario español de unas 295 palabras, así que cada palabra aparece en una fracción grande de las filas. Las consultas de vocabulario de Q3 son siempre de 2 palabras distintas, que `websearch_to_tsquery` combina con AND, de modo que cada consulta encuentra solo las filas que contienen ambas. El 10 % de las consultas de texto buscan en cambio un token exacto tipo matrícula (`1234-ABC`), presente en el 1 % de las filas.
- **Carga por escalas.** Las escalas crecen de forma incremental: se añaden filas hasta llegar a cada tamaño. Antes de cada carga se eliminan el índice HNSW y el GIN de `tsv`, y se recrean después con la misma definición de la migración.
- **RAM.** Suma del RSS de los procesos de PostgreSQL del clúster (solo en Linux). Como cuenta la memoria compartida una vez por cada proceso que la ha tocado, sobrestima el uso real.
- **Base de datos.** Todo se ejecuta en la base `lodan_bench`, separada de la de datos reales.
