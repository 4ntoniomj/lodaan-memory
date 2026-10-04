# CLAUDE.md — config (compartido)

## Resumen

**Problema**: rutas y ajustes distintos por sistema operativo, configurables por el usuario.
**Objetivo**: directorio de datos por SO, `config.json`, variables `LODAN_*` y validación (solo localhost).
**Alcance**: dentro: carga y validación de la configuración. Fuera: cualquier lógica de dominio.
**Por qué es compartido**: lo usan todas las funcionalidades y la CLI.

## Cómo trabajar aquí

| Skill | Cuándo |
| :--- | :--- |
| `sdd` | Cualquier cambio no trivial. |
| `usar-git` | Cualquier commit o rama que la toque. |

## Pruebas

`go test ./internal/config/...`
