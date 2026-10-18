[ROL]
Actúa como un ingeniero experto en sistemas de memoria para IA, recuperación de información y grafos de conocimiento, con pensamiento crítico. Trabajas sobre una aplicación ya creada.

[CONTEXTO]
lodan es mi memoria persistente, compartida por todas mis IAs (Claude Code, opencode y Antigravity) y sincronizada entre máquinas por git. Es un binario Go (repositorio `C:\Users\4ntoniomj\lodaan-memory`) con PostgreSQL + pgvector y Ollama para los embeddings, instalado como servicio en `C:\Users\4ntoniomj\AppData\Local\lodan`. Lo instalado sale de la rama `feat/sincronizacion-git` (sin fusionar en `dev`).

Hoy cada registro lleva de 1 a 3 «temas» (`topics`, `memory_topics`), que suelen ser generales («lodan», «agridemur»). Cada IA los escribe a su manera, y que dos registros compartan tema no los relaciona por sí solo. Los alias viven en registros de texto sueltos (por ejemplo #45 «AGM = Agridemur», #349 y #350 glosarios de Agridemur), así que una IA que pregunta por «Agridemur» puede no encontrar lo guardado como «AGM». La `key` la inventa cada IA, y por eso dos registros sobre lo mismo rara vez comparten clave. Las contradicciones solo se sospechan (`revisa:`) y la IA decide; hay 8 sin resolver (#815).

Engram (repo Gentleman-Programming/engram, resumen en lodan #889) resuelve parte de esto: sugiere la `topic_key` con familias fijas, guarda un veredicto por cada posible conflicto (`related`, `compatible`, `scoped`, `conflicts_with`, `supersedes`, `not_conflict`, con confianza; por debajo de 0.7 pregunta al usuario) y consolida registros repetidos con vista previa y confirmación. No tiene alias ni contradicción exacta por entidad.

Decisión mía (lodan #890): quiero etiquetas «como los hashtags de las redes sociales» para entrelazarlo todo, más lo útil de Engram.

[OBJETIVOS]
1. Hashtags: cada registro lleva varias etiquetas, generales (`#agridemur`) y concretas (`#coche-1234abc`, `#matricula`), escritas en una forma normalizada. Decide si sustituyen a los temas actuales o los amplían, y cómo migrar los temas existentes sin perder nada.
2. Diccionario de equivalencias: el servidor sabe que `#agm` es `#agridemur` y lo corrige solo, venga de la IA que venga. Las equivalencias se pueden añadir y revisar, y viajan con la sincronización por git.
3. Relaciones automáticas por etiquetas: dos registros que comparten una etiqueta quedan unidos con más fuerza cuanto más rara sea (compartir una etiqueta con 4 registros pesa mucho; una con 300, poco), para que no acabe todo relacionado con todo. `recall` sigue las etiquetas concretas y trae lo conectado en una sola llamada.
4. Clave sugerida por el servidor: para datos que cambian (por ejemplo la matrícula o el seguro de un coche), el servidor propone la `key` a partir de las etiquetas, para actualizar en vez de duplicar.
5. Veredicto ante conflictos: cuando un registro nuevo choca con otro, la IA guarda un veredicto (se contradicen, sustituye, compatibles, no tiene que ver), con su confianza, y me pregunta cuando no está segura. Las 8 contradicciones pendientes (#815) deben poder resolverse así.
6. Juntar registros repetidos en uno, con vista previa y confirmación antes de escribir, sin perder el historial.
7. Instalarlo en este Windows para las tres IAs y que funcione con la sincronización entre máquinas.

[RESTRICCIONES]
- No te inventes datos: lo que no sepas, lo investigas en el código o en la documentación; si sigues sin saberlo, lo dices.
- No trabajes en `main` ni directamente en `dev`: crea una rama nueva desde `feat/sincronizacion-git` (es de donde sale lo instalado).
- Cualquier cambio de esquema es una migración sobre mi base real: backup completo antes (`pg_dump` en `AppData\Local\lodan\backups-manual`) y prueba en una copia restaurada.
- Los tests los escribe el subagente `examinador` antes del código (skill `tdd-examinador`); quien programa no los toca.
- Como máximo 6 herramientas MCP: amplía las que hay (`remember`, `recall`, `revise`…) en vez de crear nuevas.
- Respeta lo que lodan ya tiene decidido que no se guarda (por ejemplo, datos personales de trabajadores) y no guardes secretos.
- Pregúntame una sola cosa cada vez, y solo cuando sea una decisión mía.

[FORMATO]
Primero analiza el código actual y mis registros reales y devuélveme una propuesta corta y en lenguaje sencillo: cómo serán las etiquetas, el diccionario de equivalencias, el cálculo de la fuerza de una relación, la migración de los temas actuales, qué cambia en cada herramienta y los riesgos. Incluye una medición previa sobre unos 50 registros reales (cuántas contradicciones, duplicados y alias se detectan hoy y cuántos se detectarían con la propuesta). Implementa solo cuando te dé el sí. Tono claro, directo y sin tecnicismos innecesarios.
