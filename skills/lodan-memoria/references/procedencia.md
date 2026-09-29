# Procedencia

Skill creada con `crear-skills` el 2026-09-29, fusionando tres fuentes con objetivo similar (enseñar a una IA cuándo guardar y recuperar de una memoria MCP) y reescrita para las 6 herramientas de lodan y el caso de uso del usuario (memoria personal de cualquier tema, no solo de código).

| Fuente | URL | Licencia | Qué se tomó | Cambios |
| :--- | :--- | :--- | :--- | :--- |
| agentmemory, skill `memory-discipline` | https://github.com/rohitg00/agentmemory/blob/main/plugin/skills/memory-discipline/SKILL.md | Apache-2.0 | Estructura del ciclo (recuperar antes de actuar, guardar en el momento de la decisión, guardar el porqué), idea de checklist y antipatrones | Reescrito en español para lodan; sin hooks ni lecciones; memoria personal, no de código |
| Engram, skill `memory` del plugin de Claude Code | https://github.com/Gentleman-Programming/engram/blob/main/plugin/claude-code/skills/memory/SKILL.md | MIT (Copyright (c) 2026 Alan Buscaglia) | Disparadores de guardado tras confirmación o rechazo del usuario, uso de clave estable para datos que evolucionan, protocolo de cierre de sesión y de recuperación tras compactación, «la memoria no es la respuesta al usuario» | Adaptado a `remember`/`recall`/`session`; resumen de sesión corto en vez de plantilla larga; sin guardar prompts |
| `lodan-memory` del prototipo anterior (autor: el propio usuario) | `~/.gemini/config/skills/lodan-memory/SKILL.md` | Propia | Sección «No usar para…», recuperación progresiva para ahorrar tokens | Herramientas antiguas sustituidas por las 6 actuales |

Criterios de qué guardar: PRD de lodan (`specs/PRD.md`, R14) y ejemplos del usuario.

Ningún texto se copia literalmente; las ideas se reformulan. Aun así, se conservan las atribuciones de las licencias MIT y Apache-2.0.
