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

---

## Módulos de implementación

Estado actual de cada funcionalidad en `internal/`:

### Database cluster

Gestión de la conexión a PostgreSQL, pool de conexiones y operaciones de base de datos.

**Ubicación:** `internal/database/`

**Responsabilidades:**
- Inicializar y mantener pool de conexiones a PostgreSQL.
- Ejecutar migraciones de esquema.
- Gestionar transacciones para coherencia de datos.

#### Backup y Restauración

Los backups se almacenan como archivos comprimidos `.tar.xz` con metadatos en `info.txt`.

**Estrategias de backup:**
- **Full:** Copia completa de la base de datos y datos asociados.
- **Incremental:** Cambios desde el último backup (full o incremental).
- **Differential:** Cambios desde el último backup full.

**Flujo de Backup (`Backup()`):**

```
1. Adquirir lock de base de datos (evitar escrituras concurrentes)
2. Detener servicios MCP (puerto 9999)
3. Recopilar datos según estrategia:
   - Full: copiar datos completos de PostgreSQL
   - Incremental/Differential: identificar cambios desde backup anterior
4. Empaquetar en TAR.XZ con metadatos:
   - info.txt: timestamp (RFC3339Nano), tipo, hash de integridad
   - data/: contenido de la base de datos
5. Escribir archivo: lodan_backup_YYYY-MM-DD_HHMMSS.{full|incremental|differential}.tar.xz
6. Reanudar servicios
7. Liberar lock
```

**Flujo de Restauración (`Restore()`):**

```
1. Adquirir lock de base de datos
2. Auto-detectar backup (si se especifica directorio):
   - Ordenar archivos por fecha (RFC3339Nano en info.txt)
   - Seleccionar full más reciente
   - Recopilar todos los incremental/differential posteriores
3. Detener servicios MCP
4. Extraer backup full
5. Aplicar incremental/differential en orden cronológico
6. Verificar integridad de datos (hash en info.txt)
7. Reanudar servicios
8. Liberar lock
```

**Metadatos (`info.txt`):**

Cada backup incluye un archivo `info.txt` con:
- `timestamp`: fecha/hora en RFC3339Nano
- `type`: full, incremental, o differential
- `hash`: SHA256 del contenido para verificación
- `base_backup_id`: (si incremental/differential) referencia al full anterior

La restauración automática ordena por `timestamp` para asegurar aplicación correcta de cambios.

### Otros módulos

- **memory:** Guardar registros (`internal/memory/`).
- **recall:** Búsqueda por tema (`internal/recall/`).
- **topic:** Gestión de etiquetas y temas (`internal/topic/`).
- **session:** Sesiones y contexto (`internal/session/`).
- **embedding:** Cálculo y búsqueda de embeddings vía Ollama (`internal/embedding/`).
- **mcptools:** Definición de herramientas MCP (`internal/mcptools/`).
- **config:** Configuración global (`internal/config/`).
