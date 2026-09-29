# CLAUDE.md — database (compartido)

## Resumen

**Problema**: lodan necesita su propio PostgreSQL a nivel de usuario, solo en localhost y con contraseña.
**Objetivo**: ciclo de vida del clúster local (initdb con scram-sha-256, arranque, parada y estado), pool pgx con tipos pgvector, migraciones embebidas y `NewTestCluster(t)`.
**Alcance**: dentro: infraestructura de base de datos. Fuera: consultas de dominio.
**Por qué es compartido**: lo usan `memory`, `recall`, `session`, `topic` y `benchmark` (Regla de Tres).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial; tocar el esquema es siempre complejo. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Convenciones específicas

Las migraciones nunca se editan una vez fusionadas: siempre se añade una nueva.

## Pruebas

`go test ./internal/database/...`
