# Backup y restauración segura de lodan

## Intent

Implementar un mecanismo completo de backup y restauración que garantice la consistencia de datos. lodan no tiene forma de respaldar su estado actual (PostgreSQL + config); el usuario necesita hacer backups regulares sin riesgo de capturar datos corruptos, y poder restaurar desde esos backups. La solución integra comandos CLI `lodan db backup` (tres tipos: full, incremental, diferencial) y `lodan db restore` que pausa el servicio de forma atómica, asegurando integridad.

## Alcance

**Dentro**:
- Comando `lodan db backup [--to destino] [--full|--incremental|--differential]` que respalda /pg/, config.json y secret
  - Por defecto: full backup
  - Nombre: `lodan_backup_YYYY-MM-DD_HHMMSS.<tipo>.tar.zst` (comprimido con zstd)
- Comando `lodan db restore --from archivo1.tar.zst [archivo2.tar.zst ...]` que restaura desde uno o más backups
  - Soporta restauración desde full solo o full + incrementales + diferenciales
  - O `lodan db restore --from /carpeta` que auto-detecta y restaura en orden (full primero, luego incr/diff alfabéticamente)
- Manifiesto (`manifest.txt`) en cada backup con la lista completa de directorios y archivos de pg/ y sus tamaños; al restaurar se eliminan los que no estén en el manifiesto del último backup y se comprueba que no falte ninguno
- Uso del lock existente en cluster.go para garantizar serialización en backup y restore
- Pausa PostgreSQL solo mientras se copian los archivos a respaldar a una carpeta temporal del destino; se comprime con PostgreSQL ya en marcha (parada de pocos segundos con los datos reales; crece con el tamaño de lo copiado)
- Documentación en README.md con ejemplos de uso (usando skill crear-readme)
- Metadatos en cada tar.zst (incluir info de tipo, fecha, tamaño original)

**Fuera** (no-goals):
- Retención automática de backups (scripts bash en futuro)
- Validación post-backup (checksum) — futuro
- Compresión diferencial (solo copia de cambios en disco) — futuro
- Backup de embeddings de Ollama (solo PostgreSQL)
- Copias a sistemas remotos (S3, etc.)

## Criterios de aceptación

- GIVEN lodan servicio corriendo con datos en ~/.local/share/lodan/
  WHEN se ejecuta `lodan db backup --to /tmp`
  THEN se crea `/tmp/lodan_backup_YYYY-MM-DD_HHMMSS.full.tar.zst` (tipo full por defecto)

- GIVEN mismo escenario
  WHEN se ejecuta `lodan db backup --to /tmp --incremental`
  THEN se crea `/tmp/lodan_backup_YYYY-MM-DD_HHMMSS.incr.tar.zst` (cambios desde el último backup de cualquier tipo: full, incr o diff)

- GIVEN mismo escenario
  WHEN se ejecuta `lodan db backup --to /tmp --differential`
  THEN se crea `/tmp/lodan_backup_YYYY-MM-DD_HHMMSS.diff.tar.zst` (cambios desde el último full)

- GIVEN backup full completado en `/tmp/lodan_backup_2026-10-01_120000.full.tar.zst`
  WHEN se ejecuta `lodan db restore --from /tmp/lodan_backup_2026-10-01_120000.full.tar.zst`
  THEN lodan restaura datos, reinicia PostgreSQL, y verifica restauración exitosa

- GIVEN backups full + incr + diff en /tmp/
  WHEN se ejecuta `lodan db restore --from /tmp` (sin archivos específicos)
  THEN lodan auto-detecta todos los backups, restaura full primero, luego incr/diff en orden, y verifica integridad

- GIVEN lodan servicio con datos
  WHEN se ejecuta `lodan db backup` sin --to
  THEN el backup se crea en /tmp (destino por defecto)

- GIVEN backup completado
  WHEN se extrae `tar --zstd -xf lodan_backup_*.tar.zst` y se verifica contenido
  THEN contiene info.txt con metadata, manifest.txt, pg/, config.json y secret

- GIVEN un backup full y uno incremental, y entre ambos PostgreSQL borró archivos (DROP TABLE, VACUUM FULL)
  WHEN se restaura la cadena full + incremental
  THEN pg/ contiene exactamente los archivos del manifiesto del último backup aplicado, con sus tamaños; ningún archivo borrado reaparece y, si falta alguno del manifiesto, la restauración falla sin tocar el clúster actual
