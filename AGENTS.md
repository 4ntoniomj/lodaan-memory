AGENTS.md — Instrucciones de desarrollo para la Memoria Universal Local

0. Propósito y jerarquía

Este archivo define cómo debe trabajar una IA de desarrollo dentro de este repositorio.

La fuente de verdad funcional y arquitectónica del producto es prd.md.

Orden de prioridad:

Instrucciones del sistema/plataforma del agente.

AGENTS.md.

prd.md.

Skills obligatorias del proyecto, especialmente use-sdd y screaming-architecture.

Documentación oficial y actual de las tecnologías utilizadas.

Decisiones tomadas durante el desarrollo, siempre que no contradigan los puntos anteriores.

Cuando una instrucción entre en conflicto con otra de mayor prioridad, prevalece la de mayor prioridad.

No inventes requisitos que no estén sustentados por la documentación del proyecto o por una necesidad técnica real.

1. Regla principal

No construyas un prototipo rápido. Construye un producto real, instalable, mantenible y verificable.

El objetivo es desarrollar una memoria universal local para IA con estas propiedades no negociables:

Go como lenguaje de aplicación.

PostgreSQL + pgvector como fuente de verdad persistente.

Ollama local para embeddings.

MCP como interfaz con la IA.

Funcionamiento exclusivamente local.

PostgreSQL y MCP accesibles solo por loopback.

Sin API keys ni tokens para el uso local normal del MCP.

Memoria universal sin esquemas rígidos por dominio.

Histórico no destructivo y línea temporal.

Relaciones entre recuerdos.

Búsqueda semántica y recuperación híbrida.

Recuperación progresiva para minimizar tokens.

Escalabilidad sin recorridos lineales de toda la memoria.

Servicio persistente en segundo plano.

Instalación nativa en macOS, Linux y Windows.

Exportación e importación de la memoria.

Skill de usuario para enseñar a una IA a utilizar la memoria.

Si una implementación aparentemente sencilla rompe cualquiera de estos principios, elige una implementación que los preserve.

2. Skills obligatorias

2.1. use-sdd

Debe utilizarse en toda tarea de desarrollo no trivial.

Antes de implementar una funcionalidad relevante:

Activa/consulta la skill use-sdd.

Determina requisitos, escenarios, restricciones y criterios de aceptación.

Define el cambio antes de escribir código.

Mantén la especificación alineada con la implementación.

Verifica los criterios de aceptación después de implementar.

No utilices SDD como documentación ornamental. Debe guiar realmente el cambio.

2.2. screaming-architecture

Debe utilizarse para todas las decisiones estructurales del código.

La estructura del repositorio debe hacer visible el dominio del producto. Un desarrollador debe poder mirar los directorios principales y entender inmediatamente que se trata de una memoria para IA.

Evita una estructura dominada por carpetas genéricas como:

controllers/
services/
repositories/
helpers/
utils/
models/

No prohíbas capas internas cuando sean necesarias, pero las capas no deben ser la identidad principal del repositorio.

La arquitectura debe gritar conceptos del dominio, por ejemplo:

cmd/
  memory-server/

internal/
  memory/
  retrieval/
  timeline/
  relationships/
  conversations/
  embeddings/
  importexport/
  health/
  security/
  installation/

migrations/
benchmarks/
tests/
docs/

La estructura exacta puede cambiar si screaming-architecture recomienda una organización mejor, pero debe conservar el principio de dominio visible + límites claros.

3. Flujo de trabajo obligatorio

Para cada cambio importante sigue esta secuencia:

leer contexto
→ verificar skills
→ verificar documentación actual
→ especificar
→ diseñar
→ implementar
→ probar
→ medir
→ revisar
→ documentar

Paso 1 — Entender antes de modificar

Antes de tocar código:

lee AGENTS.md;

lee las partes relevantes de prd.md;

inspecciona la estructura actual del repositorio;

identifica interfaces y dependencias afectadas;

localiza tests existentes;

comprueba si ya existe una solución reutilizable.

No crees código duplicado antes de comprobar qué existe.

Paso 2 — Tecnología actual

Cuando una decisión dependa de una API o comportamiento que pueda haber cambiado, consulta documentación oficial actual.

Esto aplica especialmente a:

MCP;

SDK Go de MCP;

PostgreSQL;

pgvector;

Ollama;

Gemini CLI Agent Skills;

Homebrew;

servicios nativos de macOS/Linux/Windows.

No inventes nombres de endpoints, flags, versiones, opciones de instalación ni APIs.

Paso 3 — SDD

Usa use-sdd para concretar el cambio antes de implementar.

Paso 4 — Diseño

Usa screaming-architecture para decidir dónde vive cada responsabilidad.

Paso 5 — Implementación

Implementa primero una solución pequeña, correcta y observable. Evita introducir abstracciones prematuras.

Paso 6 — Verificación

Todo cambio debe terminar con las comprobaciones apropiadas:

tests unitarios;

tests de integración;

tests de contrato MCP cuando proceda;

tests de instalación cuando proceda;

benchmarks cuando afecte al rendimiento;

análisis de consultas PostgreSQL cuando afecte a retrieval;

revisión de seguridad cuando afecte a procesos, red, almacenamiento o secretos.

4. Arquitectura del software

La aplicación principal es un proceso Go que expone MCP y coordina la memoria local.

Conceptualmente:

MCP Host / IA
      │
      ▼
MCP Transport
      │
      ▼
Application / Memory Engine
      │
      ├── Memory
      ├── Retrieval
      ├── Timeline
      ├── Relationships
      ├── Conversations
      ├── Embeddings
      ├── Import/Export
      ├── Health
      └── Security
      │
      ├──────────────► PostgreSQL + pgvector
      └──────────────► Ollama

Regla de dependencia

El dominio no debe depender directamente de:

HTTP;

MCP;

PostgreSQL;

Ollama;

detalles del sistema operativo.

Utiliza interfaces donde exista una dependencia real de infraestructura.

No crees interfaces artificiales para cada struct solo por seguir una moda.

5. Límites que deben mantenerse

MCP / transporte

Responsable de:

exponer tools;

validar entradas externas;

serializar/deserializar;

mapear errores al protocolo.

No contiene la lógica de ranking ni de persistencia.

Aplicación / Memory Engine

Responsable de orquestar casos de uso:

guardar;

buscar;

obtener;

historial;

relaciones;

conversaciones;

importar/exportar;

health.

Dominio

Responsable de conceptos y reglas de memoria:

recuerdo universal;

estado activo/inactivo;

línea temporal;

versiones;

relaciones;

relevancia conceptual.

Infraestructura

Responsable de:

PostgreSQL;

pgvector;

FTS;

Ollama;

filesystem;

cifrado/secret storage;

servicios del sistema operativo.

No filtres detalles de infraestructura hasta el dominio si no son necesarios.

6. Memoria universal

No introduzcas una tabla o agregado específico para cada dominio de usuario.

No crear estructuras permanentes como:

projects
work_notes
childhood_memories
ai_conversations
preferences
ideas

Todo debe poder representarse mediante la memoria universal y sus relaciones.

El tipo de memoria, metadatos y relaciones deben seguir siendo extensibles.

No utilices ENUM de PostgreSQL para conceptos que el usuario puede ampliar en el futuro.

7. Regla IA → lenguaje natural → Memory Engine

La IA no debe necesitar conocer:

SQL;

tablas;

joins;

nombres de índices;

dimensiones del embedding;

parámetros internos del ranking.

Las herramientas MCP deben poder trabajar principalmente con lenguaje natural.

El programa transforma esa intención en operaciones estructuradas.

Nunca obligues al modelo a generar SQL para utilizar la memoria.

8. Retrieval y rendimiento

La recuperación es una parte crítica del producto.

Prohibido

No hagas búsquedas semánticas que hagan:

SELECT todos los embeddings
→ comparar todos
→ ordenar todos
→ devolver unos pocos

Obligatorio

La búsqueda semántica debe apoyarse en el índice vectorial apropiado, inicialmente HNSW con pgvector.

La recuperación normal debe utilizar una cantidad de candidatos acotada y posteriormente combinar:

similitud semántica;

búsqueda textual;

filtros de metadatos;

recencia;

estado activo/inactivo;

relaciones cuando aporten valor.

Regla de regresión

Si una consulta que antes utilizaba índice pasa a realizar un scan lineal completo, el cambio se considera una regresión aunque los tests funcionales sigan pasando.

Usa EXPLAIN (ANALYZE, BUFFERS) en las consultas críticas.

9. Rendimiento: medir, no suponer

No afirmes que una búsqueda es escalable porque “usa HNSW”. Hay que medirla.

Mantén benchmarks reproducibles para:

10k
100k
1M
5M
10M

cuando el entorno lo permita.

Controla, como mínimo:

P50;

P95;

P99;

candidatos procesados;

tiempo de embedding de consulta;

tiempo de búsqueda vectorial;

tiempo de FTS;

tiempo de reranking;

latencia total;

RAM;

tamaño de PostgreSQL.

Cuando cambies retrieval, crea o actualiza benchmarks.

No optimices únicamente un caso artificial. Mide consultas representativas.

10. PostgreSQL

PostgreSQL es la fuente de verdad.

La base de datos debe permanecer accesible solo mediante localhost.

La aplicación debe gestionar de forma segura:

detección;

inicialización;

extensión vector;

migraciones;

conexión;

health checks;

configuración necesaria.

No realices operaciones destructivas sobre una instalación existente sin una condición explícita y segura.

Las migraciones deben ser:

versionadas;

reproducibles;

idempotentes cuando corresponda;

comprobables en CI.

No modifiques una migración ya aplicada para solucionar un problema nuevo; crea una nueva migración.

11. Ollama y embeddings

Ollama es una dependencia de infraestructura local.

No mezcles las llamadas a Ollama con la lógica de dominio.

Utiliza una interfaz/proveedor de embeddings para poder cambiar de modelo en el futuro.

La configuración del modelo debe estar centralizada.

No dependas de internet durante una consulta normal de memoria.

Gestiona explícitamente:

Ollama no instalado;

Ollama no iniciado;

modelo inexistente;

timeout;

errores de generación;

cambio de modelo/dimensiones.

No ocultes un fallo de embeddings insertando silenciosamente recuerdos sin vector cuando eso pueda romper el retrieval. Define y prueba la política de fallback.

12. MCP y red local

El producto debe funcionar como servicio en segundo plano.

Binding obligatorio

MCP HTTP:

127.0.0.1

PostgreSQL:

localhost / loopback

Nunca uses 0.0.0.0 como binding por defecto.

No añadas autenticación remota innecesaria.

No expongas secretos mediante tools, logs, errores ni métricas.

13. Sistema operativo

La aplicación debe ser nativa en:

macOS;

Linux;

Windows.

No uses Docker, WSL, Electron ni runtimes adicionales como solución al problema de instalación.

La experiencia de servicio debe integrarse con los mecanismos nativos del sistema operativo siempre que sea posible:

macOS: launchd / integración Homebrew;

Linux: systemd user cuando sea adecuado;

Windows: Windows Service.

La lógica de instalación debe mantenerse separada de la lógica de memoria.

14. Dependencias

Cada dependencia nueva debe justificar su existencia.

Antes de añadir una librería, comprueba:

si la funcionalidad puede resolverse con la librería estándar de Go;

si ya existe una dependencia equivalente en el proyecto;

si añade un proceso/runtime adicional;

si complica la distribución multiplataforma;

si está mantenida y documentada;

si introduce una licencia incompatible.

No añadas una dependencia solo porque sea más cómoda.

Especialmente evita introducir otra base de datos, cola, cache distribuida o motor de búsqueda externo para solucionar algo que PostgreSQL ya puede resolver.

15. Seguridad y privacidad

Este es un sistema de memoria personal y puede contener información sensible.

Principios:

local-first;

mínimo acceso de red;

cero cloud obligatorio;

secretos fuera del contenido de memoria;

logs sin contenido sensible por defecto;

errores sin credenciales;

permisos de archivos adecuados;

cifrado de datos cuando esté configurado;

claves almacenadas mediante mecanismos seguros del sistema cuando sea posible.

No registres en logs el contenido completo de una memoria salvo que el modo diagnóstico lo solicite explícitamente y exista una advertencia clara.

Nunca incluyas contraseñas, tokens o claves privadas en tests, fixtures, documentación o código.

16. Calidad de código Go

Sigue las convenciones idiomáticas de Go.

Prioridades:

corrección;

claridad;

observabilidad;

rendimiento medido;

simplicidad.

Usa context.Context para operaciones que puedan bloquear o depender de I/O.

Gestiona timeouts.

Devuelve errores útiles y con contexto.

No ignores errores.

Evita goroutines sin propietario claro ni mecanismo de parada.

Los procesos de fondo deben tener:

cancelación;

cierre limpio;

recuperación ante fallos donde proceda;

tests de shutdown.

17. Tests

La suite debe cubrir como mínimo:

Unitarios

normalización de memoria;

reglas temporales;

deduplicación;

ranking;

filtros;

relaciones;

serialización.

Integración

PostgreSQL real;

pgvector real;

migraciones;

FTS;

HNSW;

Ollama cuando esté disponible;

MCP.

End-to-end

Como mínimo debe existir un flujo que pruebe:

MCP
→ guardar recuerdo
→ generar embedding
→ persistir en PostgreSQL
→ buscar mediante lenguaje natural
→ recuperar recuerdo

Instalación

Probar instalación, arranque, parada y detección de dependencias en las plataformas soportadas o en CI equivalente.

Seguridad

Probar que:

MCP escucha solo en loopback;

PostgreSQL escucha solo en loopback;

secretos no aparecen en respuestas MCP;

logs normales no exponen contenido sensible.

18. Skill de usuario para la memoria

El proyecto debe generar y mantener una Agent Skill para la IA que vaya a usar la memoria.

La creación de esta skill debe hacerse utilizando la skill crear-skills / skill-creator disponible en el entorno.

La skill debe enseñar a la IA:

cuándo guardar información;

cuándo no guardarla;

cómo formular una búsqueda en lenguaje natural;

cómo pedir histórico;

cómo recuperar conversaciones anteriores;

cómo utilizar relaciones;

cómo distinguir memoria activa de histórica;

cuándo solicitar memory_get después de memory_search;

cómo evitar guardar duplicados innecesarios;

cómo manejar contradicciones o cambios de decisión;

cómo guardar conversaciones o fragmentos relevantes;

cómo minimizar llamadas y contexto innecesario.

La skill no debe enseñar SQL ni detalles internos innecesarios.

Ubicación

La implementación debe detectar la ruta de skills soportada por la versión real de Gemini CLI/Agent Skills instalada.

Actualmente Gemini CLI soporta skills de usuario en ~/.gemini/skills/ y también el alias ~/.agents/skills/. Si el proyecto necesita interoperar con una convención local distinta, documenta y genera la skill en la ubicación compatible correspondiente.

No asumas que ~/.gemini/config/skills es una ruta válida para todas las versiones sin verificarlo.

La skill debe tener un SKILL.md válido con frontmatter name y description.

19. Documentación

Toda funcionalidad pública relevante debe estar documentada.

Debe existir como mínimo documentación para:

instalación;

configuración;

arranque del servicio;

conexión MCP;

tools MCP;

recuperación de memoria;

backup/export/import cuando esté disponible;

troubleshooting;

arquitectura;

desarrollo local;

benchmarks;

modelo de datos;

skill de usuario.

No copies documentación entera de terceros. Enlaza a la documentación oficial y explica únicamente lo necesario para el proyecto.

20. Observabilidad y diagnóstico

El servicio debe incluir health checks y diagnósticos suficientes para saber:

si PostgreSQL está disponible;

si pgvector está disponible;

si Ollama está disponible;

qué modelo de embedding está configurado;

si las migraciones están al día;

si MCP está escuchando correctamente;

si existe un conflicto de puerto;

si la configuración es válida.

Los diagnósticos no deben revelar secretos.

21. Migraciones y compatibilidad

Los cambios de esquema deben ser compatibles con instalaciones existentes siempre que sea razonablemente posible.

No destruyas datos para simplificar una migración.

Cuando haya que cambiar el modelo de embeddings:

detecta incompatibilidad de dimensiones/modelo;

define un proceso de reindexación;

no mezcles silenciosamente vectores incompatibles;

conserva la trazabilidad del modelo utilizado.

22. Exportación e importación

Toda la memoria debe poder exportarse e importarse sin perder:

contenido;

IDs;

timestamps;

estado activo/inactivo;

temporalidad;

metadatos;

relaciones;

información de conversaciones;

trazabilidad del embedding cuando sea relevante.

La importación debe validar datos antes de modificar la memoria existente.

Evita dejar una instalación en un estado parcialmente importado sin una estrategia de recuperación.

23. Git y cambios

Mantén cambios pequeños y coherentes.

No mezcles en un mismo cambio:

refactor arquitectónico grande;

cambio funcional no relacionado;

actualización masiva de formato;

actualización de dependencias sin relación.

No reformatees archivos que no necesites tocar.

No borres tests para hacer que la suite pase.

No marques tareas como terminadas porque compile: debe existir evidencia de verificación.

24. Definition of Done

Una funcionalidad no se considera terminada hasta que, según corresponda:

existe especificación SDD;

la arquitectura respeta screaming-architecture;

el código compila;

los tests relevantes pasan;

los tests de integración pasan cuando corresponda;

las migraciones funcionan desde una instalación limpia y una existente;

no aparecen regresiones de rendimiento conocidas;

la seguridad local está verificada;

la documentación está actualizada;

los logs y errores son razonables;

la instalación/arranque/parada funciona en la plataforma afectada;

la skill de usuario se mantiene compatible cuando cambia la interfaz MCP.

25. Prohibiciones explícitas

No:

cambies prd.md para hacer encajar una implementación mediocre;

introduzcas cloud obligatorio;

introduzcas API keys para el uso local normal;

expongas servicios en 0.0.0.0;

permitas acceso directo de la IA a PostgreSQL;

recorras toda la memoria para una búsqueda semántica normal;

introduzcas Python/Node/Docker/Redis/Elasticsearch como runtime adicional;

crees tablas rígidas por tipo de usuario o dominio;

destruyas histórico al actualizar recuerdos;

obligues a la IA a conocer SQL;

escondas fallos de infraestructura con silencios o datos falsamente completos;

inventes APIs o versiones no verificadas;

añadas abstracciones sin necesidad;

dejes trabajo crítico sin tests.

26. Regla para decisiones no especificadas

Cuando una decisión no esté determinada por prd.md, elige la opción que, en este orden:

preserve los objetivos del producto;

reduzca complejidad operacional;

mantenga los datos locales;

minimice dependencias;

mantenga la arquitectura orientada al dominio;

sea fácil de probar;

sea reversible;

tenga documentación oficial actual;

pueda verificarse mediante tests o benchmarks.

Documenta las decisiones importantes en el lugar apropiado. No conviertas AGENTS.md en un registro de decisiones de implementación.

27. Principio final

La IA que desarrolla este repositorio debe optimizar para esta propiedad:

Una persona debe poder instalar el sistema localmente, conectar una IA mediante MCP y disponer de una memoria universal persistente que siga siendo rápida y utilizable cuando pase de miles a millones de recuerdos, sin conocer PostgreSQL ni la implementación interna.

Cuando existan varias soluciones técnicamente válidas, elige la más sencilla que conserve esa propiedad y demuestra con pruebas que realmente funciona.
