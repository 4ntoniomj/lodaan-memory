# Codebase

Última actualización: 2026-09-29

| Tecnología | Tipo | Versión | Notas |
| :--- | :--- | :--- | :--- |
| Go | lenguaje | 1.25.0 (mínimo), toolchain go1.27.1 | El mínimo lo impone el SDK de MCP |
| github.com/modelcontextprotocol/go-sdk | librería | v1.8.0 | SDK oficial de MCP: herramientas, stdio y HTTP streamable |
| github.com/jackc/pgx/v5 | librería | v5.11.0 | Driver de PostgreSQL |
| github.com/pgvector/pgvector-go | librería | v0.4.1 | Tipos `vector`/`halfvec` para pgx |
| github.com/oklog/ulid/v2 | librería | v2.1.2 | Identificadores de sesión ordenables por tiempo |
| github.com/google/jsonschema-go | librería | v0.4.3 | Esquemas de entrada de las herramientas MCP (enums) |
| github.com/klauspost/compress | librería | v1.20.1 | zstd: compresión de los backups (`.tar.zst`) y descompresión de los paquetes de Ollama |
| github.com/jackc/pgpassfile | librería | v1.0.0 | Indirecta (pgx) |
| github.com/jackc/pgservicefile | librería | v0.0.0-20240606120523-5a60cdf6a761 | Indirecta (pgx) |
| golang.org/x/text | librería | v0.29.0 | Indirecta (pgx) |
| github.com/jackc/puddle/v2 | librería | v2.2.2 | Indirecta (pgxpool) |
| golang.org/x/sync | librería | v0.20.0 | Indirecta |
| github.com/segmentio/asm | librería | v1.1.3 | Indirecta (SDK MCP) |
| github.com/segmentio/encoding | librería | v0.5.4 | Indirecta (SDK MCP) |
| github.com/yosida95/uritemplate/v3 | librería | v3.0.2 | Indirecta (SDK MCP) |
| golang.org/x/oauth2 | librería | v0.35.0 | Indirecta (SDK MCP) |
| golang.org/x/sys | librería | v0.41.0 | Servicio de Windows (SCM) y llamadas al sistema |
| golang.org/x/time | librería | v0.15.0 | Indirecta |
| PostgreSQL | base de datos | ≥ 16 (desarrollo: 18) | En Windows, conda-forge solo tiene pgvector para PG 16 |
| pgvector | base de datos (extensión) | ≥ 0.7.0 (objetivo: 0.8.6) | `halfvec`, `binary_quantize`, HNSW |
| Ollama | servicio externo | desarrollo: 0.34.4 | Embeddings vía `POST /api/embed` |
| embeddinggemma | modelo de embeddings | `300m-qat-q4_0` (por defecto) | Multilingüe, 768 dimensiones; por defecto, configurable. Umbrales calibrados en `specs/001-nucleo-memoria/calibracion.md` |

> Go, PostgreSQL, pgvector, Ollama y el modelo no aparecen en `go.mod`: `detectar_stack.py` los reporta como «no detectados» y se mantienen documentados a propósito.

---

## Módulos de implementación

Estado actual de cada funcionalidad en `internal/`:

### Database cluster

Gestión de la conexión a PostgreSQL, pool de conexiones y operaciones de base de datos.

**Ubicación:** `internal/database/`

**Responsabilidades:**
- Inicializar y mantener pool de conexiones a PostgreSQL.
- Ejecutar migraciones de esquema.
- Gestionar transacciones para coherencia de datos.

#### Backup y Restauración

Los backups son copias en frío (PostgreSQL parado mientras se leen los archivos), almacenadas como `.tar.zst` (tar comprimido con zstd, `github.com/klauspost/compress/zstd`, nivel por defecto) con `info.txt`, `manifest.txt`, `pg/`, `config.json` (si existe) y `secret`, en ese orden. Formato actual: `lodan-backup/2` (el 1 se rechaza).

**Estrategias de backup:**
- **Full:** Copia completa del clúster.
- **Differential (`diff`):** Archivos modificados desde el último backup **full** del destino.
- **Incremental (`incr`):** Archivos modificados desde el último backup **de cualquier tipo** (full, incremental o differential) del destino.

Differential e incremental copian un archivo si su fecha de modificación es posterior al `fecha` del backup de referencia (`desde` en `info.txt`, con 2 s de margen), o si su ruta no figura en el manifiesto de referencia, o si su tamaño difiere del que ese manifiesto indica. Las dos últimas reglas existen porque `rename` conserva la fecha de modificación (PostgreSQL recicla los segmentos de `pg_wal` renombrándolos) y garantizan, por inducción, que la cadena contiene todo lo que lista el manifiesto del último backup. Fallan si el destino no tiene ningún full.

**Flujo de Backup (`Backup()`):**

```
1. Validar el destino (no puede estar dentro de pg/) y adquirir el lock del destino (<destino>/.lodan_backup.lock)
2. Borrar los .lodan_backup_*.staging huérfanos del destino (de un backup interrumpido)
3. Adquirir el lock del clúster; para incr/diff: localizar el backup de referencia en el destino
4. Parar PostgreSQL (si estaba en marcha)
5. Recorrer pg/ una vez: manifiesto completo + archivos a copiar (todos en full; los modificados en incr/diff)
6. Copiar (sin comprimir) esas entradas, config.json y secret a <destino>/.lodan_backup_<fecha>.<tipo>.staging/ (0700),
   conservando modo y mtime y comprobando el tamaño de cada archivo
7. Reiniciar PostgreSQL (si estaba en marcha) y liberar el lock
8. Con PostgreSQL ya en marcha, escribir lodan_backup_YYYY-MM-DD_HHMMSS.{full|incr|diff}.tar.zst desde el staging
   (a un .part que se renombra al final)
9. Borrar siempre el staging (y el .part si falló) y liberar el lock del destino
```

La parada de PostgreSQL dura lo que tarda la copia (pasos 4 a 7) y el destino necesita espacio libre para esa copia sin comprimir. El lock se suelta antes de comprimir para no bloquear al supervisor del servicio. El lock del destino (mismo mecanismo que el del clúster: se refresca mientras se mantiene y se considera abandonado a los 2 minutos) serializa los backups hacia el mismo destino de principio a fin, compresión incluida, para que un full lento y un incremental lanzado por cron no se borren el staging. El segundo espera hasta 60 s y, si sigue ocupado, falla con "hay otro backup en curso en el destino". Orden de locks: destino primero, clúster después. `Restore` no usa el lock del destino. `listBackups` y `AutoDetectBackups` ignoran `.lodan_backup.lock`.

**Con el supervisor del servicio (`lodan service run`) en marcha:** PostgreSQL debe seguir siendo hijo del servicio (si no, con systemd el servicio cae y PostgreSQL queda colgando fuera de él). Para eso:
- El supervisor toca cada 5 s `<datos>/supervisor.heartbeat` (`Config.HeartbeatFile()`) mientras vive, también durante una pausa, y lo borra al salir. `Cluster.SupervisorActive()` es true si el archivo existe y tiene menos de 15 s.
- `Backup` y `Restore` toman el lock del clúster **antes** de parar PostgreSQL. `Cluster.LockHeld()` dice si ese lock está tomado y no obsoleto (mismo criterio que `acquireLock`).
- **Marca de mantenimiento** (`<datos>/maintenance.pending`, `Config.MaintenanceFile()`). Con el PostgreSQL lanzado con `pg_ctl` (Windows, el caso normal) o externo, el supervisor detecta la parada por sondeo (10 s / 30 s), y `Backup` puede soltar el lock antes del siguiente sondeo: sin más información, el supervisor vería PostgreSQL parado, sin lock, y fallaría (ocurrió en la prueba real en Windows). Por eso, cuando van a ceder el arranque al supervisor (solo entonces), `Backup` y `Restore` crean la marca (`MarkMaintenancePending`, con la hora de creación) bajo el lock y **antes** de parar PostgreSQL. `MaintenancePending()` es true si existe y tiene menos de `restartTimeout` + 30 s; una marca más vieja se considera abandonada y se ignora.
- Supervisor: cuando PostgreSQL no está en marcha (hijo, arrancado con `pg_ctl` o externo sondeado) y `LockHeld() || MaintenancePending()`, no falla: registra «PostgreSQL parado por una operación de mantenimiento (backup o restauración): se espera a que termine», espera a que se libere el lock y vuelve a `prepareCluster` + `superviseStack` (adopta PostgreSQL si ya está en marcha; si no, lo lanza). Al final de `prepareCluster`, ya con PostgreSQL en marcha y todavía bajo el lock, borra la marca (`ClearMaintenancePending`). Sin lock ni marca vigente, un PostgreSQL parado sigue siendo un fallo.
- `Backup`: si PostgreSQL estaba en marcha y `SupervisorActive()` (se comprueba antes de pararlo), tras la copia al staging no lo arranca: suelta el lock del clúster y sondea `Status` cada 500 ms (hasta `restartTimeout`, 2 min) a que el supervisor lo arranque, y entonces comprime. Si no vuelve a tiempo, lo arranca él (con el lock), borra la marca y devuelve un error que lo explica; el archivo se escribe igualmente. Si la copia falla antes de ceder, la marca también se borra.
- `Restore`: igual que antes (arranque propio, verificación y rollback), pero si había supervisor activo, crea la marca antes de parar PostgreSQL y, tras verificar con éxito, para PostgreSQL de nuevo, suelta el lock y espera al supervisor con la misma lógica y el mismo plan B. Un restore fallido que vuelve al estado anterior con PostgreSQL en marcha también se lo cede al supervisor. Si no hay cesión, la marca se borra antes de devolver.
- Solo el supervisor (bajo el lock) y el plan B borran la marca tras una cesión; `Backup` y `Restore` no la borran por su cuenta al ver PostgreSQL en marcha, para no pisar la marca de otra operación que haya empezado entretanto.

**Flujo de Restauración (`Restore()`):**

```
1. Validar la cadena (un full primero; cada backup referencia por `desde` a uno anterior de la lista)
   Con una carpeta, AutoDetectBackups elige el full más reciente y los backups posteriores
2. Adquirir el lock y parar PostgreSQL (si estaba en marcha)
3. Extraer la cadena, de más antiguo a más reciente, a un directorio temporal
4. Con el manifiesto del último backup: borrar lo que no lista y comprobar que cada entrada existe con su tipo y tamaño
   Si no cuadra: abortar sin tocar el clúster actual
5. Apartar el clúster actual (pg.pre-restore-<fecha>), instalar el restaurado y arrancar
6. Si no arranca o no acepta conexiones: volver al estado anterior
```

**Metadatos (`info.txt`):** una línea `clave: valor` por campo: `formato`, `tipo` (full, incr o diff), `fecha` (RFC3339Nano), `desde` (fecha del backup de referencia; solo incr/diff), `tamano_original_bytes`, `archivos` y `postgresql`.

**Manifiesto (`manifest.txt`):** lista de todo lo que había en `pg/` al hacer el backup (no solo lo copiado), sin `postmaster.pid` ni el propio `pg/`. Una línea por entrada, ordenada por ruta: `<d|f> <tamaño> <ruta/relativa>` (`d` directorio con tamaño 0, `f` archivo regular con su tamaño en bytes). Se lee con un tope de 64 MiB (unas 800.000 entradas). Permite que la restauración elimine lo borrado desde el full y detecte cadenas incompletas.

### Otros módulos

- **memory:** Guardar registros (`internal/memory/`).
- **recall:** Búsqueda por tema (`internal/recall/`).
- **topic:** Gestión de etiquetas y temas (`internal/topic/`).
- **session:** Sesiones y contexto (`internal/session/`).
- **embedding:** Cálculo y búsqueda de embeddings vía Ollama (`internal/embedding/`).
- **mcptools:** Definición de herramientas MCP (`internal/mcptools/`).
- **config:** Configuración global (`internal/config/`).
