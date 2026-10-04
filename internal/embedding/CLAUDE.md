# CLAUDE.md — embedding

## Resumen

**Problema**: la IA no calcula vectores; el servidor necesita generarlos de forma barata y tolerante a fallos.
**Objetivo**: cliente de Ollama (`POST /api/embed`, en lote, `keep_alive`, `dimensions`), prefijos de documento y consulta por modelo, y `FakeEmbedder` determinista para tests.
**Alcance**: dentro: generación de vectores. Fuera: decidir qué se guarda.

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Convenciones específicas

Los prefijos de cada modelo salen de su ficha oficial; no se inventan. Un error de prefijos degrada la búsqueda sin avisar.

## Pruebas

`go test ./internal/embedding/...`
