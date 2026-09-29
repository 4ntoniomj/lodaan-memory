# CLAUDE.md — benchmark

## Resumen

**Problema**: la prioridad número uno es la velocidad a cualquier escala, con una máquina de 8 GB sin GPU; hay que medirla, no suponerla.
**Objetivo**: generar datos sintéticos, cargarlos con COPY, construir índices y medir latencia p50/p95, recall@10, tamaños y RAM (criterio 20).
**Alcance**: dentro: `lodan bench` sobre la base de datos separada `lodan_bench`. Fuera: tocar los datos reales.

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Pruebas

`go test ./internal/benchmark/...`
