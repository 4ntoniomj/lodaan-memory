# Codebase

Última actualización: 2026-09-29

| Tecnología | Tipo | Versión | Notas |
| :--- | :--- | :--- | :--- |
| Go | lenguaje | 1.25.0 (mínimo), toolchain go1.27.1 | El mínimo lo impone el SDK de MCP |
| github.com/modelcontextprotocol/go-sdk | librería | v1.8.0 | SDK oficial de MCP: herramientas, stdio y HTTP streamable |
| github.com/jackc/pgx/v5 | librería | v5.11.0 | Driver de PostgreSQL |
| github.com/pgvector/pgvector-go | librería | v0.4.1 | Tipos `vector`/`halfvec` para pgx |
| github.com/oklog/ulid/v2 | librería | v2.1.2 | Identificadores de sesión ordenables por tiempo |
| github.com/google/jsonschema-go | librería | v0.4.3 | Esquemas de entrada de las herramientas MCP (enums) |
| github.com/klauspost/compress | librería | v1.20.1 | Descompresión zstd de los paquetes de Ollama |
| github.com/jackc/pgpassfile | librería | v1.0.0 | Indirecta (pgx) |
| github.com/jackc/pgservicefile | librería | v0.0.0-20240606120523-5a60cdf6a761 | Indirecta (pgx) |
| golang.org/x/text | librería | v0.29.0 | Indirecta (pgx) |
| github.com/jackc/puddle/v2 | librería | v2.2.2 | Indirecta (pgxpool) |
| golang.org/x/sync | librería | v0.20.0 | Indirecta |
| github.com/segmentio/asm | librería | v1.1.3 | Indirecta (SDK MCP) |
| github.com/segmentio/encoding | librería | v0.5.4 | Indirecta (SDK MCP) |
| github.com/yosida95/uritemplate/v3 | librería | v3.0.2 | Indirecta (SDK MCP) |
| golang.org/x/oauth2 | librería | v0.35.0 | Indirecta (SDK MCP) |
| golang.org/x/sys | librería | v0.41.0 | Servicio de Windows (SCM) y llamadas al sistema |
| golang.org/x/time | librería | v0.15.0 | Indirecta |
| PostgreSQL | base de datos | ≥ 16 (desarrollo: 18) | En Windows, conda-forge solo tiene pgvector para PG 16 |
| pgvector | base de datos (extensión) | ≥ 0.7.0 (objetivo: 0.8.6) | `halfvec`, `binary_quantize`, HNSW |
| Ollama | servicio externo | desarrollo: 0.34.4 | Embeddings vía `POST /api/embed` |
| embeddinggemma | modelo de embeddings | `300m-qat-q4_0` (por defecto) | Multilingüe, 768 dimensiones; por defecto, configurable. Umbrales calibrados en `specs/001-nucleo-memoria/calibracion.md` |

> Go, PostgreSQL, pgvector, Ollama y el modelo no aparecen en `go.mod`: `detectar_stack.py` los reporta como «no detectados» y se mantienen documentados a propósito.
