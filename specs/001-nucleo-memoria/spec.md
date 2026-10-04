# Spec 001 — Núcleo de memoria

> Nivel: **complejo** · Rama: `feat/nucleo-memoria` · Hereda de: [specs/PRD.md](../PRD.md)

## Intent

Concreta el corazón de lodan descrito en el PRD. Es el servicio que:
- guarda en lenguaje natural lo que tiene sustancia;
- lo recupera en una sola llamada, rápido y con pocos tokens;
- mantiene sesiones y la evolución de cada registro (sustituido, invalidado, relaciones).

Cubre los objetivos O1, O2, O3 y O5 del PRD (en O5, solo la conexión MCP) y la base de O6 (las instrucciones del propio servidor). Sin esto no hay producto: el instalador (spec 002) y la skill (spec 003) se apoyan en él.

## Alcance

### Dentro

- Requisitos del PRD R1–R12 y R15.
- De R14, solo las instrucciones que el propio servidor MCP entrega al conectarse.
- Transportes MCP: stdio y HTTP, este último solo en 127.0.0.1.
- Gestión del PostgreSQL local de lodan (inicializar, arrancar, parar, estado y migraciones), usando binarios de PostgreSQL + pgvector ya presentes en la máquina.
- Herramienta de benchmark reproducible y su ejecución a 10 mil, 100 mil y 1 millón de registros.

### Fuera

- Descarga e instalación de PostgreSQL y Ollama, arranque automático y configuración de clientes MCP: van en la spec 002.
- La skill de uso: va en la spec 003.
- Copias de seguridad y Windows ARM64: fuera, como dice el PRD.
- La ejecución del benchmark a 10 millones. La herramienta la soporta, pero la ejecución es manual y larga: se documenta como hecha o pendiente, con motivo.
- Elegir el modelo de embeddings definitivo con textos reales del usuario. Queda `embeddinggemma` por defecto y es configurable.

## Criterios de aceptación

### Guardar

1. - GIVEN un cliente MCP conectado y sin sesión activa
     WHEN llama a `remember` con un registro (título, contenido, tipo `decision`, temas `[lodan]`)
     THEN se crea un registro vigente con embedding, se crea la sesión de esa conexión y la respuesta es texto compacto con el id `#n`.
2. - GIVEN un registro vigente con el mismo contenido normalizado
     WHEN se llama a `remember` con ese contenido
     THEN no se crea fila nueva y la respuesta devuelve el id existente marcado como duplicado.
3. - GIVEN un registro vigente con clave `entrenamiento/horario`
     WHEN se guarda otro con la misma clave
     THEN el anterior pasa a `superseded`, enlazado al nuevo mediante una relación `supersedes` confirmada.
4. - GIVEN un registro vigente semánticamente muy parecido (similitud por encima del umbral configurado)
     WHEN se guarda uno nuevo
     THEN se crea, y la respuesta lista los posibles duplicados o contradicciones (id y título) con una relación `suggested`.
5. - GIVEN Ollama no disponible
     WHEN se llama a `remember`
     THEN el registro se guarda con el embedding pendiente, la respuesta lo indica, y al volver Ollama el embedding se calcula sin intervención.
     Lo mismo ocurre si Ollama tarda más que `embed_timeout_ms` (por defecto 2000): el registro queda pendiente y el cálculo del embedding y de los parecidos se completa en segundo plano.
6. - GIVEN existe el tema `entrenamiento`
     WHEN se guarda con un tema nuevo cuyo embedding supera el umbral de equivalencia con él
     THEN se usa `entrenamiento` y la respuesta lo indica.
     (Se verifica con un embedder controlado en test; el umbral con el modelo real se calibra y documenta.)

### Recuperar

7. - GIVEN registros del tema `entrenamiento` (preferencias, decisiones y eventos)
     WHEN se llama a `recall` con "¿qué hice en el último entreno?" sin indicar tema
     THEN una sola respuesta contiene: los datos estables vigentes del tema, los últimos eventos (el más reciente primero) y otros resultados relacionados. No incluye registros sustituidos ni invalidados y no supera el tope de bytes configurado.
8. - GIVEN un registro que contiene "1234-KLM"
     WHEN se llama a `recall` con "1234-KLM"
     THEN aparece entre los 3 primeros resultados.
9. - GIVEN un registro en español
     WHEN se llama a `recall` con una paráfrasis sin palabras en común
     THEN aparece entre los 10 primeros resultados. Test de integración con el modelo real; se salta con aviso si no hay Ollama.
10. - WHEN se llama a `recall` con `include_history`
      THEN también aparecen los registros sustituidos e invalidados, marcados con su estado y enlace.
11. - WHEN se llama a `get` con ids
      THEN devuelve el contenido completo y las relaciones de cada uno.

### Revisar

12. - WHEN se hace `revise` con `invalidate`
      THEN el registro deja de salir en `recall` normal.
    - WHEN se hace `revise` con `delete` sin `confirm: true`
      THEN se rechaza.
    - WHEN se hace con `confirm: true`
      THEN se borra físicamente el registro, junto con sus relaciones y temas asociados.
13. - `revise` con `relate` crea una relación confirmada.
    - Una relación `suggested` se puede confirmar o rechazar.
    - `revise` con `supersede` marca el antiguo y crea el enlace.

### Sesiones

14. - GIVEN varias sesiones de clientes distintos
      WHEN se llama a `session` con `last`
      THEN devuelve la más reciente con fecha y hora hasta el minuto, cliente, temas tocados y resumen. Con filtro de tema, cliente o fecha devuelve la que corresponda.
15. - `session` con `end` y un resumen lo guarda.
    - Una sesión sin actividad durante el tiempo configurado (por defecto 30 min) queda cerrada.
16. - Dos procesos stdio simultáneos crean sesiones distintas sin colisión.

### MCP y seguridad

17. - `tools/list` devuelve como máximo 6 herramientas.
    - `initialize` devuelve instrucciones de uso de como máximo 1.500 caracteres.
18. - En modo HTTP el servidor solo escucha en 127.0.0.1.
    - Una petición con cabecera `Host` que no sea localhost se rechaza.
19. - El PostgreSQL de lodan escucha solo en 127.0.0.1.
    - Usa autenticación `scram-sha-256`, nunca `trust` en TCP.
    - Guarda los datos en el directorio de datos del usuario y la contraseña en un fichero con permisos 0600.

### Rendimiento y calidad

20. - `lodan bench` a 10 mil, 100 mil y 1 millón genera `specs/001-nucleo-memoria/benchmark.md` con:
      - p50 y p95 de latencia de búsqueda;
      - latencia de embedding de una consulta;
      - recall@10 de la búsqueda aproximada frente a la exacta;
      - tamaño de tabla e índices;
      - RAM.
    - La ejecución a 10 millones queda documentada como hecha o pendiente, con motivo.
    - El objetivo de latencia (p95 < 300 ms) aplica a la búsqueda en la base de datos; el embedding se reporta aparte como coste fijo (PRD O1).
21. - `go test ./...` pasa.
    - Los tests que necesitan PostgreSQL arrancan un clúster temporal; si no hay binarios, se saltan con un mensaje claro.
    - `go vet ./...` queda limpio.
