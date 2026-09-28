# Documento de Diseño de Software (SDD) — Memoria Universal Local para IA (Lodan)

> **Metodología:** Specification-Driven Development (SDD) — Modo 2: Cambio Complejo / Arquitectura Base
> **Estado:** Consolidado
> **Documento de Referencia de Producto:** [PRD.md](../PRD.md)
> **Directrices de Implementación:** [AGENTS.md](../AGENTS.md) | [CODEBASE.md](../CODEBASE.md)

---

## 1. Explore (Contexto e Incertidumbres Resueltas)

### 1.1. Contexto Tecnológico y Operativo
- **Objetivo del sistema:** Proporcionar una memoria persistente, universal y local para Inteligencia Artificial, accesible exclusivamente a través de Model Context Protocol (MCP).
- **Entorno de ejecución:** Proceso binario compilado nativo en Go, ejecutado como servicio en segundo plano (daemon) en Linux, macOS y Windows, sin dependencias de Docker ni runtimes de otros lenguajes (Node, Python, Java).
- **Almacenamiento persistente:** PostgreSQL 18 con la extensión `pgvector` (versión 0.8+).
- **Motor de embeddings:** Ollama ejecutado localmente, consumiendo el modelo `embeddinggemma:300m-qat-q4_0` (vector de 768 dimensiones).

### 1.2. Incertidumbres y Decisiones Críticas Resueltas
1. **Dimensiones de embeddings:** Aunque inicialmente se barajó una dimensión de 384, la variante cuantitativa real y las pruebas de EmbeddingGemma en Ollama operan con vectores de **768 dimensiones**. El esquema de base de datos se fija en `vector(768)`.
2. **Generación de identificadores temporales:** En lugar de depender de extensiones obsoletas de contribución externa como `ossp-uuid` o funciones personalizadas (`uuid_generate_v7()`), PostgreSQL 18 implementa nativamente la función estándar `uuidv7()`.
3. **SDK oficial de MCP para Go:** El protocolo evoluciona con el estándar de Anthropic/Linux Foundation. Se adopta la biblioteca canónica `github.com/modelcontextprotocol/go-sdk/mcp`, sustituyendo stubs previos.
4. **Persistencia de vectores en Go:** El tipo `pgvector.Vector` provisto por `github.com/pgvector/pgvector-go` implementa de forma determinista las interfaces `driver.Valuer` y `sql.Scanner`, eliminando la serialización inconsistente mediante cadenas.

---

## 2. Proposal (Propósito, Alcance y Límites)

### 2.1. Propósito
Diseñar e implementar el núcleo de la Memoria Universal Local ("Lodan"), garantizando que una IA pueda registrar recuerdos, consultar información en lenguaje natural mediante recuperación sublineal, rastrear la evolución temporal de ideas y navegar grafos de conocimiento sin exponer datos fuera del entorno local del usuario.

### 2.2. Alcance (In-Scope)
- **Capa MCP:** Servidor MCP que expone herramientas canónicas con transporte dual (Streamable HTTP persistente en loopback y Stdio para diagnósticos y clientes tradicionales).
- **Modelo de Dominio Universal:** Entidad polimórfica `memory_items` con soporte de metadatos flexibles en JSONB y enlaces tipados `memory_links`.
- **Motor de Persistencia e Índices:** Almacenamiento en PostgreSQL 18 con índices HNSW (distancia coseno) para vectores, índices GIN para búsqueda de texto completo y para atributos JSONB.
- **Motor de Línea Temporal:** Preservación de recuerdos históricos mediante marcado de estado (`active = false`) y relaciones de superación (`supersedes` / `superseded_by`).
- **Instalación y CLI Administrativa:** Binario Go unificado con comandos `install`, `start`, `stdio`, `status`, `health`, `export` e `import`.
- **Gobernanza:** Cumplimiento riguroso de *Screaming Architecture* y *Two-Branch Model* de Git (`main` y `dev`).

### 2.3. Fuera de Alcance (Out-of-Scope)
- Conexiones a servicios en la nube, telemetría externa o autenticación remota (sistema estrictamente *local-first* y confinado a `127.0.0.1`).
- Interfaz gráfica de usuario (GUI): el acceso es puramente programático vía MCP y administrativo vía terminal CLI.
- Creación de un modelo de LLM generativo local embebido en el servidor: la generación lingüística reside en el host MCP consumidor.

### 2.4. Restricciones y Supuestos
- **Restricción de Red:** PostgreSQL y el servidor MCP escucharán única y exclusivamente en `127.0.0.1`.
- **Restricción de Tokens:** La recuperación de recuerdos debe ser progresiva: devolver resúmenes o candidatos ligeros por defecto para no saturar la ventana de contexto del LLM.
- **Supuesto Operativo:** El usuario cuenta con PostgreSQL 18 + `pgvector` y una instancia de Ollama ejecutándose en localhost.

---

## 3. Specs (Especificaciones Funcionales y Criterios de Aceptación)

### 3.1. Requisitos Funcionales y Contrato MCP

#### Herramientas MCP Obligatorias:
1. `memory_store`: Recibe texto libre y metadatos opcionales. Genera embedding localmente, calcula content hash y persiste en PostgreSQL con estado activo.
2. `memory_search`: Búsqueda híbrida (semántica + léxica) que recibe una consulta en lenguaje natural, consulta el índice HNSW y devuelve el Top-K ordenado.
3. `memory_get`: Recupera el contenido completo de un recuerdo dado su ID (UUIDv7).
4. `memory_history`: Recupera la evolución de un hecho o recuerdos inactivos/reemplazados a lo largo del tiempo.
5. `memory_related`: Navega por los enlaces en `memory_links` y expande el grafo de conocimiento.
6. `memory_link`: Vincula dos recuerdos mediante un tipo de relación arbitrario (`supersedes`, `related_to`, `contradicts`, etc.).
7. `memory_update`: Inactiva la versión anterior y registra una versión nueva vinculada con `supersedes`.
8. `memory_forget`: Inactiva un recuerdo (`active = false`) sin borrado físico.
9. `memory_delete`: Borrado físico administrativo condicionado.
10. `memory_get_conversation`: Reconstruye turnos cronológicos de una conversación por `conversation_id`.
11. `memory_export` / `memory_import`: Portabilidad de recuerdos y enlaces en formato JSON estructurado versionado (`memory-export-v1`).
12. `memory_health`: Diagnóstico del estado de la base de datos, extensión vector, conexión Ollama e índices.

### 3.2. Criterios de Aceptación Observables (AccC)
- **AccC-1 (Compilación y Tipado):** El proyecto compila limpiamente (`go build ./...`) con Go 1.25+ sin dependencias faltantes ni paquetes no oficiales.
- **AccC-2 (Esquema SQL Determinista):** `migrations/001_init.sql` utiliza `uuidv7()` nativo y define vectores con dimensión 768. La migración se ejecuta con éxito en PostgreSQL 18.
- **AccC-3 (Transporte MCP Estándar):** El servidor MCP utiliza el paquete canónico `github.com/modelcontextprotocol/go-sdk/mcp`, soportando arranque Streamable HTTP (`/mcp`) y Stdio.
- **AccC-4 (Rendimiento Sublineal):** Las consultas semánticas operan sobre índice HNSW con operadores de distancia coseno `<=>`, sin escaneos secuenciales completos de tablas en grandes volúmenes.
- **AccC-5 (Compatibilidad Multiplataforma):** La CLI de instalación y gestión del daemon soporta Linux (systemd), macOS (launchd) y Windows (Windows Service) sin romper la compilación cruzada.

---

## 4. Design (Diseño Técnico y Arquitectura de Dominio)

### 4.1. Arquitectura de Dominio (Screaming Architecture)

El sistema separa estrictamente los límites del dominio de la infraestructura y el transporte:

```text
       ┌────────────────────────┐
       │     Cliente MCP / IA   │
       └───────────┬────────────┘
                   │ MCP (Streamable HTTP / Stdio)
       ┌───────────▼────────────┐
       │   internal/mcp         │ (Adaptador de Presentación MCP)
       └───────────┬────────────┘
                   │ Llamadas a Casos de Uso
       ┌───────────▼────────────┐
       │   internal/memory      │ (Núcleo de Dominio: MemoryItem, MemoryLink, Service)
       └─────┬────────────┬─────┘
             │            │
  ┌──────────▼─────┐   ┌──▼───────────────┐
  │internal/storage│   │internal/embedding│ (Adaptadores de Infraestructura)
  └──────────┬─────┘   └──┬───────────────┘
             │            │
  ┌──────────▼─────┐   ┌──▼───────────────┐
  │ PostgreSQL 18  │   │  Ollama Local    │
  │ + pgvector     │   │ (Gemma 300m)     │
  └────────────────┘   └──────────────────┘
```

### 4.2. Esquema de Datos y Persistencia

#### Tabla `memory_items`
```sql
CREATE TABLE IF NOT EXISTS memory_items (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    content TEXT NOT NULL,
    memory_type TEXT NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now(),
    occurred_at TIMESTAMPTZ,
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    source_type TEXT NOT NULL,
    source_ref TEXT,
    conversation_id TEXT,
    turn_index BIGINT,
    importance REAL,
    embedding vector(768),
    embedding_model TEXT,
    content_hash TEXT,
    metadata JSONB,
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);
```

#### Tabla `memory_links`
```sql
CREATE TABLE IF NOT EXISTS memory_links (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    source_id UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    target_id UUID NOT NULL REFERENCES memory_items(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT now(),
    metadata JSONB
);
```

### 4.3. Estrategia de Búsqueda Híbrida y Reranking
1. **Recuperación Semántica:** Búsqueda HNSW con distancia coseno (`embedding <=> $1`) para obtener los primeros $K_1$ candidatos semánticos.
2. **Recuperación Léxica:** Búsqueda por texto completo con operador `@@` sobre `search_vector` para obtener los primeros $K_2$ candidatos de coincidencia exacta.
3. **Fusión y Reranking:** Algoritmo de combinación (RRF - Reciprocal Rank Fusion) ponderando similitud semántica, relevancia léxica, frescura temporal (`created_at`/`occurred_at`) y estado activo.
4. **Proyección Progresiva:** Entrega de datos esenciales (ID, resumen, relevancia, metadatos clave) para evitar el desbordamiento de contexto del LLM.

---

## 5. Tasks (Plan de Trabajo y Dependencias)

- [x] **Task 1: Inicialización de Repositorio y Git Base**
  - Inicializar repositorio Git (`git init -b main`).
  - Configurar `.gitignore` robusto para proyectos Go.
  - Verificar ausencia de secretos con `verificar_secretos.py`.
  - Crear commit inicial bajo Conventional Commits en `main` y crear rama `dev`.
- [x] **Task 2: Corrección de Migración de Base de Datos**
  - Modificar `migrations/001_init.sql` para emplear `uuidv7()` nativo en claves primarias.
  - Ajustar dimensiones de vectores de embeddings a `vector(768)`.
- [x] **Task 3: Adaptación al SDK Oficial de MCP y pgvector-go**
  - Actualizar `go.mod` con `github.com/modelcontextprotocol/go-sdk` y `github.com/pgvector/pgvector-go`.
  - Refactorizar `internal/mcp/server.go` para emplear `mcp.NewServer`, `mcp.AddTool`, `mcp.NewStreamableHTTPHandler` y `mcp.StdioTransport`.
  - Actualizar `internal/storage/repository.go` para utilizar `pgvector.NewVector`.
- [x] **Task 4: Resolución de Compilación Multiplataforma**
  - Desacoplar `internal/installer/` utilizando build tags por archivo (`_linux.go`, `_darwin.go`, `_windows.go`, `_other.go`).
  - Limpiar imports no utilizados en `internal/memory/service.go` y `internal/storage/repository.go`.
- [x] **Task 5: Elaboración de Documentación Arquitectónica y SDD**
  - Crear `CODEBASE.md` documentando estado actual y arquitectura objetivo según *Screaming Architecture*.
  - Redactar `docs/SDD.md` formalizando la especificación en Modo 2.
- [ ] **Task 6: Integración y Verificación Final**
  - Ejecución de `go build ./...` y `go test ./...`.
  - Consolidación de cambios en rama de feature e integración en `dev`.

---

## 6. Human Approval (Punto de Control)

Plan arquitectónico acordado según especificación de `PRD.md` y directrices de `AGENTS.md`. No se introducen servicios externos ni dependencias cloud. Procedimiento validado con herramientas deterministas locales.

---

## 7. Implementation (Notas de Implementación)

- Se utilizó el comando oficial `go mod tidy` para registrar sumas criptográficas en `go.sum`.
- Se validó el aislamiento de plataformas mediante selectores de compilación del compilador de Go.
- El servidor MCP HTTP responde en `/mcp` mediante `StreamableHTTPHandler`, permitiendo sesiones de streaming sobre conexiones persistentes HTTP estándar de MCP.

---

## 8. Verify (Verificación y Evidencias Técnicas)

### Evidencias de Validación:
1. **Compilación completa:**
   ```bash
   $ go build ./...
   # Código de salida: 0 (Sin errores ni advertencias)
   ```
2. **Suite de pruebas unitarias:**
   ```bash
   $ go test ./...
   ok  github.com/lodan/memory/internal/embedding  0.008s
   ok  github.com/lodan/memory/internal/memory     0.003s
   ```
3. **Verificación de Seguridad y Secretos:**
   Ejecución de `verificar_secretos.py` con 0 hallazgos en stage.

---

## 9. Archive (Estado Consolidado)

Este SDD queda archivado en `docs/SDD.md` como la especificación de referencia para el hito fundacional de la arquitectura de Lodan. Cualquier ampliación subsiguiente (ej. implementación del motor avanzado RRF o Keystore nativo) deberá formularse mediante un nuevo ciclo SDD que tome como base este diseño.
