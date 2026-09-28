# Documento de Diseño de Software (SDD) - Memoria Universal Local para IA

## 1. Contexto y Objetivos
- **Objetivo:** Construir un sistema de memoria local, universal y persistente para IA.
- **Acceso:** Exclusivamente mediante MCP (Model Context Protocol).
- **Tecnologías confirmadas:** Go, SDK oficial MCP (`github.com/modelcontextprotocol/go-sdk`), PostgreSQL 18, pgvector v0.8.6, y Ollama (modelo `embeddinggemma:300m-qat-q4_0`).

## 2. Arquitectura del Sistema
El sistema sigue una arquitectura local orientada a características (*Screaming Architecture*).

### Componentes Principales
- **Memory MCP Server (Go):** Proceso persistente que maneja la lógica y expone las herramientas MCP.
- **PostgreSQL 18 + pgvector:** Almacenamiento persistente, búsqueda híbrida (vectorial, texto, relacional). Uso de `uuidv7()` nativo.
- **Ollama Local:** Generación de embeddings.

## 3. Estructura de Directorios (Screaming Architecture)
```text
lodan-memory/
├── cmd/
│   └── memory-server/     # Entrypoint del servidor
├── docs/                  # Documentación (SDD, etc)
├── internal/
│   ├── memory/            # Core logic de recuerdos universales
│   ├── storage/           # Adaptador a PostgreSQL/pgvector
│   ├── embedding/         # Adaptador a Ollama y generación de vectores
│   └── mcp/               # Implementación de servidor y herramientas MCP
├── go.mod
```

## 4. Decisiones Clave de Diseño
- **Almacenamiento Universal:** Un único modelo base extensible en lugar de múltiples tablas por caso de uso.
- **Identificadores:** Uso de UUIDv7 para soporte nativo de ordenación temporal y eficiencia en PostgreSQL 18.
- **Búsqueda y Recuperación:** Búsqueda híbrida (HNSW vectorial + Texto completo + Metadatos) para asegurar rendimiento sublineal.
- **Aislamiento:** La IA nunca accede directamente a la BD. Todo ocurre a través de las herramientas MCP que abstraen SQL.
