# CLAUDE.md — topic

## Resumen

**Problema**: los temas son libres y sin catálogo, pero no deben multiplicarse en sinónimos (`gym` frente a `entrenamiento`).
**Objetivo**: normalizar slugs, cachear temas y resolver equivalencias por similitud de embedding (criterio 6).
**Alcance**: dentro: resolución y caché de temas. Fuera: la ficha del tema (va en `recall`).

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Pruebas

`go test ./internal/topic/...`
