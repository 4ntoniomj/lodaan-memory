# Mapa del Codebase — Lodan (Memoria Universal Local para IA)

Este documento es un mapa orientativo y persistente del repositorio para facilitar la navegación y coordinación. Describe la organización orientada a características (*Screaming Architecture*) según el dominio del sistema.

---

## 1. Visión General y Dominio

**Lodan** es un sistema de memoria universal, persistente y local para Inteligencia Artificial.
- **Acceso:** Exclusivamente vía Model Context Protocol (MCP) sobre localhost (Streamable HTTP y Stdio).
- **Persistencia y Búsqueda:** PostgreSQL 18 + extensión `pgvector` con índices HNSW y búsqueda de texto completo con `uuidv7()` nativo.
- **Embeddings:** Ollama local (modelo `embeddinggemma:300m-qat-q4_0`, dimensiones vectoriales 768).
- **Lenguaje:** Go (sin runtimes externos, sin cloud, local-first).

---

## 2. Estado Actual del Repositorio

```text
/home/antonio/lodan/
├── cmd/
│   └── memory-server/         # Entrypoint único CLI y daemon MCP (install, start, stdio, status, health, export, import)
├── internal/
│   ├── config/                # Centralización de configuración (puertos, URI postgres, URI ollama, dimensiones)
│   ├── embedding/             # Adaptador de embeddings con Ollama local (/api/embed)
│   ├── export/                # Exportación e importación de la memoria en formato versionado
│   ├── installer/             # Abstracción e instaladores nativos por SO (Linux systemd, macOS launchd, Windows Service)
│   ├── mcp/                   # Servidor MCP oficial (github.com/modelcontextprotocol/go-sdk/mcp), transporte HTTP/stdio y herramientas
│   ├── memory/                # Núcleo de dominio: MemoryItem, MemoryLink y Service
│   └── storage/               # Adaptador a PostgreSQL/pgvector, ejecución de migraciones SQL y persistencia
├── migrations/
│   └── 001_init.sql           # Esquema base: memory_items (vector 768, uuidv7), memory_links, HNSW y GIN
├── docs/
│   ├── SDD.md                 # Documento de Diseño de Software (Modo 2 / Complex Change)
│   └── PRD.md                 # Requisitos y arquitectura de producto
├── AGENTS.md                  # Directrices de desarrollo y gobernanza para agentes
├── go.mod                     # Módulo Go (go 1.25+ con MCP SDK y pgvector-go)
└── .gitignore                 # Reglas de exclusión de binarios y temporales
```

### Componentes y Responsabilidades Actuales

- **`cmd/memory-server`**: Punto de entrada binario. Provee subcomandos para inicialización, ejecución de servidor (HTTP streamable o Stdio), gestión de servicios y utilidades de import/export.
- **`internal/memory`**: Entidades centrales de memoria (`MemoryItem`, `MemoryLink`). Contiene la lógica de negocio para crear y buscar recuerdos sin acoplarse a capas de transporte ni a bases de datos específicas.
- **`internal/storage`**: Implementación de almacenamiento sobre PostgreSQL usando el driver `github.com/lib/pq` y `github.com/pgvector/pgvector-go`. Incluye la aplicación automática de migraciones SQL.
- **`internal/embedding`**: Cliente HTTP para Ollama que envía texto al endpoint `/api/embed` y extrae vectores numéricos (`[]float32`).
- **`internal/mcp`**: Implementación de herramientas MCP (`memory_store`, `memory_search`) sobre el SDK oficial `github.com/modelcontextprotocol/go-sdk/mcp`. Soporta transporte HTTP (`StreamableHTTPHandler`) y Stdio (`StdioTransport`).
- **`internal/export`**: Serialización y deserialización de recuerdos para copia de seguridad o migración entre entornos.
- **`internal/installer`**: Gestión desacoplada del ciclo de vida del servicio como daemon en segundo plano según el SO anfitrión (`//go:build`).
- **`internal/config`**: Parámetros de entorno y configuración centralizada sin valores fijos dispersos.

---

## 3. Organización Objetivo (Screaming Architecture)

A medida que el proyecto madure y se implementen todas las especificaciones de `PRD.md`, los módulos evolucionarán hacia límites de dominio aún más explícitos y especializados:

```text
internal/
├── memory/            # Modelo de memoria universal y ciclo de vida de recuerdos
├── retrieval/         # Motor de búsqueda híbrida avanzada (HNSW vectorial + Full-Text + RRF Reranking + Top-K)
├── timeline/          # Motor temporal: gestión de vigencia, recuerdos activos vs históricos, relaciones 'supersedes'
├── relationships/     # Motor de enlaces y navegación del grafo de recuerdos (memory_links)
├── conversations/     # Contextualización de turnos y reconstrucción de conversaciones completas
├── embeddings/        # Gestión del ciclo de vida de vectores, versionado de modelos y migración en segundo plano
├── importexport/      # Formato propio versionado (memory-export-v1) con validación de modelo y dimensiones
├── health/            # Motor de diagnóstico ("doctor") para verificar PostgreSQL, pgvector, Ollama y socket loopback
├── security/          # Abstracción de cifrado en reposo con integración de KeyStore nativo del SO (Keychain, Credential Manager, Secret Service)
├── installation/      # Gestor idempotente de instalación multiplataforma sin dependencias Docker ni runtimes
├── config/            # Configuración unificada versionada
└── mcp/               # Superficie de contacto MCP: catálogo completo de herramientas (store, search, get, history, link, update, etc.)
```

---

## 4. Puntos de Entrada y Comandos de Validación

### Compilación
```bash
go build ./...
```

### Ejecución de Pruebas
```bash
go test ./...
```

### Servidor Local
```bash
# Modo Stdio (para hosts MCP locales vía stdin/stdout)
go run ./cmd/memory-server stdio

# Modo HTTP (transporte persistente por defecto en 127.0.0.1:8080)
go run ./cmd/memory-server start
```

---

## 5. Reglas de Dependencias Internas

1. El dominio central (`memory`, `retrieval`, `timeline`, `relationships`) **no debe depender** de `mcp`, HTTP ni de frameworks de presentación.
2. `mcp` es una capa adaptadora externa que consume los servicios del dominio.
3. El acceso a bases de datos (`storage`) implementa las interfaces requeridas por el dominio (`Storage`), garantizando inversión de dependencias.
4. Las conexiones de red están estrictamente restringidas a interfaces loopback (`127.0.0.1`).
