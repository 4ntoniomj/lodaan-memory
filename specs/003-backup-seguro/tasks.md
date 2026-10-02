# Tareas: Backup y restauración segura de lodan

## Fase 1: Funcionalidad Core — Backup (Implementación Go)

- [x] 1. Implementar `Cluster.Backup(ctx, destDir, backupType)` en `internal/database/cluster.go`
  - Parámetro `backupType`: "full", "incremental", "differential"
  - Adquirir lock exclusivo con `c.Lock(ctx)` y defer unlock
  - Validar que cluster está inicializado (Initialized())
  - Llamar `c.Stop(ctx)` para parar PostgreSQL
  - Según tipo:
    - **full**: copiar completo /pg/, config.json, secret
    - **incremental**: copiar solo archivos modificados desde último full (basado en mtime)
    - **differential**: copiar solo archivos modificados desde último full o diff (basado en mtime)
  - Comprimir en tar.xz: `archive/tar` + `compress/gzip` (o xz si se agrega dependencia)
  - Nombre archivo: `lodan_backup_YYYY-MM-DD_HHMMSS.<tipo>.tar.xz`
  - Llamar `c.Start(ctx)` para reiniciar PostgreSQL (con defer)
  - Crear metadata dentro tar.xz (info.txt con fecha, tipo, tamaño original)
  - Retornar ruta del archivo tar.xz o error
  > Hecho con zstd y copia previa en lugar de xz (ver tarea 21).

- [x] 2. Implementar helper `findLastFullBackup(destDir)` en `internal/database/cluster.go`
  - Buscar archivo más reciente con `.full.tar.xz` en destDir
  - Retornar ruta o nil si no existe
  - Usado por incremental/differential para saber desde cuándo copiar cambios
  > Sustituido por referenceBackup (semántica estándar, tarea 17).

- [x] 3. Implementar helper `collectChangedFiles(pgDataDir, sinceTime)` en `internal/database/cluster.go`
  - Scannear /pg/ recursivamente
  - Retornar lista de archivos con mtime > sinceTime
  - Usado por incremental y differential

- [x] 4. [P] Implementar `Cluster.Restore(ctx, files []string)` en `internal/database/cluster.go`
  - Parámetro `files`: lista de rutas a archivos .tar.xz (full + opcionales incr/diff)
  - Validar que first file es .full.tar.xz
  - Validar secuencia (full → incr/diff en orden cronológico)
  - Adquirir lock, parar PostgreSQL
  - Extraer full.tar.xz a /pg/ temporal
  - Para cada incr/diff: extraer y merge en /pg/ (sobrescribir archivos)
  - Copiar config.json y secret desde uno de los tars (preferir del full)
  - Llamar `c.Start(ctx)` para reiniciar PostgreSQL
  - Verificar que PostgreSQL inicia correctamente
  - Retornar error si algo falla

- [x] 5. Implementar helper `autoDetectBackups(dirPath)` en `internal/database/cluster.go`
  - Scannear directorio en busca de archivos `lodan_backup_*.tar.xz`
  - Ordenar: full primero (por fecha), luego incr/diff en orden cronológico
  - Retornar slice de rutas en orden correcto
  - Retornar error si no hay full backup

## Fase 1B: Funcionalidad Core — CLI Commands

- [x] 6. Agregar command case "backup" en `cmd/lodan/db.go:runDB()`
  - Parsear flags: `--to` (string, por defecto "/tmp"), `--full`, `--incremental`, `--differential`
  - Si ningún flag de tipo, usar `--full` por defecto
  - Validar que --to es ruta absoluta (usar filepath.Abs)
  - Crear Cluster y llamar `cluster.Backup(ctx, to, backupType)`
  - Imprimir ruta del archivo creado a stdout: `Backup creado: /ruta/lodan_backup_....tar.xz`
  - Reportar errores a stderr

- [x] 7. Agregar command case "restore" en `cmd/lodan/db.go:runDB()`
  - Parsear flags: `--from` (puede ser archivo .tar.xz o directorio)
  - Si --from es directorio: llamar `autoDetectBackups()` para obtener lista de archivos
  - Si --from es archivo individual: tratar como lista de 1 archivo
  - Soportar múltiples archivos: `lodan db restore --from file1.tar.xz file2.tar.xz file3.tar.xz`
  - Crear Cluster y llamar `cluster.Restore(ctx, files)`
  - Imprimir confirmación: `Restauración completada desde: [lista de archivos]`
  - Reportar errores a stderr

- [x] 8. Actualizar help text en `cmd/lodan/main.go`
  - Agregar "backup" y "restore" a usageText de subcomando db
  - Documentar: 
    - `lodan db backup [--to destino] [--full|--incremental|--differential]`
    - `lodan db restore --from archivo1.tar.xz [archivo2.tar.xz ...]` o `lodan db restore --from /carpeta`

## Fase 2: Validación y Testing

- [x] 9. [P] Testing manual: `lodan db backup --to /tmp` (full por defecto) con lodan corriendo
  - Verificar: `/tmp/lodan_backup_YYYY-MM-DD_HHMMSS.full.tar.xz` existe
  - Verificar: `tar -xJf` (tar con xz) extrae correctamente
  - Verificar: contiene pg/, config.json, secret, info.txt
  - Verificar: lodan sigue corriendo sin errores post-backup
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

- [x] 10. [P] Testing manual: backups incremental y differential
  - Crear data adicional en lodan
  - Ejecutar `lodan db backup --to /tmp --incremental`
  - Verificar: archivo `.incr.tar.xz` es más pequeño que `.full.tar.xz`
  - Ejecutar `lodan db backup --to /tmp --differential`
  - Verificar: archivo `.diff.tar.xz` existe
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

- [x] 11. [P] Testing manual: restore desde full
  - Parar lodan (`lodan service stop`)
  - Borrar/renombrar ~/.local/share/lodan/pg
  - Ejecutar `lodan db restore --from /tmp/lodan_backup_YYYY-MM-DD_HHMMSS.full.tar.xz`
  - Verificar: /pg/ se restauró
  - Iniciar lodan: `lodan service start`
  - Verificar: lodan arranca correctamente, datos restaurados
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

- [x] 12. Testing manual: restore desde full + incremental + diferencial
  - Mismo setup que anterior
  - Ejecutar `lodan db restore --from /tmp` (auto-detecta full + incr + diff)
  - Verificar: restauración en orden correcto
  - Verificar: lodan arranca y datos están
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

- [x] 13. [P] Testing manual: flag --from con directorio
  - Ejecutar `lodan db restore --from /tmp` sin especificar archivos
  - Verificar: auto-detecta `lodan_backup_*.tar.xz`, restaura en orden
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

- [x] 14. Testing edge cases
  - Restaurar sin full backup disponible → error claro
  - Restaurar con archivos en orden incorrecto → error o reordenar automáticamente
  - Backup cuando lodan está parado
  - Restore cuando lodan está corriendo (debe pausar)
  > Cubierto por la prueba de extremo a extremo de la tarea 20, en un directorio temporal, sin tocar la instalación real.

## Fase 3: Documentación

- [x] 15. Actualizar README.md usando skill `crear-readme`
  - Nueva sección "Backup y Restauración"
  - Explicar tipos: full, incremental, differential
  - Ejemplos:
    - `lodan db backup --to /backups` (full por defecto)
    - `lodan db backup --to /backups --incremental`
    - `lodan db backup --to /backups --differential`
    - `lodan db restore --from /backups/lodan_backup_2026-10-01_120000.full.tar.xz`
    - `lodan db restore --from /backups` (auto-detecta y restaura)
  - Advertencia: downtime breve (~5s) durante backup/restore
  - Nota: gestión de retención será via script bash en futuro
  > README actualizado directamente (sección «Backup y Restauración»), sin pasar por la skill crear-readme.

- [x] 16. Actualizar CODEBASE.md
  - Agregar sección "Backup y Restauración" bajo database cluster
  - Documentar flujo de Backup(): lock → stop → collect/copy → tar.xz → start
  - Documentar flujo de Restore(): lock → stop → extract (full → incr/diff) → start → verify

## Fase 4: Correcciones tras la revisión (2026-10-02)

- [x] 17. Semántica estándar: `--differential` = cambios desde el último full; `--incremental` = cambios desde el último backup de cualquier tipo. Actualizar ayuda de la CLI, README y CODEBASE.
- [x] 18. Manifiesto `manifest.txt` (formato `lodan-backup/2`): escribirlo en cada backup y, al restaurar, eliminar lo que no esté en el del último backup y comprobar que no falte nada.
- [x] 19. Tests automáticos de `Backup`/`Restore` en `internal/database` (ida y vuelta full, cadenas incr/diff, autodetección, errores de cadena, destino dentro de pg/, borrados entre backups, backup con PostgreSQL parado, restore con PostgreSQL en marcha).
- [x] 20. Prueba de extremo a extremo con el binario en un `LODAN_DATA_DIR` y un HOME temporales (sustituye a las pruebas manuales 9–14 sobre la instalación real).
  > Hecha el 2026-10-02: ver verificacion.md.
- [x] 21. Compresión zstd (`.tar.zst`) en lugar de xz y copia previa a staging para que la parada dure solo la copia; quitar `github.com/ulikunitz/xz` de go.mod.

## Fase 5: Coordinación con el supervisor (2026-10-02)

- [x] 22. El supervisor (`lodan service run`) trata la parada de PostgreSQL con el lock del clúster tomado como pausa de mantenimiento; latido `supervisor.heartbeat`; backup y restore ceden el rearranque al supervisor si está activo (plan B: lo arrancan ellos y avisan).
- [x] 23. Lock por destino (`.lodan_backup.lock`) para que dos backups al mismo destino no se pisen.
