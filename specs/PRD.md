# PRD — lodan: memoria persistente local para IAs

> Estado: **aprobado** (2026-09-29) · Autor: Antonio (con Claude como orquestador)

## Resumen

lodan da a cualquier asistente de IA compatible con MCP una memoria persistente, única y privada del usuario. Guarda por su cuenta lo que tiene sustancia de cada conversación (decisiones, hechos, preferencias, notas, eventos) y lo devuelve en una sola consulta en lenguaje natural, rápido y con pocos tokens, tenga 10 registros o 10 millones. Funciona solo en la máquina del usuario (localhost), en Windows, Linux y macOS, instalado a nivel de usuario. Incluye una skill que enseña a cualquier IA qué guardar, qué no y cómo recuperarlo.

## Problema y contexto

- El usuario habla con varias IAs muchas horas al día: del orden de 8 h con Claude y otras 8 h con otra IA. Cada una olvida al cerrar la sesión, y lo que sabe una no lo sabe la otra.
- Consecuencia: repetir el mismo contexto una y otra vez (cómo entrena, qué decidió en un proyecto, qué le pasa al coche), perder decisiones y datos, y gastar tiempo y tokens reconstruyendo el contexto paso a paso en vez de que la IA lo tenga claro desde la primera pregunta.
- La información puede ser de cualquier tema: proyectos, trabajo, entrenamiento, salud, el coche, notas del día, lo que hay que hacer mañana, recuerdos de hace diez años. No se puede prever una estructura fija de antemano.
- Las memorias que existen hoy no resuelven el problema para este usuario:
  - las memorias propias de cada herramienta están aisladas por herramienta y tienen topes de tamaño (por ejemplo, Claude Code solo carga al arrancar las primeras 200 líneas o 25 KB de su índice de memoria);
  - Engram (Gentleman Programming) solo busca por texto, no por significado, expone 23 herramientas y guarda también los prompts del usuario;
  - el servidor MCP de memoria de referencia devuelve el grafo entero sin límite y busca por subcadena, así que no escala.
- Por qué ahora: el volumen de conversación es diario y alto; cada día sin memoria compartida es contexto que se pierde. No hay fecha límite externa.

## Usuarios objetivo

- **Usuario único: Antonio.** Perfil técnico, trabaja en Linux/WSL2 y usa a diario varios asistentes: Claude Code, Claude Desktop, Cursor, Codex, Hermes y cualquier otro cliente MCP. Máquina de referencia: 8 GB de RAM, sin GPU. No habrá otros usuarios ni contextos separados por persona.
- **Usuarios indirectos: los agentes de IA.** Son quienes llaman a las herramientas de lodan y siguen la skill. Su "experiencia" importa: menos herramientas, respuestas compactas y reglas claras equivalen a menos tokens y a menos errores al decidir qué guardar.

## Objetivos y métricas de éxito

Por orden de prioridad:

| # | Objetivo | Cómo se mide |
| :--- | :--- | :--- |
| O1 | **Recuperación rápida a cualquier escala** | (a) Búsqueda en la base de datos (candidatos semánticos, reordenación, texto y ficha del tema): p95 < 300 ms con 10 mil, 1 millón y 10 millones de registros en la máquina de referencia, medido con el benchmark reproducible. (b) Embedding de la consulta: coste fijo que no depende del número de registros; se mide y documenta (en la máquina de referencia, 0,5–2,7 s con Ollama en CPU) y se solapa en paralelo con la búsqueda de texto y la ficha. Decisión del usuario del 2026-09-29 (opción A). |
| O2 | **Pocos tokens** | (a) Recuperar el contexto de un tema cuesta 1 llamada en el caso habitual. (b) Como máximo 6 herramientas MCP expuestas. (c) Toda respuesta tiene un tope de tamaño configurable (valor por defecto: pregunta abierta). |
| O3 | **Eficacia al recuperar** | Conjunto de consultas reales del usuario, en español, con sus respuestas esperadas: se mide qué porcentaje aparece entre los 10 primeros resultados (recall@10). El umbral se fija tras la primera medición (pregunta abierta). |
| O4 | **Barato en recursos** | RAM y disco que ocupa lodan en reposo y buscando, medidos y documentados en la máquina de referencia. Objetivo propuesto: que conviva sin problemas con el sistema y las IAs en 8 GB (el tope exacto es una pregunta abierta). |
| O5 | **Universal** | Funciona con cualquier cliente MCP. Verificado al menos con Claude Code, Claude Desktop, Cursor, Codex y Hermes. |
| O6 | **Guardado con criterio** | Revisión de una muestra de sesiones reales: porcentaje de registros que el usuario considera ruido y número de decisiones importantes que faltan. Umbrales: pregunta abierta. |

## Alcance

### Dentro (primera versión)

- Guardar y recuperar registros de cualquier tema, hablando en lenguaje natural con el servidor MCP.
- Temas como etiquetas libres, sin catálogo fijo; un registro puede tener varias. Al etiquetar, se reutiliza una etiqueta existente si es equivalente (por ejemplo, `gym` frente a `entrenamiento`).
- Tipos de registro (dato estable, evento, decisión, preferencia, nota), para que la "ficha" de un tema salga sin generar texto con ningún modelo.
- Sesiones: identificador único ordenable por tiempo, fecha y hora legible hasta el minuto, cliente de IA, temas tocados y resumen corto al cerrar. Permite pedir "la última conversación", "la última sobre el coche" o "la del martes".
- Evolución de los registros:
  - estados `vigente`, `sustituido` (enlazado al que lo reemplaza) e `invalidado`; los dos últimos se conservan como historial y no salen en las búsquedas normales;
  - borrado definitivo solo cuando el usuario lo pide de forma explícita.
- Relaciones entre registros.
- Búsqueda por significado y por texto combinadas.
- Funcionamiento degradado: si el servicio de embeddings no está disponible, se guarda igualmente y se busca por texto; el embedding se calcula cuando vuelve.
- Acceso solo desde localhost; un único usuario.
- Instalación y configuración a nivel de usuario en Windows x64, Linux x64/ARM64 y macOS Intel/Apple Silicon:
  - arranque automático al iniciar sesión;
  - configuración automática de los clientes MCP que se detecten;
  - reutilización de un Ollama ya instalado si lo hay.
- Skill de uso de lodan para cualquier IA, creada con `crear-skills`.
- Benchmark de rendimiento reproducible.

### Fuera (primera versión)

- Copias de seguridad (irán en la siguiente versión).
- Windows ARM64: no hay distribución de PostgreSQL para esa arquitectura.
- Varios usuarios, acceso desde la red local o desde internet, nube y sincronización entre máquinas.
- Interfaz gráfica o TUI.
- Carga automática de contexto al abrir la IA: la IA consulta solo cuando lo necesita.
- Resúmenes o fichas generados por un modelo de lenguaje.
- Guardar la conversación literal o los prompts del usuario.
- Aceleración por GPU.
- Importar memorias de otras herramientas (Engram, claude.ai, etc.).

## Requisitos

| # | Requisito | Objetivo |
| :--- | :--- | :--- |
| R1 | **Guardar en lenguaje natural.** La IA envía el texto, el tipo y los temas. El servidor calcula el embedding. La IA nunca escribe SQL ni calcula vectores. | O2, O6 |
| R2 | **Contexto de un tema en una llamada.** Devuelve los datos estables vigentes del tema, los últimos eventos y lo más parecido a la consulta, dentro del tope de tamaño. El detalle completo de un registro se pide aparte, solo si hace falta. | O1, O2 |
| R3 | **Búsqueda híbrida**: por significado (embeddings) y por texto completo de PostgreSQL (equivalente al FTS5 que usa Engram en SQLite), fusionando ambas listas; encuentra tanto paráfrasis en español como términos exactos (nombres, matrículas, comandos). | O3 |
| R4 | **Temas libres**, varios por registro, con detección de etiquetas equivalentes. | O3 |
| R5 | **Sesiones** según el alcance: última conversación, última sobre un tema, conversación de una fecha, y resumen al cerrar. | O2 |
| R6 | **Evolución de registros.** Sustituir, invalidar y borrar definitivamente solo bajo petición explícita; el historial se puede consultar ("¿cómo ha cambiado mi horario de entreno?"). | O3 |
| R7 | **Relaciones tipadas entre registros** (sustituye, contradice, relacionado, parte de) con estado de juicio (sugerida, confirmada, rechazada), inspiradas en Engram. Al guardar, el servidor detecta registros vigentes muy parecidos y los devuelve como posibles duplicados o contradicciones, con una relación sugerida, para que la IA decida. | O3 |
| R8 | **Sin duplicados.** Un registro equivalente a uno reciente no crea fila nueva, y un dato estable del mismo tema se actualiza en vez de duplicarse. | O2, O4 |
| R9 | **Como máximo 6 herramientas MCP**, cada una con una descripción breve. | O2 |
| R10 | **Degradación sin embeddings**, según el alcance. | O1, O5 |
| R11 | **Solo localhost y un único usuario.** Ningún componente escucha fuera de la interfaz de loopback y la base de datos no acepta conexiones sin autenticar. | O5 |
| R12 | **Modelo de embeddings siempre cargado en memoria**, para evitar la latencia de carga en la primera consulta. | O1 |
| R13 | **Instalación con servicio de sistema nativo** (decisión del usuario del 2026-09-29): `lodan install` pide administrador una vez para registrar lodan como servicio de sistema (systemd en Linux, LaunchDaemon en macOS, servicio en `services.msc` en Windows); el resto (clientes MCP detectados, skill, datos) queda en el perfil del usuario. `lodan service start|stop|restart|status|enable|disable` gestiona el servicio con el gestor nativo de cada SO. Reutiliza un Ollama existente. | O4, O5 |
| R14 | **Skill de uso de lodan**, con varias reglas: guardar sin interrumpir lo que tiene sustancia (con los ejemplos del usuario: "quiero que la base de datos use embeddings" sí se guarda; "eres el agente orquestador" no); avisar en una línea de qué se guardó; recuperar el contexto de un tema en cuanto sale en la conversación; volver a consultar tras una compactación de contexto; cerrar la sesión con un resumen. Las mismas reglas básicas viajan en las instrucciones del propio servidor MCP para clientes sin soporte de skills. | O2, O6 |
| R15 | **Benchmark reproducible** a 10 mil, 1 millón y 10 millones de registros. | O1 |

## Supuestos y restricciones

### Restricciones (decididas por el usuario)

- **Stack:**
  - servidor MCP escrito en Go;
  - PostgreSQL con la extensión pgvector como base de datos;
  - Ollama para generar los embeddings.
- **Nativo en Windows, Linux y macOS**, configurable a nivel de usuario y ejecutado como **servicio de sistema** gestionable con las herramientas nativas (`systemctl`, `services.msc`, `launchctl`); la instalación pide administrador una sola vez para registrar el servicio.
- **Solo localhost.** Ningún dispositivo de la red local puede acceder.
- **Un único usuario y un único contexto.**
- **Máquina de referencia:** 8 GB de RAM, sin GPU.
- **Prioridades:** barato y rápido por encima de todo.

### Supuestos

- **Escala.** El objetivo de diseño es 10 millones de registros. El horizonte de 1.000 millones no es requisito: solo en vectores ocuparía del orden de 1,5–3 TB.
- **Modelo de embeddings.** Existe un modelo pequeño y multilingüe con calidad suficiente en español funcionando solo en CPU. Candidatos: `embeddinggemma` y `qwen3-embedding:0.6b`; alternativas: `bge-m3` y `nomic-embed-text-v2-moe`. Pendiente de verificar con textos del usuario. `nomic-embed-text`, `mxbai-embed-large` y `all-minilm` quedan descartados porque son solo en inglés.
- **Con 8 GB, los vectores completos no caben en RAM a 10 millones.** Con `halfvec` de 768 dimensiones ocuparían unos 15,4 GB. Hará falta una representación compacta en memoria y reordenar los candidatos con el vector completo leído de disco. El diseño concreto corresponde a `sdd`.
- **Distribución de PostgreSQL y pgvector.** Se obtienen a nivel de usuario desde conda-forge (PostgreSQL 18.6 y pgvector 0.8.6). En Windows, pgvector está fijado a PostgreSQL 16.
- **PostgreSQL en Windows sin administrador.** Se puede ejecutar como usuario estándar sin permisos de administrador (es lo que indica su diseño), pero falta probarlo en un Windows real.
- **Ollama en Linux.** Instalarlo sin `sudo` extrayendo el tarball en el directorio del usuario funciona según informes de usuarios, pero no está en la documentación oficial.
- **Las IAs siguen la skill.** Una skill o una instrucción es contexto, no una garantía técnica.

### Dependencias externas

- Ollama.
- Paquetes de conda-forge.
- Los clientes MCP de cada IA y su formato de configuración.

## Criterios de release

- **Rendimiento:** el benchmark R15 se ha ejecutado en la máquina de referencia y sus resultados están documentados. Si 10 millones no cumple O1, la versión no se publica hasta que el usuario decida explícitamente (aceptar el límite documentado o cambiar el diseño).
- **Recursos:** el consumo de RAM y disco está medido y documentado según O4.
- **Plataformas:** el binario compila para las cinco combinaciones del alcance. La instalación y una prueba de humo pasan en al menos Linux x64 (WSL2), Windows x64 y macOS Apple Silicon; las plataformas que no se hayan podido probar en real quedan listadas explícitamente.
- **Seguridad:**
  - verificado que PostgreSQL, Ollama y el servidor escuchan solo en 127.0.0.1;
  - la autenticación de PostgreSQL no es `trust`;
  - ningún secreto queda en texto plano fuera del perfil del usuario.
- **Clientes:** conexión y uso verificados con Claude Code, Claude Desktop, Cursor, Codex y Hermes.
- **Fiabilidad:**
  - sin Ollama se sigue guardando, y los embeddings se completan al volver;
  - reiniciar la máquina no pierde datos;
  - el arranque automático funciona.
- **Skill:** creada con `crear-skills`, e instalada. Probada en una sesión real: guarda las decisiones y los datos con sustancia, y no guarda la charla.
- **Idioma:** el contenido se guarda y se busca en cualquier idioma, con el español como caso principal. La documentación va en español; los identificadores y el código, en inglés.

## Riesgos

| Riesgo | Qué se haría |
| :--- | :--- |
| O1 no se alcanza a 10 millones con 8 GB de RAM | Medir pronto (el benchmark es de las primeras tareas); probar representaciones más compactas y menos dimensiones; si aun así no llega, documentar el límite real y que decida el usuario. |
| La IA guarda ruido o se deja lo importante | Una skill con ejemplos concretos del usuario; invalidar un registro tiene que ser fácil; revisar una muestra de sesiones tras las primeras semanas (O6). |
| El embedding en CPU es lento (0,5–2,7 s por consulta en la máquina de referencia) | Solapar el embedding con la búsqueda de texto y la ficha; caché de consultas; al guardar, tiempo máximo de espera y cálculo en segundo plano. Posible prueba futura de un motor de embeddings dentro del binario (opción B descartada por ahora). |
| Los temas se fragmentan (sinónimos, variantes) | Detección de etiquetas equivalentes al etiquetar (R4). |
| La instalación falla en algún sistema (PostgreSQL 16 en Windows, Ollama sin `sudo` en Linux) | Probar en máquinas reales antes de publicar; mensajes de error claros; documentar el procedimiento manual como alternativa. |
| El modelo elegido rinde mal en español | Probarlo con textos reales del usuario antes de fijarlo. |
| Cambiar de modelo de embeddings más adelante obliga a recalcular todo | Guardar qué modelo generó cada vector y prever un proceso de recálculo. |
| Algunos clientes no cargan skills | Reglas básicas en las instrucciones del propio servidor MCP (R14). |
| Pérdida de datos sin copias en la primera versión | Riesgo aceptado conscientemente; las copias van en la siguiente versión. |

## Preguntas abiertas

1. ¿Qué modelo de embeddings concreto? Se decide con una prueba sobre textos reales del usuario.
2. ¿Qué umbrales de eficacia (recall@10, O3) y de ruido (O6) se consideran aceptables? Se fijan tras la primera medición.
3. ¿Qué tope de tamaño por defecto tienen las respuestas de recuperación (O2)?
4. ¿Cuánta RAM como máximo puede ocupar lodan en la máquina de referencia (O4)?
5. ¿Debe haber una lista de datos sensibles que nunca se guardan (documentos de identidad, cuentas bancarias, contraseñas, claves de API)? claude.ai tiene una; aquí no se ha decidido.
6. ¿Se querrá importar en el futuro memorias de otras herramientas?