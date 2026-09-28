PRD — Memoria Universal Local para IA

Estado: Especificación de producto y arquitectura
Fecha: 2026-09-28
Objetivo: construir un sistema de memoria local, universal y persistente para una IA, accesible exclusivamente mediante MCP, con PostgreSQL + pgvector + Ollama y sin servicios cloud.

1. Resumen del producto

El producto es un servidor de memoria local para IA.

La IA no accede directamente a PostgreSQL. La arquitectura debe ser:

IA / MCP Host
      │
      │ MCP
      ▼
┌──────────────────────┐
│ Memory MCP Server    │
│ Go                   │
├──────────────────────┤
│ Memory API           │
│ Query Engine         │
│ Retrieval Engine     │
│ Timeline Engine      │
│ Relationship Engine  │
│ Embedding Adapter    │
│ Import / Export       │
│ Security / Config    │
└──────────┬───────────┘
           │ localhost
     ┌─────┴─────┐
     ▼           ▼
PostgreSQL     Ollama
+ pgvector     local
     │
     ▼
 Memoria persistente

La aplicación debe poder guardar y recuperar cualquier tipo de información sin diseñar tablas nuevas para cada caso: proyectos, trabajo, negocio, infancia, ideas, preferencias, decisiones, conversaciones con la IA, notas, documentos, hechos, relaciones, etc.

La memoria debe ser una sola memoria universal.

2. Problema que debe resolver

Una IA pierde contexto entre conversaciones o no puede consultar cantidades grandes de información sin consumir muchos tokens y tiempo.

El sistema debe permitir que la IA:

Guarde recuerdos cuando considere que merece la pena conservarlos.

Consulte recuerdos mediante lenguaje natural.

Recupere solo una pequeña cantidad de información relevante.

Priorice los recuerdos actuales y activos en el uso normal.

Permita recuperar recuerdos históricos o desactivados cuando el usuario lo pida.

Reconstruya conversaciones anteriores cuando sea necesario.

Relacione recuerdos entre sí.

Mantenga una línea temporal sin destruir el histórico.

Funcione completamente en local.

Mantenga un comportamiento de búsqueda sublineal al crecer el número de registros.

3. Principios no negociables

3.1. Local-first

No se enviará contenido de memoria a servicios externos.

No habrá API externa obligatoria.

No habrá claves API para usar el sistema.

Ollama será utilizado localmente para generar embeddings.

3.2. PostgreSQL como almacenamiento principal

PostgreSQL es la fuente de verdad persistente.

pgvector proporciona el almacenamiento y la búsqueda vectorial.

3.3. Go como lenguaje de aplicación

Toda la aplicación debe estar implementada en Go.

No usar Python, Node.js, Rust, Java, .NET, Electron, Docker ni runtimes adicionales para ejecutar el producto.

Se permiten scripts de instalación propios del sistema operativo cuando sean necesarios, pero no deben convertirse en dependencias de ejecución.

3.4. Memoria universal

No crear una arquitectura rígida con tablas independientes para proyectos, notas, trabajo, infancia, conversaciones, etc.

El contenido debe almacenarse en una estructura genérica extensible.

3.5. La IA decide qué merece ser recordado; el programa decide cómo almacenarlo

La IA puede decidir:

“Esto merece guardarse.”

El MCP debe recibir ese contenido de forma natural y el Memory Engine se encargará de:

persistirlo;

generar el embedding;

normalizar metadatos disponibles;

registrar fecha y origen;

conservar el histórico;

crear o actualizar relaciones cuando haya información suficiente;

indexarlo.

No exigir a la IA que conozca SQL ni el esquema interno.

3.6. La IA debe poder hablar con la memoria en lenguaje natural

Las herramientas MCP deben estar diseñadas para que el modelo pueda enviar:

“¿Qué habíamos decidido sobre los pagos de Atlas?”

“Guarda que el cliente X quiere una reunión el viernes.”

“¿Qué pensábamos antes sobre la arquitectura de pagos?”

“Recupérame lo que hablamos con la IA sobre esta memoria hace unos meses.”

El esquema JSON del protocolo puede contener parámetros técnicos opcionales, pero el parámetro principal de consulta debe ser lenguaje natural.

4. Transporte MCP

Transporte principal

Usar MCP Streamable HTTP en 127.0.0.1, porque el producto debe ejecutarse como servicio persistente en segundo plano.

No requiere autenticación para el acceso MCP local.

La aplicación debe escuchar únicamente en loopback:

127.0.0.1

Nunca:

0.0.0.0
::
IP pública

Compatibilidad

Añadir también un modo stdio para clientes MCP que no soporten el transporte HTTP o para diagnóstico.

El modo HTTP es el modo principal de ejecución persistente.

5. Arquitectura de procesos

El sistema se compone de tres servicios/procesos principales:

5.1. PostgreSQL

Responsable de persistencia, índices relacionales, texto completo, vectores y relaciones.

Debe escuchar únicamente en localhost.

5.2. Ollama

Responsable exclusivamente de generar embeddings locales.

No se necesita un LLM generativo dentro del Memory Engine para el funcionamiento normal.

5.3. Memory Server

Proceso Go que permanece ejecutándose en segundo plano y proporciona el MCP.

Debe encargarse de toda la lógica de memoria.

6. Modelo de embeddings

Usar un modelo local y ligero.

La implementación inicial debe utilizar EmbeddingGemma 300M, preferiblemente la variante cuantizada ligera disponible mediante Ollama, como configuración predeterminada.

Motivos:

300M parámetros.

Uso orientado a búsqueda y retrieval.

Entrenamiento multilingüe en más de 100 idiomas.

En Ollama existe una variante cuantizada embeddinggemma:300m-qat-q4_0 de aproximadamente 239 MB.

El nombre del modelo y sus dimensiones nunca deben estar hardcodeados por toda la aplicación.

Debe existir una configuración central:

embedding.model
embedding.dimensions
embedding.provider
embedding.storage_precision

El mismo modelo debe utilizarse para indexar y consultar.

El sistema debe registrar el modelo de embedding utilizado por cada recuerdo o, como mínimo, por cada versión de índice/modelo, para permitir futuras migraciones.

No introducir un modelo generativo local adicional en esta fase.

7. Objetivo de rendimiento

La exigencia principal no es que la búsqueda tarde exactamente el mismo tiempo con 1.000 y 10.000.000 de registros.

La exigencia es que no exista una degradación lineal con el número de recuerdos.

Nunca hacer esto para una consulta semántica:

consulta → comparar contra los N embeddings → ordenar todos → devolver 10

Sí hacer esto:

consulta
  ↓
embedding de consulta
  ↓
HNSW
  ↓
pequeño conjunto de candidatos
  ↓
búsqueda textual / filtros
  ↓
ránking
  ↓
Top-K

Requisito funcional

El número de candidatos examinados debe quedar limitado por configuración y no crecer proporcionalmente con el total de recuerdos.

Benchmark obligatorio

Automatizar pruebas con, como mínimo:

10.000 recuerdos

100.000 recuerdos

1.000.000 recuerdos

5.000.000 recuerdos

10.000.000 recuerdos cuando el entorno de CI/referencia lo permita

Medir por separado:

generación del embedding de la consulta;

búsqueda vectorial;

búsqueda textual;

combinación/ranking;

recuperación del contenido;

latencia total;

memoria RAM;

tamaño de PostgreSQL;

número de filas/candidatos procesados.

Criterio de escalabilidad

El benchmark debe demostrar que no aparece un cambio de O(n) en la fase de recuperación.

Como objetivo práctico de referencia:

no realizar secuencias completas de la tabla en consultas semánticas normales;

utilizar HNSW para candidatos vectoriales;

mantener un Top-K pequeño;

mantener un límite máximo de candidatos antes del reranking;

documentar P50/P95/P99.

Los números absolutos dependerán del hardware, pero la tendencia debe ser sublineal y estable al aumentar el volumen.

8. Índices de búsqueda

La memoria debe soportar varias formas de recuperación.

8.1. Vectorial

Usar pgvector con HNSW para similitud semántica.

Distancia preferida:

cosine

8.2. Texto

Implementar búsqueda de texto completo de PostgreSQL para coincidencias exactas o léxicas.

8.3. Metadatos

Usar JSONB para atributos variables.

Añadir los índices adecuados para los metadatos realmente utilizados.

8.4. Relaciones

Índices sobre identificadores origen/destino y tipo de relación.

8.5. Tiempo

Índices sobre fechas para permitir consultas históricas y ordenación temporal eficiente.

9. Búsqueda híbrida

La búsqueda no debe depender solamente del embedding.

Debe combinar, cuando sea apropiado:

Semántica
+
Texto
+
Metadatos
+
Recencia
+
Relaciones
+
Estado activo/inactivo

La implementación recomendada:

obtener candidatos semánticos mediante HNSW;

obtener candidatos léxicos mediante búsqueda textual;

unir ambos conjuntos;

eliminar duplicados;

aplicar filtros;

aplicar ponderación temporal;

expandir relaciones cuando la consulta lo requiera;

rerankear;

devolver Top-K pequeño.

El algoritmo concreto puede ser RRF o una estrategia equivalente, siempre que esté medido y probado.

10. Memoria universal

La entidad principal será memory_item.

No utilizar ENUM PostgreSQL rígido para el tipo de recuerdo.

Propuesta mínima:

memory_items
├── id UUID
├── content TEXT
├── memory_type TEXT
├── active BOOLEAN
├── created_at TIMESTAMPTZ
├── occurred_at TIMESTAMPTZ NULL
├── valid_from TIMESTAMPTZ NULL
├── valid_until TIMESTAMPTZ NULL
├── source_type TEXT
├── source_ref TEXT NULL
├── conversation_id TEXT NULL
├── turn_index BIGINT NULL
├── importance REAL NULL
├── embedding
├── embedding_model TEXT
├── content_hash
├── metadata JSONB
└── search_vector TSVECTOR

Notas importantes

memory_type es una etiqueta flexible, no una estructura cerrada.

Ejemplos posibles:

fact
preference
idea
decision
note
personal_memory
work
project
conversation_message
document_chunk
relationship_context

Pero la aplicación debe permitir tipos nuevos sin migración de esquema.

11. Línea temporal

Nunca sobrescribir silenciosamente un recuerdo histórico.

Si existe:

“Atlas utiliza Stripe.”

y posteriormente se registra:

“Atlas ha migrado de Stripe a Adyen.”

el sistema debe poder conservar ambas afirmaciones.

El recuerdo nuevo debe poder convertirse en el recuerdo actual mientras el antiguo permanece disponible como histórico.

La aplicación debe soportar el concepto de:

actual
histórico
inactivo
reemplazado

No es necesario que esos estados sean necesariamente ENUM; pueden resolverse con booleanos y relaciones/versiones.

Regla por defecto

La recuperación normal debe considerar únicamente recuerdos activos, salvo que la consulta indique expresamente que se desea historial.

Recuperación histórica

Si el usuario pregunta:

“¿Qué habíamos pensado antes?”

“¿Cuál era la idea original?”

“¿Qué dijimos hace seis meses?”

la IA debe poder recuperar recuerdos inactivos o antiguos.

12. Versionado y relaciones entre versiones

Añadir soporte para relacionar recuerdos mediante tipos como:

supersedes
superseded_by
related_to
supports
contradicts
derived_from
part_of
about
mentions
follow_up_to

No es necesario imponer una lista cerrada; debe ser extensible.

La aplicación nunca debe borrar automáticamente una versión anterior simplemente porque exista una nueva.

13. Grafo de relaciones

Crear una tabla independiente:

memory_links
├── id UUID
├── source_id UUID
├── target_id UUID
├── relation_type TEXT
├── active BOOLEAN
├── created_at TIMESTAMPTZ
└── metadata JSONB

Esto permite representar cosas como:

Atlas
 ├── pertenece_a → Cliente X
 ├── utiliza → Stripe
 ├── tiene_decision → Migrar a Adyen
 └── discutido_en → Conversación Y

La relación no sustituye al embedding.

El embedding encuentra contenido parecido.

Las relaciones permiten navegar entre contenidos relacionados aunque no sean semánticamente cercanos.

14. Conversaciones con la IA

Las conversaciones deben poder almacenarse y recuperarse como parte de la misma memoria universal.

No crear una arquitectura separada de memoria para conversaciones.

Cada mensaje de conversación puede utilizar:

source_type = conversation
conversation_id = identificador de conversación
turn_index = posición dentro de la conversación
metadata.speaker = user | assistant | system

El sistema debe permitir:

“Recupérame aquella conversación en la que hablamos de la arquitectura de memoria.”

y posteriormente obtener los mensajes relevantes y, cuando se solicite, reconstruir el contexto completo de la conversación.

Importante

El Memory Server no puede conocer mágicamente las conversaciones del host MCP.

La IA debe decidir cuándo persistirlas y utilizar las herramientas MCP para almacenarlas.

El skill de usuario creado por el proyecto debe enseñar a la IA cuándo conviene guardar una conversación completa, un resumen o recuerdos atómicos.

15. Herramientas MCP

Crear una API MCP pequeña y coherente.

memory_store

Guarda información nueva.

Entrada principal:

memory: lenguaje natural

Opcionales:

occurred_at
source_type
source_ref
conversation_id
turn_index
importance
metadata

La IA no debe proporcionar embedding.

memory_search

Consulta natural.

Ejemplo:

“¿Qué habíamos decidido sobre los pagos de Atlas?”

Parámetros opcionales:

query
limit
include_inactive
since
until

memory_get

Obtiene un recuerdo concreto por ID.

memory_history

Consulta explícitamente la línea temporal e histórico.

Debe servir para:

ideas anteriores;

recuerdos desactivados;

versiones anteriores;

cambios de decisión;

información de una fecha concreta.

memory_related

Obtiene recuerdos relacionados mediante enlaces y/o similitud semántica.

memory_link

Crea una relación entre dos recuerdos.

memory_update

Actualiza un recuerdo sin destruir el histórico.

Debe poder crear una nueva versión y marcar la anterior como no activa.

memory_forget

Desactiva un recuerdo:

active = false

No debe destruirlo.

memory_delete

Borrado físico e irreversible.

Debe tratarse como operación administrativa y no utilizarse como sustituto de memory_forget.

memory_get_conversation

Reconstruye una conversación mediante conversation_id.

memory_export

Exporta la memoria completa a un formato portable y versionado.

memory_import

Importa una memoria previamente exportada.

Debe detectar incompatibilidades y ofrecer migraciones cuando sea posible.

memory_health

Devuelve el estado de:

PostgreSQL;

pgvector;

Ollama;

modelo de embedding;

MCP;

migraciones;

índices.

16. Recuperación progresiva para ahorrar tokens

El Memory Engine no debe devolver grandes bloques de información por defecto.

Una consulta normal debe devolver algo parecido a:

ID
relevancia
fecha
estado
tipo
resumen corto / contenido corto
relaciones relevantes

La IA puede solicitar después el contenido completo mediante memory_get.

Objetivo:

consulta → pocos candidatos → contexto pequeño

No:

consulta → cientos/miles de recuerdos → cientos de miles de tokens

17. Formato de exportación/importación

Definir un formato propio versionado, por ejemplo:

memory-export-v1

Debe incluir:

recuerdos;

relaciones;

conversaciones;

metadatos;

estado activo/inactivo;

información temporal;

versión del esquema;

modelo de embeddings;

configuración necesaria para reconstrucción.

Los embeddings pueden incluirse para conservar la búsqueda sin recalcularlos.

El importador debe comprobar que las dimensiones y el modelo sean compatibles.

Si no lo son, debe ser posible importar el contenido y marcar los embeddings para reconstrucción.

18. Instalación y distribución

Objetivo general

Instalable sin Docker ni runtimes de programación adicionales.

La aplicación compilada debe ser un único binario Go en tiempo de ejecución, con PostgreSQL y Ollama como dependencias del sistema.

macOS

Soporte nativo:

Apple Silicon;

Intel cuando las dependencias estén disponibles.

Homebrew debe proporcionar:

PostgreSQL 18;

pgvector;

Ollama;

aplicación de memoria.

La instalación debe preparar PostgreSQL, crear la base de datos, activar vector y descargar el modelo de embeddings.

Linux

Soporte nativo para arquitecturas soportadas por Go y por las dependencias.

Homebrew debe ser una vía de instalación soportada.

La aplicación también debe poder utilizar una instalación nativa existente de PostgreSQL y Ollama, siempre que cumpla los requisitos.

Windows

Soporte nativo, sin WSL como requisito.

Homebrew no debe ser requisito de Windows nativo.

La distribución de Windows debe utilizar binarios nativos y preparar PostgreSQL + pgvector + Ollama sin Docker.

Para pgvector, evitar exigir al usuario Visual Studio o herramientas de compilación: la distribución final debe incluir o instalar un artefacto de pgvector previamente construido y compatible con la versión de PostgreSQL soportada.

Regla de compatibilidad

No depender de una ruta fija de PostgreSQL.

Detectar pg_config, psql, postgres y directorios de instalación dinámicamente.

19. Versiones de dependencias

El sistema debe mantener versiones compatibles en configuración centralizada.

La combinación inicial recomendada es:

PostgreSQL 18
pgvector 0.8.x
Ollama versión compatible actual
EmbeddingGemma 300M cuantizado

No asumir que una futura actualización de PostgreSQL es compatible automáticamente con pgvector.

El instalador debe comprobar la compatibilidad antes de activar el sistema.

20. Servicio en segundo plano

El usuario no debería tener que arrancar manualmente el servidor de memoria después de la instalación.

Implementar integración nativa:

macOS

launchd y/o integración con brew services.

Linux

systemd --user preferentemente.

Windows

Windows Service.

El servicio debe reiniciarse automáticamente si falla, con límites razonables para evitar bucles de reinicio infinitos.

21. Red y seguridad

MCP

No exigir token ni API key para el uso local.

PostgreSQL

Configurar:

listen_addresses = '127.0.0.1'

La configuración de acceso debe impedir conexiones remotas.

Ollama

Consumir únicamente el endpoint local.

Exposición de puertos

No abrir ningún servicio a interfaces públicas.

El instalador debe verificar después de configurar que PostgreSQL y Memory Server están escuchando únicamente en loopback.

22. Cifrado de datos

La información puede contener datos sensibles.

Implementar una abstracción de cifrado de datos en reposo, con preferencia por almacenamiento de claves mediante mecanismos seguros nativos del sistema operativo:

Keychain en macOS;

Credential Manager en Windows;

Secret Service/keyring cuando esté disponible en Linux.

El diseño debe separar:

DataEncryption
KeyStore

para que los mecanismos específicos del sistema operativo no contaminen el Memory Engine.

La aplicación nunca debe registrar claves en logs.

Si el almacenamiento seguro de claves no está disponible, el sistema debe indicarlo explícitamente y no fingir que el cifrado está habilitado.

23. Configuración

Toda la configuración debe estar centralizada en un archivo local versionado por la aplicación.

Ejemplo conceptual:

server:
  host: 127.0.0.1
  http_port: auto
  transport: streamable-http

database:
  host: 127.0.0.1
  port: auto
  name: ai_memory
  user: ai_memory

embedding:
  provider: ollama
  model: embeddinggemma:300m-qat-q4_0
  dimensions: auto

retrieval:
  default_limit: 8
  candidate_limit: 64
  include_inactive_by_default: false
  recency_weight: configurable
  semantic_weight: configurable
  lexical_weight: configurable

security:
  localhost_only: true
  encryption_at_rest: true

No utilizar valores mágicos esparcidos por el código.

24. CLI administrativa

El sistema debe incluir una CLI Go para instalación y mantenimiento.

Comandos mínimos:

memory install
memory setup
memory start
memory stop
memory restart
memory status
memory health
memory migrate
memory export
memory import
memory doctor
memory uninstall
memory version

La CLI debe ser idempotente.

Ejecutar dos veces setup no debe romper una instalación existente.

25. Migraciones

Usar migraciones SQL versionadas.

No modificar tablas manualmente desde la lógica de negocio.

Cada migración debe poder:

instalar una versión nueva;

detectar la versión actual;

avanzar de forma segura;

fallar sin dejar el sistema en un estado ambiguo.

Preparar el sistema para futuras migraciones de embeddings.

26. Estrategia de actualización del embedding

El modelo de embeddings puede cambiar en el futuro.

Nunca cambiar el modelo y asumir que los vectores existentes siguen siendo compatibles.

El sistema debe almacenar:

embedding_model
embedding_dimensions
embedding_version

Cuando cambie el modelo:

permitir mantener la memoria textual;

marcar embeddings antiguos;

generar embeddings nuevos en segundo plano;

reconstruir el índice cuando corresponda;

volver a activar la búsqueda nueva cuando el proceso esté listo.

No bloquear toda la memoria durante una reindexación completa cuando sea técnicamente evitable.

27. Ingesta y deduplicación

No duplicar ciegamente el mismo recuerdo miles de veces.

Calcular un hash estable del contenido y utilizarlo como ayuda para detectar duplicados exactos.

La deduplicación exacta no debe destruir versiones legítimas que cambien por fecha o contexto.

La similitud semántica no debe ser utilizada por defecto para borrar datos automáticamente.

28. Consistencia de memoria

La aplicación debe distinguir entre:

contenido guardado;

contenido activo;

contenido histórico;

contenido eliminado;

relaciones;

versiones.

Nunca presentar un recuerdo inactivo como si fuera necesariamente la versión actual.

Cuando existen varios recuerdos relacionados y contradictorios, el resultado debe incluir suficiente información temporal para que la IA pueda decidir cómo expresarlo.

29. Observabilidad

Logs estructurados, pero sin contenido sensible por defecto.

Cada petición debe poder correlacionarse mediante un request_id.

Registrar:

duración de búsqueda;

número de candidatos;

etapa que más tiempo consume;

errores;

estado de los servicios.

No registrar por defecto el contenido completo de los recuerdos.

Añadir modo debug explícito para diagnóstico.

30. Pruebas

Unitarias

almacenamiento;

búsqueda;

ranking;

filtros;

temporalidad;

versionado;

relaciones;

importación/exportación;

configuración;

seguridad de binding.

Integración

PostgreSQL real;

pgvector real;

Ollama real;

MCP real.

Conformidad MCP

Ejecutar pruebas de conformidad contra el SDK/protocolo MCP soportado.

Recuperación semántica

Crear datasets de prueba en español y varios idiomas.

Medir precision@K / recall@K o equivalentes.

Rendimiento

Ejecutar benchmark de 10k → 100k → 1M → 5M → 10M.

31. Datos de prueba

Crear un generador reproducible de memoria sintética.

Debe poder crear:

hechos;

proyectos;

notas;

conversaciones;

decisiones;

relaciones;

versiones antiguas;

recuerdos activos e inactivos;

contenido en español e inglés.

Debe existir semilla configurable para repetir benchmarks.

32. Documentación obligatoria

El repositorio final debe incluir:

README.md
ARCHITECTURE.md
INSTALL.md
CONFIGURATION.md
MCP.md
MEMORY_MODEL.md
RETRIEVAL.md
SECURITY.md
BENCHMARKS.md
MIGRATIONS.md
CONTRIBUTING.md
CHANGELOG.md

La documentación debe explicar el sistema para usuarios y también para desarrolladores.

33. Skill de uso de memoria para Gemini CLI

El proyecto debe crear una skill de usuario que enseñe a la IA a utilizar correctamente la memoria.

Debe utilizar la skill/metaskill disponible para crear skills, denominada en algunos entornos crear-skills y en la documentación actual de Gemini CLI skill-creator.

La skill resultante debe contener un SKILL.md válido y, si hace falta, referencias auxiliares.

Ubicación objetivo

Usar la ruta de usuario indicada por el entorno.

La ruta solicitada para este proyecto es:

$HOME/.gemini/config/skills/

Pero el desarrollador debe detectar también la ubicación estándar reconocida por la versión instalada de Gemini CLI. Actualmente Gemini CLI documenta las skills de usuario bajo:

~/.gemini/skills/

Si el entorno reconoce solamente la ruta estándar, instalar allí la skill y dejar documentada la relación con la ruta solicitada.

La skill debe enseñar a la IA

cuándo guardar memoria;

cómo formular una petición memory_store en lenguaje natural;

cómo buscar primero de forma breve;

cuándo pedir memory_get;

cuándo pedir histórico;

cuándo incluir recuerdos inactivos;

cómo recuperar conversaciones anteriores;

cuándo crear relaciones;

cuándo no guardar información;

cómo evitar duplicar recuerdos exactos;

cómo distinguir información actual de histórica;

cómo usar memory_forget en lugar de borrar físicamente;

cómo exportar/importar cuando el usuario lo solicite.

La skill no debe duplicar toda la documentación de implementación; debe enseñar a la IA a utilizar la memoria correctamente.

34. Experiencia de uso esperada

Después de instalar el sistema, la experiencia ideal debe ser:

1. Instalar la aplicación.
2. El instalador comprueba/instala PostgreSQL.
3. Instala pgvector.
4. Comprueba/instala Ollama.
5. Descarga el modelo de embeddings.
6. Inicializa la base de datos.
7. Activa la extensión vector.
8. Configura localhost.
9. Arranca PostgreSQL.
10. Arranca Ollama.
11. Arranca Memory Server.
12. Deja el servicio preparado para que la IA se conecte mediante MCP.

A partir de ahí:

IA → MCP → Memory Server → PostgreSQL/Ollama

El usuario no debe conocer SQL.

35. Qué NO debe hacer el proyecto

No implementar:

nube;

base de datos remota;

autenticación MCP obligatoria;

API keys para la memoria local;

Docker como dependencia;

Python como dependencia de ejecución;

Node.js como dependencia de ejecución;

interfaz web obligatoria;

múltiples memorias aisladas;

tablas rígidas por dominio;

escaneo completo de la memoria para cada consulta;

borrado automático de históricos;

reescritura destructiva de recuerdos;

modelo generativo local adicional en MVP.

36. Evolución futura prevista

El diseño debe permitir añadir posteriormente, sin rediseñar la memoria universal:

búsqueda multimodal;

imágenes y documentos binarios;

OCR si se decide posteriormente;

nuevos modelos de embeddings;

rerankers locales;

backups automáticos;

replicación;

cifrado más avanzado;

interfaz gráfica;

sincronización entre dispositivos;

memorias especializadas derivadas de la memoria universal.

Estas funcionalidades no son requisito del MVP.

37. Criterios de aceptación del MVP

El MVP solo se considera terminado si cumple todo lo siguiente:

Memoria

Puede guardar cualquier texto como recuerdo sin cambiar el esquema.

Puede guardar metadatos variables mediante JSONB.

Conserva histórico.

Puede activar/desactivar recuerdos.

Puede enlazar recuerdos.

Puede guardar conversaciones.

Recuperación

Búsqueda por lenguaje natural.

Búsqueda semántica con embeddings locales.

HNSW operativo.

Búsqueda textual.

Ranking híbrido.

Preferencia por recuerdos activos y recientes en consultas normales.

Recuperación explícita de históricos.

MCP

Servidor MCP funcional.

Streamable HTTP en localhost.

Modo stdio disponible.

Herramientas documentadas.

Infraestructura

PostgreSQL local.

pgvector habilitado.

Ollama local.

Modelo embedding local.

Sin credenciales MCP.

Sin exposición de red pública.

Instalación

macOS nativo.

Linux nativo.

Windows nativo.

Instalación mediante Homebrew donde sea aplicable.

Compilación mediante Go.

Servicios de segundo plano nativos.

Seguridad

PostgreSQL limitado a localhost.

MCP limitado a localhost.

Logs sin datos sensibles por defecto.

Sistema de cifrado en reposo implementado o claramente bloqueado por ausencia del mecanismo seguro de claves.

Portabilidad

Exportación completa.

Importación completa.

Versionado del formato.

Calidad

Tests unitarios.

Tests de integración.

Tests MCP.

Benchmarks de escalabilidad.

Documentación completa.

Skill de usuario creada y validada.

38. Referencias técnicas verificadas para esta especificación

Las decisiones siguientes deben contrastarse con la documentación actual durante la implementación:

Model Context Protocol y su transporte Streamable HTTP/stdio.

SDK oficial de MCP para Go.

PostgreSQL 18.

pgvector 0.8.x y sus índices HNSW.

API local de embeddings de Ollama (/api/embed).

EmbeddingGemma 300M como modelo local ligero.

Gemini CLI Agent Skills y ubicación de skills de usuario.

La implementación debe comprobar las versiones actuales antes de fijar versiones definitivas en el repositorio.
